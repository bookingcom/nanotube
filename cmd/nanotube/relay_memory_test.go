package main

// Opt-in process model. The receiver is a separate process so its dictionaries,
// decompression buffers and CPU do not enter the relay measurements. Transport
// uses a multiplexed pipe; routing and both stream codecs are the real code.
import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/bookingcom/nanotube/pkg/rewrites"
	"github.com/bookingcom/nanotube/pkg/rules"
	codec "github.com/bookingcom/nanotube/pkg/streamcodec"
	"github.com/bookingcom/nanotube/pkg/target"
	"github.com/dgryski/go-jump"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

type relayMemoryReceiverStats struct {
	Count, Sum [33]uint64
	Error      string
}

type relayMemoryIdentity struct {
	index, hash uint64
}

func relayMemoryInputFlags() byte {
	if os.Getenv("RELAY_MEMORY_INPUT_ZSTD") == "0" {
		return 0
	}
	return codec.FlagZstd
}

type relayCPU struct{ user, system, faults int64 }

func readRelayCPU() relayCPU {
	var r syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &r); err != nil {
		panic(err)
	}
	return relayCPU{r.Utime.Sec*1e9 + int64(r.Utime.Usec)*1000, r.Stime.Sec*1e9 + int64(r.Stime.Usec)*1000, r.Minflt}
}

func (c relayCPU) since(before relayCPU) relayCPU {
	return relayCPU{c.user - before.user, c.system - before.system, c.faults - before.faults}
}

func (c *relayCPU) add(other relayCPU) {
	c.user += other.user
	c.system += other.system
	c.faults += other.faults
}

