package target

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"testing"

	"github.com/bookingcom/nanotube/pkg/rec"
)

func checkStreamNumber(t testing.TB, s []byte) {
	t.Helper()
	want, e1 := strconv.ParseFloat(string(s), 64)
	got, e2 := parseStreamNumber(s)
	if math.Float64bits(want) != math.Float64bits(got) || (e1 == nil) != (e2 == nil) || (e1 != nil && e1.Error() != e2.Error()) {
		t.Fatalf("%q: got %x/%v, want %x/%v", s, math.Float64bits(got), e2, math.Float64bits(want), e1)
	}
}

func TestStreamNumberDifferential(t *testing.T) {
	for _, s := range []string{
		"", ".", "+", "-", "+.", "-.", "0", "-0", "+0", "-0.000", "-.0", "1.", ".5", "-.5",
		"999999999999999", "99999999999999.9", "9999999999999.9", "999999999999.99", "1.23456789012345",
		"0.0000000000001", "-00000000000000", "4503599627370495", "9007199254740993", "9223372036854775807",
		"-9223372036854775808", "1e-324", "-1e-9999", "1e309", "NaN", "nan", "+Inf", "-Inf", "Infinity",
		"0x1.8p+2", "1_000.125", "1.2.3", " 1", "1 ", "0\x00", "1e", "--0", "0,1",
	} {
		checkStreamNumber(t, []byte(s))
	}
	rng := rand.New(rand.NewPCG(3, 4))
	for range 100_000 {
		n := rng.Uint64() % 1_000_000_000_000_000
		s := strconv.AppendUint(nil, n, 10)
		if rng.IntN(2) == 0 {
			at := rng.IntN(len(s) + 1)
			s = append(s, 0)
			copy(s[at+1:], s[at:])
			s[at] = '.'
		}
		if rng.IntN(2) == 0 {
			s = append([]byte{'-'}, s...)
		}
		checkStreamNumber(t, s)
		// Exercise exact fallback as well as the common fixed-point path.
		checkStreamNumber(t, []byte(fmt.Sprintf("%.17g", math.Float64frombits(rng.Uint64()))))
	}
}

func FuzzStreamNumber(f *testing.F) {
	for _, s := range []string{"0", "-0", "12345.125", "1700000000", "-.0", "1.9999999999999", "NaN", "-Inf", "1e300", "0x1.8p+2", "1_000.5"} {
		f.Add([]byte(s), []byte("1700000000"))
	}
	f.Fuzz(func(t *testing.T, value, stamp []byte) {
		checkStreamNumber(t, value)
		checkStreamNumber(t, stamp)
		v, ve := strconv.ParseFloat(string(value), 64)
		ts, te := strconv.ParseFloat(string(stamp), 64)
		wantOK := ve == nil && te == nil && !math.IsNaN(v) && !math.IsNaN(ts)
		p, ok := parseStreamPoint(&rec.RecBytes{Path: []byte("example.metric"), Val: value, Time: stamp})
		if ok != wantOK || (ok && (math.Float64bits(p.value) != math.Float64bits(v) || p.timestamp != int64(ts))) {
			t.Fatal("point parser differs from float64 oracle")
		}
	})
}
