package placement

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/internal/v2wire"
	"github.com/bookingcom/nanotube/pkg/rec"
)

func openSpool(t testing.TB, root string, maxRecords int) (*Router, *Durable) {
	t.Helper()
	r, d, e := OpenDurable(loadFixture(t), "site-a", testLane, 4, DurableConfig{Root: root, MaxRecords: maxRecords, MaxBytes: 4 << 20})
	if e != nil {
		t.Fatal(e)
	}
	return r, d
}
func durableRecord() *rec.RecBytes { return rec.NewTyped([]byte("example.metric"), -0.0, 123) }
func TestDurableSIGKILLReopensHoldAndUnacked(t *testing.T) {
	if root := os.Getenv("V2_RELAY_CRASH_ROOT"); root != "" {
		r, _ := openSpool(t, root, 8)
		p := durableRecord()
		if e := r.Push(p); e != nil {
			t.Fatal(e)
		}
		rt, _ := r.current.Load().selectRoute(p)
		if _, e := r.BeginMove(rt.group); e != nil {
			t.Fatal(e)
		}
		if e := r.Push(p); e != nil {
			t.Fatal(e)
		}
		fmt.Println("SPOOL_READY")
		select {}
	}
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestDurableSIGKILLReopensHoldAndUnacked$")
	cmd.Env = append(os.Environ(), "V2_RELAY_CRASH_ROOT="+root)
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	cmd.Stderr = os.Stderr
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	ready := make(chan bool, 1)
	go func() {
		scan := bufio.NewScanner(stdout)
		for scan.Scan() {
			if scan.Text() == "SPOOL_READY" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			cmd.Wait()
			t.Fatal("child did not create durable state")
		}
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal("child timeout")
	}
	if e = cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	_ = cmd.Wait()
	r, d := openSpool(t, root, 8)
	defer d.Close()
	rt, _ := r.current.Load().selectRoute(durableRecord())
	g := rt.state
	if g.sent != 1 || g.acked != 0 || !g.moving || len(g.held) != 1 || len(d.queue) != 1 {
		t.Fatalf("lost persisted barrier/suffix: %+v queue=%d", g, len(d.queue))
	}
	if d.queue[0].Sequence != 1 || d.queue[0].Stream.Epoch != 1 || string(g.held[0].record.Path) != "example.metric" {
		t.Fatal("lost original envelope")
	}
	if _, _, e = OpenDurable(loadFixture(t), "site-a", testLane, 4, DurableConfig{Root: root, MaxRecords: 8, MaxBytes: 4 << 20}); e == nil {
		t.Fatal("two processes can own spool")
	}
	if e = r.Acknowledge(rt.group, 1, 1); e == nil {
		t.Fatal("unauthenticated caller advanced ACK")
	}
}
func TestDurableLostACKReplayMoveAndRestart(t *testing.T) {
	root := t.TempDir()
	r, d := openSpool(t, root, 8)
	p := durableRecord()
	if e := r.Push(p); e != nil {
		t.Fatal(e)
	}
	rt, _ := r.current.Load().selectRoute(p)
	group := rt.group
	if _, e := r.BeginMove(group); e != nil {
		t.Fatal(e)
	}
	if e := r.Push(p); e != nil {
		t.Fatal(e)
	}
	next, acts := moved(t, loadFixture(t), group, 1)
	if e := r.Adopt(next, acts); e == nil {
		t.Fatal("unACKed move adopted")
	}
	key := bytes.Repeat([]byte{0x4b}, 32)
	var mu sync.Mutex
	var received []v2wire.Frame
	dropped := false
	dial := func(ctx context.Context, _ string) (net.Conn, error) {
		c, s := net.Pipe()
		go func() {
			defer s.Close()
			for {
				f, e := v2wire.Read(s, key)
				if e != nil {
					return
				}
				mu.Lock()
				received = append(received, f)
				lose := !dropped
				dropped = true
				mu.Unlock()
				if lose {
					return
				}
				if e = v2wire.Write(s, f.Acknowledgement(), key); e != nil {
					return
				}
			}
		}()
		return c, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, key, dial) }()
	wait := func(predicate func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if predicate() {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("delivery timeout")
	}
	wait(func() bool { d.mu.Lock(); defer d.mu.Unlock(); return len(d.queue) == 0 })
	// Persisted ACK proves the store accepted the exact old envelope. Only then
	// may trusted fence/coverage receipts release the held record at epoch two.
	if e := r.Adopt(next, acts); e != nil {
		t.Fatal(e)
	}
	wait(func() bool { d.mu.Lock(); defer d.mu.Unlock(); return len(d.queue) == 0 })
	cancel()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if e := d.Close(); e != nil {
		t.Fatal(e)
	}
	mu.Lock()
	got := append([]v2wire.Frame(nil), received...)
	mu.Unlock()
	if len(got) != 3 || got[0] != got[1] || got[2].Stream.Epoch != 2 || got[2].Stream.Store != "d" || got[2].Sequence != 1 {
		t.Fatalf("bad old retry / new epoch ordering: %+v", got)
	}
	reopened, out, e := OpenDurable(next, "site-a", testLane, 4, DurableConfig{Root: root, MaxRecords: 8, MaxBytes: 4 << 20})
	if e != nil {
		t.Fatal(e)
	}
	defer out.Close()
	if reopened.Revision() != 2 || len(out.queue) != 0 {
		t.Fatal("advanced restart failed")
	}
	if e = reopened.Push(p); e != nil {
		t.Fatal(e)
	}
	if out.queue[0].Sequence != 2 || out.queue[0].Stream.Epoch != 2 {
		t.Fatal("sequence reused after restart")
	}
}
func TestDurableBoundsAndStickySyncFailure(t *testing.T) {
	r, d := openSpool(t, t.TempDir(), 1)
	defer d.Close()
	if e := r.Push(durableRecord()); e != nil {
		t.Fatal(e)
	}
	if e := r.Push(durableRecord()); !errors.Is(e, ErrHeldFull) {
		t.Fatal("queue not bounded", e)
	}
	root := t.TempDir()
	r2, d2 := openSpool(t, root, 8)
	defer d2.Close()
	if e := os.Mkdir(filepath.Join(root, "state.json.new"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := r2.Push(durableRecord()); e == nil {
		t.Fatal("sync failure reported success")
	}
	if _, e := r2.BeginMove("anything"); e == nil {
		t.Fatal("failed spool kept accepting")
	}
	if e := os.Remove(filepath.Join(root, "state.json.new")); e != nil {
		t.Fatal(e)
	}
	d2.Close()
	_, d3 := openSpool(t, root, 8)
	defer d3.Close()
	if len(d3.queue) != 0 {
		t.Fatal("failed mutation escaped committed snapshot")
	}
}
func TestDurableOtherStoreMakesProgress(t *testing.T) {
	r, d := openSpool(t, t.TempDir(), 8)
	defer d.Close()
	first, second := recordsForGroups(r)
	// Find records going to distinct store incarnations.
	a, _ := r.current.Load().selectRoute(first)
	for i := 0; ; i++ {
		second = rec.NewTyped([]byte(fmt.Sprintf("other.metric.%d", i)), 1, 123)
		b, _ := r.current.Load().selectRoute(second)
		if a.replica.Writer != b.replica.Writer {
			break
		}
	}
	first = rec.NewTyped(first.Path, 1, 123)
	if e := r.Push(first); e != nil {
		t.Fatal(e)
	}
	if e := r.Push(second); e != nil {
		t.Fatal(e)
	}
	key := bytes.Repeat([]byte{1}, 32)
	blocked := a.replica.Writer
	dial := func(ctx context.Context, address string) (net.Conn, error) {
		for _, s := range r.current.Load().document.Stores {
			if s.ID == blocked && s.Write == address {
				return nil, errors.New("old owner unavailable")
			}
		}
		c, s := net.Pipe()
		go func() {
			defer s.Close()
			f, e := v2wire.Read(s, key)
			if e == nil {
				_ = v2wire.Write(s, f.Acknowledgement(), key)
			}
		}()
		return c, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, key, dial) }()
	deadline := time.Now().Add(5 * time.Second)
	ok := false
	for time.Now().Before(deadline) {
		d.mu.Lock()
		ok = len(d.queue) == 1 && d.queue[0].Stream.Store == blocked
		d.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if !ok {
		t.Fatal("unrelated store stalled")
	}
}
func TestDurableRejectsCorruptSnapshot(t *testing.T) {
	root := t.TempDir()
	_, d := openSpool(t, root, 4)
	d.Close()
	path := filepath.Join(root, "state.json")
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	b = []byte(strings.Replace(string(b), `"Sent":0`, `"Sent":9`, 1))
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	if _, out, e := OpenDurable(loadFixture(t), "site-a", testLane, 4, DurableConfig{Root: root, MaxRecords: 4, MaxBytes: 4 << 20}); e == nil {
		out.Close()
		t.Fatal("accepted missing suffix")
	}
}
