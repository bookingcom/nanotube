//go:build unix

package target

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/dgryski/go-jump"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

// TestStreamChurn is an opt-in, accelerated soak of the real output path. Scale
// divides records, names and the dictionary budget, never sockets or replication.
// Input generation and observation are excluded from process CPU measurements.
// Example: STREAM_CHURN=stream STREAM_CHURN_SCALE=100 STREAM_CHURN_MINUTES=120
// go test ./pkg/target -run '^TestStreamChurn$' -v -count=1 -timeout=0
func TestStreamChurn(t *testing.T) {
	mode := os.Getenv("STREAM_CHURN")
	if mode == "" {
		t.Skip("set STREAM_CHURN=stream or plain")
	}
	integer := func(key string, fallback int) int {
		if s := os.Getenv(key); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				t.Fatal(key, s)
			}
			return n
		}
		return fallback
	}
	scale, minutes := integer("STREAM_CHURN_SCALE", 100), integer("STREAM_CHURN_MINUTES", 120)
	rollup := os.Getenv("STREAM_CHURN_ROLLUP") == "1"
	peerBytes := integer("STREAM_CHURN_PEER", 64<<20)
	// 12.7M names/min / 4 relays = core + new. 32M definitions in
	// twelve minutes / three replicas = core + 12*new.
	core, normal := 2_493_939/scale, 681_061/scale
	if core == 0 || normal == 0 {
		t.Fatal("scale too large")
	}
	var child *exec.Cmd
	var input io.WriteCloser
	var output *bufio.Reader
	var receiverLog *os.File
	if path := os.Getenv("STREAM_CHURN_RECEIVER"); path != "" {
		child = exec.Command(path, "-connections=40", "-dictionary-bytes="+strconv.Itoa(peerBytes), "-profile="+os.Getenv("STREAM_CHURN_RECEIVER_PROFILE"))
		var err error
		input, err = child.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		out, err := child.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		output = bufio.NewReader(out)
		child.Stderr = os.Stderr
		receiverLog, err = os.Create(os.Getenv("STREAM_CHURN_RECEIVER_LOG"))
		if err != nil {
			t.Fatal(err)
		}
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("CHILD pid=%d\n", child.Process.Pid)
		defer func() {
			_ = input.Close()
			if err := child.Wait(); err != nil {
				t.Error(err)
			}
			_ = receiverLog.Close()
		}()
	}
	ms := metrics.New()
	var hosts []*HostMTCP
	var senders []*streamSender
	var sockets []*countingStreamConn
	indices := [9]int{}
	for host := range 9 {
		cfg := conf.MakeDefault()
		cfg.StreamDictionaryBytes = integer("STREAM_CHURN_TOTAL", 1<<30) / scale
		cfg.StreamFullNames = os.Getenv("STREAM_CHURN_FULL_NAMES") == "1"
		cfg.TCPInitialConnCheck = false
		cfg.TCPOutConnectionRefreshPeriodSec = 0
		cfg.HostQueueSize = 4096
		cfg.TCPOutCompression = "zstd"
		cfg.TCPOutZstdFrameSize = 1 << 20
		opts := conf.StreamOptions{Zstd: true}.WithDefaults(1)
		opts.PeerDictionaryBytes = peerBytes
		// Decode through JSON so the same harness compiles against the baseline.
		budget := integer("STREAM_CHURN_BUDGET", ((1<<30)-(1<<30)/2)/90) / scale
		if err := json.Unmarshal(fmt.Appendf(nil, `{"DictionaryBytes":%d}`, budget), &opts); err != nil {
			t.Fatal(err)
		}
		h := ConstructHostMTCP("churn", cfg, conf.Host{Name: fmt.Sprintf("sink-%d", host), MTCP: 10, Stream: mode != "plain", StreamOptions: opts}, zap.NewNop(), ms)
		for i := range h.MTCPs {
			w := &countingStreamConn{}
			sockets = append(sockets, w)
			var conn net.Conn = w
			if input != nil {
				conn = &churnPipeConn{countingStreamConn: w, out: input, id: uint16(host*10 + i)}
			}
			h.MTCPs[i].New(conn, cfg.TCPOutZstdFrameSize)
			if mode == "plain" {
				h.MTCPs[i].W.configureCompression(cfg)
			}
		}
		hosts = append(hosts, h)
		senders = append(senders, h.makeStreamSender(opts))
	}
	defer func() {
		for i, h := range hosts {
			for j := range h.MTCPs {
				if mode == "plain" {
					h.MTCPs[j].W.compression.encoder.Close()
				} else if c := senders[i].connections[j].codec; c != nil {
					c.close()
				}
			}
		}
	}()
	if p := os.Getenv("STREAM_CHURN_PROFILE"); p != "" {
		f, err := os.Create(p + ".cpu")
		if err != nil {
			t.Fatal(err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			t.Fatal(err)
		}
		defer func() { pprof.StopCPUProfile(); _ = f.Close() }()
	}
	const observationRecords = 64 << 10
	var batches [observationRecords / queueChunkLen][9][]*rec.RecBytes
	var storage [observationRecords]rec.RecBytes
	var lines [queueChunkLen][320]byte
	var totalCPU, totalRecords int64
	next := uint64(core)
	for minute := range minutes {
		phase, _ := strconv.Atoi(os.Getenv("STREAM_CHURN_PHASE"))
		logicalMinute := minute + phase
		fresh := normal
		if logicalMinute > 0 && logicalMinute%15 == 0 {
			// Alternate the observed 20M and 37M names/minute system bursts.
			fresh = 2_506_061 / scale
			if logicalMinute%30 == 0 {
				fresh = 6_756_061 / scale
			}
		}
		count := 39_000_000 / scale
		var rollupIDs func(int) uint64
		if rollup {
			count, rollupIDs = rollupMinute(minute, 10_800_000/scale)
		}
		// Spread four updates per new name through the minute; the rest
		// cycles uniformly through the core. A bounded 64k-record input window
		// keeps generation outside timing without a syscall per queue chunk.
		for start := 0; start < count; start += observationRecords {
			end := min(start+observationRecords, count)
			for i := start; i < end; i++ {
				k := i - start
				j := int64(i) * int64(4*fresh) / int64(count)
				previous := int64(i-1) * int64(4*fresh) / int64(count)
				id := uint64((i - int(j)) % core)
				if i == 0 || j != previous {
					id = next + uint64(j/4)
				}
				if rollup {
					id = rollupIDs(i)
				}
				name := fmt.Appendf(lines[k%queueChunkLen][:0], "example.application.%02d.region.%02d.namespace.%s.node.%012x.requests", id%uint64(max(1, 32/scale)), id%uint64(max(1, 7/scale)), strings.Repeat("component.", 16+int(id%uint64(max(1, 7/scale)))), id)
				parsed, err := rec.ParseRec(fmt.Appendf(name, " %d.%03d %d", (id%10000+uint64(minute*17+i/core))%10000, (id+uint64(minute*13+i/core*3))%1000, 1700000000+minute*60+i*60/count), false, false, time.Now, zap.NewNop())
				if err != nil {
					t.Fatal(err)
				}
				storage[k] = *parsed
				r := &storage[k]
				hash := r.PathHash()
				for replica := range 3 {
					host := replica*3 + int(jump.Hash(hash, 3))
					batches[k/queueChunkLen][host] = append(batches[k/queueChunkLen][host], r)
				}
			}
			before := outputCPUTime(t)
			for group := range (end - start + queueChunkLen - 1) / queueChunkLen {
				for i, batch := range batches[group] {
					if len(batch) == 0 {
						continue
					}
					h := hosts[i]
					h.pushBulk(batch)
					for chunk := h.q.pop(); chunk != nil; chunk = h.q.pop() {
						if mode == "plain" {
							h.sendBatch(chunk.recs[:chunk.n], &h.MTCPs[indices[i]])
							indices[i] = (indices[i] + 1) % 10
						} else {
							senders[i].dispatch(chunk.recs[:chunk.n])
						}
						h.q.recycle(chunk)
						break // pushBulk holds at most one chunk here
					}
					clear(batch)
					batches[group][i] = batch[:0]
				}
			}
			totalCPU += outputCPUTime(t) - before
			totalRecords += int64(end-start) * 3
		}
		next += uint64(fresh)
		before := outputCPUTime(t)
		for i, h := range hosts {
			if mode == "plain" {
				h.tryToFlushIfNecessary()
			} else {
				for j := range senders[i].connections {
					senders[i].flush(&senders[i].connections[j])
				}
			}
		}
		totalCPU += outputCPUTime(t) - before
		if input != nil {
			var marker [4]byte
			binary.BigEndian.PutUint32(marker[:], uint32(minute+1))
			if err := writeChurnPacket(input, 65535, marker[:]); err != nil {
				t.Fatal(err)
			}
			line, err := output.ReadBytes('\n')
			if err != nil {
				t.Fatal(err)
			}
			if _, err = receiverLog.Write(line); err != nil {
				t.Fatal(err)
			}
		}
		sharedBytes, sharedNames, nameBytes, evictions := 0, 0, 0, uint64(0)
		if stats, ok := any(ms.Stream).(interface {
			DictionaryStats() (int, int, int, uint64)
		}); ok {
			sharedBytes, sharedNames, nameBytes, evictions = stats.DictionaryStats()
		}
		retained, names, maps := 0, 0, 0
		for _, s := range senders {
			for i := range s.connections {
				if r, ok := s.connections[i].codec.(interface{ retention() (int, int, int) }); ok {
					n, b, m := r.retention()
					names += n
					retained += b
					maps += m
				}
			}
		}
		definitions, resets, peerResets, localResets, nameResets := 0.0, 0.0, 0.0, 0.0, 0.0
		for _, s := range senders {
			definitions += testutil.ToFloat64(s.ms.Names)
			resets += testutil.ToFloat64(s.ms.Resets)
			peerResets += testutil.ToFloat64(s.ms.PeerResets)
			localResets += testutil.ToFloat64(s.ms.LocalResets)
			nameResets += testutil.ToFloat64(s.ms.NameResets)
		}
		fmt.Printf("DICT minute=%d shared_bytes=%d shared_names=%d name_bytes=%d connection_bytes=%d map_bytes=%d connection_names=%d definitions=%.0f resets=%.0f evictions=%d\n", minute+1, sharedBytes, sharedNames, nameBytes, retained, maps, names, definitions, resets, evictions)
		fmt.Printf("REASONS minute=%d peer_bytes=%.0f local_bytes=%.0f names=%.0f peer_limit=%d rollup=%v\n", minute+1, peerResets, localResets, nameResets, peerBytes, rollup)
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		rss, _ := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
		var wire int64
		for _, w := range sockets {
			wire += w.bytes
		}
		clockBefore := outputCPUTime(t)
		clockCost := outputCPUTime(t) - clockBefore
		fmt.Printf("CLOCK minute=%d cpu_read_ns=%d output_records_per_window=%d\n", minute+1, clockCost, 3*observationRecords)
		fmt.Printf("CHURN minute=%d scale=%d mode=%s records=%d cpu_ns=%.3f wire_B=%.4f heap_alloc=%d heap_inuse=%d rss_KiB=%s\n", minute+1, scale, mode, totalRecords, float64(totalCPU)/float64(totalRecords), float64(wire)/float64(totalRecords), m.HeapAlloc, m.HeapInuse, strings.TrimSpace(string(rss)))
	}
	if p := os.Getenv("STREAM_CHURN_PROFILE"); p != "" {
		f, err := os.Create(p + ".heap")
		if err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		if err := pprof.WriteHeapProfile(f); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	runtime.KeepAlive(hosts)
	runtime.KeepAlive(senders)
}

// rollupMinute keeps 360k active names per connection at full scale: 10.8M
// names, three partitions and ten sockets, copied to three replica sites.
// Equal-sized cohorts repeat every 1..60 minutes with evenly spread phases.
// Minute zero primes every cohort. At each quarter hour, one eighth of the
// pool changes generation and emits four nearby updates; subsequent scheduled
// updates use that generation. This models short bursts and slow rollup reuse
// without retaining an unbounded generated input fixture.
func rollupMinute(minute, pool int) (int, func(int) uint64) {
	cohort := pool / 60
	pool = cohort * 60
	if minute == 0 {
		return pool, func(i int) uint64 { return uint64(i) }
	}
	type span struct{ first, step, count int }
	spans := make([]span, 0, 60)
	count := 0
	for period := 1; period <= 60; period++ {
		phase := minute % period
		n := (cohort - phase + period - 1) / period
		spans = append(spans, span{(period-1)*cohort + phase, period, n})
		count += n
	}
	burst := 0
	if minute%15 == 0 {
		burst = pool / 8 * 4
	}
	return count + burst, func(i int) uint64 {
		var id int
		if i < burst {
			id = i / 4 * 8
		} else {
			i -= burst
			for _, s := range spans {
				if i < s.count {
					id = s.first + i*s.step
					break
				}
				i -= s.count
			}
		}
		if id%8 == 0 {
			id += pool * (minute / 15)
		}
		return uint64(id)
	}
}

type churnPipeConn struct {
	*countingStreamConn
	out io.Writer
	id  uint16
}

func (c *churnPipeConn) Write(b []byte) (int, error) {
	// Mirror the aligned host-zero partition from three sites, plus a fourth
	// independent wire-id connection, into one forty-connection store process.
	host := int(c.id) / 10
	if host%3 == 0 {
		id := uint16((host/3)*10 + int(c.id)%10)
		if err := writeChurnPacket(c.out, id, b); err != nil {
			return 0, err
		}
		if host == 0 {
			if err := writeChurnPacket(c.out, 30+c.id%10, b); err != nil {
				return 0, err
			}
		}
	}
	return c.countingStreamConn.Write(b)
}
func writeChurnPacket(out io.Writer, id uint16, b []byte) error {
	var header [6]byte
	binary.BigEndian.PutUint16(header[:2], id)
	binary.BigEndian.PutUint32(header[2:], uint32(len(b)))
	if n, err := out.Write(header[:]); err != nil {
		return err
	} else if n != len(header) {
		return io.ErrShortWrite
	}
	if n, err := out.Write(b); err != nil {
		return err
	} else if n != len(b) {
		return io.ErrShortWrite
	}
	return nil
}
