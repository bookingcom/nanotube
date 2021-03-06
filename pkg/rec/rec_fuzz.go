package rec

import "time"

var t = time.Now() //nolint:gochecknoglobals

func nowF() time.Time {
	return t
}

//nolint:revive
func Fuzz(data []byte) int {
	s := string(data)
	_, err := ParseRec(s, true, false, nowF, nil)
	if err != nil {
		return 0
	}

	return 1
}
