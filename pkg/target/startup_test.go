package target

import (
	"fmt"
	"math/rand"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"go.uber.org/zap"
)

// gateJitter exposes whether connection setup waits for jitter before even
// trying a healthy destination. Returning zero also lets the old path finish
// promptly after the test detects that ordering error.
type gateJitter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *gateJitter) Seed(int64)   {}
func (s *gateJitter) Int63() int64 { s.once.Do(func() { close(s.entered) }); <-s.release; return 0 }

func TestTCPFirstDialDoesNotWaitForReconnectJitter(t *testing.T) {
	for _, multi := range []bool{false, true} {
		t.Run(fmt.Sprint(multi), func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			accepted := make(chan net.Conn, 1)
			go func() {
				c, err := listener.Accept()
				if err == nil {
					accepted <- c
				}
			}()
			source := &gateJitter{entered: make(chan struct{}), release: make(chan struct{})}
			cfg := conf.MakeDefault()
			cfg.TargetConnectionJitter = true
			host := conf.Host{Name: "127.0.0.1", Port: uint16(listener.Addr().(*net.TCPAddr).Port), MTCP: 1}
			var connect func()
			var disconnect func()
			if multi {
				host.MTCP = 2
				h := ConstructHostMTCP("test", cfg, host, zap.NewNop(), metrics.New())
				h.rnd = rand.New(source)
				connect = func() { h.ensureConnection(&h.MTCPs[0]) }
				disconnect = func() { _ = h.MTCPs[0].Close() }
			} else {
				h := NewHostTCP("test", cfg, host, zap.NewNop(), metrics.New())
				h.rnd = rand.New(source)
				connect = h.ensureConnection
				disconnect = func() { _ = h.Conn.Close() }
			}
			done := make(chan struct{})
			go func() { connect(); close(done) }()
			select {
			case <-source.entered:
				t.Error("healthy first dial was held behind reconnect jitter")
			case c := <-accepted:
				defer c.Close()
			case <-time.After(time.Second):
				t.Error("healthy initial connection did not start")
			}
			// Release a broken implementation so its goroutine can be joined.
			if t.Failed() {
				close(source.release)
				<-done
				disconnect()
				return
			}
			<-done
			disconnect()
			// A warm reconnect must still consult the same backoff source.
			done = make(chan struct{})
			go func() { connect(); close(done) }()
			select {
			case <-source.entered:
			case <-done:
				t.Error("warm reconnect skipped reconnect jitter")
			case <-time.After(time.Second):
				t.Error("warm reconnect did not enter reconnect jitter")
			}
			close(source.release)
			<-done
			disconnect()
		})
	}
}
