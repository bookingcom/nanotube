package in

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/ratelimiter"
	codec "github.com/bookingcom/nanotube/pkg/streamcodec"
	"go.uber.org/zap"
)

// TCPInput owns accepted connections independently of the routing/output drain.
// Send moves one reader at a time; every other connection keeps reading.
type TCPInput struct {
	mu          sync.Mutex
	conns       map[*inputConn]struct{}
	listener    net.Listener
	acceptDone  chan struct{}
	acceptReady chan struct{}
	wg          sync.WaitGroup
	run         func(*inputConn)
	cfg         *conf.Main
	ms          *metrics.Prom
	lg          *zap.Logger
	decoders    *codec.DecoderPool
}

type inputConn struct {
	net.Conn
	mu             sync.Mutex // orders idle-deadline refresh with the handoff interrupt
	transfer       *net.UnixConn
	done           chan struct{}
	input          *TCPInput
	prefix         io.ReadCloser
	readTotal      atomic.Int64
	sending, moved chan struct{}
	moveErr        error
	stateBytes     int64
	releaseBytes   uint64 // Reader-owned storage, reclaimable only after done.
}

func (c *inputConn) moving() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transfer != nil
}
func (c *inputConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	if c.transfer != nil {
		c.mu.Unlock()
		return 0, os.ErrDeadlineExceeded
	}
	_ = c.Conn.SetReadDeadline(time.Now().Add(time.Duration(c.input.cfg.IncomingConnIdleTimeoutSec) * time.Second))
	c.mu.Unlock()
	n, err := c.Conn.Read(p)
	c.readTotal.Add(int64(n))
	return n, err
}

func NewTCPInput(queue chan<- [][]byte, term <-chan struct{}, limiters []*ratelimiter.SlidingWindow, cfg *conf.Main, ms *metrics.Prom, lg *zap.Logger, budget *BatchBudget, stream ...StreamFactory) *TCPInput {
	h := &TCPInput{conns: make(map[*inputConn]struct{}), acceptDone: make(chan struct{}), acceptReady: make(chan struct{}), cfg: cfg, ms: ms, lg: lg}
	h.decoders = codec.NewDecoderPool(cfg.StreamInputMaxFrameBytes, runtime.GOMAXPROCS(0))
	h.run = func(c *inputConn) {
		defer h.wg.Done()
		defer close(c.done)
		defer func() { h.mu.Lock(); delete(h.conns, c); h.mu.Unlock() }()
		defer ms.ActiveTCPConnections.Dec()
		defer c.Conn.Close()
		if c.prefix != nil {
			defer c.prefix.Close()
		}
		scanInput(c, queue, term, limiters, cfg, ms, lg, budget, stream...)
	}
	return h
}

func (h *TCPInput) add(conn net.Conn, prefix io.ReadCloser) {
	c := &inputConn{Conn: conn, input: h, done: make(chan struct{}), prefix: prefix, sending: make(chan struct{}), moved: make(chan struct{})}
	if a, ok := conn.(*readAhead); ok {
		a.tracked = c
	}
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.wg.Add(1)
	h.mu.Unlock()
	h.ms.ActiveTCPConnections.Inc()
	go h.run(c)
}

func (h *TCPInput) Serve(l net.Listener, term <-chan struct{}) {
	h.mu.Lock()
	h.listener = l
	h.mu.Unlock()
	close(h.acceptReady)
	acceptDone := make(chan struct{})
	go func() {
		select {
		case <-term:
			_ = l.Close()
		case <-acceptDone:
		}
	}()
	for {
		conn, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				break
			}
			select {
			case <-term:
				break
			default:
				h.lg.Error("accepting connection failed", zap.Error(err))
				continue
			}
			break
		}
		select {
		case <-term:
			_ = conn.Close()
		default:
			h.ms.InConnectionsTotalTCP.Inc()
			h.add(conn, nil)
		}
	}
	close(acceptDone)
	close(h.acceptDone)
	<-term
	h.mu.Lock()
	for c := range h.conns {
		_ = c.Conn.Close()
	}
	h.mu.Unlock()
	h.wg.Wait()
}

