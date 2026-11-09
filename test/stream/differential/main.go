// Command differential compares complete decoded receiver records from two real
// relay binaries under the same fixed, seeded producer bytes and barriers.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	codec "github.com/bookingcom/nanotube/pkg/streamcodec"
	"github.com/burntsushi/toml"
	"github.com/dgryski/go-jump"
	"github.com/klauspost/compress/zstd"
)

type point struct {
	Name      []byte
	Bits      uint64
	Timestamp int64
}
type step struct {
	Wire         [33][]byte
	Markers      []int64
	FlushWire    []byte
	FlushMarkers []int64
	Reconnect    bool
	Drop         bool
	Reload       bool
}
type record struct {
	Destination string
	Slot        int
	Name        []byte
	Bits        uint64
	Timestamp   int64
}
type tracked struct {
	net.Conn
	planned atomic.Bool
}
type store struct {
	label    string
	slots    int
	stream   bool
	listener net.Listener
	mu       sync.Mutex
	conns    map[*tracked]bool
	records  []record
	errors   []string
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func port() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
func hash(name []byte) uint64 { h := fnv.New64a(); _, _ = h.Write(name); return h.Sum64() }
func slotFor(name []byte, n int) int {
	h := hash(name)
	h = (h ^ (h >> 30)) * 0xbf58476d1ce4e5b9
	h = (h ^ (h >> 27)) * 0x94d049bb133111eb
	h ^= h >> 31
	return int(h % uint64(n))
}
func (s *store) add(connSlot *int, name []byte, value float64, ts int64) {
	slot := 0
	if s.stream {
		slot = slotFor(name, s.slots)
		if *connSlot < 0 {
			*connSlot = slot
		}
		if slot != *connSlot {
			panic("name changed output connection bucket")
		}
	}
	s.mu.Lock()
	s.records = append(s.records, record{s.label, slot, append([]byte(nil), name...), math.Float64bits(value), ts})
	s.mu.Unlock()
}
func (s *store) read(c *tracked) {
	defer c.Close()
	defer func() { s.mu.Lock(); delete(s.conns, c); s.mu.Unlock() }()
	r := bufio.NewReaderSize(c, 65536)
	connSlot := -1
	fail := func(err error) {
		if err == nil || err == io.EOF || c.planned.Load() {
			return
		}
		s.mu.Lock()
		s.errors = append(s.errors, err.Error())
		s.mu.Unlock()
	}
	prefix, err := r.Peek(4)
	if err != nil {
		fail(err)
		return
	}
	if bytes.Equal(prefix, []byte(codec.Magic)[:4]) {
		var pre [codec.PreambleSize]byte
		_, err := io.ReadFull(r, pre[:])
		if err != nil {
			fail(err)
			return
		}
		flags, err := codec.ParsePreamble(pre[:])
		if err != nil {
			fail(err)
			return
		}
		d, err := codec.NewDecoder(codec.Options{Flags: flags, DictionaryBytes: 1 << 30})
		if err != nil {
			fail(err)
			return
		}
		defer d.Close()
		for {
			_, err := d.ReadFrame(r, func(_ uint32, name []byte, v float64, ts int64, _ *any) { s.add(&connSlot, name, v, ts) })
			if err != nil {
				fail(err)
				return
			}
		}
	}
	var source io.Reader = r
	if bytes.Equal(prefix, []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		d, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1))
		if err != nil {
			fail(err)
			return
		}
		defer d.Close()
		source = d
	}
	lines := bufio.NewReaderSize(source, 65536)
	for {
		line, err := lines.ReadBytes('\n')
		if len(line) > 0 {
			fields := bytes.Fields(line)
			if len(fields) != 3 {
				fail(fmt.Errorf("plaintext fields: %d", len(fields)))
				return
			}
			v, e := strconv.ParseFloat(string(fields[1]), 64)
			if e != nil {
				fail(e)
				return
			}
			ts, e := strconv.ParseFloat(string(fields[2]), 64)
			if e != nil {
				fail(e)
				return
			}
			s.add(&connSlot, fields[0], v, int64(ts))
		}
		if err != nil {
			fail(err)
			return
		}
	}
}
func (s *store) start() {
	go func() {
		for {
			c, err := s.listener.Accept()
			if err != nil {
				return
			}
			t := &tracked{Conn: c}
			s.mu.Lock()
			s.conns[t] = true
			s.mu.Unlock()
			go s.read(t)
		}
	}()
}
func (s *store) drop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		c.planned.Store(true)
		if tcp, ok := c.Conn.(*net.TCPConn); ok {
			_ = tcp.SetLinger(0)
		}
		_ = c.Close()
	}
}
func (s *store) close() { _ = s.listener.Close(); s.drop() }
func encode(path string, v any) {
	f, err := os.Create(path)
	must(err)
	defer f.Close()
	must(toml.NewEncoder(f).Encode(v))
}

