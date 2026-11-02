package in

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Read-ahead begins before state generation. Sixteen 64 KiB blocks fit in
// memory; excess bytes spill to an unlinked file during a long snapshot/replay.
// This bounds memory without turning large-state reconstruction into a client
// write stall. Once replay ends the pump stops and the handler returns to TCP.
type readAhead struct {
	net.Conn
	tracked           *inputConn
	mu                sync.Mutex
	readable          *sync.Cond
	blocks            [][]byte
	current           []byte
	spill             *os.File
	diskRead, diskEnd int64
	stop              chan struct{}
	done              chan struct{}
	once              sync.Once
	ready             atomic.Bool
	ended             bool
	err               error
	direct            bool
}

func newReadAhead(c net.Conn) *readAhead {
	a := &readAhead{Conn: c, stop: make(chan struct{}), done: make(chan struct{})}
	a.readable = sync.NewCond(&a.mu)
	go a.pump()
	return a
}
func (a *readAhead) pump() {
	defer close(a.done)
	for {
		block := make([]byte, 64<<10)
		n, err := a.Conn.Read(block)
		a.mu.Lock()
		if n > 0 {
			if a.spill == nil && len(a.blocks) < 16 {
				a.blocks = append(a.blocks, block[:n])
			} else {
				var storageErr error
				if a.spill == nil {
					a.spill, storageErr = os.CreateTemp("", "nt-ahead-")
					if storageErr == nil {
						_ = os.Remove(a.spill.Name())
					}
				}
				if storageErr == nil {
					var written int
					written, storageErr = a.spill.WriteAt(block[:n], a.diskEnd)
					a.diskEnd += int64(written)
				}
				if storageErr != nil {
					err = storageErr
				}
			}
		}
		select {
		case <-a.stop:
			a.ended = true
		default:
		}
		if err != nil {
			a.err = err
			a.ended = true
		}
		a.readable.Broadcast()
		ended := a.ended
		a.mu.Unlock()
		if ended {
			return
		}
	}
}
func (a *readAhead) pause() {
	a.once.Do(func() { close(a.stop); _ = a.Conn.SetReadDeadline(time.Unix(1, 0)) })
	<-a.done
}
func (a *readAhead) finish() { a.ready.Store(true) }
func (a *readAhead) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	if a.ready.Load() && !a.direct {
		a.pause()
		a.direct = true
		if a.tracked != nil {
			a.tracked.mu.Lock()
			if a.tracked.transfer == nil {
				_ = a.Conn.SetReadDeadline(time.Now().Add(time.Duration(a.tracked.input.cfg.IncomingConnIdleTimeoutSec) * time.Second))
			}
			a.tracked.mu.Unlock()
		}
	}
	if len(a.current) > 0 {
		n := copy(b, a.current)
		a.current = a.current[n:]
		return n, nil
	}
	a.mu.Lock()
	for len(a.blocks) == 0 && a.diskRead == a.diskEnd && !a.ended {
		a.readable.Wait()
	}
	if len(a.blocks) > 0 {
		a.current = a.blocks[0]
		a.blocks[0] = nil
		a.blocks = a.blocks[1:]
		n := copy(b, a.current)
		a.current = a.current[n:]
		a.mu.Unlock()
		return n, nil
	}
	if a.diskRead < a.diskEnd {
		n, err := a.spill.ReadAt(b[:min(int64(len(b)), a.diskEnd-a.diskRead)], a.diskRead)
		a.diskRead += int64(n)
		a.mu.Unlock()
		return n, err
	}
	err := a.err
	if a.spill != nil {
		_ = a.spill.Close()
		a.spill = nil
	}
	a.mu.Unlock()
	if a.direct && (err == nil || errors.Is(err, os.ErrDeadlineExceeded)) {
		return a.Conn.Read(b)
	}
	if err != nil {
		return 0, err
	}
	return 0, io.EOF
}
func (a *readAhead) pending(w io.Writer) error {
	a.pause()
	if _, err := w.Write(a.current); err != nil {
		return err
	}
	a.current = nil
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, block := range a.blocks {
		if _, err := w.Write(block); err != nil {
			return err
		}
	}
	a.blocks = nil
	if a.spill != nil {
		_, err := io.Copy(w, io.NewSectionReader(a.spill, a.diskRead, a.diskEnd-a.diskRead))
		return err
	}
	return nil
}
func (a *readAhead) Close() error {
	err := a.Conn.Close()
	a.pause()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.spill != nil {
		_ = a.spill.Close()
		a.spill = nil
	}
	return err
}
