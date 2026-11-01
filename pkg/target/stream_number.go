package target

import "strconv"

// parseStreamNumber handles short fixed-point decimals and delegates every
// other syntax to the existing parser. With at most 15 digits, both mantissa
// and the integer power-of-ten divisor are exactly representable as float64.
// One correctly rounded division therefore gives the same bits as ParseFloat.
// Applying the sign last also preserves negative zero.
func parseStreamNumber(s []byte) (float64, error) {
	start := 0
	negative := false
	if len(s) > 0 && (s[0] == '-' || s[0] == '+') {
		negative = s[0] == '-'
		start = 1
	}
	if n := len(s) - start; n > 0 && n <= 15 {
		var mantissa uint64
		scale := uint64(1)
		dot, digits := false, false
		for _, c := range s[start:] {
			if d := c - '0'; d <= 9 {
				mantissa = mantissa*10 + uint64(d)
				if dot {
					scale *= 10
				}
				digits = true
			} else if c == '.' && !dot {
				dot = true
			} else {
				return strconv.ParseFloat(string(s), 64)
			}
		}
		if digits {
			v := float64(mantissa)
			if scale != 1 {
				v /= float64(scale)
			}
			if negative {
				v = -v
			}
			return v, nil
		}
	}
	return strconv.ParseFloat(string(s), 64)
}
