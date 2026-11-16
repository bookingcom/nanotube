package placement

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/bookingcom/nanotube/internal/v2placement"
	"github.com/bookingcom/nanotube/internal/v2wire"
	"github.com/bookingcom/nanotube/pkg/rec"
)

// DurableConfig bounds the entire relay spool, including held and unacknowledged
// records. Root must be on a local filesystem honoring file/directory fsync.
// This reference implementation syncs a bounded snapshot per mutation. It is
// intentionally not a line-rate WAL implementation.
type DurableConfig struct {
	Root       string
	MaxRecords int
	MaxBytes   int64
}
type diskGroup struct {
	Sent, Acked       uint64
	Moving, Releasing bool
	Held              []v2wire.Frame
}
type diskEnvelope struct {
	State  json.RawMessage
	SHA256 string
}
type diskState struct {
	Version  int
	Document v2placement.Document
	Replica  string
	Lane     v2placement.Lane
	Limit    int
	Groups   map[string]diskGroup
	Queue    []v2wire.Frame
}

// Durable owns a process-exclusive snapshot and all asynchronous ACK handling.
// Do not call Close until Run returns. A persistence failure is sticky: dispatch
// stops until reopen, so uncertain writes can never be mistaken for a drain.
type Durable struct {
	mu      sync.Mutex
	cfg     DurableConfig
	router  *Router
	queue   []v2wire.Frame
	failed  error
	lock    *os.File
	wake    chan struct{}
	running bool
	failure chan struct{}
}

func (d *Durable) Epochs() bool { return true }
func frameFor(x Delivery) (v2wire.Frame, error) {
	v, ts, ok := x.Record.Values()
	if !ok {
		return v2wire.Frame{}, errors.New("invalid numeric placement record")
	}
	return v2wire.Frame{Kind: v2wire.Data, Stream: v2wire.Stream{Group: x.Group, Replica: x.Replica, Store: x.Store, MapDigest: x.MapDigest, Revision: x.Revision, Epoch: x.Epoch, Producer: x.Lane.Producer, Lane: x.Lane.Stream}, Sequence: x.Sequence, VShard: x.VShard, Name: string(x.Record.Path), Bits: math.Float64bits(v), Timestamp: ts}, nil
}
func recordFor(f v2wire.Frame) *rec.RecBytes {
	return rec.NewTyped([]byte(f.Name), math.Float64frombits(f.Bits), f.Timestamp)
}

