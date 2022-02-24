package rec

import (
	"bytes"
	"math"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
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
				Path:    []byte("a"),
				RawVal:  []byte("a"),
				RawTime: []byte("a"),
			},
			isErr: false,
		},
		{
			s: "1 a 1",
			res: RecBytes{
				Path:    []byte("1"),
				RawVal:  []byte("a"),
				RawTime: []byte("1"),
			},
			isErr: false,
		},
		{
			s: "a 1 1",
			res: RecBytes{
				Path:    []byte("a"),
				RawVal:  []byte("1"),
				RawTime: []byte("1"),
			},
			isErr: false,
		},
		{
			s: "a.a.a 1.1e3 123",
			res: RecBytes{
				Path:    []byte("a.a.a"),
				RawVal:  []byte("1.1e3"),
				RawTime: []byte("123"),
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
				Path:    []byte("a"),
				RawVal:  []byte("1"),
				RawTime: []byte("a"),
			},
			isErr: false,
		},
		{
			s: "asdf.fdsa.a.1.c 12e-3 1234567890",
			res: RecBytes{
				Path:    []byte("asdf.fdsa.a.1.c"),
				RawVal:  []byte("12e-3"),
				RawTime: []byte("1234567890"),
			},
			isErr: false,
		},
		// test path normalization
		{
			s: ".abc.abc.abc 1.23 12345678",
			res: RecBytes{
				Path:    []byte("abc.abc.abc"),
				RawVal:  []byte("1.23"),
				RawTime: []byte("12345678"),
			},
			isErr: false,
		},
		{
			s: "..abc..ab&+=.jklm.jk% 1.234 1234567890",
			res: RecBytes{
				Path:    []byte("abc.ab___.jklm.jk_"),
				RawVal:  []byte("1.234"),
				RawTime: []byte("1234567890"),
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
				Path:    []byte("abc.jkl.mno"),
				RawVal:  []byte("1.23"),
				RawTime: []byte("12345"),
			},
			isErr: false,
		},
		{
			s: "some.thing.here			98.7  12345",
			res: RecBytes{
				Path:    []byte("some.thing.here"),
				RawVal:  []byte("98.7"),
				RawTime: []byte("12345"),
			},
			isErr: false,
		},
		{
			s: "fractional.time.test 3.3 123.45",
			res: RecBytes{
				Path:    []byte("fractional.time.test"),
				RawVal:  []byte("3.3"),
				RawTime: []byte("123.45"),
			},
		},
		{
			s: "large.float.test 1.79769313486231e+308 12345",
			res: RecBytes{
				Path:    []byte("large.float.test"),
				RawVal:  []byte("1.79769313486231e+308"),
				RawTime: []byte("12345"),
			},
		},
		{
			s: ". 0 0",
			res: RecBytes{
				Path:    []byte(""),
				RawVal:  []byte("0"),
				RawTime: []byte("0"),
			},
			isErr: true,
		},
		{
			s: "a.b.c NaN 12345",
			res: RecBytes{
				Path:    []byte("a.b.c"),
				RawVal:  []byte("NaN"),
				RawTime: []byte("12345"),
			},
			isErr: false,
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
			res, err := ParseRecBytes([]byte(tst.s), true, true, nowF, lg)
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
						if diff := cmp.Diff(*res, tst.res, opt); diff != "" {
							t.Errorf("diff in rec vals:\n%s", diff)
						}
					}
				}
			}
		})
	}
}

func TestNormalization(t *testing.T) {
	tt := []struct {
		in  string
		out string
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
	}

	for _, test := range tt {
		s, err := normalizePathBytes([]byte(test.in))
		if err != nil {
			t.Fatalf("got error while normalizing: %v", err)
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
				Path:    []byte("this.is.a.path"),
				RawVal:  []byte("1.23"),
				RawTime: []byte("987654"),
			},
			out: []byte("this.is.a.path 1.23 987654\n"),
		},
		{
			in: RecBytes{
				Path:    []byte("a.b.c.d.path"),
				RawVal:  []byte("89.0987"),
				RawTime: []byte("1568889265"),
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
