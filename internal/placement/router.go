// Package placement compiles immutable placement maps for relay routing.
package placement

import (
	"bytes"
	"encoding/json"
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

var crcTable = crc32.MakeTable(crc32.Castagnoli)

type Counters struct{ Rejected atomic.Uint64 }
type route struct {
	group   string
	replica v2placement.Replica
}
type namespace struct {
	prefix string
	index  int
}
type snapshot struct {
	document   v2placement.Document
	routes     []route
	namespaces []namespace
	spans      int
}

// Router publishes a fully validated map with a single atomic pointer swap.
// An in-flight selection may finish under the previous map; the move controller
// drains relay output before its final incremental part copy.
type Router struct {
	control sync.Mutex
	current atomic.Pointer[snapshot]
	replica string
	Stats   Counters
}

func New(d v2placement.Document, replica string) (*Router, error) {
	s, err := compile(d, replica)
	if err != nil {
		return nil, err
	}
	r := &Router{replica: replica}
	r.current.Store(s)
	return r, nil
}
func compile(d v2placement.Document, replica string) (*snapshot, error) {
	// Detach caller-owned slices as well as validating the exact canonical format.
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(d); err != nil {
		return nil, err
	}
	d, err := v2placement.Decode(&buf)
	if err != nil {
		return nil, err
	}
	s := &snapshot{document: d, spans: v2placement.VShards / int(d.Routing.Span)}
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
		s.routes[index] = route{id, *selected}
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

// Owners returns one consistent snapshot's donor and optional move destination.
// It neither mutates the shared record nor allocates an envelope.
func (r *Router) Owners(record *rec.RecBytes) (string, string) {
	rt, _ := r.current.Load().selectRoute(record)
	return rt.replica.Writer, rt.replica.MovingTo
}
func (r *Router) Adopt(d v2placement.Document) (err error) {
	r.control.Lock()
	defer r.control.Unlock()
	defer func() {
		if err != nil {
			r.Stats.Rejected.Add(1)
		}
	}()
	old := r.current.Load()
	next, err := compile(d, r.replica)
	if err != nil {
		return err
	}
	if err = old.document.ValidateSuccessor(next.document); err != nil {
		return err
	}
	if !reflect.DeepEqual(old.document.Stores, next.document.Stores) {
		return fmt.Errorf("transport inventory changes require restart")
	}
	for _, g := range old.document.Groups {
		for _, before := range g.Replicas {
			after, _ := next.document.Lookup(g.Selector.GroupID(), before.ID)
			if after.Writer != before.Writer {
				if before.MovingTo != after.Writer || after.MovingTo != "" {
					return fmt.Errorf("writer change requires prior dual-owner routing")
				}
			} else if after.Epoch != before.Epoch {
				return fmt.Errorf("unchanged writer must retain epoch")
			}
			if before.MovingTo != "" && after.MovingTo != "" && before.MovingTo != after.MovingTo {
				return fmt.Errorf("move destination changed before completion or cancellation")
			}
		}
	}
	r.current.Store(next)
	return nil
}
func (r *Router) Revision() uint64 { return r.current.Load().document.Revision }
