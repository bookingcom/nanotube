//go:build unix

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/bookingcom/nanotube/pkg/rewrites"
	"github.com/bookingcom/nanotube/pkg/rules"
	codec "github.com/bookingcom/nanotube/pkg/streamcodec"
	"github.com/bookingcom/nanotube/pkg/target"
	"github.com/dgryski/go-jump"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

const relayModelBase = 1700000000

type relayDigest struct{ Count, Sum uint64 }
type relayReceiveName struct {
	hash  uint64
	index int
}
type relayFrameAge struct {
	Timestamps []int64
	At         int64
}
type relayQuery struct {
	Count      uint64
	Admissions []int64
}
type relayReply struct {
	Addresses []string
	Wire      uint64
	Digests   [33]relayDigest
	P99       float64
}

func relayModelName(i, epoch int) string {
	if i%20 != 0 {
		epoch = 0
	}
	return fmt.Sprintf("example.%08d.epoch%d.%s", i, epoch, strings.Repeat("component.", 17+i%7)+"requests")
}

func relayModelValue(i int, ts int64, producer int) float64 {
	return float64((i%10000)*64+producer) + float64(ts%97)/128
}

func relayModelHash(name []byte) uint64 { h := fnv.New64a(); _, _ = h.Write(name); return h.Sum64() }

func relayModelSum(hash uint64, value float64, ts int64) uint64 {
	return hash ^ math.Float64bits(value) ^ uint64(ts)*0x9e3779b97f4a7c15
}

type relayCountReader struct {
	io.Reader
	bytes *atomic.Uint64
}

func (r *relayCountReader) Read(b []byte) (int, error) {
	n, err := r.Reader.Read(b)
	r.bytes.Add(uint64(n))
	return n, err
}

// A separate process runs the receiving codec and oracle. Its CPU and memory
// never enter the relay's rusage or allocation measurements. Each host has a
// real loopback listener, including for reconnects; the wire is unmodified.
func TestStreamRelayReceiver(t *testing.T) {
	if os.Getenv("NANOTUBE_RELAY_RECEIVER") != "1" {
		t.Skip("model helper")
	}
	var listeners []net.Listener
	var addresses []string
	for range 33 {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		listeners = append(listeners, l)
		addresses = append(addresses, l.Addr().String())
	}
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(relayReply{Addresses: addresses}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var digests [33]relayDigest
	var ages []relayFrameAge
	var total uint64
	var wireBytes atomic.Uint64
	var wg sync.WaitGroup
	for host, l := range listeners {
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer c.Close()
					r := bufio.NewReaderSize(&relayCountReader{c, &wireBytes}, 65536)
					p := make([]byte, codec.PreambleSize)
					if _, err := io.ReadFull(r, p); err != nil {
						if err == io.EOF {
							return
						}
						panic(err)
					}
					flags, err := codec.ParsePreamble(p)
					if err != nil {
						panic(err)
					}
					d, err := codec.NewDecoder(codec.Options{Flags: flags, DictionaryBytes: 256 << 20})
					if err != nil {
						panic(err)
					}
					defer d.Close()
					for {
						var digest relayDigest
						var stamps []int64
						_, err := d.ReadFrame(r, func(_ uint32, name []byte, v float64, ts int64, slot *any) {
							state, _ := (*slot).(*relayReceiveName)
							if state == nil {
								index, err := strconv.Atoi(string(name[8:16]))
								if err != nil {
									panic(err)
								}
								hash := relayModelHash(name)
								if int(host)%6 != int(jump.Hash(hash, 6)) {
									panic("changed route")
								}
								// Decoder names are immutable until RESET, which clears this ID slot.
								state = &relayReceiveName{hash, index}
								*slot = state
							}
							producer := int(v) % 64
							if producer >= 33 || v != relayModelValue(state.index, ts, producer) {
								panic("changed record")
							}
							hash := state.hash
							digest.Count++
							digest.Sum += relayModelSum(hash, v, ts)
							if ts > relayModelBase && (len(stamps) == 0 || stamps[len(stamps)-1] != (ts-relayModelBase)*33+int64(producer)) {
								stamps = append(stamps, (ts-relayModelBase)*33+int64(producer))
							}
						})
						if err == io.EOF {
							return
						}
						if err != nil {
							panic(err)
						}
						mu.Lock()
						digests[host].Count += digest.Count
						digests[host].Sum += digest.Sum
						total += digest.Count
						if len(stamps) != 0 {
							ages = append(ages, relayFrameAge{stamps, time.Now().UnixNano()})
						}
						mu.Unlock()
					}
				}()
			}
		}()
	}
	dec := json.NewDecoder(os.Stdin)
	for {
		var q relayQuery
		if err := dec.Decode(&q); err != nil {
			break
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			mu.Lock()
			n := total
			mu.Unlock()
			if n >= q.Count {
				break
			}
			if time.Now().After(deadline) {
				panic(fmt.Sprintf("receiver timeout: %d / %d", n, q.Count))
			}
			time.Sleep(time.Millisecond)
		}
		mu.Lock()
		reply := relayReply{Digests: digests, Wire: wireBytes.Load()}
		var latencies []int64
		for _, a := range ages {
			oldest := int64(math.MaxInt64)
			for _, stamp := range a.Timestamps {
				if int(stamp) < len(q.Admissions) && q.Admissions[stamp] != 0 {
					oldest = min(oldest, q.Admissions[stamp])
				}
			}
			if oldest != math.MaxInt64 {
				latencies = append(latencies, a.At-oldest)
			}
		}
		ages = nil
		mu.Unlock()
		if len(latencies) > 0 {
			slices.Sort(latencies)
			reply.P99 = float64(latencies[(len(latencies)-1)*99/100]) / 1e6
		}
		if err := enc.Encode(reply); err != nil {
			panic(err)
		}
	}
	wg.Wait()
}