// Enqueue runs under the durable mutation lock. Acceptance is published to Run
// only after the router state and entire queue have been fsynced together.
func (d *Durable) Enqueue(x Delivery) bool {
	f, e := frameFor(x)
	if e != nil || f.Validate() != nil || len(d.queue) >= d.cfg.MaxRecords {
		return false
	}
	d.queue = append(d.queue, f)
	return true
}
func OpenDurable(doc v2placement.Document, replica string, lane v2placement.Lane, holdLimit int, cfg DurableConfig) (r *Router, d *Durable, err error) {
	if cfg.Root == "" || cfg.MaxRecords < 1 || cfg.MaxBytes < 4096 {
		return nil, nil, errors.New("durable placement requires spool root and positive record/byte bounds")
	}
	if err = os.MkdirAll(cfg.Root, 0700); err != nil {
		return nil, nil, err
	}
	parent, e := os.Open(filepath.Dir(cfg.Root))
	if e != nil {
		return nil, nil, e
	}
	e = parent.Sync()
	parent.Close()
	if e != nil {
		return nil, nil, e
	}
	lock, e := os.OpenFile(filepath.Join(cfg.Root, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, nil, e
	}
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		lock.Close()
		return nil, nil, fmt.Errorf("placement spool already owned: %w", e)
	}
	d = &Durable{cfg: cfg, lock: lock, wake: make(chan struct{}, 1), failure: make(chan struct{})}
	defer func() {
		if err != nil {
			d.Close()
		}
	}()
	for _, s := range doc.Stores {
		if s.Blackhole {
			return nil, d, errors.New("durable placed output requires acknowledged live stores")
		}
	}
	file, e := os.Open(filepath.Join(cfg.Root, "state.json"))
	var data []byte
	if e == nil {
		data, e = io.ReadAll(io.LimitReader(file, cfg.MaxBytes+1))
		e = errors.Join(e, file.Close())
	}
	if errors.Is(e, os.ErrNotExist) {
		r, err = New(doc, replica, lane, d, holdLimit)
		if err != nil {
			return nil, d, err
		}
		d.router = r
		err = d.save()
		if err != nil {
			return nil, d, err
		}
	} else if e != nil {
		return nil, d, e
	} else {
		if int64(len(data)) > cfg.MaxBytes {
			return nil, d, errors.New("persisted placement spool exceeds byte bound")
		}
		var envelope diskEnvelope
		if e = json.Unmarshal(data, &envelope); e != nil {
			return nil, d, e
		}
		digest := sha256.Sum256(envelope.State)
		if hex.EncodeToString(digest[:]) != envelope.SHA256 {
			return nil, d, errors.New("placement spool checksum mismatch")
		}
		var state diskState
		dec := json.NewDecoder(bytes.NewReader(envelope.State))
		dec.DisallowUnknownFields()
		if e = dec.Decode(&state); e != nil {
			return nil, d, e
		}
		if dec.Decode(new(any)) != io.EOF {
			return nil, d, errors.New("trailing spool state")
		}
		if state.Version != 1 || state.Document.Digest() != doc.Digest() || state.Replica != replica || state.Lane != lane || state.Limit != holdLimit {
			return nil, d, errors.New("placement spool identity/map differs; reconcile before restart")
		}
		s, e := compile(doc, replica, lane, nil)
		if e != nil {
			return nil, d, e
		}
		r = &Router{replica: replica, lane: lane, sink: d, limit: holdLimit}
		r.current.Store(s)
		d.router = r
		d.queue = state.Queue
		if len(state.Groups) != len(s.groups) {
			return nil, d, errors.New("incomplete spool groups")
		}
		total := len(d.queue)
		for id, i := range s.groups {
			g, ok := state.Groups[id]
			if !ok || g.Acked > g.Sent || len(g.Held) > holdLimit || (!g.Moving && (g.Releasing || len(g.Held) > 0)) {
				return nil, d, errors.New("invalid spool barrier")
			}
			st := s.routes[i].state
			st.sent = g.Sent
			st.acked = g.Acked
			st.moving = g.Moving
			st.releasing = g.Releasing
			for _, f := range g.Held {
				rt, v := s.selectRoute(recordFor(f))
				if rt.group != id || v != f.VShard || f.Name == "" {
					return nil, d, errors.New("held placement identity mismatch")
				}
				st.held = append(st.held, pending{recordFor(f), f.VShard})
			}
			total += len(g.Held)
			r.Stats.Pending.Add(int64(len(g.Held)))
		}
		if total > cfg.MaxRecords {
			return nil, d, errors.New("persisted placement spool exceeds record bound")
		}
		next := make(map[string]uint64)
		for id, i := range s.groups {
			next[id] = s.routes[i].state.acked
		}
		for _, f := range d.queue {
			if e = f.Validate(); e != nil {
				return nil, d, e
			}
			i, ok := s.groups[f.Stream.Group]
			if !ok {
				return nil, d, errors.New("unknown queued group")
			}
			rt := &s.routes[i]
			selected, v := s.selectRoute(recordFor(f))
			if selected.group != rt.group || v != f.VShard || f.Kind != v2wire.Data || f.Stream.Replica != replica || f.Stream.Producer != lane.Producer || f.Stream.Lane != lane.Stream || f.Stream.Store != rt.replica.Writer || f.Stream.Epoch != rt.replica.Epoch || f.Stream.Revision != doc.Revision || f.Stream.MapDigest != doc.Digest() || f.Sequence != next[rt.group]+1 {
				return nil, d, errors.New("queued placement position mismatch")
			}
			next[rt.group] = f.Sequence
		}
		for id, i := range s.groups {
			if next[id] != s.routes[i].state.sent {
				return nil, d, errors.New("spool missing unacknowledged suffix")
			}
		}
	}
	r.Stats.Queued.Store(int64(len(d.queue)))
	r.durable = d
	return r, d, nil
}
func (d *Durable) snapshot() diskState {
	r := d.router
	s := r.current.Load()
	out := diskState{Version: 1, Document: s.document, Replica: r.replica, Lane: r.lane, Limit: r.limit, Groups: make(map[string]diskGroup, len(s.groups)), Queue: d.queue}
	for id, i := range s.groups {
		g := s.routes[i].state
		st := diskGroup{Sent: g.sent, Acked: g.acked, Moving: g.moving, Releasing: g.releasing}
		for _, p := range g.held {
			st.Held = append(st.Held, v2wire.Frame{Name: string(p.record.Path), VShard: p.vshard})
			v, ts, _ := p.record.Values()
			st.Held[len(st.Held)-1].Bits = math.Float64bits(v)
			st.Held[len(st.Held)-1].Timestamp = ts
		}
		out.Groups[id] = st
	}
	return out
}
func (d *Durable) save() error {
	raw, e := json.Marshal(d.snapshot())
	if e != nil {
		return e
	}
	h := sha256.Sum256(raw)
	b, e := json.Marshal(diskEnvelope{State: raw, SHA256: hex.EncodeToString(h[:])})
	if e != nil {
		return e
	}
	if int64(len(b)) > d.cfg.MaxBytes {
		return errors.New("placement spool byte bound reached")
	}
	path := filepath.Join(d.cfg.Root, "state.json")
	f, e := os.OpenFile(path+".new", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return e
	}
	if e = os.Rename(path+".new", path); e != nil {
		return e
	}
	dir, e := os.Open(d.cfg.Root)
	if e != nil {
		return e
	}
	e = dir.Sync()
	return errors.Join(e, dir.Close())
}
func (d *Durable) commit(e error) error {
	if e != nil {
		return e
	}
	if e = d.save(); e != nil {
		d.failed = e
		close(d.failure)
		d.router.Stats.PersistenceErrors.Add(1)
		return e
	}
	d.router.Stats.Queued.Store(int64(len(d.queue)))
	select {
	case d.wake <- struct{}{}:
	default:
	}
	return nil
}
func (d *Durable) push(p *rec.RecBytes) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failed != nil {
		return d.failed
	}
	if p == nil || len(p.Path) > v2wire.MaxFrame-2048 || !utf8.Valid(p.Path) {
		return errors.New("nil record")
	}
	if _, _, ok := p.Values(); !ok {
		return errors.New("invalid numeric record")
	}
	s := d.router.current.Load()
	count := len(d.queue)
	for _, rt := range s.routes {
		count += len(rt.state.held)
	}
	if count >= d.cfg.MaxRecords {
		d.router.Stats.Dropped.Add(1)
		return ErrHeldFull
	}
	v, ts, _ := p.Values()
	owned := rec.NewTyped(bytes.Clone(p.Path), v, ts)
	return d.commit(d.router.push(owned))
}
func (d *Durable) beginMove(group string) (Barrier, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failed != nil {
		return Barrier{}, d.failed
	}
	b, e := d.router.beginMove(group)
	if e != nil {
		return b, e
	}
	return b, d.commit(nil)
}
func (d *Durable) acknowledge(group string, epoch, seq uint64) error {
	return errors.New("durable placement ACKs are owned by authenticated output")
}
func (d *Durable) adopt(doc v2placement.Document, a []v2placement.Activation) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failed != nil {
		return d.failed
	}
	if len(d.queue) != 0 {
		d.router.Stats.Rejected.Add(1)
		return errors.New("map adoption requires acknowledged queue drain")
	}
	return d.commit(d.router.adopt(doc, a))
}
func (d *Durable) retryHeld(group string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failed != nil {
		return d.failed
	}
	return d.commit(d.router.retryHeld(group))
}
func (d *Durable) acceptAck(f v2wire.Frame) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failed != nil {
		return d.failed
	}
	for i, q := range d.queue {
		if q == f {
			if e := d.router.acknowledge(q.Stream.Group, q.Stream.Epoch, q.Sequence); e != nil {
				return e
			}
			copy(d.queue[i:], d.queue[i+1:])
			d.queue[len(d.queue)-1] = v2wire.Frame{}
			d.queue = d.queue[:len(d.queue)-1]
			d.router.Stats.Acknowledged.Add(1)
			return d.commit(nil)
		}
	}
	return errors.New("ACK has no pending delivery")
}

