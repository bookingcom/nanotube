package in

import (
	"bufio"
	"errors"
	"io"
	"net"
	"time"
	"unsafe"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/ratelimiter"
	codec "github.com/bookingcom/nanotube/pkg/streamcodec"
	"go.uber.org/zap"
)

// StreamSink belongs to one input connection. It is called serially and must
// retain immutable routing paths, and must not retain the decoder slot pointer.
type StreamSink interface {
	Point(uint32, []byte, float64, int64, *any)
	Reset()
	Flush()
}
type StreamFactory func() StreamSink

// StreamNameBinder shares immutable definitions and plans across connections.
// NameBytes is the connection's logical allowance charge, the codec's external-
// name estimate; physical shared bytes are admitted and reported by the binder.
// ReleaseName must undo every BindName, including handoff snapshot definitions.
type StreamNameBinder interface {
	BindName([]byte) any
	ReleaseName(any)
	NameBytes() int
	SetNameLimit(int)
}

// StreamBoundSink uses compact decoder references instead of arbitrary slots.
type StreamBoundSink interface {
	StreamNameBinder
	codec.NameBinder
	BoundPoint(*codec.BoundName, bool, float64, int64)
}

type inputNameBinder struct {
	StreamBoundSink
	decoder *codec.Decoder
	limit   int
}

func (b *inputNameBinder) BindStreamName(name []byte) (*codec.BoundName, bool, error) {
	b.SetNameLimit(b.limit - b.decoder.RetainedBytes())
	return b.StreamBoundSink.BindStreamName(name)
}

// Names bound outside the decoder arena remain immutable across reset. Queued
// records can outlive the dictionary generation; the copied codec reuses arenas.
type inputName struct {
	name  []byte
	route any
}

func (n *inputName) StreamName() []byte { return n.name }

type inputPoint struct {
	id        uint32
	name      []byte
	value     float64
	timestamp int64
	slot      *any
}

type boundInputPoint struct {
	name      *codec.BoundName
	value     float64
	timestamp int64
	cached    bool
}