func fixture(seed int64, mixed, pressure bool) ([]step, [33]bool, string) {
	random := rand.New(rand.NewSource(seed))
	var plain [33]bool
	var encoders [33]*codec.Encoder
	var fresh [33]bool
	for i := range encoders {
		plain[i] = mixed && i >= 22
		fresh[i] = true
		options := codec.Options{Flags: codec.FlagZstd, DictionaryBytes: 128 << 20, PeerDictionaryBytes: 1 << 30, MaxRows: 257}
		if pressure {
			options.MaxNames = 7
			options.DictionaryBytes = 1 << 20
		}
		e, err := codec.NewEncoder(options)
		must(err)
		encoders[i] = e
		defer e.Close()
	}
	patterns := []uint64{0x7ff8000000000001, 0x7ff8000000001234, 0x7ff0000000000001, 0xfff8000000004321, 0x7ff0000000000000, 0xfff0000000000000, 0, 0x8000000000000000, 1, 0x8000000000000001, 0x000fffffffffffff, 0x0010000000000000, 0x8010000000000000, 0x7fefffffffffffff, 0xffefffffffffffff}
	stamps := []int64{0, -1, math.MaxInt64, math.MinInt64, 9999999999, -9999999999, 1700000000, 42}
	var result []step
	digest := sha256.New()
	for phase := range 7 {
		current := step{Reconnect: phase == 2, Drop: phase == 3, Reload: phase == 4}
		if current.Reconnect && !plain[0] {
			encoders[0].Close()
			options := codec.Options{Flags: codec.FlagZstd, DictionaryBytes: 128 << 20, PeerDictionaryBytes: 1 << 30}
			if pressure {
				options.MaxNames = 7
			}
			e, err := codec.NewEncoder(options)
			must(err)
			encoders[0] = e
			fresh[0] = true
		}
		for producer := range 33 {
			if phase == 5 && producer != 0 || phase == 6 && producer != 1 {
				continue
			}
			var points []point
			if phase >= 5 {
				points = []point{{[]byte("synthetic.secondly.shared.reference"), 0x8000000000000000, int64(7 - phase)}, {[]byte("synthetic.secondly.shared.reference"), 0x3ff0000000000000, int64(-phase)}}
			} else {
				count := 64
				if pressure {
					count = 512
				}
				for i := range count {
					name := fmt.Sprintf("synthetic.secondly.oracle.p%d.n%d", producer, i%11)
					if i%13 == 0 {
						name = fmt.Sprintf("synthetic.secondly.oracle.p%d.by_owner_group_unit.n%d", producer, i%11)
					}
					if i%17 == 0 {
						name = "user.oracle.p" + strconv.Itoa(producer)
					}
					switch i {
					case 23:
						name = fmt.Sprintf("synthetic.probe.secondly.groupa.oracle.p%d", producer)
					case 29:
						name = fmt.Sprintf("synthetic.probe.secondly.groupb.oracle.p%d", producer)
					case 31:
						name = fmt.Sprintf("synthetic.probe.secondly.user.oracle.p%d", producer)
					case 37:
						name = fmt.Sprintf("synthetic.minutely.capacity.service.secondly.oracle.pods.per_owner.instance.p%d", producer)
					case 41:
						name = fmt.Sprintf("synthetic.secondly.service.oracle.per_env.live.per_group.instance.p%d", producer)
					}
					bits := patterns[(i+phase)%len(patterns)]
					if i >= len(patterns) {
						bits = random.Uint64()
					}
					ts := stamps[(i+phase)%len(stamps)]
					if i%3 == 0 {
						ts = int64(random.Uint64())
					}
					if pressure {
						name += fmt.Sprintf(".pressure.%d.%d", phase, i)
					}
					points = append(points, point{[]byte(name), bits, ts})
					if i%19 == 0 {
						points = append(points, points[len(points)-1])
					}
				}
				if producer == 0 {
					prefix := "synthetic.secondly.maximum."
					points = append(points, point{[]byte(prefix + strings.Repeat("x", 65536-len(prefix))), 0x3ff0000000000000, math.MinInt64}, point{[]byte("synthetic.secondly.bytes.\x00\x01\t\n\r \x7f\x80\xff..tail."), 0x8000000000000000, -1}, point{[]byte("..."), 1, 0})
				}
			}
			marker := seed*1000000 + int64(phase*1000+producer)
			current.Markers = append(current.Markers, marker)
			points = append(points, point{[]byte("synthetic.secondly.oracle_barrier." + strconv.FormatInt(marker, 10)), 0x3fc0000000000000, marker})
			var wire []byte
			if plain[producer] {
				for _, p := range points {
					wire = append(wire, p.Name...)
					wire = append(wire, ' ')
					wire = strconv.AppendFloat(wire, math.Float64frombits(p.Bits), 'g', -1, 64)
					wire = append(wire, ' ')
					wire = strconv.AppendInt(wire, p.Timestamp, 10)
					wire = append(wire, '\n')
				}
			} else {
				if fresh[producer] {
					var err error
					wire, err = codec.AppendPreamble(wire, codec.FlagZstd)
					must(err)
					fresh[producer] = false
				}
				if phase == 1 {
					must(encoders[producer].Reset())
				}
				for _, p := range points {
					must(encoders[producer].Add(p.Name, math.Float64frombits(p.Bits), p.Timestamp))
				}
				var err error
				wire, err = encoders[producer].Flush(wire)
				must(err)
			}
			current.Wire[producer] = wire
			_, _ = digest.Write(wire)
		}

		// Application barriers cover every routed store/socket bucket. These follow
		// the first input barrier and pending=0, so receiver snapshots cannot race
		// older bytes still buffered on a different TCP output connection.
		for family, spec := range []struct {
			prefix             string
			hosts, connections int
		}{{"synthetic.secondly.oracle_barrier.flush.", 6, 5}, {"synthetic.secondly.oracle.by_owner_group_unit.oracle_barrier.flush.", 5, 10}, {"user.oracle_barrier.flush.", 1, 1}} {
			seen := map[int]bool{}
			for n := 0; len(seen) < spec.hosts*spec.connections; n++ {
				name := []byte(fmt.Sprintf("%s%d.%d.%d", spec.prefix, phase, family, n))
				bucket := int(jump.Hash(hash(name), spec.hosts))*spec.connections + slotFor(name, spec.connections)
				if seen[bucket] {
					continue
				}
				seen[bucket] = true
				timestamp := seed*1000000 + int64(phase*1000+200+family*100+bucket)
				current.FlushMarkers = append(current.FlushMarkers, timestamp)
				must(encoders[0].Add(name, 0.125, timestamp))
			}
		}
		var flushErr error
		current.FlushWire, flushErr = encoders[0].Flush(nil)
		must(flushErr)
		_, _ = digest.Write(current.FlushWire)
		result = append(result, current)
	}
	return result, plain, hex.EncodeToString(digest.Sum(nil))
}

