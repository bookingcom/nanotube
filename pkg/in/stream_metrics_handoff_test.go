//go:build unix

package in

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	codec "github.com/bookingcom/nanotube/pkg/streamcodec"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

func TestStreamInputDictionaryMetricsHandoff(t *testing.T) {
	for _, flags := range []byte{0, codec.FlagZstd} {
		t.Run(fmt.Sprint(flags), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cfg := conf.MakeDefault()
			cfg.IncomingConnIdleTimeoutSec = 30
			oldMS, nextMS := metrics.New(), metrics.New()
			oldFrame, nextFrame := make(chan struct{}, 4), make(chan struct{}, 4)
			release := make(chan struct{})
			old := NewTCPInput(nil, ctx.Done(), nil, &cfg, oldMS, zap.NewNop(), nil, func() StreamSink {
				s := &boundAccountingSink{}
				s.flush = func() {
					if s.bytes == 0 {
						<-release
						return
					} // Hold the old reader's final flush.
					oldFrame <- struct{}{}
				}
				return s
			})
			next := NewTCPInput(nil, ctx.Done(), nil, &cfg, nextMS, zap.NewNop(), nil, func() StreamSink {
				s := &boundAccountingSink{}
				s.flush = func() {
					if s.bytes > 0 {
						nextFrame <- struct{}{}
					}
				}
				return s
			})
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			client, err := net.Dial("tcp", l.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			server, err := l.Accept()
			if err != nil {
				client.Close()
				t.Fatal(err)
			}
			a, b := handoffPair(t)
			t.Cleanup(func() {
				cancel()
				client.Close()
				server.Close()
				a.Close()
				b.Close()
				close(release)
				old.wg.Wait()
				next.wg.Wait()
			})
			old.add(server, nil)
			e, err := codec.NewEncoder(codec.Options{Flags: flags})
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			wire, _ := codec.AppendPreamble(nil, flags)
			for _, name := range []string{"one", "two"} {
				if err := e.Add([]byte(name), 1, 2); err != nil {
					t.Fatal(err)
				}
			}
			wire, err = e.Flush(wire)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Write(wire); err != nil {
				t.Fatal(err)
			}
			await := func(ch <-chan struct{}) {
				t.Helper()
				select {
				case <-ch:
				case <-ctx.Done():
					t.Fatal("input did not reach frame boundary")
				}
			}
			await(oldFrame)
			if testutil.ToFloat64(oldMS.StreamInputDictionaryNames) != 2 {
				t.Fatal("old dictionary not counted")
			}
			old.mu.Lock()
			var conn *inputConn
			for c := range old.conns {
				conn = c
			}
			old.mu.Unlock()
			received := make(chan error, 1)
			go func() { received <- next.Receive(ctx, b) }()
			var capability [8]byte
			if _, err := io.ReadFull(a, capability[:]); err != nil {
				t.Fatal(err)
			}
			sent := make(chan error, 1)
			go func() { sent <- old.sendConnections(ctx, a, []*inputConn{conn}, newHandoffMemory()) }()
			// This event occurs during state replay, before the sender can return
			// from detach. It catches overlap that a final-value assertion misses.
			await(nextFrame)
			if got := testutil.ToFloat64(oldMS.StreamInputDictionaryNames) + testutil.ToFloat64(nextMS.StreamInputDictionaryNames); got != 2 {
				t.Fatalf("handoff double-counted live ids: %g", got)
			}
			for _, result := range []<-chan error{sent, received} {
				select {
				case err := <-result:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal("handoff blocked")
				}
			}
			select {
			case <-conn.done:
				t.Fatal("old reader should still be flushing")
			default:
			}
			if testutil.ToFloat64(oldMS.StreamInputDictionaryNames) != 0 || testutil.ToFloat64(nextMS.StreamInputDictionaryNames) != 2 {
				t.Fatal("dictionary ownership did not move")
			}
			if testutil.ToFloat64(oldMS.StreamInputNamesDefined.WithLabelValues("data")) != 2 || testutil.ToFloat64(nextMS.StreamInputNamesDefined.WithLabelValues("data")) != 0 || testutil.ToFloat64(nextMS.StreamInputNamesDefined.WithLabelValues("state")) != 2 {
				t.Fatal("snapshot definitions were not counted separately")
			}
			if err := e.Add([]byte("three"), 3, 4); err != nil {
				t.Fatal(err)
			}
			wire, err = e.Flush(nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Write(wire); err != nil {
				t.Fatal(err)
			}
			await(nextFrame)
			if testutil.ToFloat64(nextMS.StreamInputDictionaryNames) != 3 || testutil.ToFloat64(nextMS.StreamInputNamesDefined.WithLabelValues("data")) != 1 {
				t.Fatal("new wire definition was not counted after replay")
			}
			client.Close()
			next.wg.Wait()
			if testutil.ToFloat64(nextMS.StreamInputDictionaryNames) != 0 {
				t.Fatal("successor close leaked names")
			}
		})
	}
}
