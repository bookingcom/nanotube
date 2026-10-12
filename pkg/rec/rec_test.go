package rec

import (
	"bytes"
	"hash/fnv"
	"math"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/test"
	"github.com/google/go-cmp/cmp"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

func TestRec(t *testing.T) {
	tt := []struct {
		s     string
		res   RecBytes
		isErr bool
	}{
		{
			s:     "",
			isErr: true,
		},
		{
			s:     "   ",
			isErr: true,
		},
		{
			s: "a a a",
			res: RecBytes{
				Path: []byte("a"),
				Val:  []byte("a"),
				Time: []byte("a"),
			},
			isErr: false,
		},
		{
			s: "1 a 1",
			res: RecBytes{
				Path: []byte("1"),
				Val:  []byte("a"),
				Time: []byte("1"),
			},
			isErr: false,
		},
		{
			s: "a 1 1",
			res: RecBytes{
				Path: []byte("a"),
				Val:  []byte("1"),
				Time: []byte("1"),
			},
			isErr: false,
		},
		{
			s: "a.a.a 1.1e3 123",
			res: RecBytes{
				Path: []byte("a.a.a"),
				Val:  []byte("1.1e3"),
				Time: []byte("123"),
			},
			isErr: false,
		},
		{
			s:     " 1 1",
			isErr: true,
		},
		{
			s: "a 1 a",
			res: RecBytes{
				Path: []byte("a"),
				Val:  []byte("1"),
				Time: []byte("a"),
			},
			isErr: false,
		},
		{
			s: "asdf.fdsa.a.1.c 12e-3 1234567890",
			res: RecBytes{
				Path: []byte("asdf.fdsa.a.1.c"),
				Val:  []byte("12e-3"),
				Time: []byte("1234567890"),
			},
			isErr: false,
		},
		// test path normalization
		{
			s: ".abc.abc.abc 1.23 12345678",
			res: RecBytes{
				Path: []byte("abc.abc.abc"),
				Val:  []byte("1.23"),
				Time: []byte("12345678"),
			},
			isErr: false,
		},
		{
			s: "..abc..ab&+=.jklm.jk% 1.234 1234567890",
			res: RecBytes{
				Path: []byte("abc.ab___.jklm.jk_"),
				Val:  []byte("1.234"),
				Time: []byte("1234567890"),
			},
			isErr: false,
		},
		{
			s:     "abc.ab c.abc 1.234 1234",
			isErr: true,
		},
		// test other kinds of whitespace
		{
			s: "abc.jkl.mno    1.23   12345",
			res: RecBytes{
				Path: []byte("abc.jkl.mno"),
				Val:  []byte("1.23"),
				Time: []byte("12345"),
			},
			isErr: false,
		},
		{
			s: "some.thing.here			98.7  12345",
			res: RecBytes{
				Path: []byte("some.thing.here"),
				Val:  []byte("98.7"),
				Time: []byte("12345"),
			},
			isErr: false,
		},
		{
			s: "fractional.time.test 3.3 123.45",
			res: RecBytes{
				Path: []byte("fractional.time.test"),
				Val:  []byte("3.3"),
				Time: []byte("123.45"),
			},
		},
		{
			s: "large.float.test 1.79769313486231e+308 12345",
			res: RecBytes{
				Path: []byte("large.float.test"),
				Val:  []byte("1.79769313486231e+308"),
				Time: []byte("12345"),
			},
		},
		{
			s: ". 0 0",
			res: RecBytes{
				Path: []byte(""),
				Val:  []byte("0"),
				Time: []byte("0"),
			},
			isErr: true,
		},
		{
			s: "a.b.c NaN 12345",
			res: RecBytes{
				Path: []byte("a.b.c"),
				Val:  []byte("NaN"),
				Time: []byte("12345"),
			},
			isErr: false,
		},
		{
			s:     "0",
			isErr: true,
		},
	}
	nowF := func() time.Time {
		return time.Time{}
	}

	lg := zap.NewNop()
	opt := cmp.Comparer(func(x, y float64) bool {
		delta := math.Abs(x - y)
		return delta < 0.00001
	})
	for _, tst := range tt {
		t.Run(tst.s, func(t *testing.T) {
			res, err := ParseRec([]byte(tst.s), true, true, nowF, lg)
			if err != nil {
				if !tst.isErr {
					t.Error("unexpected error", err)
				}
			} else {
				if tst.isErr {
					t.Errorf("error expected, but got none, result: %v", *res)
				} else {
					if res == nil {
						t.Error("unexpectected nil value of parsed rec")
					} else {
						if diff := cmp.Diff(exportedFields(*res), exportedFields(tst.res), opt); diff != "" {
							t.Errorf("diff in rec vals:\n%s", diff)
						}
					}
				}
			}
		})
	}
}

// recFieldsView holds the exported fields of RecBytes; cmp refuses structs
// with unexported fields.
type recFieldsView struct {
	Path, Val, Time []byte
	Received        time.Time
}

func exportedFields(r RecBytes) recFieldsView {
	return recFieldsView{Path: r.Path, Val: r.Val, Time: r.Time, Received: r.Received}
}

func TestNormalization(t *testing.T) {
	tt := []struct {
		in  string
		out string
		err bool
	}{
		{
			in:  "a.b.c",
			out: "a.b.c",
		},
		{
			in:  "a.b.c.",
			out: "a.b.c",
		},
		{
			in:  "abc.abc.abc",
			out: "abc.abc.abc",
		},
		{
			in:  ".abc.abc.abc",
			out: "abc.abc.abc",
		},
		{
			in:  "...abc.abc.abc..",
			out: "abc.abc.abc",
		},
		{
			in:  "abc..ab.abc",
			out: "abc.ab.abc",
		},
		{
			in:  "abc..def..jkl..xyz",
			out: "abc.def.jkl.xyz",
		},
		{
			in:  "ab&c",
			out: "ab_c",
		},
		{
			in:  "ab   cd.a  b. zkl",
			out: "ab___cd.a__b._zkl",
		},
		{
			in:  "ab^%+=.cdef.jk&",
			out: "ab____.cdef.jk_",
		},
		{
			in:  "a.",
			out: "a",
		},
		{
			in:  ".",
			out: "",
			err: true,
		},
		{
			in:  "...",
			out: "",
			err: true,
		},
		{
			in:  ".a.",
			out: "a",
		},
		{
			in:  ".a.b.c",
			out: "a.b.c",
		},
		{
			in:  "",
			out: "",
		},
		{
			in:  "...-..-..-.",
			out: "-.-.-",
		},
	}

	for _, test := range tt {
		s, err := appendNormalizedPath(nil, []byte(test.in))
		if err != nil && !test.err {
			t.Fatalf("got unexpected error while normalizing: %v", err)
		}
		if err == nil && test.err {
			t.Fatalf("no error when error expected, record: %s", test.in)
		}
		if string(s) != test.out {
			t.Fatalf("Got %s after normalization of %s, expected %s", s, test.in, test.out)
		}
	}
}

func TestSerialization(t *testing.T) { testSerialization(t) }
func testSerialization(t testing.TB) {
	tt := []struct {
		in  RecBytes
		out []byte
	}{
		{
			in: RecBytes{
				Path: []byte("this.is.a.path"),
				Val:  []byte("1.23"),
				Time: []byte("987654"),
			},
			out: []byte("this.is.a.path 1.23 987654\n"),
		},
		{
			in: RecBytes{
				Path: []byte("a.b.c.d.path"),
				Val:  []byte("89.0987"),
				Time: []byte("1568889265"),
			},
			out: []byte("a.b.c.d.path 89.0987 1568889265\n"),
		},
	}

	for _, test := range tt {
		if !bytes.Equal(test.in.Serialize(), test.out) {
			t.Errorf("expected serialization output %s, got %s for record %+v", test.out, test.in.Serialize(), test.in)
		}
	}
}

func BenchmarkSerialization(b *testing.B) {
	for i := 0; i < b.N; i++ {
		// This also benches string comparison in the test, but let's keep it simple.
		testSerialization(b)
	}
}

func FuzzParseRecBytes(f *testing.F) {
	data, _, _, _ := test.Setup()
	data = data[:10]
	for _, rec := range data {
		f.Add(rec)
	}

	f.Fuzz(func(t *testing.T, in []byte) {
		rec, err := ParseRec(in, true, false, func() time.Time { return time.Unix(1e8, 0) }, nil)
		if err != nil {
			return
		}

		if len(rec.Path) == 0 {
			t.Error("got 0 length path")
		}
		if len(rec.Time) == 0 {
			t.Error("got 0 length time")
		}
		if len(rec.Val) == 0 {
			t.Error("got 0 length value")
		}

		norm2, err := appendNormalizedPath(nil, rec.Path)
		if err != nil {
			t.Errorf("error on second normalization, %v", err)
		}
		if !bytes.Equal(rec.Path, norm2) {
			t.Errorf("second normalization %s differs from the first %s", norm2, rec.Path)
		}
	})
}

// oldNormalizePath is the pre-optimisation normalizePath, kept verbatim as
// the oracle for appendNormalizedPath.
func oldNormalizePath(s []byte) ([]byte, error) {
	if len(s) == 0 {
		return []byte{}, nil
	}

	start := 0
	for ; start < len(s) && s[start] == '.'; start++ {
	}
	if start == len(s) {
		return []byte{}, errors.New("path contains only dots")
	}

	end := len(s) - 1
	for ; end >= start && s[end] == '.'; end-- {
	}

	needsNormalization := false
	for i := start; i <= end; i++ {
		if s[i] == '.' {
			if i == len(s)-1 {
				needsNormalization = true
				break
			} else if s[i+1] == '.' {
				needsNormalization = true
				break
			}
		}

		if !validChar(s[i]) {
			needsNormalization = true
			break
		}
	}

	res := []byte{}
	if needsNormalization {
		for i := start; i <= end; i++ {
			if s[i] == '.' {
				if i == len(s)-1 {
					continue
				} else if s[i+1] == '.' {
					continue
				}
			}

			if validChar(s[i]) {
				res = append(res, s[i])
			} else {
				res = append(res, byte('_'))
			}
		}
	} else {
		if start == 0 && end == len(s)-1 {
			return s, nil
		}
		res = append(res, s[start:end+1]...)
		return res, nil
	}

	return res, nil
}

// oldSerialize is the pre-optimisation ParseRec + Serialize pipeline.
func oldParseSerialize(s []byte, normalize bool) ([]byte, error) {
	pathStart, pathEnd, valStart, valEnd, timeStart, timeEnd, err := recFields(s)
	if err != nil {
		return nil, err
	}
	var path []byte
	if normalize {
		path, err = oldNormalizePath(s[pathStart:pathEnd])
		if err != nil {
			return nil, err
		}
	} else {
		path = append(path, s[pathStart:pathEnd]...)
	}
	r := RecBytes{Path: path}
	r.Val = append(r.Val, s[valStart:valEnd]...)
	r.Time = append(r.Time, s[timeStart:timeEnd]...)
	return r.Serialize(), nil
}

func randomRecord(rnd *rand.Rand) []byte {
	const alphabet = "ab.c.._-:#zZ09 \t&^%+=\xff\xe2\n"
	n := rnd.Intn(24)
	b := make([]byte, 0, n)
	for i := 0; i < n; i++ {
		b = append(b, alphabet[rnd.Intn(len(alphabet))])
	}
	return b
}

func TestNormalizationDifferential(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	for i := 0; i < 200000; i++ {
		in := randomRecord(rnd)
		want, wantErr := oldNormalizePath(in)
		got, gotErr := appendNormalizedPath(nil, in)
		if (wantErr != nil) != (gotErr != nil) || !bytes.Equal(got, want) {
			t.Fatalf("appendNormalizedPath(%q) = %q, %v; want %q, %v", in, got, gotErr, want, wantErr)
		}
		// appending must not touch the prefix already in dst
		pre := []byte("prefix")
		got3, _ := appendNormalizedPath(pre, in)
		if wantErr == nil && !bytes.Equal(got3, append([]byte("prefix"), want...)) {
			t.Fatalf("appendNormalizedPath with prefix(%q) = %q", in, got3)
		}
	}
}

func checkParseDifferential(t *testing.T, in []byte) {
	t.Helper()
	for _, normalize := range []bool{true, false} {
		want, wantErr := oldParseSerialize(in, normalize)
		r, gotErr := ParseRec(in, normalize, false, time.Now, zap.NewNop())
		if (wantErr != nil) != (gotErr != nil) {
			t.Fatalf("ParseRec(%q, %v) err=%v, want err=%v", in, normalize, gotErr, wantErr)
		}
		if gotErr != nil {
			continue
		}
		if !bytes.Equal(r.Bytes(), want) || !bytes.Equal(r.Serialize(), want) {
			t.Fatalf("ParseRec(%q, %v): Bytes=%q Serialize=%q, want %q", in, normalize, r.Bytes(), r.Serialize(), want)
		}
		if !r.lineIsCurrent() {
			t.Fatalf("ParseRec(%q): line not current", in)
		}
		h := fnv.New64a()
		h.Write(r.Path)
		if r.PathHash() != h.Sum64() {
			t.Fatalf("PathHash(%q) mismatch", r.Path)
		}
	}
}

func TestParseRecDifferential(t *testing.T) {
	rnd := rand.New(rand.NewSource(2))
	for i := 0; i < 200000; i++ {
		f1, f2, f3 := randomRecord(rnd), randomRecord(rnd), randomRecord(rnd)
		checkParseDifferential(t, bytes.Join([][]byte{f1, f2, f3}, []byte(" ")))
		checkParseDifferential(t, randomRecord(rnd))
	}
}

// TestParseRecCapture runs the parse differential over a captured traffic
// file, e.g. NANOTUBE_CAPTURE=/path/to/in.txt.
func TestParseRecCapture(t *testing.T) {
	path := os.Getenv("NANOTUBE_CAPTURE")
	if path == "" {
		t.Skip("NANOTUBE_CAPTURE not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		checkParseDifferential(t, line)
		n++
	}
	t.Logf("checked %d records", n)
}

func TestBytesTracksReassignment(t *testing.T) {
	r, err := ParseRec([]byte("a.b.c 1 2"), true, false, time.Now, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if string(r.Bytes()) != "a.b.c 1 2\n" {
		t.Fatalf("got %q", r.Bytes())
	}
	h1 := r.PathHash()

	r.Path = []byte("x.y")
	if string(r.Bytes()) != "x.y 1 2\n" {
		t.Fatalf("after Path reassignment got %q", r.Bytes())
	}
	h := fnv.New64a()
	h.Write([]byte("x.y"))
	if r.PathHash() != h.Sum64() || r.PathHash() == h1 {
		t.Fatal("PathHash not recomputed after Path reassignment")
	}

	r.Val = append(r.Val, '5')
	if string(r.Bytes()) != "x.y 15 2\n" {
		t.Fatalf("after Val append got %q", r.Bytes())
	}

	r2, _ := ParseRec([]byte("a.b 1 2"), true, false, time.Now, zap.NewNop())
	r2.Path = append(r2.Path, 'z')
	if string(r2.Bytes()) != "a.bz 1 2\n" || string(r2.Val) != "1" {
		t.Fatalf("append to Path clobbered the record: %q", r2.Bytes())
	}
	r2.Path = r2.Path[:1]
	if string(r2.Bytes()) != "a 1 2\n" {
		t.Fatalf("after reslicing Path got %q", r2.Bytes())
	}

	cpy, _ := r.Copy()
	if string(cpy.Bytes()) != "x.y 15 2\n" {
		t.Fatalf("copy got %q", cpy.Bytes())
	}
}

func TestCharClasses(t *testing.T) {
	for c := 0; c < 256; c++ {
		if got, want := charClasses[c] != charInvalid, validChar(byte(c)); got != want {
			t.Fatalf("byte %#x: valid=%v, validChar=%v", c, got, want)
		}
		if (charClasses[c] == charDot) != (c == '.') {
			t.Fatalf("byte %#x: wrong dot class", c)
		}
	}
}
