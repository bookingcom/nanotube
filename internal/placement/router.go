// Package placement compiles the portable map for allocation-free relay routing.
// Its barrier API requires a trusted epoch-aware transport; legacy socket writes
// never count as durable acknowledgements.
package placement

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/bookingcom/nanotube/internal/v2placement"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/dgryski/go-jump"
)

var ErrHeldFull = errors.New("placement group hold is full")
var ErrTransport = errors.New("placement transport does not support epochs and durable acknowledgements")
var crcTable = crc32.MakeTable(crc32.Castagnoli)

// Delivery is an immutable, per-destination envelope. Never stamp the shared
// RecBytes: the same record may be routed to several independent replicas.
// Sequence is local to one group/epoch/lane. Restart-safe identities and replay
// require a durable transport adapter, which this package does not provide.
type Delivery struct {
	Record                           *rec.RecBytes
	Group, Replica, Store, MapDigest string
	Revision, Epoch, Sequence        uint64
	VShard                           uint32
	Lane                             v2placement.Lane
}

// Sink must enqueue without blocking or invoking Acknowledge synchronously.
// A true return transfers ownership; false means no record was accepted.
// Epochs reports whether the sink preserves the envelope and supplies trusted,
// cumulative, durable acknowledgements for the configured producer lane.
type Sink interface {
	Enqueue(Delivery) bool
	Epochs() bool
}

type Counters struct {
	Rejected, Held, Dropped                       atomic.Uint64
	Pending                                       atomic.Int64
	Queued                                        atomic.Int64
	OutputErrors, PersistenceErrors, Acknowledged atomic.Uint64
}

type pending struct {
	record *rec.RecBytes
	vshard uint32
}
type groupState struct {
	mu                sync.Mutex
	moving, releasing bool
	sent, acked       uint64
	held              []pending
}
type route struct {
	group   string
	replica v2placement.Replica
	state   *groupState
}
type namespace struct {
	prefix string
	index  int
}
type snapshot struct {
	document   v2placement.Document
	digest     string
	routes     []route
	groups     map[string]int
	namespaces []namespace
	spans      int
}

// Router serializes control operations, but each group has an independent
// dispatch gate. Waiting for one group's ACKs never holds another group's gate.
type Router struct {
	durable *Durable
	control sync.Mutex
	current atomic.Pointer[snapshot]
	replica string
	lane    v2placement.Lane
	sink    Sink
	limit   int
	Stats   Counters
}

func New(d v2placement.Document, replica string, lane v2placement.Lane, sink Sink, holdLimit int) (*Router, error) {
	if sink == nil || holdLimit < 1 || lane.Producer == "" || lane.Stream == "" {
		return nil, fmt.Errorf("placement requires sink, lane and positive hold limit")
	}
	// No durable relay state is available to authenticate an advanced restart.
	if d.Revision != 1 {
		return nil, fmt.Errorf("fresh relay requires bootstrap revision one; durable restart is not implemented")
	}
	s, err := compile(d, replica, lane, nil)
	if err != nil {
		return nil, err
	}
	r := &Router{replica: replica, lane: lane, sink: sink, limit: holdLimit}
	r.current.Store(s)
	return r, nil
}