// Fallback leaves normal termination responsible for closing readers. The
// unknown kernel tail is explicit: successful writes have no point receipt.
func (h *TCPInput) Fallback(err error) {
	if h == nil {
		return
	}
	h.mu.Lock()
	n := 0
	for c := range h.conns {
		select {
		case <-c.moved:
			if c.moveErr == nil {
				continue
			}
		default:
		}
		n++
	}
	h.mu.Unlock()
	h.ms.InputHandoffFailures.Inc()
	h.lg.Error("Reload: accepted input handoff failed; using connection-close fallback", zap.Error(err), zap.Int("connections_at_risk", n), zap.String("dropped", "unparsed input and kernel receive tails on connections closed during drain; point count unknown"))
}

// Send runs only after service ownership has committed to the ready successor.
// Failures never kill a child that may already own accepted sockets.
func (h *TCPInput) Send(ctx context.Context, control *net.UnixConn) error {
	if h == nil {
		return errors.New("TCP input handoff unavailable")
	}
	_ = control.SetReadDeadline(time.Now().Add(time.Second))
	var capability [8]byte
	if _, err := io.ReadFull(control, capability[:]); err != nil {
		return fmt.Errorf("successor has no input handoff capability: %w", err)
	}
	if string(capability[:]) != "NTINPUT1" {
		return errors.New("unsupported successor input handoff version")
	}
	_ = control.SetDeadline(time.Time{})
	select {
	case <-h.acceptReady:
	case <-ctx.Done():
		return ctx.Err()
	}
	h.mu.Lock()
	l := h.listener
	h.mu.Unlock()
	if l == nil {
		return errors.New("no TCP listener to hand off")
	}
	_ = l.Close()
	<-h.acceptDone // no late Accept can escape the snapshot
	h.mu.Lock()
	conns := make([]*inputConn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	return h.sendConnections(ctx, control, conns, newHandoffMemory())
}

func (h *TCPInput) sendConnections(ctx context.Context, control *net.UnixConn, conns []*inputConn, memory *handoffMemory) error {
	defer memory.finish()
	for _, c := range conns {
		select {
		case <-c.done:
			continue
		default:
		}
		c.mu.Lock()
		h.lg.Info("Reload: paused input reader", zap.String("peer", c.RemoteAddr().String()))
		c.transfer = control
		_ = c.Conn.SetReadDeadline(time.Unix(1, 0))
		c.mu.Unlock()
		// Give a blocked parser one second to reach its boundary. Once it
		// starts sending, its socket/pipe deadlines scale with its own state.
		timer := time.NewTimer(time.Second)
		select {
		case <-c.sending:
			timer.Stop()
		case <-c.done:
			timer.Stop()
			select {
			case <-c.moved:
				if c.moveErr != nil {
					return c.moveErr
				}
				memory.track(c)
			default:
			}
			continue
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
			return errors.New("input reader did not reach a handoff boundary within one second")
		}
		select {
		case <-c.moved:
		case <-ctx.Done():
			return ctx.Err()
		}
		if c.moveErr != nil {
			return c.moveErr
		}
		// Reader cleanup can still be blocked flushing the old main queue.
		// Account its storage asynchronously; never delay the next transfer.
		memory.track(c)
	}
	_, err := control.Write([]byte{'E'})
	return err
}

// detach supplies the socket before constructing its state. The successor starts
// bounded read-ahead while a large dictionary is serialized/replayed through a
// pipe. No JSON/base64 or aggregate fleet snapshot is retained.
func (c *inputConn) detach(write func(io.Writer) error, workBytes ...uint64) {
	if c == nil {
		return
	}
	start := time.Now()
	close(c.sending)
	err := c.send(write, workBytes...)
	c.moveErr = err
	close(c.moved)
	if err != nil {
		c.input.ms.InputHandoffFailures.Inc()
		c.input.lg.Error("Reload: input connection handoff failed", zap.String("peer", c.RemoteAddr().String()), zap.Error(err), zap.String("dropped", "unparsed connection bytes, including kernel receive tail; point count unknown"))
		return
	}
	c.input.ms.InputHandoffConnections.Inc()
	c.input.lg.Info("Reload: handed off accepted input", zap.String("peer", c.RemoteAddr().String()), zap.Duration("handoff_seconds", time.Since(start)), zap.Int64("state_bytes", c.stateBytes), zap.Int64("wire_bytes_read", c.readTotal.Load()))
}

// reader places inherited wire before new socket reads. The pipe is drained
// continuously by parsing; read-ahead bounds memory and spills excess to disk.
func (c *inputConn) reader() (*bufio.Reader, io.Reader) {
	var r io.Reader = c
	if c.prefix != nil {
		r = io.MultiReader(c.prefix, c)
	}
	return bufio.NewReaderSize(r, 4096), r
}
