package rec

import (
	"math"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/zap"
)

func TestRec(t *testing.T) {
	tt := []struct {
		s     string
		res   Rec
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
			s:     "a a a",
			isErr: true,
		},
		{
			s:     "1 a 1",
			isErr: true,
		},
		{
			s: "a 1 1",
			res: Rec{
				Path:    "a",
				Val:     1.0,
				RawVal:  "1",
				Time:    1,
				RawTime: "1",
			},
			isErr: false,
		},
		{
			s: "a.a.a 1.1e3 123",
			res: Rec{
				Path:    "a.a.a",
				Val:     1.1e3,
				RawVal:  "1.1e3",
				Time:    123,
				RawTime: "123",
			},
			isErr: false,
		},
		{
			s:     " 1 1",
			isErr: true,
		},
		{
			s:     "a 1 a",
			isErr: true,
		},
		{
			s: "asdf.fdsa.a.1.c 12e-3 1234567890",
			res: Rec{
				Path:    "asdf.fdsa.a.1.c",
				Val:     0.012,
				RawVal:  "12e-3",
				Time:    1234567890,
				RawTime: "1234567890",
			},
			isErr: false,
		},
		// test path normalization
		{
			s: ".abc.abc.abc 1.23 12345678",
			res: Rec{
				Path:    "abc.abc.abc",
				Val:     1.23,
				RawVal:  "1.23",
				Time:    12345678,
				RawTime: "12345678",
			},
			isErr: false,
		},
		{
			s: "..abc..ab&+=.jklm.jk% 1.234 1234567890",
			res: Rec{
				Path:    "abc.ab___.jklm.jk_",
				Val:     1.234,
				RawVal:  "1.234",
				Time:    1234567890,
				RawTime: "1234567890",
			},
			isErr: false,
		},
		{
			s:     "abc.ab c.abc 1.234 1234",
			res:   Rec{},
			isErr: true,
		},
		// test other kinds of whitespace
		{
			s: "abc.jkl.mno    1.23   12345",
			res: Rec{
				Path:    "abc.jkl.mno",
				Val:     1.23,
				RawVal:  "1.23",
				Time:    12345,
				RawTime: "12345",
			},
			isErr: false,
		},
		{
			s: "some.thing.here			98.7  12345",
			res: Rec{
				Path:    "some.thing.here",
				Val:     98.7,
				RawVal:  "98.7",
				Time:    12345,
				RawTime: "12345",
			},
			isErr: false,
		},
		{
			s: "fractional.time.test 3.3 123.45",
			res: Rec{
				Path:    "fractional.time.test",
				Val:     3.3,
				RawVal:  "3.3",
				Time:    123,
				RawTime: "123.45",
			},
		},
		{
			s: "large.float.test 1.79769313486231e+308 12345",
			res: Rec{
				Path:    "large.float.test",
				Val:     float64(1.79769313486231e+308),
				RawVal:  "1.79769313486231e+308",
				Time:    12345,
				RawTime: "12345",
			},
		},
		{
			s: ". 0 0",
			res: Rec{
				Path:    "",
				Val:     0,
				RawVal:  "0",
				Time:    0,
				RawTime: "0",
			},
		},
		{
			s:     "a.b.c NaN 12345",
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
			res, err := ParseRec(tst.s, true, true, nowF, lg)
			if err != nil {
				if !tst.isErr {
					t.Error("unexpected error", err)
				}
			} else {
				if tst.isErr {
					t.Error("error expected, but got none")
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
		s := normalizePath(test.in)
		if s != test.out {
			t.Fatalf("Got %s after normalization of %s, expected %s", s, test.in, test.out)
		}
	}
}

func TestSerialization(t *testing.T) { testSerialization(t) }
func testSerialization(t testing.TB) {
	tt := []struct {
		in  Rec
		out string
	}{
		{
			in: Rec{
				Path:    "this.is.a.path",
				Val:     1.23,
				RawVal:  "1.23",
				Time:    987654,
				RawTime: "987654",
			},
			out: "this.is.a.path 1.23 987654\n",
		},
		{
			in: Rec{
				Path:    "a.b.c.d.path",
				Val:     89.0987,
				RawVal:  "89.0987",
				Time:    1568889265,
				RawTime: "1568889265",
			},
			out: "a.b.c.d.path 89.0987 1568889265\n",
		},
	}

	for _, test := range tt {
		if test.in.Serialize() != test.out {
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