func compile(d v2placement.Document, replica string, lane v2placement.Lane, old *snapshot) (*snapshot, error) {
	// Detach caller-owned slices as well as validating the exact canonical format.
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(d); err != nil {
		return nil, err
	}
	d, err := v2placement.Decode(&buf)
	if err != nil {
		return nil, err
	}
	s := &snapshot{document: d, digest: d.Digest(), spans: v2placement.VShards / int(d.Routing.Span), groups: make(map[string]int)}
	nsIDs := make(map[string]int)
	for i, n := range d.Routing.Namespaces {
		nsIDs[n.ID] = i
		s.namespaces = append(s.namespaces, namespace{n.Prefix, i})
	}
	sort.Slice(s.namespaces, func(i, j int) bool { return len(s.namespaces[i].prefix) > len(s.namespaces[j].prefix) })
	s.routes = make([]route, len(d.Groups))
	for _, g := range d.Groups {
		rank := int(g.Selector.Cell[0])
		for i := 1; i < len(g.Selector.Cell); i++ {
			digit := 0
			if g.Selector.Cell[i] != g.Selector.Cell[i-1] {
				digit = int(g.Selector.Cell[i]-d.Routing.JumpSizes[i-1]) + 1
			}
			rank = rank*(int(d.Routing.JumpSizes[i]-d.Routing.JumpSizes[i-1])+1) + digit
		}
		index := (rank*len(s.namespaces)+nsIDs[g.Selector.Namespace])*s.spans + int(g.Selector.Start/d.Routing.Span)
		id := g.Selector.GroupID()
		var selected *v2placement.Replica
		for i := range g.Replicas {
			if g.Replicas[i].ID == replica {
				selected = &g.Replicas[i]
				break
			}
		}
		if selected == nil {
			return nil, fmt.Errorf("unknown placement replica %q", replica)
		}
		found := false
		for _, l := range selected.Producers {
			if l == lane {
				found = true
			}
		}
		blackhole := false
		for _, st := range d.Stores {
			if st.ID == selected.Writer {
				blackhole = st.Blackhole
			}
		}
		if !found && !blackhole {
			return nil, fmt.Errorf("producer lane absent from group %s", id)
		}
		state := &groupState{}
		if old != nil {
			if oi, ok := old.groups[id]; ok {
				state = old.routes[oi].state
			}
		}
		s.routes[index] = route{id, *selected, state}
		s.groups[id] = index
	}
	return s, nil
}

func (s *snapshot) selectRoute(record *rec.RecBytes) (*route, uint32) {
	h := record.PathHash()
	sizes := s.document.Routing.JumpSizes
	previous := uint32(jump.Hash(h, int(sizes[0])))
	rank := int(previous)
	for i := 1; i < len(sizes); i++ {
		slot := uint32(jump.Hash(h, int(sizes[i])))
		digit := 0
		if slot != previous {
			digit = int(slot-sizes[i-1]) + 1
		}
		rank = rank*(int(sizes[i]-sizes[i-1])+1) + digit
		previous = slot
	}
	ns := 0
	for _, n := range s.namespaces {
		if bytes.HasPrefix(record.Path, []byte(n.prefix)) {
			ns = n.index
			break
		}
	}
	v := crc32.Checksum(record.Path, crcTable) & (v2placement.VShards - 1)
	i := (rank*len(s.namespaces)+ns)*s.spans + int(v/s.document.Routing.Span)
	return &s.routes[i], v
}

func (r *Router) delivery(s *snapshot, rt *route, record *rec.RecBytes, v uint32) Delivery {
	return Delivery{Record: record, Group: rt.group, Replica: r.replica, Store: rt.replica.Writer, MapDigest: s.digest, Revision: s.document.Revision, Epoch: rt.replica.Epoch, Sequence: rt.state.sent + 1, VShard: v, Lane: r.lane}
}

// Push routes the post-rewrite bytes. Placement batches use this same gate
// immediately: an old epoch cannot escape later through a staged host batch.
func (r *Router) Push(record *rec.RecBytes) error {
	if r.durable != nil {
		return r.durable.push(record)
	}
	return r.push(record)
}

func (r *Router) push(record *rec.RecBytes) error {
	if record == nil || len(record.Path) == 0 {
		return fmt.Errorf("empty placement routing name")
	}
	for {
		s := r.current.Load()
		rt, v := s.selectRoute(record)
		g := rt.state
		g.mu.Lock()
		if s != r.current.Load() {
			g.mu.Unlock()
			continue
		}
		if g.moving {
			if len(g.held) == r.limit {
				g.mu.Unlock()
				r.Stats.Dropped.Add(1)
				return ErrHeldFull
			}
			g.held = append(g.held, pending{record, v})
			r.Stats.Held.Add(1)
			r.Stats.Pending.Add(1)
			g.mu.Unlock()
			return nil
		}
		if g.sent == ^uint64(0) {
			g.mu.Unlock()
			r.Stats.Dropped.Add(1)
			return fmt.Errorf("placement sequence exhausted")
		}
		if !r.sink.Enqueue(r.delivery(s, rt, record, v)) {
			g.mu.Unlock()
			r.Stats.Dropped.Add(1)
			return fmt.Errorf("placement sink queue is full")
		}
		g.sent++
		g.mu.Unlock()
		return nil
	}
}

