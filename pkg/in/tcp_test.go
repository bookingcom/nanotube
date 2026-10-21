package in

import (
	"bytes"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/ratelimiter"
	"go.uber.org/zap"
)

// pipeListener lets a completed client Write prove that the reader has received
// the records before shutdown, without waiting for the batch's flush interval.
type pipeListener struct {
	connections chan net.Conn
	closed      chan struct{}
	once        sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.connections:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return &net.TCPAddr{} }

func TestTCPShutdownDrainsOpenReaders(t *testing.T) {
	for _, limited := range []bool{false, true} {
		name := "idle-reader"
		if limited {
			name = "rate-limited-reader"
		}
		t.Run(name, func(t *testing.T) {
			listener := &pipeListener{connections: make(chan net.Conn, 1), closed: make(chan struct{})}
			server, client := net.Pipe()
			defer client.Close()
			defer listener.Close()
			listener.connections <- server
			queue := make(chan [][]byte, 10)
			stop := make(chan struct{})
			cfg := &conf.Main{MainQueueBatchSize: 100, BatchFlushPerdiodSec: 3600, IncomingConnIdleTimeoutSec: 3600, RateLimiterRetryDurationMs: 60000}
			ms := metrics.New()
			var limits []*ratelimiter.SlidingWindow
			if limited {
				limits = []*ratelimiter.SlidingWindow{ratelimiter.NewSlidingWindowRateLimiter(time.Hour, 0, nil)}
			}
			var wg sync.WaitGroup
			wg.Add(1)
			go AcceptAndListenTCPBuf(listener, queue, stop, limits, cfg, &wg, ms, zap.NewNop())
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			data := []byte("first 1 100\nsecond 2 101\n")
			if limited {
				data = []byte("first 1 100\n")
			}
			if err := client.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := client.Write(data); err != nil {
				t.Fatal(err)
			}
			close(stop)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("shutdown did not interrupt an open input reader")
			}
			close(queue)
			var got []byte
			for batch := range queue {
				for _, record := range batch {
					got = append(got, record...)
					got = append(got, '\n')
				}
			}
			if !bytes.Equal(got, data) {
				t.Fatalf("shutdown lost or duplicated records: got %q, want %q", got, data)
			}
		})
	}
}

func TestBatchCloseFlushesWithoutWaitingForPeriod(t *testing.T) {
	queue := make(chan [][]byte, 1)
	batch := NewBatchChan(queue, 100, 3600, metrics.New())
	batch.Push([]byte("pending 1 100"))
	done := make(chan struct{})
	go func() { batch.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("batch close waited for the periodic timer")
	}
	select {
	case records := <-queue:
		if len(records) != 1 || string(records[0]) != "pending 1 100" {
			t.Fatalf("unexpected final batch: %q", records)
		}
	default:
		t.Fatal("batch close lost the partially filled batch")
	}
}
