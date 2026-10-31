package rec

import (
	"bytes"
	"hash/fnv"
	"strings"
	"testing"
	"time"
)

func BenchmarkNormalize(b *testing.B) {
	path := []byte("example." + strings.Repeat("component_1.", 24) + "count")
	dst := make([]byte, 0, len(path))
	b.ReportAllocs()
	for b.Loop() {
		_, _ = appendNormalizedPath(dst[:0], path)
	}
}

func FuzzNormalizationDifferential(f *testing.F) {
	for _, s := range []string{"", ".", "..a..b...", "a.b", "a\xffb", strings.Repeat("a.", 128)} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		want, wantErr := oldNormalizePath(in)
		got, gotErr := appendNormalizedPath(nil, in)
		if (wantErr == nil) != (gotErr == nil) || !bytes.Equal(want, got) {
			t.Fatal("normalization differs from reference")
		}
	})
}

func TestNormalizationAllBytesAndBoundaries(t *testing.T) {
	for n := 0; n < 40; n++ {
		for c := 0; c < 256; c++ {
			in := append(bytes.Repeat([]byte{'a'}, n), byte(c), '.')
			in = append(in, []byte(".end")...)
			want, wantErr := oldNormalizePath(in)
			got, gotErr := appendNormalizedPath(nil, in)
			if (wantErr == nil) != (gotErr == nil) || !bytes.Equal(want, got) {
				t.Fatalf("n=%d byte=%d", n, c)
			}
		}
	}
}

func TestPreparedPathOwnsBytes(t *testing.T) {
	path := []byte("prepared.path")
	h := fnv.New64a()
	_, _ = h.Write(path)
	expected := h.Sum64()
	r, err := ParseRecWithPath([]byte("raw.path 1 2"), true, func() time.Time { return time.Unix(1, 0) }, func([]byte) ([]byte, uint64, bool) { return path, expected, true })
	if err != nil {
		t.Fatal(err)
	}
	path[0] = 'x'
	if string(r.Path) != "prepared.path" || r.PathHash() != expected {
		t.Fatal("record aliases resolver storage or lost hash")
	}
	r.Path = []byte("another.path")
	h.Reset()
	_, _ = h.Write(r.Path)
	if r.PathHash() != h.Sum64() {
		t.Fatal("seeded hash was not invalidated")
	}
}