type relayFixture struct {
	Initial         [33][]byte
	Frames          [33][][]byte
	InitialExpected [33]relayDigest
	CycleExpected   [][33]relayDigest
	Start           int
}

func relayModelFixture(t testing.TB, names, cycles, epoch, start int) relayFixture {
	t.Helper()
	f := relayFixture{CycleExpected: make([][33]relayDigest, cycles), Start: start}
	current := &f.InitialExpected
	producer := 0
	addExpected := func(i int, name []byte, ts int64, count uint64) {
		hash := relayModelHash(name)
		host := int(jump.Hash(hash, 6))
		for replica := range 3 {
			d := &current[replica*6+host]
			d.Count += count
			d.Sum += count * relayModelSum(hash, relayModelValue(i, ts, producer), ts)
		}
	}
	budget := 512 << 20
	if value := os.Getenv("RELAY_MODEL_INPUT_DICTIONARY"); value != "" {
		budget, _ = strconv.Atoi(value)
	}
	for connection := range 33 {
		stripe := connection
		producer = connection
		current = &f.InitialExpected
		e, err := codec.NewEncoder(codec.Options{Flags: codec.FlagZstd, DictionaryBytes: budget, PeerDictionaryBytes: 1 << 30, MaxRows: 1024})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Reset(); err != nil {
			t.Fatal(err)
		}
		order := rand.New(rand.NewPCG(uint64(connection+1), uint64(epoch+7919))).Perm(names / 33)
		for _, position := range order {
			i := stripe + position*33
			name := []byte(relayModelName(i, epoch))
			ts := int64(relayModelBase + start)
			if err := e.Add(name, relayModelValue(i, ts, producer), ts); err != nil {
				t.Fatal(err)
			}
			addExpected(i, name, ts, 1)
		}
		f.Initial[connection], err = e.Flush(nil)
		if err != nil {
			t.Fatal(err)
		}
		for cycle := range cycles {
			current = &f.CycleExpected[cycle]
			ts := int64(relayModelBase + start + 1 + cycle)
			for j := range 1024 {
				// Producers visit different windows; sharing a dictionary does not mean
				// all twenty producers update the same names in the same DATA frame.
				i := stripe + (((cycle*1024+j)*37)%(names/33))*33
				name := []byte(relayModelName(i, epoch))
				if err := e.Add(name, relayModelValue(i, ts, producer), ts); err != nil {
					t.Fatal(err)
				}
				addExpected(i, name, ts, 1)
			}
			wire, err := e.Flush(nil)
			if err != nil {
				t.Fatal(err)
			}
			f.Frames[connection] = append(f.Frames[connection], wire)
		}
		e.Close()
	}
	return f
}