func readStream(r *bufio.Reader, term <-chan struct{}, limiters []*ratelimiter.SlidingWindow, cfg *conf.Main, ms *metrics.Prom, lg *zap.Logger, sink StreamSink, handoff ...*inputConn) {
	var c *inputConn
	if len(handoff) > 0 {
		c = handoff[0]
	}
	fail := func(err error) {
		// An idle producer hitting IncomingConnIdleTimeoutSec is a normal close, not a stream error.
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			lg.Info("stream input connection idle, closing", zap.Error(err))
			return
		}
		ms.ErrorRecs.Inc()
		ms.StreamInputErrors.Inc()
		lg.Error("stream input failed", zap.Error(err))
	}
	ms.StreamInputConnections.Inc()
	// Fixed connection allowance, independent of its maximum dictionary size.
	const initialReservation = 4096
	reservation := int64(initialReservation)
	if !ms.ReserveStreamInput(reservation, int64(cfg.StreamInputTotalLimit())) {
		fail(errors.New("stream input global dictionary/route reservation exhausted"))
		return
	}
	defer func() { ms.ReleaseStreamInput(reservation) }()
	resize := func(n int) bool {
		want := int64(initialReservation + n)
		if want > reservation {
			if !ms.ReserveStreamInput(want-reservation, int64(cfg.StreamInputTotalLimit())) {
				return false
			}
		} else if want < reservation {
			ms.ReleaseStreamInput(reservation - want)
		}
		reservation = want
		return true
	}
	ms.StreamInputActive.Inc()
	defer ms.StreamInputActive.Dec()
	preamble := make([]byte, codec.PreambleSize)
	if n, err := io.ReadFull(r, preamble); err != nil {
		if c.moving() {
			c.detach(func(w io.Writer) error { return writeWirePrefix(w, c, r, preamble[:n]) })
			return
		}
		fail(err)
		return
	}
	flags, err := codec.ParsePreamble(preamble)
	if err != nil {
		fail(err)
		return
	}
	frame, names := cfg.StreamInputMaxFrameBytes, cfg.StreamInputMaxNames
	if frame == 0 {
		frame = 8 << 20
	}
	if names == 0 {
		names = 1000000
	}
	if frame < 64 || frame > 8<<20 || names < 1 || names > 5000000 {
		fail(errors.New("invalid stream input limits"))
		return
	}
	d, err := codec.NewDecoder(codec.Options{Flags: flags, MaxFrameSize: frame, MaxNames: names, MaxRows: 65536, DictionaryBytes: cfg.StreamInputDictionaryLimit()})
	if err != nil {
		fail(err)
		return
	}
	if c != nil && c.input != nil {
		d.SetDecoderPool(c.input.decoders)
	}
	nameBytes, decoderBytes, bufferBytes, pendingBytes := 0, 0, 0, 0
	var pending []inputPoint
	var boundPending []boundInputPoint
	binder, shared := sink.(StreamNameBinder)
	boundSink, bound := sink.(StreamBoundSink)
	if bound {
		d.SetNameBinder(&inputNameBinder{StreamBoundSink: boundSink, decoder: d, limit: cfg.StreamInputDictionaryLimit()})
	} else if shared {
		d.SetNameResolver(func(name []byte) any {
			binder.SetNameLimit(cfg.StreamInputDictionaryLimit() - d.RetainedBytes())
			return binder.BindName(name)
		})
		d.SetNameReleaser(binder.ReleaseName)
	} else {
		d.SetNameResolver(func(name []byte) any {
			charge := len(name) + 64
			if !resize(decoderBytes + bufferBytes + pendingBytes + nameBytes + charge) {
				return codec.ErrLimit
			}
			owned := append([]byte(nil), name...)
			nameBytes += charge
			return &inputName{name: owned}
		})
	}
	d.SetDictionaryBudget(func(n int) bool {
		if !resize(n + bufferBytes + pendingBytes + nameBytes) {
			return false
		}
		decoderBytes = n
		return true
	})
	d.SetBufferBudget(func(n, rows int) bool {
		capacity := max(cap(pending), rows)
		pointBytes := int(unsafe.Sizeof(inputPoint{}))
		if bound {
			capacity = max(cap(boundPending), rows)
			pointBytes = int(unsafe.Sizeof(boundInputPoint{}))
		}
		staging := capacity * pointBytes
		if !resize(decoderBytes + n + staging + nameBytes) {
			return false
		}
		if bound {
			if capacity > cap(boundPending) {
				boundPending = make([]boundInputPoint, 0, capacity)
			}
		} else if capacity > cap(pending) {
			pending = make([]inputPoint, 0, capacity)
		}
		bufferBytes, pendingBytes = n, staging
		return true
	})
	defer sink.Flush()
	if closer, ok := sink.(interface{ Close() }); ok {
		defer closer.Close()
	}
	defer d.Close() // Release names before closing their sink, including on handoff.
	retained, retainedNames := 0, 0
	defer func() {
		ms.StreamInputDictionaryBytes.Sub(float64(retained))
		ms.StreamInputDictionaryNames.Sub(float64(retainedNames))
	}()
	dataDefinitions := ms.StreamInputNamesDefined.WithLabelValues("data")
	stateDefinitions := ms.StreamInputNamesDefined.WithLabelValues("state")
	detach := func() {
		// Transfer accounting before the successor can replay any snapshot names.
		// The old decoder stays alive only to serialize state and release storage.
		ms.StreamInputDictionaryNames.Sub(float64(retainedNames))
		retainedNames = 0
		c.releaseBytes = uint64(reservation)
		detachStream(c, r, d, flags)
	}
	// Stage at most one bounded frame. No point reaches routing before both
	// codec validation and the aggregate dictionary-byte budget pass.

	for {
		if c.moving() {
			detach()
			return
		}
		pending = pending[:0]
		boundPending = boundPending[:0]
		var info codec.FrameInfo
		if bound {
			info, err = d.ReadBoundFrame(r, func(name *codec.BoundName, cached bool, value float64, ts int64) {
				boundPending = append(boundPending, boundInputPoint{name, value, ts, cached})
			})
		} else {
			info, err = d.ReadFrame(r, func(id uint32, name []byte, value float64, ts int64, slot *any) {
				if !shared {
					slot = &(*slot).(*inputName).route
				}
				// Slots can move only on the next frame, after pending delivery.
				pending = append(pending, inputPoint{id: id, name: name, value: value, timestamp: ts, slot: slot})
			})
		}
		if err != nil {
			if c.moving() {
				detach()
				return
			}
			select {
			case <-term:
				return
			default:
			}
			if err != io.EOF {
				fail(err)
			}
			return
		}
		if info.Kind == codec.ResetFrame {
			nameBytes = 0
			pending, boundPending, pendingBytes = nil, nil, 0
			_ = resize(decoderBytes + bufferBytes)
		}
		current := d.RetainedBytes() + nameBytes
		if shared && d.RetainedBytes()+binder.NameBytes() > cfg.StreamInputDictionaryLimit() {
			fail(errors.New("stream input dictionary allowance exhausted"))
			return
		}
		ms.StreamInputDictionaryBytes.Add(float64(current - retained))
		retained = current
		if names := d.NameCount(); names != retainedNames {
			ms.StreamInputDictionaryNames.Add(float64(names - retainedNames))
			retainedNames = names
		}
		if info.Names != 0 {
			definitions := dataDefinitions
			if info.Kind == codec.StateFrame {
				definitions = stateDefinitions
			}
			definitions.Add(float64(info.Names))
		}
		if info.Kind == codec.ResetFrame {
			sink.Reset()
			ms.StreamInputResets.Inc()
		}
		count := len(pending) + len(boundPending)
		if limiters != nil && count > 0 {
			if !ratelimiter.RateLimitWithStop(limiters, count, time.Duration(cfg.RateLimiterRetryDurationMs)*time.Millisecond, term) {
				return
			}
		}
		ms.StreamInputFrames.Inc()
		ms.StreamInputPoints.Add(float64(count))
		ms.InRecs.Add(float64(count))
		for _, p := range pending {
			sink.Point(p.id, p.name, p.value, p.timestamp, p.slot)
		}
		for _, p := range boundPending {
			boundSink.BoundPoint(p.name, p.cached, p.value, p.timestamp)
		}
		sink.Flush()
		clear(pending) // No decoder names remain retained across the next reset.
		clear(boundPending)
	}
}

func detachStream(c *inputConn, r *bufio.Reader, d *codec.Decoder, flags byte) {
	c.detach(func(w io.Writer) error {
		prefix, err := codec.AppendPreamble(nil, flags)
		if err != nil {
			return err
		}
		if _, err = w.Write([]byte{0}); err != nil {
			return err
		}
		if _, err = w.Write(prefix); err != nil {
			return err
		}
		if err = d.WriteSnapshot(w); err != nil {
			return err
		}
		if _, err = w.Write(d.Pending()); err != nil {
			return err
		}
		return writeBuffered(w, c, r)
	})
}
