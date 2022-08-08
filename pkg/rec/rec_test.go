package rec

import (
	"bytes"
	"math"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/test"
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
		s, err := normalizePath([]byte(test.in))
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

		norm2, err := normalizePath(rec.Path)
		if err != nil {
			t.Errorf("error on second normalization, %v", err)
		}
		if !bytes.Equal(rec.Path, norm2) {
			t.Errorf("second normalization %s differs from the first %s", norm2, rec.Path)
		}
	})
}