type Barrier struct {
	Group                  string
	MapDigest              string
	Revision, Epoch, Final uint64
	Lane                   v2placement.Lane
}

// BeginMove closes dispatch for exactly one group. The caller must obtain its
// durable ACK through Final, a store fence, and coverage before calling Adopt.
func (r *Router) BeginMove(group string) (Barrier, error) {
	if r.durable != nil {
		return r.durable.beginMove(group)
	}
	return r.beginMove(group)
}

func (r *Router) beginMove(group string) (Barrier, error) {
	r.control.Lock()
	defer r.control.Unlock()
	if !r.sink.Epochs() {
		return Barrier{}, ErrTransport
	}
	s := r.current.Load()
	i, ok := s.groups[group]
	if !ok {
		return Barrier{}, fmt.Errorf("unknown group")
	}
	rt := &s.routes[i]
	g := rt.state
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.moving {
		return Barrier{}, fmt.Errorf("group already moving")
	}
	g.moving = true
	g.held = make([]pending, 0, r.limit)
	return Barrier{Group: group, MapDigest: s.digest, Revision: s.document.Revision, Epoch: rt.replica.Epoch, Final: g.sent, Lane: r.lane}, nil
}

// Acknowledge accepts only cumulative durable positions from the trusted sink.
// The sink must authenticate the producer, stream, group and epoch externally.
func (r *Router) Acknowledge(group string, epoch, sequence uint64) error {
	if r.durable != nil {
		return r.durable.acknowledge(group, epoch, sequence)
	}
	return r.acknowledge(group, epoch, sequence)
}

func (r *Router) acknowledge(group string, epoch, sequence uint64) error {
	r.control.Lock()
	defer r.control.Unlock()
	if !r.sink.Epochs() {
		return ErrTransport
	}
	s := r.current.Load()
	i, ok := s.groups[group]
	if !ok {
		return fmt.Errorf("unknown group")
	}
	rt := &s.routes[i]
	g := rt.state
	g.mu.Lock()
	defer g.mu.Unlock()
	if epoch != rt.replica.Epoch || sequence > g.sent || sequence < g.acked {
		return fmt.Errorf("invalid durable acknowledgement")
	}
	g.acked = sequence
	return nil
}

// Adopt installs a whole snapshot atomically after validating the canonical
// predecessor and every epoch activation. Failed validation retains all holds.
// Successful adoption attempts release; a full sink retains held records until
// RetryHeld succeeds. No socket flush or queue-empty check authorizes adoption.
func (r *Router) Adopt(d v2placement.Document, activations []v2placement.Activation) (err error) {
	if r.durable != nil {
		return r.durable.adopt(d, activations)
	}
	return r.adopt(d, activations)
}