// Run sends independent store streams. A failed store cannot block another
// store's worker. Frames remain on disk until an exact authenticated durable ACK.
// The key authenticates one producer incarnation and must be provisioned only
// to that producer and its configured store endpoints. No confidentiality is
// provided; wrap dial in TLS when that is required.
func (d *Durable) Run(ctx context.Context, key []byte, dial func(context.Context, string) (net.Conn, error)) error {
	if len(key) < 32 {
		return errors.New("placement key must contain at least 32 bytes")
	}
	d.mu.Lock()
	if d.failed != nil {
		e := d.failed
		d.mu.Unlock()
		return e
	}
	if d.running || d.lock == nil {
		d.mu.Unlock()
		return errors.New("placement output already running or closed")
	}
	d.running = true
	d.mu.Unlock()
	defer func() { d.mu.Lock(); d.running = false; d.mu.Unlock() }()
	key = append([]byte(nil), key...)
	if dial == nil {
		dial = func(ctx context.Context, addr string) (net.Conn, error) {
			return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", addr)
		}
	}
	s := d.router.current.Load()
	var wg sync.WaitGroup
	for _, store := range s.document.Stores {
		store := store
		wg.Add(1)
		go func() {
			defer wg.Done()
			var c net.Conn
			defer func() {
				if c != nil {
					c.Close()
				}
			}()
			tick := time.NewTicker(100 * time.Millisecond)
			defer tick.Stop()
			for {
				if ctx.Err() != nil {
					return
				}
				d.mu.Lock()
				var f v2wire.Frame
				found := false
				if d.failed == nil {
					for _, q := range d.queue {
						if q.Stream.Store == store.ID {
							f = q
							found = true
							break
						}
					}
				}
				d.mu.Unlock()
				if found {
					var e error
					if c == nil {
						c, e = dial(ctx, store.Write)
					}
					if e == nil {
						_ = c.SetDeadline(time.Now().Add(time.Second))
						e = v2wire.Write(c, f, key)
					}
					if e == nil {
						var ack v2wire.Frame
						ack, e = v2wire.Read(c, key)
						if e == nil && ack != f.Acknowledgement() {
							e = errors.New("placement ACK identity mismatch")
						}
						if e == nil {
							e = d.acceptAck(f)
						}
					}
					if e == nil {
						continue
					}
					d.router.Stats.OutputErrors.Add(1)
					if c != nil {
						c.Close()
						c = nil
					}
				}
				select {
				case <-ctx.Done():
					return
				case <-d.failure:
					return
				case <-d.wake:
				case <-tick.C:
				}
			}
		}()
	}
	select {
	case <-ctx.Done():
	case <-d.failure:
	}
	wg.Wait()
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.failed
}
func (d *Durable) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lock == nil {
		return nil
	}
	e := syscall.Flock(int(d.lock.Fd()), syscall.LOCK_UN)
	e = errors.Join(e, d.lock.Close())
	d.lock = nil
	d.failed = errors.New("placement spool closed")
	return e
}
