package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/in"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/bookingcom/nanotube/pkg/rewrites"
	"github.com/bookingcom/nanotube/pkg/rules"
	codec "github.com/bookingcom/nanotube/pkg/streamcodec"
	"github.com/bookingcom/nanotube/pkg/target"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

type admissionReceiver struct{ count, sum atomic.Uint64 }

func (h *admissionReceiver) Push(r *rec.RecBytes) {
	v, ts, ok := r.Values()
	if !ok || v < 1 || v > 2.5 || (ts != 1700000000 && ts != 1700000060) {
		panic("admission model changed a record")
	}
	h.count.Add(1)
	h.sum.Add(r.PathHash() ^ math.Float64bits(v) ^ uint64(ts))
}
func (*admissionReceiver) Stream(*sync.WaitGroup) {}
func (*admissionReceiver) Stop()                  {}
func (*admissionReceiver) IsAvailable() bool      { return true }

// Opt-in subprocess: fixtures and client sockets live in a separate driver so
// relay RSS/heap/CPU contain only the actual listener and routing pipeline.
// Its stdin accepts memory, sample, wait <total records>, and stop commands.
func TestStreamAdmissionProcess(t *testing.T) {
	if os.Getenv("STREAM_ADMISSION_MODEL") != "server" {
		t.Skip("local model subprocess only")
	}
	cfg := conf.MakeDefault()
	cfg.StreamInputMaxDictionaryBytes = 256 << 20
	cfg.StreamInputMaxRouteBytes = 128 << 20
	cfg.StreamInputMaxNames = 5000000
	cfg.StreamInputTotalBytes = 16 << 30
	if n, err := strconv.Atoi(os.Getenv("STREAM_ADMISSION_TOTAL")); err == nil {
		cfg.StreamInputTotalBytes = n
	}
	cfg.IncomingConnIdleTimeoutSec = 900
	cfg.MainQueueBatchSize = 256
	cfg.BatchFlushPerdiodSec = 1
	ms := metrics.New()
	host := &admissionReceiver{}
	rs, err := rules.Build(&conf.Rules{Rule: []conf.Rule{{Prefixes: []string{"example."}, Clusters: []string{"out"}}}}, target.Clusters{
		"out": {Name: "out", Type: conf.ToallCluster, Hosts: []target.Target{host}},
	}, false, ms)
	if err != nil {
		t.Fatal(err)
	}
	// Keep this helper compilable on the base revision as a matched control.
	var s *streamRouter
	switch build := any(newStreamRouter).(type) {
	case func(rules.Rules, rewrites.Rewrites, bool, *zap.Logger, *metrics.Prom, ...int) *streamRouter:
		s = build(rs, rewrites.Rewrites{}, true, zap.NewNop(), ms, cfg.StreamInputTotalBytes)
	case func(rules.Rules, rewrites.Rewrites, bool, *zap.Logger, *metrics.Prom) *streamRouter:
		s = build(rs, rewrites.Rewrites{}, true, zap.NewNop(), ms)
		if !ms.ReserveStreamInput(s.names.fixed, int64(cfg.StreamInputTotalBytes)) {
			t.Fatal("model index admission failed")
		}
	}
	s.maxBytes = cfg.StreamInputRouteLimit()
	queue := make(chan [][]byte, 256)
	budget := in.NewBatchBudget(cfg.TCPMainQueueBytes)
	stop := make(chan struct{})
	input := in.NewTCPInput(queue, stop, nil, &cfg, ms, zap.NewNop(), budget, func() in.StreamSink { return s.NewSink() })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan struct{})
	go func() { input.Serve(listener, stop); close(serveDone) }()
	processDone := processBuf(queue, rs, rewrites.Rewrites{}, runtime.GOMAXPROCS(0), true, false, zap.NewNop(), ms, nil, budget)
	defer func() {
		close(stop)
		<-serveDone
		close(queue)
		<-processDone
	}()
	emit := json.NewEncoder(os.Stdout)
	if err := emit.Encode(map[string]any{"address": listener.Addr().String()}); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		command := scanner.Text()
		if command == "stop" {
			return
		}
		if strings.HasPrefix(command, "wait ") {
			want, err := strconv.ParseUint(strings.TrimPrefix(command, "wait "), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(90 * time.Second)
			for host.count.Load() < want && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
		}
		if command == "memory" {
			runtime.GC()
		}
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		cpu := modelCPU()
		var usage syscall.Rusage
		if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
			t.Fatal(err)
		}
		rss := usage.Maxrss
		if runtime.GOOS == "linux" {
			rss *= 1024
		}
		if err := emit.Encode(map[string]any{
			"cpu_ns": cpu, "records": host.count.Load(), "hash_sum": host.sum.Load(),
			"rss_peak_bytes": rss, "heap": memory.HeapAlloc, "heap_sys": memory.HeapSys,
			"stack": memory.StackInuse, "goroutines": runtime.NumGoroutine(),
			"reserved":   testutil.ToFloat64(ms.StreamInputReservedBytes),
			"dictionary": testutil.ToFloat64(ms.StreamInputDictionaryBytes), "routes": testutil.ToFloat64(ms.StreamInputRouteBytes),
			"shared_names": testutil.ToFloat64(ms.StreamInputSharedNames), "active": testutil.ToFloat64(ms.StreamInputActive),
			"tcp_active": testutil.ToFloat64(ms.ActiveTCPConnections), "errors": testutil.ToFloat64(ms.StreamInputErrors),
			"throttled": testutil.ToFloat64(ms.ThrottledRecs),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

// Generate bounded 4 KiB frames once per sharing group, outside relay CPU/RSS.
// The driver reuses bytes but creates a real independent connection per client.
func TestStreamAdmissionFixtures(t *testing.T) {
	dir := os.Getenv("STREAM_ADMISSION_FIXTURES")
	if dir == "" {
		t.Skip("set STREAM_ADMISSION_FIXTURES")
	}
	names, _ := strconv.Atoi(os.Getenv("STREAM_ADMISSION_NAMES"))
	groups, _ := strconv.Atoi(os.Getenv("STREAM_ADMISSION_GROUPS"))
	frameBytes, _ := strconv.Atoi(os.Getenv("STREAM_ADMISSION_FRAME_BYTES"))
	if frameBytes == 0 {
		frameBytes = 4096
	}
	rows, _ := strconv.Atoi(os.Getenv("STREAM_ADMISSION_ROWS"))
	if rows == 0 {
		rows = 256
	}
	for group := range groups {
		for _, flags := range []byte{0, codec.FlagZstd} {
			e, err := codec.NewEncoder(codec.Options{Flags: flags, FrameSize: frameBytes, MaxRows: rows, DictionaryBytes: 1 << 30, PeerDictionaryBytes: 256 << 20})
			if err != nil {
				t.Fatal(err)
			}
			wire, _ := codec.AppendPreamble(nil, flags)
			var plain []byte
			for i := range names {
				name := admissionName(group, i)
				plain = fmt.Appendf(plain, "%s 1 1700000000\n", name)
				if err := e.Add([]byte(name), 1, 1700000000); err != nil {
					t.Fatal(err)
				}
				if e.Ready() {
					wire, err = e.Flush(wire)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			wire, err = e.Flush(wire)
			if err != nil {
				t.Fatal(err)
			}
			if e.NameCount() != names {
				t.Fatalf("model requires %d live names; encoder retains %d after automatic resets", names, e.NameCount())
			}
			files := map[string][]byte{"prime": wire, "plain": plain}
			// Alternate two complete update epochs. Values and timestamps change,
			// and replaying A/B preserves predictor state and exact plaintext parity.
			for epoch := range 2 {
				var warmPlain []byte
				for i := range 64 {
					name := admissionName(group, i*max(1, names/64))
					v, ts := 1.0, int64(1700000000)
					if epoch == 0 {
						v, ts = 1+float64(i%13)/8, ts+60
					}
					warmPlain = fmt.Appendf(warmPlain, "%s %g %d\n", name, v, ts)
					if err := e.Add([]byte(name), v, ts); err != nil {
						t.Fatal(err)
					}
				}
				warm, err := e.Flush(nil)
				if err != nil {
					t.Fatal(err)
				}
				files[fmt.Sprintf("warm%d", epoch)] = warm
				files[fmt.Sprintf("plainwarm%d", epoch)] = warmPlain
			}
			if e.NameCount() != names {
				t.Fatal("warm update introduced definitions or reset the model dictionary")
			}
			e.Close()
			for suffix, contents := range files {
				path := filepath.Join(dir, fmt.Sprintf("%d-%d-%s", group, flags, suffix))
				if err := os.WriteFile(path, contents, 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func admissionName(group, i int) string {
	prefix := fmt.Sprintf("example.family%06d.metric%08d.", group, i)
	return prefix + strings.Repeat("x", 60+i%101-len(prefix))
}
