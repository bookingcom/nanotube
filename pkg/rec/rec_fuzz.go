// +build gofuzz

package rec

import "time"

// Fuzz does the fuzz testing
func Fuzz(data []byte) int {
	_, err := ParseRecBytes(data, true, false, func() time.Time { return time.Unix(1e8, 0) }, nil)
	if err != nil {
		return 0
	}

	return 1
}
