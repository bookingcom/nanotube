package rec

import "time"

var t = time.Now() //nolint:gochecknoglobals

func nowF() time.Time {
	return t
}

//nolint:revive
func Fuzz(data []byte) int {
	_, err := ParseRec(data, true, false, nowF, nil)
	if err != nil {
		return 0
	}

	return 1
}