func pending(prom int) bool {
	client := http.Client{Timeout: time.Second}
	response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", prom))
	if err != nil {
		return false
	}
	defer response.Body.Close()
	scan := bufio.NewScanner(response.Body)
	seen := false
	for scan.Scan() {
		line := scan.Text()
		if strings.HasPrefix(line, "nanotube_tcp_pending_records{") {
			parts := strings.Fields(line)
			if len(parts) != 2 {
				return false
			}
			n, err := strconv.ParseFloat(parts[1], 64)
			if err != nil || n != 0 {
				return false
			}
			seen = true
		}
	}
	return seen && scan.Err() == nil
}
func barrier(stores []*store, markers []int64, prom int) {
	deadline := time.Now().Add(25 * time.Second)
	for {
		counts := map[int64]int{}
		for _, s := range stores {
			s.mu.Lock()
			if len(s.errors) > 0 {
				panic(s.label + ": " + s.errors[0])
			}
			for _, r := range s.records {
				if bytes.Contains(r.Name, []byte("oracle_barrier")) {
					counts[r.Timestamp]++
				}
			}
			s.mu.Unlock()
		}
		ok := true
		for _, m := range markers {
			if counts[m] != 3 {
				ok = false
			}
		}
		if ok && pending(prom) {
			return
		}
		if time.Now().After(deadline) {
			panic(fmt.Sprintf("barrier timeout markers=%v counts=%v", markers, counts))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func connect(address string) net.Conn {
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			return c
		}
		if time.Now().After(deadline) {
			panic(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func write(c net.Conn, b []byte) {
	_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	for len(b) > 0 {
		n, err := c.Write(b)
		must(err)
		if n == 0 {
			panic(io.ErrShortWrite)
		}
		b = b[n:]
	}
}

func run(binary, bundle, root string, steps []step, pressure bool) []record {
	must(os.MkdirAll(root, 0755))
	f, err := os.Open(filepath.Join(bundle, "clusters.toml"))
	must(err)
	clusters, err := conf.ReadClustersConfig(f)
	_ = f.Close()
	must(err)
	f, err = os.Open(filepath.Join(bundle, "config.toml"))
	must(err)
	config, err := conf.ReadMain(f)
	_ = f.Close()
	must(err)
	var stores []*store
	defer func() {
		for _, s := range stores {
			s.close()
		}
	}()
	for i := range clusters.Cluster {
		cl := &clusters.Cluster[i]
		for j := range cl.Hosts {
			host := &cl.Hosts[j]
			originalPort := config.TargetPort
			if host.Port != 0 {
				originalPort = host.Port
			}
			label := fmt.Sprintf("%s/%s:%d/index%d", cl.Name, host.Name, originalPort, host.Index)
			l, err := net.Listen("tcp", "127.0.0.1:0")
			must(err)
			slots := max(1, host.MTCP)
			s := &store{label: label, slots: slots, stream: host.Stream || cl.Stream, listener: l, conns: make(map[*tracked]bool)}
			s.start()
			stores = append(stores, s)
			host.Name = "127.0.0.1"
			host.Port = uint16(l.Addr().(*net.TCPAddr).Port)
			if pressure && s.stream {
				host.StreamOptions.MaxNames = 3
				host.StreamOptions.DictionaryBytes = 4096
				host.StreamOptions.PeerDictionaryBytes = 1 << 20
			}
		}
	}
	encode(filepath.Join(root, "clusters.toml"), clusters)
	rules, err := os.ReadFile(filepath.Join(bundle, "rules.toml"))
	must(err)
	must(os.WriteFile(filepath.Join(root, "rules.toml"), rules, 0644))
	input, prom := port(), port()
	config.ClustersConfig = filepath.Join(root, "clusters.toml")
	config.RulesConfig = filepath.Join(root, "rules.toml")
	config.ListenTCP = fmt.Sprintf("127.0.0.1:%d", input)
	config.ListenUDP = ""
	config.ListenGRPC = ""
	config.PromPort = prom
	config.PromLocal = true
	config.PprofPort = -1
	config.PidFilePath = filepath.Join(root, "relay.pid")
	config.TCPInitialConnCheck = false
	config.TargetConnectionJitter = false
	if pressure {
		config.StreamDictionaryBytes = 240 * 4096 * 2
	}
	config.WorkerPoolSize = 1
	config.MainQueueBatchSize = 64
	config.BatchFlushPerdiodSec = 1
	config.TCPOutBufFlushPeriodSec = 1
	config.TCPOutConnectionRefreshPeriodSec = 0
	config.SendTimeoutSec = 5
	encode(filepath.Join(root, "config.toml"), config)
	log, err := os.Create(filepath.Join(root, "relay.log"))
	must(err)
	defer log.Close()
	cmd := exec.Command(binary, "-config", filepath.Join(root, "config.toml"))
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	must(cmd.Start())
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); _ = cmd.Wait() }()
	address := config.ListenTCP
	var conns [33]net.Conn
	for i := range conns {
		conns[i] = connect(address)
		defer func(i int) { _ = conns[i].Close() }(i)
	}
	maximumEvictions := float64(0)
	for phase, current := range steps {
		if current.Reconnect {
			_ = conns[0].Close()
			conns[0] = connect(address)
		}
		if current.Drop {
			for _, s := range stores {
				s.drop()
			}
			time.Sleep(100 * time.Millisecond)
		}
		if current.Reload {
			data, err := os.ReadFile(config.PidFilePath)
			must(err)
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			must(err)
			must(syscall.Kill(pid, syscall.SIGUSR2))
			deadline := time.Now().Add(15 * time.Second)
			for {
				now, err := os.ReadFile(config.PidFilePath)
				if err == nil && string(now) != string(data) {
					break
				}
				if time.Now().After(deadline) {
					panic("relay reload did not replace pid")
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		for i, b := range current.Wire {
			if len(b) > 0 {
				write(conns[i], b)
			}
		}
		barrier(stores, current.Markers, prom)
		write(conns[0], current.FlushWire)
		barrier(stores, current.FlushMarkers, prom)
		if pressure {
			maximumEvictions = max(maximumEvictions, readEvictions(prom))
		}
		fmt.Fprintf(os.Stderr, "oracle phase=%d complete\n", phase)
	}
	for _, c := range conns {
		_ = c.Close()
	}
	if pressure && maximumEvictions == 0 {
		panic("pressure did not exercise shared-name evictions")
	}

	if pressure {
		fmt.Fprintf(os.Stderr, "oracle shared_name_evictions_max=%.0f\n", maximumEvictions)
	}
	var records []record
	for _, s := range stores {
		s.mu.Lock()
		records = append(records, s.records...)
		if len(s.errors) > 0 {
			panic(s.errors[0])
		}
		s.mu.Unlock()
	}
	return records
}

func key(r record) string { return r.Destination + "/" + strconv.Itoa(r.Slot) + "/" + string(r.Name) }
func compare(a, b []record) {
	if len(a) != len(b) {
		panic(fmt.Sprintf("count mismatch %d != %d", len(a), len(b)))
	}
	left, right := map[string][]point{}, map[string][]point{}
	for _, r := range a {
		left[key(r)] = append(left[key(r)], point{r.Name, r.Bits, r.Timestamp})
	}
	for _, r := range b {
		right[key(r)] = append(right[key(r)], point{r.Name, r.Bits, r.Timestamp})
	}
	if len(left) != len(right) {
		panic("destination/name cardinality mismatch")
	}
	for k, rows := range left {
		other := right[k]
		if len(rows) != len(other) {
			panic("record count differs for " + k)
		}
		for i, p := range rows {
			q := other[i]
			if !bytes.Equal(p.Name, q.Name) || p.Bits != q.Bits || p.Timestamp != q.Timestamp {
				panic(fmt.Sprintf("record/order mismatch name=%q position=%d bits=%016x/%016x ts=%d/%d", p.Name, i, p.Bits, q.Bits, p.Timestamp, q.Timestamp))
			}
		}
	}
}
func main() {
	old := flag.String("old", "", "baseline relay binary")
	candidate := flag.String("new", "", "candidate relay binary")
	bundle := flag.String("bundle", "", "profile/config directory")
	scratch := flag.String("scratch", "", "task scratch directory")
	seed := flag.Int64("seed", 1, "fixed random seed")
	mixed := flag.Bool("mixed", false, "mix plaintext and stream inputs")
	pressure := flag.Bool("pressure", false, "force output dictionary RESETs")
	flag.Parse()
	if *old == "" || *candidate == "" || *bundle == "" || *scratch == "" {
		panic("old, new, bundle and scratch are required")
	}
	steps, _, digest := fixture(*seed, *mixed, *pressure)
	root, err := os.MkdirTemp(*scratch, "differential-")
	must(err)
	success := false
	defer func() {
		if success {
			_ = os.RemoveAll(root)
		} else {
			fmt.Fprintln(os.Stderr, "ORACLE_FAILURE_ROOT", root)
		}
	}()
	a := run(*old, *bundle, filepath.Join(root, "old"), steps, *pressure)
	b := run(*candidate, *bundle, filepath.Join(root, "new"), steps, *pressure)
	compare(a, b)
	success = true
	sort.Slice(a, func(i, j int) bool {
		if key(a[i]) != key(a[j]) {
			return key(a[i]) < key(a[j])
		}
		if a[i].Timestamp != a[j].Timestamp {
			return a[i].Timestamp < a[j].Timestamp
		}
		return a[i].Bits < a[j].Bits
	})
	body, err := json.Marshal(a)
	must(err)
	sum := sha256.Sum256(body)
	must(json.NewEncoder(os.Stdout).Encode(map[string]any{"seed": *seed, "mixed": *mixed, "pressure": *pressure, "input_sha256": digest, "records": len(a), "records_sha256": hex.EncodeToString(sum[:]), "equality": "full name bytes, float64 bits, timestamp, destination, connection bucket, per-name order", "result": "PASS"}))
}

func readEvictions(prom int) float64 {
	client := http.Client{Timeout: time.Second}
	response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", prom))
	must(err)
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	must(err)
	var evictions float64
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "nanotube_stream_name_table_evictions_total ") {
			fields := strings.Fields(line)
			evictions, err = strconv.ParseFloat(fields[1], 64)
			must(err)
		}
	}
	return evictions
}
