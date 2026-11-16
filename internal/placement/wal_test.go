package placement

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/internal/v2wire"
	"github.com/bookingcom/nanotube/pkg/rec"
)

func TestWALBatchCommitsOnceAndRetainsFrontier(t *testing.T) {
	root := t.TempDir()
	r, d := openSpool(t, root, 128)
	before, e := os.ReadFile(filepath.Join(root, "state.json"))
	if e != nil {
		t.Fatal(e)
	}
	records := make([]*rec.RecBytes, 64)
	for i := range records {
		records[i] = durableRecord()
	}
	syncs := d.syncs
	if e = r.PushBatch(records); e != nil {
		t.Fatal(e)
	}
	if d.syncs-syncs != 1 {
		t.Fatalf("batch used %d syncs", d.syncs-syncs)
	}
	after, _ := os.ReadFile(filepath.Join(root, "state.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("hot admission rewrote checkpoint")
	}
	for _, f := range append([]v2wire.Frame(nil), d.queue...) {
		if e = d.acceptAck(f); e != nil {
			t.Fatal(e)
		}
	}
	d.Close()
	r, d = openSpool(t, root, 128)
	defer d.Close()
	rt, _ := r.current.Load().selectRoute(durableRecord())
	if len(d.queue) != 0 || rt.state.acked != 64 || rt.state.sent != 64 {
		t.Fatalf("frontier lost %+v queue=%d", rt.state, len(d.queue))
	}
	if e = r.Push(durableRecord()); e != nil {
		t.Fatal(e)
	}
	if d.queue[0].Sequence != 65 {
		t.Fatal("reused durable sequence")
	}
}
func TestWALConcurrentGroupCommit(t *testing.T) {
	r, d := openSpool(t, t.TempDir(), 128)
	defer d.Close()
	d.cfg.CommitInterval = 20 * time.Millisecond
	start := make(chan struct{})
	var wg sync.WaitGroup
	syncs := d.syncs
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if e := r.Push(durableRecord()); e != nil {
				t.Error(e)
			}
		}()
	}
	close(start)
	wg.Wait()
	if d.syncs-syncs >= 32 {
		t.Fatal("one sync per record")
	}
	if len(d.queue) != 32 || d.synced != d.walID {
		t.Fatal("uncommitted successful admission")
	}
}
func TestWALTornFinalRecordAndCorruption(t *testing.T) {
	for _, tail := range []int{1, 7, 15, 16, 25} {
		t.Run(string(rune('a'+tail)), func(t *testing.T) {
			root := t.TempDir()
			r, d := openSpool(t, root, 8)
			if e := r.Push(durableRecord()); e != nil {
				t.Fatal(e)
			}
			path := d.wal.Name()
			d.Close()
			f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			if e != nil {
				t.Fatal(e)
			}
			torn := make([]byte, 32)
			binary.BigEndian.PutUint32(torn, 100)
			if _, e = f.Write(torn[:tail]); e != nil {
				t.Fatal(e)
			}
			f.Close()
			_, out := openSpool(t, root, 8)
			defer out.Close()
			if len(out.queue) != 1 || out.queue[0].Sequence != 1 {
				t.Fatal("lost synced prefix")
			}
		})
	}
	root := t.TempDir()
	r, d := openSpool(t, root, 8)
	if e := r.Push(durableRecord()); e != nil {
		t.Fatal(e)
	}
	path := d.wal.Name()
	d.Close()
	data, _ := os.ReadFile(path)
	data[len(data)-1] ^= 1
	os.WriteFile(path, data, 0600)
	if _, out, e := OpenDurable(loadFixture(t), "site-a", testLane, 4, DurableConfig{Root: root, MaxRecords: 8, MaxBytes: 4 << 20}); e == nil {
		out.Close()
		t.Fatal("accepted corrupt complete WAL record")
	}
}
func TestWALCheckpointBoundsAndReplay(t *testing.T) {
	root := t.TempDir()
	r, d := openSpool(t, root, 128)
	d.cfg.SegmentBytes = 1024
	for range 40 {
		if e := r.Push(durableRecord()); e != nil {
			t.Fatal(e)
		}
		if e := d.acceptAck(d.queue[0]); e != nil {
			t.Fatal(e)
		}
	}
	d.Close()
	r, d = openSpool(t, root, 128)
	defer d.Close()
	if len(d.queue) != 0 {
		t.Fatal("ACK truncation resurrected data")
	}
	rt, _ := r.current.Load().selectRoute(durableRecord())
	if rt.state.acked != 40 {
		t.Fatal("checkpoint frontier lost")
	}
	files, _ := filepath.Glob(filepath.Join(root, "*.wal"))
	if len(files) != 1 {
		t.Fatal("unbounded obsolete segments", len(files))
	}
}

func TestWALACKBatchCommitsOnce(t *testing.T) {
	r, d := openSpool(t, t.TempDir(), 128)
	defer d.Close()
	records := make([]*rec.RecBytes, 64)
	for i := range records {
		records[i] = durableRecord()
	}
	if e := r.PushBatch(records); e != nil {
		t.Fatal(e)
	}
	frames := append([]v2wire.Frame(nil), d.queue...)
	before := d.syncs
	if e := d.acceptAcks(frames); e != nil {
		t.Fatal(e)
	}
	if d.syncs-before != 1 || len(d.queue) != 0 {
		t.Fatalf("ACK batch syncs=%d pending=%d", d.syncs-before, len(d.queue))
	}
}

func TestWALKillMidGroup(t *testing.T) {
	if root := os.Getenv("V2_WAL_MIDGROUP"); root != "" {
		r, d := openSpool(t, root, 8)
		if e := r.Push(durableRecord()); e != nil {
			t.Fatal(e)
		}
		d.cfg.CommitInterval = time.Hour
		go func() { _ = r.Push(durableRecord()) }()
		for {
			d.mu.Lock()
			pending := len(d.buffer) > 0
			d.mu.Unlock()
			if pending {
				break
			}
			time.Sleep(time.Millisecond)
		}
		fmt.Println("WAL_PENDING")
		for {
			time.Sleep(time.Hour)
		}
	}
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestWALKillMidGroup$")
	cmd.Env = append(os.Environ(), "V2_WAL_MIDGROUP="+root)
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	cmd.Stderr = os.Stderr
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	ready := make(chan bool, 1)
	go func() {
		scan := bufio.NewScanner(stdout)
		for scan.Scan() {
			if scan.Text() == "WAL_PENDING" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("child failed")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("child timeout")
	}
	cmd.Process.Kill()
	cmd.Wait()
	r, d := openSpool(t, root, 8)
	defer d.Close()
	if len(d.queue) != 1 || d.queue[0].Sequence != 1 {
		t.Fatal("synced prefix lost or uncommitted group escaped")
	}
	if e = r.Push(durableRecord()); e != nil {
		t.Fatal(e)
	}
	if len(d.queue) != 2 || d.queue[1].Sequence != 2 {
		t.Fatal("recovered frontier gap")
	}
}
