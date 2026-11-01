package target

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

// Child relay process for the opt-in harness. The parent SIGKILLs one relay,
// leaving all queueing, connection failure detection and replay in real senders.
func TestStickyHarnessRelay(t *testing.T) {
	addr := os.Getenv("NANOTUBE_HARNESS_RELAY")
	if addr == "" {
		t.Skip("harness helper")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("READY", ln.Addr())
	var out sync.Mutex
	for {
		c, err := ln.Accept()
		if err != nil {
			t.Fatal(err)
		}
		if os.Getenv("NANOTUBE_HARNESS_FIN") != "1" {
			if err := c.(*net.TCPConn).SetLinger(0); err != nil {
				t.Fatal(err)
			}
		}
		go func(c net.Conn) {
			defer c.Close()
			scanner := bufio.NewScanner(c)
			for scanner.Scan() {
				out.Lock()
				fmt.Println(scanner.Text())
				out.Unlock()
			}
		}(c)
	}
}

type harnessCounts struct {
	mu       sync.Mutex
	received map[int]int
	perRelay map[string]int
}

type harnessRelay struct {
	cmd  *exec.Cmd
	addr string
	done chan struct{}
}

func startHarnessRelay(t *testing.T, addr string, counts *harnessCounts) *harnessRelay {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestStickyHarnessRelay$")
	cmd.Env = append(os.Environ(), "NANOTUBE_HARNESS_RELAY="+addr)
	cmd.Stderr = os.Stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(pipe)
	if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), "READY ") {
		t.Fatal("relay startup failed")
	}
	r := &harnessRelay{cmd: cmd, addr: strings.TrimPrefix(scanner.Text(), "READY "), done: make(chan struct{})}
	go func() {
		defer close(r.done)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) != 3 {
				continue
			}
			id, err := strconv.Atoi(fields[1])
			if err != nil {
				continue
			}
			counts.mu.Lock()
			counts.received[id]++
			counts.perRelay[r.addr]++
			counts.mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			<-r.done
			_ = cmd.Wait()
		}
	})
	return r
}

func killHarnessRelay(t *testing.T, r *harnessRelay) {
	t.Helper()
	if err := r.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-r.done
	_ = r.cmd.Wait()
}

