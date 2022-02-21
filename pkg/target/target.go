package target

import (
	"sync"

	"github.com/bookingcom/nanotube/pkg/rec"
)

// Target represents target the records are sent to
type Target interface {
	Stream(wg *sync.WaitGroup)
	Push(r *rec.Rec)
	IsAvailable() bool
	Stop()
}
