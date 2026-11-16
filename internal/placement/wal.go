package placement

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/bookingcom/nanotube/internal/v2wire"
)

type walEvent struct {
	Kind  string
	Group string `json:",omitempty"`
	Frame v2wire.Frame
}

var walCRC = crc32.MakeTable(crc32.Castagnoli)

func (d *Durable) markCommitted() {
	clear(d.committed)
	for _, f := range d.queue {
		if f.Sequence > d.committed[f.Stream] {
			d.committed[f.Stream] = f.Sequence
		}
	}
	d.synced = d.walID
	d.router.Stats.Queued.Store(int64(len(d.queue)))
	select {
	case d.wake <- struct{}{}:
	default:
	}
}
func syncDir(root string) error {
	f, e := os.Open(root)
	if e != nil {
		return e
	}
	return errors.Join(f.Sync(), f.Close())
}
func (d *Durable) openWAL() error {
	path := filepath.Join(d.cfg.Root, fmt.Sprintf("%020d.wal", d.walID+1))
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	d.wal = f
	return syncDir(d.cfg.Root)
}
func (d *Durable) flush() error {
	if len(d.buffer) == 0 {
		return d.failed
	}
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	if n, e := d.wal.Write(d.buffer); e != nil {
		return d.fail(e)
	} else if n != len(d.buffer) {
		return d.fail(io.ErrShortWrite)
	}
	if e := d.wal.Sync(); e != nil {
		return d.fail(e)
	}
	d.syncs++
	d.walBytes += int64(len(d.buffer))
	d.buffer = d.buffer[:0]
	d.markCommitted()
	d.cond.Broadcast()
	return nil
}

// checkpoint publishes the complete current state before removing older WAL
// segments. Unacknowledged data is copied into the checkpoint; ACKed prefixes
// disappear only after this file and its rename are durable.
func (d *Durable) checkpoint() error {
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	if e := d.save(); e != nil {
		return e
	}
	d.syncs++
	if d.wal != nil {
		if e := d.wal.Close(); e != nil {
			return e
		}
		d.wal = nil
	}
	paths, e := filepath.Glob(filepath.Join(d.cfg.Root, "*.wal"))
	if e != nil {
		return e
	}
	for _, p := range paths {
		if e = os.Remove(p); e != nil {
			return e
		}
	}
	if e = syncDir(d.cfg.Root); e != nil {
		return e
	}
	d.buffer = d.buffer[:0]
	d.walBytes = 0
	if e = d.openWAL(); e != nil {
		return e
	}
	d.markCommitted()
	d.cond.Broadcast()
	return nil
}