func relayMemoryReceiver(t *testing.T) {
	count, _ := strconv.Atoi(os.Getenv("RELAY_MEMORY_NAMES"))
	var decoders [330]*codec.Decoder
	var stats relayMemoryReceiverStats
	in := bufio.NewReaderSize(os.Stdin, 256<<10)
	out := json.NewEncoder(os.Stdout)
	var wire []byte
	for {
		var header [8]byte
		if _, err := io.ReadFull(in, header[:]); err != nil {
			return
		}
		id, n := binary.BigEndian.Uint32(header[:4]), binary.BigEndian.Uint32(header[4:])
		if id == ^uint32(0) {
			if err := out.Encode(stats); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if id >= 330 || n > 8<<20 {
			t.Fatal("invalid model envelope")
		}
		if cap(wire) < int(n) {
			wire = make([]byte, n)
		}
		wire = wire[:n]
		if _, err := io.ReadFull(in, wire); err != nil {
			t.Fatal(err)
		}
		if decoders[id] == nil {
			var err error
			decoders[id], err = codec.NewDecoder(codec.Options{Flags: codec.FlagZstd, DictionaryBytes: relayMemoryLimit(256<<20, count)})
			if err != nil {
				t.Fatal(err)
			}
		}
		for len(wire) > 0 {
			size := codec.HeaderSize + int(binary.BigEndian.Uint32(wire[4:8]))
			_, err := decoders[id].Decode(wire[:size], func(_ uint32, name []byte, v float64, ts int64, slot *any) {
				host := int(id) / 10
				if *slot == nil {
					r := rec.RecBytes{Path: name}
					hash := r.PathHash()
					width, offset := 6, 0
					if strings.HasPrefix(string(name), "example.containers.") {
						width, offset = 5, 18
					}
					parts := strings.Split(string(name), ".")
					if len(parts) != 7 {
						stats.Error = "invalid name"
						return
					}
					x, err := strconv.ParseUint(parts[4], 16, 64)
					index := x * 0xf1de83e19937733d
					if err != nil || index >= uint64(count) || host < offset || host >= offset+3*width || (host-offset)%width != int(jump.Hash(hash, width)) {
						stats.Error = "identity or destination changed"
					}
					*slot = &relayMemoryIdentity{index, hash}
				}
				identity := (*slot).(*relayMemoryIdentity)
				if !relayMemoryValidPoint(identity.index, v, ts) {
					stats.Error = "record or destination changed"
				}
				stats.Count[host]++
				stats.Sum[host] += identity.hash ^ math.Float64bits(v) ^ uint64(ts)*0x9e3779b97f4a7c15
			})
			if err != nil {
				t.Fatal(err)
			}
			wire = wire[size:]
		}
	}
}

func relayMemoryValidPoint(i uint64, value float64, ts int64) bool {
	offset := ts - 1700000000
	if offset < 0 || offset > 121 || offset%60 > 1 {
		return false
	}
	base := (i*37 + uint64(offset/60)*7 + uint64(offset%60)) % 10007
	if value < 0 || value*8 != math.Trunc(value*8) || value*8 >= 10007 {
		return false
	}
	delta := (uint64(value*8) + 10007 - base) % 10007
	return delta%13 == 0 && delta/13 < 20
}

type relayMemoryOutput struct {
	t        *testing.T
	w        *bufio.Writer
	host     int
	encoders [10]*codec.Encoder
	table    *codec.NameTable
	wire     [10][]byte
	plain    [10]int
	count    uint64
	sum      uint64
	bytes    uint64
}

func (h *relayMemoryOutput) send(i int) {
	wire, err := h.encoders[i].Flush(h.wire[i][:0])
	if err != nil {
		h.t.Fatal(err)
	}
	h.wire[i] = wire
	h.plain[i] = 0
	if len(wire) == 0 {
		return
	}
	var header [8]byte
	binary.BigEndian.PutUint32(header[:4], uint32(h.host*10+i))
	binary.BigEndian.PutUint32(header[4:], uint32(len(wire)))
	if _, err := h.w.Write(header[:]); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.w.Write(wire); err != nil {
		h.t.Fatal(err)
	}
	h.bytes += uint64(len(wire))
}

func (h *relayMemoryOutput) Push(r *rec.RecBytes) {
	hash := r.PathHash()
	x := (hash ^ (hash >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	i := int((x ^ (x >> 31)) % 10)
	if h.plain[i]+r.PlainSize() > 1<<20 {
		h.send(i)
	}
	v, ts, ok := r.Values()
	if !ok {
		h.t.Fatal("untyped output")
	}
	token := r.StreamToken()
	if !h.table.Valid(token, hash) {
		token = h.table.Resolve(r.Path, hash)
		r.SetStreamToken(token)
	}
	if _, _, err := h.encoders[i].AddGlobal(r.Path, hash, token, v, ts); err != nil {
		h.t.Fatal(err)
	}
	h.plain[i] += r.PlainSize()
	if h.encoders[i].Ready() {
		h.send(i)
	}
	h.count++
	h.sum += hash ^ math.Float64bits(v) ^ uint64(ts)*0x9e3779b97f4a7c15
}

func (*relayMemoryOutput) Stream(*sync.WaitGroup) {}
func (*relayMemoryOutput) Stop()                  {}
func (*relayMemoryOutput) IsAvailable() bool      { return true }

func relayMemoryWires(t *testing.T, count, epoch, producer int) (cold, warm [4][]byte) {
	t.Helper()
	nameBytes, _ := strconv.Atoi(os.Getenv("RELAY_MEMORY_NAME_BYTES"))
	wide := os.Getenv("RELAY_MEMORY_PHASE_SPREAD") == "wide"
	for stripe := range cold {
		e, err := codec.NewEncoder(codec.Options{Flags: relayMemoryInputFlags(), DictionaryBytes: 256 << 20, PeerDictionaryBytes: relayMemoryLimit(224<<20, count), MaxRows: 1024})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Reset(); err != nil {
			t.Fatal(err)
		}
		for pass := range 2 {
			perStripe := (count + 3 - stripe) / 4
			retained := e.NameCount()
			for j := range perStripe {
				// Correlated streams preserve the profiled ~20x retained-name
				// sharing. The optional wide phase spread is a larger-name-set
				// stress shape, retaining fewer references to each distinct name.
				position, source := j, producer
				if pass == 1 {
					// Replay resident IDs in steady state. A second whole-name walk
					// exceeds the peer allowance and would measure RESET churn again.
					position = perStripe - retained + j%retained
					source = (producer + j/retained) % 20
				}
				shift := 0
				if wide {
					shift = producer * perStripe / 20
				}
				i := stripe + 4*((position+shift)%perStripe)
				generation := 0
				if i%20 == 0 {
					generation = epoch
				}
				family := "pool"
				if i%5 == 0 {
					family = "containers"
				}
				name := fmt.Sprintf("example.%s.service%04d.cluster%02d.%016x.epoch%d.", family, i%1000, i%33, uint64(i)*0x9e3779b97f4a7c15, generation)
				size := 60 + i%61
				if nameBytes > 0 {
					size = nameBytes
				}
				name += strings.Repeat("x", max(1, size-len(name)))
				value := float64((i*37+source*13+epoch*7+pass)%10007) / 8
				if err := e.Add([]byte(name), value, 1700000000+int64(epoch*60+pass)); err != nil {
					t.Fatal(err)
				}
			}
			if pass == 0 {
				cold[stripe], err = e.Flush(nil)
			} else {
				warm[stripe], err = e.Flush(nil)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		e.Close()
	}
	return cold, warm
}

// Synthetic producer pool: 85 overlapping lanes, exactly 20 references per
// distinct name, 3.1M retained definitions/lane at full scale. Unlike the older
// budget-filling model, this never substitutes the 3M shared-name snapshot for
// the current per-lane population. Counts must be divisible by 85.
func relayMemoryPoolWires(t *testing.T, count, epoch, lane int) (cold, warm []byte) {
	t.Helper()
	e, err := codec.NewEncoder(codec.Options{Flags: relayMemoryInputFlags(), DictionaryBytes: 1 << 30, PeerDictionaryBytes: relayMemoryLimit(1<<30, count), MaxNames: 5000000, MaxRows: 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err := e.Reset(); err != nil {
		t.Fatal(err)
	}
	for pass := range 2 {
		for j := range count / 85 * 20 {
			i := (lane*(count/85) + j) % count
			generation := 0
			if i%20 == 0 {
				generation = epoch
			}
			family := "pool"
			if i%5 == 0 {
				family = "containers"
			}
			name := fmt.Sprintf("example.%s.service%04d.cluster%02d.%016x.epoch%d.", family, i%1000, i%33, uint64(i)*0x9e3779b97f4a7c15, generation)
			size, _ := strconv.Atoi(os.Getenv("RELAY_MEMORY_NAME_BYTES"))
			if size == 0 {
				size = 60 + i%61
			}
			name += strings.Repeat("x", max(1, size-len(name)))
			value := float64((i*37+(lane%20)*13+epoch*7+pass)%10007) / 8
			if err := e.Add([]byte(name), value, 1700000000+int64(epoch*60+pass)); err != nil {
				t.Fatal(err)
			}
		}
		if pass == 0 {
			cold, err = e.Flush(nil)
		} else {
			warm, err = e.Flush(nil)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return cold, warm
}

func relayMemoryPool() bool { return os.Getenv("RELAY_MEMORY_POOL") == "1" }

func relayMemoryLimit(production, count int) int {
	full := int64(13000000)
	if relayMemoryPool() {
		full = 13175000
	}
	return max(1<<20, int(int64(production)*int64(count)/full))
}

func relayMemoryGenerate(t *testing.T, count int) {
	epoch, _ := strconv.Atoi(os.Getenv("RELAY_MEMORY_EPOCH"))
	if relayMemoryPool() {
		if count%85 != 0 {
			t.Fatal("pool count must be divisible by 85")
		}
		for lane := range 85 {
			cold, warm := relayMemoryPoolWires(t, count, epoch, lane)
			for pass, wire := range [][]byte{cold, warm} {
				path := filepath.Join(os.Getenv("RELAY_MEMORY_FIXTURES"), fmt.Sprintf("%d-%d", pass, lane))
				if err := os.WriteFile(path, wire, 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
		return
	}
	for producer := range 20 {
		cold, warm := relayMemoryWires(t, count, epoch, producer)
		for pass, wires := range [2][4][]byte{cold, warm} {
			for stripe, wire := range wires {
				path := filepath.Join(os.Getenv("RELAY_MEMORY_FIXTURES"), fmt.Sprintf("%d-%d", pass, producer*4+stripe))
				if err := os.WriteFile(path, wire, 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

type relayMemoryPoint struct {
	id    uint32
	name  []byte
	value float64
	ts    int64
	slot  *any
}
type relayMemoryBoundPoint struct {
	name   *codec.BoundName
	value  float64
	ts     int64
	cached bool
}
type relayMemoryAdmission struct {
	reserved, slots, buffers, staging int
	points                            []relayMemoryPoint
	boundPoints                       []relayMemoryBoundPoint
}

func relayMemoryRead(t *testing.T, m *modelStream, admission *relayMemoryAdmission, path string) relayCPU {
	t.Helper()
	wire, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Fixture I/O is not producer decoding work. Include ReadFrame, routing,
	// output encoding, writes and automatic GC, but exclude loading the fixture.
	cpu := readRelayCPU()
	r := bufio.NewReaderSize(bytes.NewReader(wire), 64<<10)
	for {
		var info codec.FrameInfo
		if m.bound {
			admission.boundPoints = admission.boundPoints[:0]
			info, err = m.d.ReadBoundFrame(r, func(name *codec.BoundName, cached bool, v float64, ts int64) {
				admission.boundPoints = append(admission.boundPoints, relayMemoryBoundPoint{name, v, ts, cached})
			})
		} else {
			admission.points = admission.points[:0]
			info, err = m.d.ReadFrame(r, func(id uint32, name []byte, v float64, ts int64, slot *any) {
				admission.points = append(admission.points, relayMemoryPoint{id, name, v, ts, slot})
			})
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if info.Kind == codec.ResetFrame {
			admission.points, admission.boundPoints = nil, nil
			admission.reserved -= admission.staging
			m.s.ms.ReleaseStreamInput(int64(admission.staging))
			admission.staging = 0
			m.s.Reset()
		}
		for _, p := range admission.points {
			m.s.Point(p.id, p.name, p.value, p.ts, p.slot)
		}
		for _, p := range admission.boundPoints {
			m.s.BoundPoint(p.name, p.cached, p.value, p.ts)
		}
		clear(admission.points)
		clear(admission.boundPoints)
		m.s.Flush()
	}
	return readRelayCPU().since(cpu)
}

// TestRelayMemoryModel is deliberately disabled in routine suites. Each model
// invocation is a fresh process. Counts scale; 80 inputs, 20x sharing and all 330
// zstd encoders remain fixed. RELAY_MEMORY_PROFILE must be inside task scratch.
func TestRelayMemoryModel(t *testing.T) {
	if os.Getenv("RELAY_MEMORY_RECEIVER") == "1" {
		relayMemoryReceiver(t)
		return
	}
	count, _ := strconv.Atoi(os.Getenv("RELAY_MEMORY_NAMES"))
	if count == 0 {
		t.Skip("set RELAY_MEMORY_NAMES to run the process model")
	}
	if os.Getenv("RELAY_MEMORY_GENERATOR") == "1" {
		relayMemoryGenerate(t, count)
		return
	}
	fixtureParent := os.Getenv("RELAY_MEMORY_FIXTURES")
	if fixtureParent == "" {
		t.Fatal("set RELAY_MEMORY_FIXTURES to a directory inside task scratch")
	}
	if err := os.MkdirAll(fixtureParent, 0700); err != nil {
		t.Fatal(err)
	}
	fixtureRoot, err := os.MkdirTemp(fixtureParent, "model-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(fixtureRoot) }()
	child := exec.Command(os.Args[0], "-test.run=^TestRelayMemoryModel$", "-test.timeout=3m")
	child.Env = append(os.Environ(), "RELAY_MEMORY_RECEIVER=1")
	child.Stderr = os.Stderr
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close(); _ = child.Wait() }()
	w := bufio.NewWriterSize(stdin, 256<<10)
	reader := json.NewDecoder(stdout)
	ms := metrics.New()
	budget := relayMemoryLimit(8<<30, count)
	connectionBudget := min(16<<20, budget/2/330)
	if connectionBudget < 4096 {
		t.Fatal("model scale cannot reserve 4 KiB for every output connection")
	}
	table, err := codec.NewNameTable(budget/2, false)
	if err != nil {
		t.Fatal(err)
	}
	var hosts []*relayMemoryOutput
	clusters := target.Clusters{}
	for group := range 6 {
		width := 6
		if group >= 3 {
			width = 5
		}
		cl := &target.Cluster{Name: fmt.Sprint(group), Type: conf.JumpCluster}
		for range width {
			h := &relayMemoryOutput{t: t, w: w, host: len(hosts), table: table}
			for i := range h.encoders {
				h.encoders[i], err = codec.NewSharedEncoder(codec.Options{Flags: codec.FlagZstd, FrameSize: 1 << 20, MaxRows: 4096, DictionaryBytes: connectionBudget, PeerDictionaryBytes: relayMemoryLimit(256<<20, count)}, table)
				if err != nil {
					t.Fatal(err)
				}
			}
			hosts = append(hosts, h)
			cl.Hosts = append(cl.Hosts, h)
		}
		clusters[cl.Name] = cl
	}
	rs, err := rules.Build(&conf.Rules{Rule: []conf.Rule{
		{Prefixes: []string{"example.pool."}, Clusters: []string{"0", "1", "2"}},
		{Prefixes: []string{"example.containers."}, Clusters: []string{"3", "4", "5"}},
	}}, clusters, false, ms)
	if err != nil {
		t.Fatal(err)
	}
	root := newStreamRouter(rs, rewrites.Rewrites{}, true, zap.NewNop(), ms)
	root.maxBytes = relayMemoryLimit(128<<20, count)
	inputs := 80
	inputBudget := relayMemoryLimit(256<<20, count)
	if relayMemoryPool() {
		inputs, inputBudget = 85, relayMemoryLimit(1<<30, count)
	}
	streams := make([]*modelStream, inputs)
	admissions := make([]*relayMemoryAdmission, inputs)
	pool := codec.NewDecoderPool(codec.DefaultMaxFrameSize, runtime.GOMAXPROCS(0))
	for i := range streams {
		s := root.NewSink()
		d, err := codec.NewDecoder(codec.Options{Flags: relayMemoryInputFlags(), DictionaryBytes: inputBudget, MaxNames: 5000000})
		if err != nil {
			t.Fatal(err)
		}
		d.SetDecoderPool(pool)
		d.SetNameBinder(s)
		streams[i] = &modelStream{d: d, s: s, shared: true, bound: true}
		a := &relayMemoryAdmission{reserved: 4096}
		admissions[i] = a
		resize := func(slots, buffers, staging int) bool {
			want := 4096 + slots + buffers + staging
			if want > a.reserved && !ms.ReserveStreamInput(int64(want-a.reserved), int64(conf.Main{}.StreamInputTotalLimit())) {
				return false
			}
			if want < a.reserved {
				ms.ReleaseStreamInput(int64(a.reserved - want))
			}
			a.reserved, a.slots, a.buffers, a.staging = want, slots, buffers, staging
			return true
		}
		if !ms.ReserveStreamInput(4096, int64(conf.Main{}.StreamInputTotalLimit())) {
			t.Fatal("fixed admission")
		}
		d.SetDictionaryBudget(func(n int) bool { return resize(n, a.buffers, a.staging) })
		d.SetBufferBudget(func(n, rows int) bool {
			capacity := max(cap(a.boundPoints), rows)
			if !resize(a.slots, n, capacity*32) {
				return false
			}
			if capacity > cap(a.boundPoints) {
				a.boundPoints = make([]relayMemoryBoundPoint, 0, capacity)
			}
			return true
		})
	}
	flush := func() {
		for _, h := range hosts {
			for i := range h.encoders {
				h.send(i)
			}
		}
		var marker [8]byte
		binary.BigEndian.PutUint32(marker[:4], ^uint32(0))
		if _, err := w.Write(marker[:]); err != nil {
			t.Fatal(err)
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		var got relayMemoryReceiverStats
		if err := reader.Decode(&got); err != nil {
			t.Fatal(err)
		}
		for i, h := range hosts {
			if got.Error != "" || got.Count[i] != h.count || got.Sum[i] != h.sum {
				t.Fatalf("receiver %d differs: %+v, expected %d/%d", i, got, h.count, h.sum)
			}
		}
	}
	sample := func(phase string, cpu relayCPU, records int, wireBefore uint64) {
		started := readRelayCPU()
		flush()
		cpu.add(readRelayCPU().since(started))
		if strings.HasPrefix(phase, "steady") && os.Getenv("RELAY_MEMORY_CPU_PROFILE") != "" {
			pprof.StopCPUProfile()
		}
		var before, mem runtime.MemStats
		runtime.ReadMemStats(&before)
		rss := func() uint64 {
			if runtime.GOOS == "linux" {
				data, err := os.ReadFile("/proc/self/statm")
				if err != nil {
					t.Fatal(err)
				}
				n, err := strconv.ParseUint(strings.Fields(string(data))[1], 10, 64)
				if err != nil {
					t.Fatal(err)
				}
				return n * uint64(os.Getpagesize())
			}
			out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
			if err != nil {
				t.Fatal(err)
			}
			n, _ := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
			return n * 1024
		}
		rssBefore := rss()
		runtime.GC()
		runtime.ReadMemStats(&mem)
		var slots, output, maps, inputIDs, inputAllowance int
		var wire uint64
		for _, m := range streams {
			slots += m.d.RetainedBytes()
			inputIDs += m.d.NameCount()
			inputAllowance += m.s.NameBytes()
		}
		for _, h := range hosts {
			wire += h.bytes
			for _, e := range h.encoders {
				if e.RetainedBytes() > connectionBudget {
					t.Fatal("output dictionary exceeded its connection reservation")
				}
				output += e.RetainedBytes()
				maps += e.MapBytes()
			}
		}
		report := map[string]any{"phase": phase, "names": count, "records": records, "cpu_ns_record": float64(cpu.user+cpu.system) / float64(records), "wire_bytes_record": float64(wire-wireBefore) / float64(records), "wire_total": wire, "heap_before_gc": before.HeapAlloc, "heap": mem.HeapAlloc, "heap_inuse": mem.HeapInuse, "heap_idle": mem.HeapIdle, "heap_released": mem.HeapReleased, "rss_before_gc": rssBefore, "rss": rss(), "sys": mem.Sys, "slots": slots, "input_names": testutil.ToFloat64(ms.StreamInputSharedBytes), "routes": testutil.ToFloat64(ms.StreamInputRouteBytes), "output": output, "output_maps": maps, "output_table": table.Stats()}
		report["cpu_user_ns_record"] = float64(cpu.user) / float64(records)
		report["cpu_system_ns_record"] = float64(cpu.system) / float64(records)
		report["minor_faults"] = cpu.faults
		report["input_zstd"] = relayMemoryInputFlags() != 0
		report["phase_spread"] = os.Getenv("RELAY_MEMORY_PHASE_SPREAD")
		if relayMemoryPool() && (inputIDs != count*20 || testutil.ToFloat64(ms.StreamInputSharedNames) != float64(count)) {
			t.Fatalf("pool retention changed: IDs=%d/%d shared=%g/%d (peer RESET would invalidate the model)", inputIDs, count*20, testutil.ToFloat64(ms.StreamInputSharedNames), count)
		}
		report["input_ids"] = inputIDs
		report["inputs"] = inputs
		report["reserved"] = testutil.ToFloat64(ms.StreamInputReservedBytes)
		var buffers, staging int
		for _, a := range admissions {
			buffers += a.buffers
			staging += a.staging
		}
		report["decoder_buffers"], report["staging"] = buffers, staging
		want := float64(inputs*4096+slots+buffers+staging) + testutil.ToFloat64(ms.StreamInputSharedBytes) + testutil.ToFloat64(ms.StreamInputRouteBytes)
		if want != testutil.ToFloat64(ms.StreamInputReservedBytes) {
			t.Fatalf("reservation != physical components: %v", want)
		}
		report["input_name_allowance"] = inputAllowance
		report["input_shared_count"] = testutil.ToFloat64(ms.StreamInputSharedNames)
		report["input_dictionary_limit"] = inputBudget
		report["output_connection_limit"] = connectionBudget
		// Production-default runs must preserve allocator/GC high-water pages
		// across phases. Explicit scavenging is a separate diagnostic only.
		if os.Getenv("RELAY_MEMORY_SCAVENGE") == "1" {
			debug.FreeOSMemory()
			report["rss_scavenged"] = rss()
			var scavenged runtime.MemStats
			runtime.ReadMemStats(&scavenged)
			report["runtime_resident_scavenged"] = scavenged.Sys - scavenged.HeapReleased
			report["heap_released_scavenged"] = scavenged.HeapReleased
		}
		report["gc_cpu_fraction"] = mem.GCCPUFraction
		report["gc_cycles"] = mem.NumGC
		b, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		t.Log(string(b))
		if path := os.Getenv("RELAY_MEMORY_PROFILE"); path != "" {
			f, err := os.Create(path + "-" + phase + ".pb.gz")
			if err != nil {
				t.Fatal(err)
			}
			if err := pprof.WriteHeapProfile(f); err != nil {
				t.Fatal(err)
			}
			_ = f.Close()
		}
	}
	var wireBefore uint64
	for epoch := range 3 {
		generator := exec.Command(os.Args[0], "-test.run=^TestRelayMemoryModel$", "-test.timeout=3m")
		generator.Env = append(os.Environ(), "RELAY_MEMORY_GENERATOR=1", "RELAY_MEMORY_FIXTURES="+fixtureRoot, fmt.Sprintf("RELAY_MEMORY_EPOCH=%d", epoch))
		if output, err := generator.CombinedOutput(); err != nil {
			t.Fatalf("fixture generator: %v: %s", err, output)
		}
		for pass := range 2 {
			phase := fmt.Sprintf("epoch%d", epoch)
			if pass == 1 {
				phase = fmt.Sprintf("steady%d", epoch)
			}
			var profile *os.File
			if path := os.Getenv("RELAY_MEMORY_CPU_PROFILE"); path != "" && pass == 1 {
				var err error
				profile, err = os.Create(fmt.Sprintf("%s-%d.pb.gz", path, epoch))
				if err != nil {
					t.Fatal(err)
				}
				if err := pprof.StartCPUProfile(profile); err != nil {
					t.Fatal(err)
				}
			}
			var cpu relayCPU
			for i, m := range streams {
				cpu.add(relayMemoryRead(t, m, admissions[i], filepath.Join(fixtureRoot, fmt.Sprintf("%d-%d", pass, i))))
			}
			sample(phase, cpu, count*20, wireBefore)
			if profile != nil {
				_ = profile.Close()
			}
			wireBefore = 0
			for _, h := range hosts {
				wireBefore += h.bytes
			}
		}
	}
	closeModel(streams)
	for _, a := range admissions {
		ms.ReleaseStreamInput(int64(a.reserved))
	}
	assertEmptyStreamNames(t, root)
	for _, h := range hosts {
		for _, e := range h.encoders {
			e.Close()
		}
	}
	runtime.KeepAlive(hosts)
	t.Logf("complete at %s", time.Now().Format(time.RFC3339))
}
