package codec

import (
	"bytes"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func (s *nameShard) sweep(pressure bool) { s.reclaim(s.used, pressure) }

func TestSharedNameReferencesAndWire(t *testing.T) {
	for _, full := range []bool{true, false} {
		for _, flags := range []byte{0, FlagZstd} {
			t.Run(fmt.Sprintf("exact=%v/flags=%d", full, flags), func(t *testing.T) {
				table, err := NewNameTable(1<<20, full)
				if err != nil {
					t.Fatal(err)
				}
				opts := Options{Flags: flags, MaxNames: 2, DictionaryBytes: 4096}
				peers := make([]*Encoder, 3)
				for i := range peers {
					peers[i], err = NewSharedEncoder(opts, table)
					if err != nil {
						t.Fatal(err)
					}
					defer peers[i].Close()
				}
				oracle := encoder(t, opts)
				var want []byte
				var wires [3][]byte
				for i := range 500 {
					name := fmt.Appendf(nil, "example.shared.%d", i%7)
					token := table.Resolve(name, 0) // forced routing-hash collisions, including zero
					add(t, oracle, string(name), float64(i)/8, int64(i)-100)
					for _, peer := range peers {
						if _, _, err := peer.AddGlobal(name, 0, token, float64(i)/8, int64(i)-100); err != nil {
							t.Fatal(err)
						}
						if peer.RetainedBytes() > opts.DictionaryBytes {
							t.Fatal("connection byte budget exceeded")
						}
					}
					clear(name)
					if i%13 == 0 {
						want = flush(t, oracle, want)
						for j, peer := range peers {
							wires[j] = flush(t, peer, wires[j])
							if !bytes.Equal(want, wires[j]) {
								t.Fatal("shared encoder changed v1 bytes")
							}
						}
					}
				}
				want = flush(t, oracle, want)
				for i, peer := range peers {
					wires[i] = flush(t, peer, wires[i])
					if !bytes.Equal(want, wires[i]) {
						t.Fatal("wire differs")
					}
				}
				count := peers[1].NameCount()
				if err := peers[0].Reset(); err != nil {
					t.Fatal(err)
				}
				if peers[1].NameCount() != count {
					t.Fatal("reset changed a peer")
				}
			})
		}
	}
}
func TestSharedTableChurnBudgetAndStaleToken(t *testing.T) {
	table, err := NewNameTable(16384, false)
	if err != nil {
		t.Fatal(err)
	}
	// Start a collision chain near the index end so deletion must wrap.
	hash := uint64(0)
	mask := len(table.shards[0].index) - 1
	for int(mixHash(hash>>5))&mask != mask-3 {
		hash += 32
	}
	stale := table.Resolve([]byte("old.name"), hash)
	peers := make([]*Encoder, 3)
	for i := range peers {
		peers[i], err = NewSharedEncoder(Options{DictionaryBytes: 4096}, table)
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := range 5000 {
		name := fmt.Appendf(nil, "metric.%d", i)
		token := table.Resolve(name, hash)
		for _, peer := range peers {
			if _, _, err := peer.AddGlobal(name, hash, token, float64(i), int64(i)); err != nil {
				t.Fatal(err)
			}
			if i%16 == 0 {
				if _, err := peer.Flush(nil); err != nil {
					t.Fatal(err)
				}
			}
			if peer.RetainedBytes() > 4096 {
				t.Fatal("connection exceeds byte share")
			}
		}
		if table.Stats().Bytes > 16384 {
			t.Fatal("global byte budget exceeded")
		}
	}
	if table.Valid(stale, hash) {
		t.Fatal("unused queued hint never expired under pressure")
	}
	e := peers[0]
	if err := e.Reset(); err != nil {
		t.Fatal(err)
	}
	_, _ = e.Flush(nil)
	if _, _, err := e.AddGlobal([]byte("old.name"), hash, stale, 42, 7); err != nil {
		t.Fatal(err)
	}
	var got []observation
	decodeAll(t, decoder(t, Options{}), flush(t, e, nil), observe(&got))
	if len(got) != 1 || got[0].name != "old.name" {
		t.Fatal("stale hint aliased a reused global id")
	}
	for _, peer := range peers {
		peer.Close()
	}
	for i := range table.shards {
		s := &table.shards[i]
		s.mu.Lock()
		s.sweep(true)
		s.mu.Unlock()
	}
	if table.Stats().Names != 0 {
		t.Fatal("close retained global references")
	}
	if table.Stats().Evictions == 0 {
		t.Fatal("churn did not reclaim names")
	}
}
func TestSharedTableConcurrentConnections(t *testing.T) {
	table, err := NewNameTable(1<<20, false)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for connection := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e, err := NewSharedEncoder(Options{DictionaryBytes: 8192, MaxNames: 70}, table)
			if err != nil {
				t.Error(err)
				return
			}
			defer e.Close()
			for i := range 2000 {
				name := fmt.Appendf(nil, "metric.%d", (i+connection)%100)
				hash := nameHash(name)
				token := table.Resolve(name, hash)
				if _, _, err := e.AddGlobal(name, hash, token, float64(i), int64(i)); err != nil {
					t.Error(err)
					return
				}
				if e.Ready() {
					if _, err := e.Flush(nil); err != nil {
						t.Error(err)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	for i := range table.shards {
		s := &table.shards[i]
		s.mu.Lock()
		s.sweep(true)
		s.mu.Unlock()
	}
	if table.Stats().Names != 0 {
		t.Fatal("connection leaked a reference")
	}
}

func TestSharedTableUnusedGrace(t *testing.T) {
	for _, full := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			table, err := NewNameTable(64<<10, full)
			if err != nil {
				t.Fatal(err)
			}
			held := table.Resolve([]byte("held.name"), 1)
			unused := table.Resolve([]byte("unused.name"), 2)
			if !table.Acquire(held, 1) {
				t.Fatal("could not acquire live name")
			}
			table.Sweep()
			time.Sleep(59 * time.Second)
			table.Sweep()
			if !table.Valid(unused, 2) {
				t.Fatal("unused name expired before its grace period")
			}
			time.Sleep(2 * time.Second)
			table.Sweep()
			if table.Valid(unused, 2) || !table.Valid(held, 1) {
				t.Fatal("sweep did not distinguish unused and referenced names")
			}
			table.Release(held)
			time.Sleep(61 * time.Second)
			table.Sweep()
			if table.Valid(held, 1) || table.Stats().Names != 0 {
				t.Fatal("released name did not expire after its own grace period")
			}
		})
	}
}

type testSeries struct{ name []byte }

func (s *testSeries) StreamName() []byte { return s.name }

func TestExternalNameReleaseRequiresValidatedReset(t *testing.T) {
	for _, mode := range []string{"valid", "crc", "header", "admission"} {
		t.Run(mode, func(t *testing.T) {
			e := encoder(t, Options{})
			add(t, e, "name.one", 1, 2)
			wire := flush(t, e, nil)
			d := decoder(t, Options{})
			bound, released := 0, 0
			d.SetNameResolver(func(name []byte) any {
				bound++
				if mode == "admission" && bound == 2 {
					return ErrLimit
				}
				return &testSeries{bytes.Clone(name)}
			})
			d.SetNameReleaser(func(any) { released++ })
			decodeAll(t, d, wire, nil)
			if mode == "admission" {
				add(t, e, "name.two", 1, 2)
				wire = flush(t, e, nil)
				visited := 0
				if _, err := d.ReadFrame(bytes.NewReader(wire), func(uint32, []byte, float64, int64, *any) { visited++ }); err != ErrLimit || visited != 0 {
					t.Fatal("admission failure emitted rows")
				}
			} else {
				if err := e.Reset(); err != nil {
					t.Fatal(err)
				}
				wire = flush(t, e, nil)
				if mode == "crc" {
					wire[16] ^= 1
				}
				if mode == "header" {
					wire[15] = 1
				}
				_, err := d.ReadFrame(bytes.NewReader(wire), nil)
				if (err == nil) != (mode == "valid") {
					t.Fatal("reset validation changed")
				}
			}
			want := 0
			if mode == "valid" {
				want = 1
			}
			if released != want {
				t.Fatal("unvalidated reset released live names")
			}
			d.Close()
			d.Close()
			if released != 1 {
				t.Fatal("close did not release each admitted reference exactly once")
			}
		})
	}
}

func TestExternalNameAdmissionWhenBindingExistingIDs(t *testing.T) {
	e := encoder(t, Options{})
	for _, name := range []string{"one", "two", "three"} {
		add(t, e, name, 1, 2)
	}
	d := decoder(t, Options{})
	decodeAll(t, d, flush(t, e, nil), func(_ uint32, _ []byte, _ float64, _ int64, slot *any) { *slot = "private caller state" })
	bound, released := 0, 0
	d.SetNameReleaser(func(ref any) {
		if _, ok := ref.(*testSeries); !ok {
			t.Fatal("released a slot that was never bound")
		}
		released++
	})
	d.SetNameResolver(func(name []byte) any {
		bound++
		if bound == 2 {
			return ErrLimit
		}
		return &testSeries{bytes.Clone(name)}
	})
	if _, err := d.Decode(nil, nil); err != ErrBroken {
		t.Fatal("admission failure did not invalidate the decoder")
	}
	d.Close()
	if released != 1 {
		t.Fatal("partial binding leaked or over-released a reference")
	}
}

func TestExternalSeriesValidationSnapshotAndReset(t *testing.T) {
	for _, flags := range []byte{0, FlagZstd} {
		identities := map[string]*testSeries{}
		calls := 0
		resolve := func(name []byte) any {
			calls++
			s := identities[string(name)]
			if s == nil {
				s = &testSeries{bytes.Clone(name)}
				identities[string(name)] = s
			}
			return s
		}
		e := encoder(t, Options{Flags: flags})
		d := decoder(t, Options{Flags: flags})
		d.SetNameResolver(resolve)
		for i := range 20 {
			add(t, e, fmt.Sprintf("name.%d", i%3), float64(i)/8, int64(i))
		}
		wire := flush(t, e, nil)
		decodeAll(t, d, wire, func(_ uint32, name []byte, _ float64, _ int64, slot *any) {
			if (*slot).(*testSeries) != identities[string(name)] {
				t.Fatal("wire id not bound to shared series")
			}
		})
		if calls != 3 {
			t.Fatal("resolved per point rather than per definition")
		}
		snapshot, err := d.Snapshot(nil)
		if err != nil {
			t.Fatal(err)
		}
		next := decoder(t, Options{Flags: flags})
		next.SetNameResolver(resolve)
		decodeAll(t, next, snapshot, nil)
		add(t, e, "name.1", 123, 456)
		wire = flush(t, e, nil)
		var a, b []observation
		decodeAll(t, d, wire, observe(&a))
		decodeAll(t, next, wire, observe(&b))
		if !reflect.DeepEqual(a, b) {
			t.Fatal("external snapshot changed predictors")
		}
		bad := bytes.Clone(wire)
		bad[len(bad)-1] ^= 1
		before := calls
		rejected := decoder(t, Options{Flags: flags})
		rejected.SetNameResolver(resolve)
		if _, err := rejected.Decode(bad, nil); err == nil {
			t.Fatal("bad frame accepted")
		}
		if calls != before {
			t.Fatal("resolver ran for a rejected frame")
		}
	}
}

func TestWireHashFilterCollision(t *testing.T) {
	seen := map[uint64]uint32{}
	var a, b uint32
	for key := uint32(1); b == 0; key++ {
		h := mixHash(uint64(key))
		signature := (h>>56)<<4 | (h & 15)
		if first := seen[signature]; first != 0 {
			a, b = first, key
		} else {
			seen[signature] = key
		}
	}
	states := wireStates{}
	index := make(wireIndex, 16)
	for i, key := range []uint32{a, b} {
		id := states.add(uint64(i+1)<<nameKeyBits | uint64(key))
		index.insert(key, id)
	}
	for i, key := range []uint32{a, b} {
		id, ok := index.find(key, &states)
		if !ok || id != uint32(i) {
			t.Fatal("hash filter aliased two global ids")
		}
	}
	index.grow(100, &states)
	for i, key := range []uint32{a, b} {
		id, ok := index.find(key, &states)
		if !ok || id != uint32(i) {
			t.Fatal("growth changed wire ids")
		}
	}
}

func TestStandaloneEncoderByteBudget(t *testing.T) {
	for _, budget := range []int{4096, 16384, 1 << 20} {
		e := encoder(t, Options{DictionaryBytes: budget, MaxRows: 16})
		for i := range 500 {
			name := fmt.Sprintf("example.%s.%d", string(bytes.Repeat([]byte{'x'}, i%400)), i)
			if err := e.Add([]byte(name), float64(i), int64(i)); err != nil {
				t.Fatal(err)
			}
			if e.Ready() {
				if _, err := e.Flush(nil); err != nil {
					t.Fatal(err)
				}
			}
			if e.RetainedBytes() > budget {
				t.Fatalf("standalone dictionary retained %d > %d", e.RetainedBytes(), budget)
			}
		}
	}
}

// Exercise keys above the old 24-bit limit without allocating millions of names.
func TestSharedNameWideKey(t *testing.T) {
	table, err := NewNameTable(16<<10, false)
	if err != nil {
		t.Fatal(err)
	}
	hash := uint64(7)
	s := table.shard(hash)
	const id = 1<<19 + 17
	s.used, s.limit = id, id+16
	s.blocks = make([]atomic.Pointer[sharedBlock], (s.limit+15)/16)
	token := table.Resolve([]byte("wide.key"), hash)
	if uint32(token) <= 1<<24 || !table.Valid(token, hash) || !table.Acquire(token, hash) {
		t.Fatal("wide key was truncated")
	}
	states := wireStates{}
	index := make(wireIndex, 16)
	index.insert(uint32(token), states.add(token))
	if _, ok := index.find(uint32(token), &states); !ok {
		t.Fatal("connection index truncated wide key")
	}
	e, err := NewSharedEncoder(Options{DictionaryBytes: 4096}, table)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	for i := range 3 {
		if _, _, err := e.AddGlobal([]byte("wide.key"), hash, token, float64(i), int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	var got []observation
	decodeAll(t, decoder(t, Options{}), flush(t, e, nil), observe(&got))
	if len(got) != 3 || e.NameCount() != 1 {
		t.Fatal("wide key lost dictionary reuse or rows")
	}
	e.Close()
	table.Release(token)
	if s.entry(id).refs != 0 {
		t.Fatal("wide release used a different slot")
	}
}

func TestSharedTableFullMissAndRecovery(t *testing.T) {
	for _, full := range []bool{false, true} {
		table, err := NewNameTable(16<<10, full)
		if err != nil {
			t.Fatal(err)
		}
		// All names use the same route, exercising collision-chain deletion too.
		var held []uint64
		for i := 0; ; i++ {
			token := table.Resolve(fmt.Appendf(nil, "held.%d", i), 0)
			if uint32(token) == 0 {
				break
			}
			if !table.Acquire(token, 0) {
				t.Fatal("could not pin name")
			}
			held = append(held, token)
		}
		before := table.Stats()
		s := table.shard(0)
		cursor := s.cursor
		peers := make([]*Encoder, 3)
		for i := range peers {
			peers[i], err = NewSharedEncoder(Options{DictionaryBytes: 1 << 20}, table)
			if err != nil {
				t.Fatal(err)
			}
			defer peers[i].Close()
		}
		for i := range 100 {
			name := fmt.Appendf(nil, "unshared.%d", i)
			token := table.Resolve(name, 0)
			if token == 0 || uint32(token) != 0 || !table.Valid(token, 0) || table.Acquire(token, 0) {
				t.Fatal("invalid failed-resolution hint")
			}
			for _, e := range peers {
				if _, _, err = e.AddGlobal(name, 0, token, float64(i), int64(i)); err != nil {
					t.Fatal(err)
				}
				if e.RetainedBytes() > 1<<20 {
					t.Fatal("connection budget exceeded")
				}
			}
		}
		if s.cursor != cursor || table.Stats().Names != before.Names {
			t.Fatal("scanned or evicted an entirely referenced table")
		}
		for _, e := range peers {
			var got []observation
			decodeAll(t, decoder(t, Options{}), flush(t, e, nil), observe(&got))
			if len(got) != 100 || e.UnsharedTotal() != 100 {
				t.Fatal("unshared definition lost")
			}
			for i, g := range got {
				if g.name != fmt.Sprintf("unshared.%d", i) {
					t.Fatal("unshared name changed")
				}
			}
		}
		// A publisher's failed hint also prevents a retry after space becomes
		// available between publishing and encoding on the replicas.
		hint := table.Resolve([]byte("deferred"), 0)
		table.Release(held[0])
		if _, _, err := peers[0].AddGlobal([]byte("deferred"), 0, hint, 1, 1); err != nil {
			t.Fatal(err)
		}
		if table.Stats().Evictions != before.Evictions || !table.Acquire(held[0], 0) {
			t.Fatal("failed-resolution hint retried shared insertion")
		}
		for _, token := range held {
			if !table.Valid(token, 0) {
				t.Fatal("live reference evicted")
			}
			table.Release(token)
		}
		token := table.Resolve([]byte("recovered"), 0)
		if uint32(token) == 0 || !table.Valid(token, 0) {
			t.Fatal("did not resume sharing after release")
		}
		if table.Stats().Bytes > 16<<10 {
			t.Fatal("table budget exceeded")
		}
	}
}

func TestSharedNameEpochReuseAndTableIdentity(t *testing.T) {
	a, err := NewNameTable(2<<20, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewNameTable(16<<10, false)
	if err != nil {
		t.Fatal(err)
	}
	old := a.Resolve([]byte("old"), 0)
	other := b.Resolve([]byte("other"), 0)
	if a.Valid(other, 0) || b.Valid(old, 0) {
		t.Fatal("cross-table token alias")
	}
	for hash := uint64(1); ; hash++ {
		if a.shard(hash) != a.shard(0) {
			a.Resolve([]byte("other.shard"), hash)
			if a.Valid(old, hash) || a.Acquire(old, hash) {
				t.Fatal("same epoch aliased another shard")
			}
			break
		}
	}
	epoch := nameGeneration.Load()
	for i := range 100 {
		a.Resolve(fmt.Appendf(nil, "distinct.%d", i), 0)
	}
	if nameGeneration.Load() != epoch {
		t.Fatal("distinct slots consumed global epochs")
	}
	s := a.shard(0)
	s.sweep(true)
	for i := range 1000 {
		token := a.Resolve(fmt.Appendf(nil, "churn.%d", i), 0)
		if uint32(token) == 0 {
			t.Fatal("churn unexpectedly disabled sharing")
		}
		if a.Valid(old, 0) || a.Acquire(old, 0) {
			t.Fatal("stale slot acquired")
		}
		s.sweep(true)
	}
}

func TestSharedNameEpochExhaustion(t *testing.T) {
	previous := nameGeneration.Swap(nameGenerationLimit - 2)
	defer nameGeneration.Store(previous)
	table, err := NewNameTable(16<<10, false)
	if err != nil {
		t.Fatal(err)
	}
	old := table.Resolve([]byte("old"), 0)
	s := table.shard(0)
	s.sweep(true)
	token := table.Resolve([]byte("next"), 0)
	if uint32(token) != 0 || table.Valid(old, 0) || table.Acquire(old, 0) {
		t.Fatal("generation wrapped")
	}
	if table.Stats().GenerationExhaustions != 1 {
		t.Fatal("epoch exhaustion not reported")
	}
	if _, err := NewNameTable(16<<10, false); err == nil {
		t.Fatal("new table reused exhausted epoch")
	}
	if nameGeneration.Load() != nameGenerationLimit-1 {
		t.Fatal("counter advanced beyond its bound")
	}
}
