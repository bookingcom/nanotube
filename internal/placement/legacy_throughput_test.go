package placement

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"os"
	"runtime"
	"sort"
	"testing"
	"time"
)

func TestExternalLegacyThroughput(t *testing.T) {
	addr := os.Getenv("V2_LEGACY_BENCH_ADDRESS")
	if addr == "" {
		t.Skip("requires loopback raw store")
	}
	host, _, e := net.SplitHostPort(addr)
	if e != nil || !net.ParseIP(host).IsLoopback() {
		t.Fatal("requires loopback")
	}
	controlAddr := os.Getenv("V2_LEGACY_BENCH_CONTROL")
	host, _, e = net.SplitHostPort(controlAddr)
	if e != nil || !net.ParseIP(host).IsLoopback() {
		t.Fatal("requires loopback control")
	}
	c, e := net.Dial("tcp", addr)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	writer := bufio.NewWriterSize(c, 256<<10)
	record := []byte("example.metric -0 100\n")
	const size, rounds = 256, 32
	latency := make([]time.Duration, 0, rounds)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	for range rounds {
		start := time.Now()
		for range size {
			if _, e = writer.Write(record); e != nil {
				t.Fatal(e)
			}
		}
		if e = writer.Flush(); e != nil {
			t.Fatal(e)
		}
		latency = append(latency, time.Since(start))
	}
	c.Close()
	control, e := net.Dial("tcp", controlAddr)
	if e != nil {
		t.Fatal(e)
	}
	defer control.Close()
	var barrier [8]byte
	binary.BigEndian.PutUint64(barrier[:], size*rounds)
	if _, e = control.Write(barrier[:]); e != nil {
		t.Fatal(e)
	}
	if _, e = io.ReadFull(control, barrier[:]); e != nil {
		t.Fatal(e)
	}
	elapsed := time.Since(started)
	runtime.ReadMemStats(&after)
	sort.Slice(latency, func(i, j int) bool { return latency[i] < latency[j] })
	t.Logf("PERF records=%d records/s=%.2f ns/record=%.2f relay-fsyncs/s=0 store-journal-fsyncs/s=%.2f relay-allocs/record=%.4f p99-local-flush-us=%d", size*rounds, float64(size*rounds)/elapsed.Seconds(), float64(elapsed.Nanoseconds())/(size*rounds), float64(binary.BigEndian.Uint64(barrier[:]))/elapsed.Seconds(), float64(after.Mallocs-before.Mallocs)/(size*rounds), latency[(len(latency)-1)*99/100].Microseconds())
}