// Run explicitly: NANOTUBE_FAILOVER_HARNESS=1 go test ./pkg/target -run
// '^TestStickyFailoverHarness$' -v -count=1. Every point has a unique value ID;
// loss and duplicates are measured at receivers, not inferred from socket writes.
func TestStickyFailoverHarness(t *testing.T) {
	if os.Getenv("NANOTUBE_FAILOVER_HARNESS") != "1" {
		t.Skip("set NANOTUBE_FAILOVER_HARNESS=1")
	}
	var endpoints []string
	var losses []int
	for _, mode := range []string{conf.LB, conf.StickyCluster} {
		t.Run(mode, func(t *testing.T) {
			counts := &harnessCounts{received: make(map[int]int), perRelay: make(map[string]int)}
			cfg := conf.MakeDefault()
			cfg.HostQueueSize = 50000
			cfg.TCPOutBufSize = 4096
			cfg.TCPOutBufFlushPeriodSec = 1
			cfg.SendTimeoutSec = 3
			cfg.OutConnTimeoutSec = 1
			cfg.TCPInitialConnCheck = false
			cfg.TargetConnectionJitter = false
			cfg.MaxHostReconnectPeriodMs = 100
			cfg.HostReconnectPeriodDeltaMs = 10
			cc := conf.Cluster{Name: "harness", Type: mode}
			relays := make([]*harnessRelay, 4)
			for i := range relays {
				addr := "127.0.0.1:0"
				if i < len(endpoints) {
					addr = endpoints[i]
				}
				relays[i] = startHarnessRelay(t, addr, counts)
				if i >= len(endpoints) {
					endpoints = append(endpoints, relays[i].addr)
				}
				host, port, _ := net.SplitHostPort(relays[i].addr)
				p, _ := strconv.Atoi(port)
				cc.Hosts = append(cc.Hosts, conf.Host{Name: host, Port: uint16(p)})
			}
			ms := metrics.New()
			cls, err := NewClusters(&cfg, &conf.Clusters{Cluster: []conf.Cluster{cc}}, zap.NewNop(), ms)
			if err != nil {
				t.Fatal(err)
			}
			cl := cls[cc.Name]
			finish := make(chan struct{})
			done := cls.Send(finish)
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			sent := 0
			killed, restarted := false, false
			maxUnhealthy := 0.0
			for round := 0; round < 600; round++ {
				<-tick.C
				if !killed && round >= 100 {
					if os.Getenv("NANOTUBE_HARNESS_INFLIGHT") != "1" {
						deadline := time.Now().Add(3 * time.Second)
						for {
							counts.mu.Lock()
							received := len(counts.received)
							counts.mu.Unlock()
							if received == sent {
								break
							}
							if time.Now().After(deadline) {
								t.Fatal("warmup did not drain before failure")
							}
							time.Sleep(time.Millisecond)
						}
					}
					killHarnessRelay(t, relays[1])
					killed = true
					tick.Reset(10 * time.Millisecond)
				}
				if !restarted && round >= 300 {
					relays[1] = startHarnessRelay(t, relays[1].addr, counts)
					restarted = true
				}
				batch := NewBatch()
				for j := 0; j < 100; j++ {
					// Names repeat, unique point IDs do not. Parse and normalize as real input.
					line := fmt.Sprintf("harness.metric.%08d %08d 1700000000", sent%10000, sent)
					r, err := rec.ParseRec([]byte(line), true, false, time.Now, zap.NewNop())
					if err != nil {
						t.Fatal(err)
					}
					if err := cl.PushBytesBatch(r, ms, batch); err != nil {
						t.Fatal(err)
					}
					sent++
				}
				batch.Flush()
				if mode == conf.StickyCluster {
					maxUnhealthy = max(maxUnhealthy, testutil.ToFloat64(ms.StickyUnhealthy.WithLabelValues(cc.Name)))
				}
			}
			close(finish)
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("sender did not drain after recovery")
			}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				counts.mu.Lock()
				n := len(counts.received)
				counts.mu.Unlock()
				if n == sent {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			for _, r := range relays {
				killHarnessRelay(t, r)
			}
			counts.mu.Lock()
			defer counts.mu.Unlock()
			unique, total := 0, 0
			firstMissing, lastMissing := -1, -1
			for id, n := range counts.received {
				if id < 0 || id >= sent {
					t.Fatalf("invalid received ID %d", id)
				}
				unique++
				total += n
			}
			for id := 0; id < sent; id++ {
				if counts.received[id] == 0 {
					if firstMissing < 0 {
						firstMissing = id
					}
					lastMissing = id
				}
			}
			failovers := 0.0
			if cl.sticky != nil {
				for _, h := range cl.sticky.hosts {
					failovers += testutil.ToFloat64(h.failovers)
				}
			}
			losses = append(losses, sent-unique)
			t.Logf("mode=%s sent=%d received=%d unique=%d missing=%d duplicates=%d queue_drops=%.0f failovers=%.0f peak_unhealthy=%.0f first_missing=%d last_missing=%d per_relay=%v", mode, sent, total, unique, sent-unique, total-unique, testutil.ToFloat64(ms.ThrottledHostsTotal), failovers, maxUnhealthy, firstMissing, lastMissing, counts.perRelay)
			// TCP has no receiver acknowledgement; uncertain writes may lose/duplicate
			// a small number of points. Comparison across repeated runs is reported.
			if testutil.ToFloat64(ms.ThrottledHostsTotal) != 0 {
				t.Fatal("unexpected queue drops")
			}
		})
	}
	if len(losses) == 2 && os.Getenv("NANOTUBE_HARNESS_INFLIGHT") != "1" && losses[1] > losses[0] {
		t.Errorf("sticky lost %d points; lb lost %d in the controlled failure", losses[1], losses[0])
	}
}