func (r *Router) adopt(d v2placement.Document, activations []v2placement.Activation) (err error) {
	r.control.Lock()
	defer r.control.Unlock()
	defer func() {
		if err != nil {
			r.Stats.Rejected.Add(1)
		}
	}()
	old := r.current.Load()
	next, err := compile(d, r.replica, r.lane, old)
	if err != nil {
		return err
	}
	if err = old.document.ValidateTransition(next.document, activations); err != nil {
		return err
	}
	// Addresses are immutable for an incarnation in this relay's transport set.
	// Otherwise a same-epoch endpoint edit could bypass its handoff barrier.
	if !reflect.DeepEqual(old.document.Stores, next.document.Stores) {
		return fmt.Errorf("transport inventory changes require restart integration")
	}
	var moving []*route
	defer func() {
		for i := len(moving) - 1; i >= 0; i-- {
			moving[i].state.mu.Unlock()
		}
	}()
	for i := range next.routes {
		n := &next.routes[i]
		before := &old.routes[old.groups[n.group]]
		g := n.state
		g.mu.Lock()
		if n.replica.Epoch == before.replica.Epoch {
			if g.moving {
				g.mu.Unlock()
				return fmt.Errorf("metadata adoption cannot change a moving group's barrier map")
			}
			g.mu.Unlock()
			continue
		}
		if !r.sink.Epochs() {
			g.mu.Unlock()
			return ErrTransport
		}

		moving = append(moving, n)
		if !g.moving || g.releasing || g.acked != g.sent {
			return fmt.Errorf("group %s has no acknowledged barrier", n.group)
		}
		finalMatches := false
		for _, a := range activations {
			if a.Fence.Group == n.group && a.Fence.Replica == r.replica {
				for _, e := range a.Fence.Ends {
					if e.Lane == r.lane && e.Final == g.sent && e.Durable == g.acked {
						finalMatches = true
					}
				}
			}
		}
		if !finalMatches {
			return fmt.Errorf("fence does not match relay barrier")
		}
	}
	r.current.Store(next)
	for _, n := range moving {
		g := n.state
		g.sent = 0
		g.acked = 0
		g.releasing = true
		r.release(next, n)
	}
	return nil
}

// Requires the group's lock. An unsuccessful enqueue leaves the suffix held.
func (r *Router) release(s *snapshot, rt *route) {
	g := rt.state
	n := 0
	for _, p := range g.held {
		if g.sent == ^uint64(0) {
			break
		}
		if !r.sink.Enqueue(r.delivery(s, rt, p.record, p.vshard)) {
			break
		}
		g.sent++
		n++
		r.Stats.Pending.Add(-1)
	}
	copy(g.held, g.held[n:])
	clear(g.held[len(g.held)-n:])
	g.held = g.held[:len(g.held)-n]
	if len(g.held) == 0 {
		g.moving = false
		g.releasing = false
		g.held = nil
	}
}

func (r *Router) RetryHeld(group string) error {
	if r.durable != nil {
		return r.durable.retryHeld(group)
	}
	return r.retryHeld(group)
}

func (r *Router) retryHeld(group string) error {
	r.control.Lock()
	defer r.control.Unlock()
	s := r.current.Load()
	i, ok := s.groups[group]
	if !ok {
		return fmt.Errorf("unknown group")
	}
	rt := &s.routes[i]
	rt.state.mu.Lock()
	defer rt.state.mu.Unlock()
	if !rt.state.releasing {
		return fmt.Errorf("group has no activated hold")
	}
	r.release(s, rt)
	return nil
}

func (r *Router) Revision() uint64 { return r.current.Load().document.Revision }

// DrainStatus is a committed moving-group frontier. A true result means every
// dispatched old-epoch record has an authenticated durable ACK. It is not a
// store fence or target coverage receipt.
func (r *Router) DrainStatus(group string) (Barrier, bool, error) {
	if r.durable != nil {
		r.durable.mu.Lock()
		defer r.durable.mu.Unlock()
		if r.durable.failed != nil {
			return Barrier{}, false, r.durable.failed
		}
	}
	r.control.Lock()
	defer r.control.Unlock()
	s := r.current.Load()
	i, ok := s.groups[group]
	if !ok {
		return Barrier{}, false, fmt.Errorf("unknown placement group")
	}
	rt := &s.routes[i]
	g := rt.state
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.moving || g.releasing {
		return Barrier{}, false, fmt.Errorf("group has no old-epoch barrier")
	}
	return Barrier{Group: group, MapDigest: s.digest, Revision: s.document.Revision, Epoch: rt.replica.Epoch, Final: g.sent, Lane: r.lane}, g.sent == g.acked, nil
}