// Opt-in full pipeline model. Use a fresh process for each matched sample.
// All input frames and independent expected digests are prepared before timing.
func TestStreamRelayModel(t *testing.T) {
	if os.Getenv("NANOTUBE_RELAY_MODEL") != "1" {
		t.Skip("opt-in CPU model")
	}
	names := 270336
	if v := os.Getenv("RELAY_MODEL_NAMES"); v != "" {
		names, _ = strconv.Atoi(v)
	}
	cycles := 32
	if v := os.Getenv("RELAY_MODEL_CYCLES"); v != "" {
		cycles, _ = strconv.Atoi(v)
	}
	fmt.Printf("MODEL_LAYOUT name_entry=%d record=%d\n", unsafe.Sizeof(streamName{}), unsafe.Sizeof(rec.RecBytes{}))
	var f0, f1 relayFixture
	if path := os.Getenv("RELAY_MODEL_FIXTURE"); path != "" {
		input, release := readRelayFixture(t, path)
		defer release()
		if input.Names != names || input.Cycles != cycles {
			t.Fatal("fixture shape mismatch")
		}
		f0, f1 = input.Zero, input.One
		if second := os.Getenv("RELAY_MODEL_SECOND_FIXTURE"); second != "" {
			input, release := readRelayFixture(t, second)
			defer release()
			if input.Names != names || input.Cycles != cycles {
				t.Fatal("second fixture shape mismatch")
			}
			f1 = input.One
		}
	} else {
		part := os.Getenv("RELAY_MODEL_FIXTURE_PART")
		if part != "one" {
			f0 = relayModelFixture(t, names, cycles*3, 0, 0)
		}
		if part != "zero" {
			f1 = relayModelFixture(t, names, cycles, 1, cycles*12+1)
		}
	}
	if path := os.Getenv("RELAY_MODEL_FIXTURE_OUT"); path != "" {
		writeRelayFixture(t, path, relayFixtureArchive{names, cycles, f0, f1})
		return
	}

	if len(f0.CycleExpected) != cycles*3 || len(f1.CycleExpected) != cycles {
		t.Fatal("incomplete model fixture")
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestStreamRelayReceiver$", "-test.timeout=170s")
	cmd.Env = append(os.Environ(), "NANOTUBE_RELAY_RECEIVER=1", "GOGC=50")
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	dec, enc := json.NewDecoder(out), json.NewEncoder(in)
	var reply relayReply
	if err := dec.Decode(&reply); err != nil {
		t.Fatal(err)
	}
	ms := metrics.New()
	cfg := conf.MakeDefault()
	cfg.TCPInitialConnCheck = false
	cfg.TCPOutConnectionRefreshPeriodSec = 55
	cfg.HostQueueSize = 800000
	cfg.StreamDictionaryBytes = 16 << 30
	cls := target.Clusters{}
	var hosts []*target.HostMTCP
	for replica := range 6 {
		cl := &target.Cluster{Name: fmt.Sprint(replica), Type: conf.JumpCluster}
		cls[cl.Name] = cl
		slots, connections, offset := 6, 5, replica*6
		if replica >= 3 {
			slots, connections, offset = 5, 10, 18+(replica-3)*5
		}
		for host := range slots {
			id := offset + host
			_, portText, err := net.SplitHostPort(reply.Addresses[id])
			if err != nil {
				t.Fatal(err)
			}
			port, err := strconv.Atoi(portText)
			if err != nil {
				t.Fatal(err)
			}
			// Distinct metric labels retain the production per-host counters while
			// every actual connection (and reconnect) stays on loopback.
			h := target.ConstructHostMTCP(fmt.Sprintf("%d/%d", replica, host), cfg, conf.Host{Name: "127.0.0.1", Port: uint16(port), MTCP: connections, Stream: true, StreamOptions: conf.StreamOptions{Zstd: true, DictionaryBytes: 32 << 20, PeerDictionaryBytes: 256 << 20, FlushPeriodMS: 1000}}, zap.NewNop(), ms)
			for j := range h.MTCPs {
				c, err := net.Dial("tcp", reply.Addresses[id])
				if err != nil {
					t.Fatal(err)
				}
				h.MTCPs[j].New(c, 65536)
			}
			cl.Hosts = append(cl.Hosts, h)
			hosts = append(hosts, h)
		}
	}
	rs, err := rules.Build(&conf.Rules{Rule: []conf.Rule{{Prefixes: []string{"example."}, Clusters: []string{"0", "1", "2"}}}}, cls, false, ms)
	if err != nil {
		t.Fatal(err)
	}
	var streams []*modelStream
	factory := newStreamRouter(rs, rewrites.Rewrites{}, true, zap.NewNop(), ms)
	decoders := codec.NewDecoderPool(codec.DefaultMaxFrameSize, runtime.GOMAXPROCS(0))
	for range 33 {
		s := factory.NewSink()
		if os.Getenv("RELAY_MODEL_SCALED_ROUTES") == "1" {
			s.maxBytes = (128 << 20) * names / 13000000
		}
		d, err := codec.NewDecoder(codec.Options{Flags: codec.FlagZstd, DictionaryBytes: 1 << 30})
		if err != nil {
			t.Fatal(err)
		}
		d.SetDecoderPool(decoders)
		d.SetNameBinder(s)
		streams = append(streams, &modelStream{d: d, s: s, shared: true, bound: true})
	}
	defer closeModel(streams)
	var wg sync.WaitGroup
	wg.Add(len(hosts))
	for _, h := range hosts {
		go h.Stream(&wg)
	}
	defer func() {
		for _, h := range hosts {
			h.Stop()
		}
		wg.Wait()
	}()
	var expected [33]relayDigest
	query := func(admissions []int64) relayReply {
		var n uint64
		for _, d := range expected {
			n += d.Count
		}
		if err := enc.Encode(relayQuery{n, admissions}); err != nil {
			t.Fatal(err)
		}
		var r relayReply
		if err := dec.Decode(&r); err != nil {
			t.Fatal(err)
		}
		if r.Digests != expected {
			t.Fatalf("receiver count/field checksum mismatch: got %v want %v", r.Digests, expected)
		}
		return r
	}
	add := func(f relayFixture, initial bool, first int) {
		sum := func(ds [33]relayDigest) {
			for i, d := range ds {
				expected[i].Count += d.Count
				expected[i].Sum += d.Sum
			}
		}
		if initial {
			sum(f.InitialExpected)
		}
		for cycle := first; cycle < first+cycles; cycle++ {
			sum(f.CycleExpected[cycle])
		}
	}
	// Queue all 33 independent dictionaries with eight input workers. A worker
	// owns disjoint decoder states; no concurrent access to a decoder is permitted.
	run := func(f relayFixture, initial bool, first int, rate float64, admissions []atomic.Int64) {
		start := time.Now()
		admit := func(i, cycle int) {
			stripe := 0
			wire := f.Initial[i]
			if cycle >= 0 {
				wire = f.Frames[i][first+cycle]
			}
			if admissions != nil {
				id := f.Start
				if cycle >= 0 {
					id += 1 + (first + cycle) + stripe
				}
				now := time.Now().UnixNano()
				admissions[id*33+i].CompareAndSwap(0, now)
			}
			streams[i].decode(t, wire)
		}
		// A fixed publisher order separates codec bandwidth from incidental
		// input-worker interleaving. Output queues and sockets stay concurrent.
		if os.Getenv("RELAY_MODEL_ORDERED") == "1" {
			for cycle := -1; cycle < cycles; cycle++ {
				if cycle < 0 && !initial {
					continue
				}
				if rate > 0 && cycle >= 0 {
					due := start.Add(time.Duration(float64(cycle*33*1024) / rate * 1e9))
					if delay := time.Until(due); delay > 0 {
						time.Sleep(delay)
					}
				}
				for i := range streams {
					admit(i, cycle)
				}
			}
			return
		}
		var workers sync.WaitGroup
		workers.Add(8)
		for worker := range 8 {
			go func() {
				defer workers.Done()
				for cycle := -1; cycle < cycles; cycle++ {
					if cycle < 0 && !initial {
						continue
					}
					if rate > 0 && cycle >= 0 {
						due := start.Add(time.Duration(float64(cycle*33*1024) / rate * 1e9))
						if delay := time.Until(due); delay > 0 {
							time.Sleep(delay)
						}
					}
					for i := worker; i < len(streams); i += 8 {
						admit(i, cycle)
					}
				}
			}()
		}
		workers.Wait()
	}
	// Output keeps names after their producer disconnects. Match the captured
	// output/input retention ratio before timing; this also matches index capacity.
	if text := os.Getenv("RELAY_MODEL_RETAINED_OUTPUT_NAMES"); text != "" {
		retained, err := strconv.Atoi(text)
		if err != nil {
			t.Fatal(err)
		}
		old := factory.NewSink()
		for i := 0; i < retained-names; i++ {
			name := []byte(relayModelName(i, 0) + ".retired")
			ref := old.BindName(name)
			value := relayModelValue(i, relayModelBase, 0)
			old.Point(0, name, value, relayModelBase, &ref)
			old.ReleaseName(ref)
			hash := relayModelHash(name)
			for replica := range 3 {
				d := &expected[replica*6+int(jump.Hash(hash, 6))]
				d.Count++
				d.Sum += relayModelSum(hash, value, relayModelBase)
			}
			if i%1024 == 0 {
				old.Flush()
			}
		}
		old.Flush()
		old.Close()
		query(nil)
	}
	// Warm complete dictionaries, then measure each independently drained phase.
	run(f0, true, 0, 0, nil)
	add(f0, true, 0)
	query(nil)
	fmt.Printf("MODEL_STATE shared_names=%.0f shared_bytes=%.0f route_bytes=%.0f\n", testutil.ToFloat64(ms.StreamInputSharedNames), testutil.ToFloat64(ms.StreamInputSharedBytes), testutil.ToFloat64(ms.StreamInputRouteBytes))
	reg := prometheus.NewRegistry()
	reg.MustRegister(ms.Stream.RetainedNames, ms.Stream.MapBytes)
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	totals := map[string]float64{}
	for _, family := range families {
		for _, metric := range family.Metric {
			totals[family.GetName()] += metric.GetGauge().GetValue()
		}
	}
	_, retainedNames, _, _ := ms.Stream.DictionaryStats()
	fmt.Printf("MODEL_OUTPUT shared_names=%d connection_names=%.0f map_bytes=%.0f\n", retainedNames, totals["nanotube_stream_connection_names"], totals["nanotube_stream_connection_map_bytes"])
	runtime.GC()
	wireBefore := query(nil).Wire
	for phase, rate := range []float64{1000000, 2000000, 1000000} {
		f, initial, label, first := f0, false, "steady", cycles
		if phase == 1 {
			label = "burst"
			first = cycles * 2
		}
		if phase == 2 {
			f, initial, label, first = f1, true, "churn", 0
		}
		var profile *os.File
		profilePhase := os.Getenv("RELAY_MODEL_PROFILE_PHASE")
		if profilePhase == "" {
			profilePhase = "steady"
		}
		if path := os.Getenv("RELAY_MODEL_PROFILE"); path != "" && label == profilePhase {
			profile, err = os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := pprof.StartCPUProfile(profile); err != nil {
				t.Fatal(err)
			}
		}
		add(f, initial, first)
		admissions := make([]atomic.Int64, (cycles*16+8)*33)

		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		cpu := modelCPU()
		run(f, initial, first, rate, admissions)
		times := make([]int64, len(admissions))
		for i := range admissions {
			times[i] = admissions[i].Load()
		}
		r := query(times)
		used := modelCPU() - cpu
		runtime.ReadMemStats(&after)
		n := cycles * 33 * 1024
		if initial {
			n += names
		}
		fmt.Printf("BenchmarkRelayStream/%s-8 1 %.6f core-sec/Mrec %.6f allocs/record %.3f B/record %.3f p99-frame-ms\n", label, float64(used)/float64(n)/1000, float64(after.Mallocs-before.Mallocs)/float64(n), float64(after.TotalAlloc-before.TotalAlloc)/float64(n), r.P99)
		fmt.Printf("MODEL_WIRE phase=%s bytes=%d records=%d bytes_per_record=%.9f\n", label, r.Wire-wireBefore, n, float64(r.Wire-wireBefore)/float64(n))
		wireBefore = r.Wire
		if profile != nil {
			pprof.StopCPUProfile()
			_ = profile.Close()
		}
	}
	runtime.GC()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	rss, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		t.Fatal(err)
	}
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	peak := usage.Maxrss
	if runtime.GOOS != "darwin" {
		peak *= 1024
	}
	fmt.Printf("MODEL_MEMORY heap=%d peak_rss_bytes=%d rss_kib=%s", mem.HeapAlloc, peak, rss)
}