// persist runs with mu held. Wait releases mu, allowing independent callers to
// share one sync. A byte threshold flushes immediately; the timer bounds idle
// latency. No caller or network worker observes success before the sync.
func (d *Durable) persist(event walEvent) error { return d.appendEvent(event, true) }
func (d *Durable) appendEvent(event walEvent, wait bool) error {
	payload, e := json.Marshal(event)
	if e != nil {
		return d.fail(e)
	}
	if d.walID == ^uint64(0) {
		return d.fail(errors.New("WAL sequence exhausted"))
	}
	d.walID++
	if d.snapshotBytes+d.walBytes+int64(len(d.buffer)+len(payload)+16) > d.cfg.MaxBytes || d.walBytes+int64(len(d.buffer)) >= d.cfg.SegmentBytes {
		return d.fail(d.checkpoint())
	}
	start := len(d.buffer)
	d.buffer = append(d.buffer, make([]byte, 16)...)
	d.buffer = append(d.buffer, payload...)
	binary.BigEndian.PutUint32(d.buffer[start:], uint32(len(payload)))
	binary.BigEndian.PutUint64(d.buffer[start+8:], d.walID)
	binary.BigEndian.PutUint32(d.buffer[start+4:], crc32.Checksum(d.buffer[start+8:], walCRC))
	target := d.walID
	if len(d.buffer) >= d.cfg.CommitBytes {
		if e = d.flush(); e != nil {
			return e
		}
	} else if d.timer == nil {
		d.timer = time.AfterFunc(d.cfg.CommitInterval, func() {
			d.mu.Lock()
			defer d.mu.Unlock()
			if d.failed == nil {
				_ = d.flush()
			}
		})
	}
	for wait && d.synced < target && d.failed == nil {
		d.cond.Wait()
	}
	return d.failed
}
func applyEvent(s *diskState, e walEvent) error {
	f := e.Frame
	switch e.Kind {
	case "append":
		g, ok := s.Groups[f.Stream.Group]
		if !ok || f.Sequence != g.Sent+1 {
			return errors.New("WAL append gap")
		}
		g.Sent = f.Sequence
		s.Groups[f.Stream.Group] = g
		s.Queue = append(s.Queue, f)
	case "hold":
		g, ok := s.Groups[e.Group]
		if !ok || !g.Moving {
			return errors.New("WAL hold outside move")
		}
		g.Held = append(g.Held, f)
		s.Groups[e.Group] = g
	case "ack":
		g, ok := s.Groups[f.Stream.Group]
		if !ok || f.Sequence != g.Acked+1 {
			return errors.New("WAL ACK gap")
		}
		found := false
		for i, q := range s.Queue {
			if q == f {
				s.Queue = append(s.Queue[:i], s.Queue[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			return errors.New("WAL ACK without payload")
		}
		g.Acked = f.Sequence
		s.Groups[f.Stream.Group] = g
	default:
		return errors.New("unknown WAL event")
	}
	return nil
}
func (d *Durable) replay(state *diskState) error {
	paths, e := filepath.Glob(filepath.Join(d.cfg.Root, "*.wal"))
	if e != nil {
		return e
	}
	sort.Strings(paths)
	d.walID = state.WAL
	total := int64(0)
	if info, err := os.Stat(filepath.Join(d.cfg.Root, "state.json")); err == nil {
		total = info.Size()
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		total += info.Size()
		if total > d.cfg.MaxBytes && total-d.cfg.MaxBytes > d.cfg.MaxBytes {
			return errors.New("persisted WAL exceeds byte bound")
		}
	}

	for index, path := range paths {
		f, e := os.OpenFile(path, os.O_RDWR, 0600)
		if e != nil {
			return e
		}
		var offset int64
		for {
			var head [16]byte
			_, e = io.ReadFull(f, head[:])
			if e == io.EOF {
				e = nil
				break
			}
			if e == io.ErrUnexpectedEOF && index == len(paths)-1 {
				e = f.Truncate(offset)
				break
			}
			if e != nil {
				f.Close()
				return e
			}
			n := binary.BigEndian.Uint32(head[:])
			id := binary.BigEndian.Uint64(head[8:])
			if n == 0 || n > v2wire.MaxFrame+2048 {
				f.Close()
				return errors.New("WAL event size")
			}
			payload := make([]byte, n)
			_, e = io.ReadFull(f, payload)
			if (e == io.ErrUnexpectedEOF || e == io.EOF) && index == len(paths)-1 {
				e = f.Truncate(offset)
				break
			}
			if e != nil {
				f.Close()
				return e
			}
			h := crc32.New(walCRC)
			h.Write(head[8:])
			h.Write(payload)
			if h.Sum32() != binary.BigEndian.Uint32(head[4:]) {
				f.Close()
				return errors.New("WAL checksum mismatch")
			}
			offset += int64(16 + n)
			if id <= state.WAL {
				continue
			}
			if id != d.walID+1 {
				f.Close()
				return errors.New("WAL position gap")
			}
			var event walEvent
			if e = json.Unmarshal(payload, &event); e != nil {
				f.Close()
				return e
			}
			if e = applyEvent(state, event); e != nil {
				f.Close()
				return e
			}
			d.walID = id
		}
		if e == nil {
			e = f.Sync()
		}
		e = errors.Join(e, f.Close())
		if e != nil {
			return e
		}
		info, e := os.Stat(path)
		if e != nil {
			return e
		}
		d.walBytes += info.Size()
	}
	// Recovered events become a checkpoint before accepting any new group. This
	// also removes a torn tail and prevents reusing an uncommitted segment name.
	state.WAL = d.walID
	return nil
}
