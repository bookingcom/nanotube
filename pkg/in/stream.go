package in

import (
	"bufio"
	"errors"
	"io"
	"net"
	"time"

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
// NameBytes is the connection's admission charge, the codec's external-name
// estimate; physical shared bytes are reported by the binder itself. ReleaseName
// must undo every BindName, including definitions restored by a handoff snapshot.
type StreamNameBinder interface {
	BindName([]byte) any
	ReleaseName(any)
	NameBytes() int
	SetNameLimit(int)
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
	reservation := int64(cfg.StreamInputDictionaryLimit()) + int64(cfg.StreamInputRouteLimit())
	if !ms.ReserveStreamInput(reservation, int64(cfg.StreamInputTotalLimit())) {
		fail(errors.New("stream input global dictionary/route reservation exhausted"))
		return
	}
	defer ms.ReleaseStreamInput(reservation)
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
	nameBytes := 0
	binder, shared := sink.(StreamNameBinder)
	if shared {
		d.SetNameResolver(func(name []byte) any {
			binder.SetNameLimit(cfg.StreamInputDictionaryLimit() - d.RetainedBytes())
			return binder.BindName(name)
		})
		d.SetNameReleaser(binder.ReleaseName)
	} else {
		d.SetNameResolver(func(name []byte) any {
			owned := append([]byte(nil), name...)
			nameBytes += len(owned) + 64
			return &inputName{name: owned}
		})
	}
	defer sink.Flush()
	if closer, ok := sink.(interface{ Close() }); ok {
		defer closer.Close()
	}
	defer d.Close() // Release names before closing their sink, including on handoff.
	retained := 0
	defer func() { ms.StreamInputDictionaryBytes.Sub(float64(retained)) }()
	// Stage at most one bounded frame. No point reaches routing before both
	// codec validation and the aggregate dictionary-byte budget pass.
	var pending []inputPoint
	for {
		if c.moving() {
			detachStream(c, r, d, flags)
			return
		}
		pending = pending[:0]
		info, err := d.ReadFrame(r, func(id uint32, name []byte, value float64, ts int64, slot *any) {
			if !shared {
				slot = &(*slot).(*inputName).route
			}
			// Decoder slots can move only on the next frame. All pending points
			// are delivered (or discarded) before then.
			pending = append(pending, inputPoint{id, name, value, ts, slot})
		})
		if err != nil {
			if c.moving() {
				detachStream(c, r, d, flags)
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
		}
		current := d.RetainedBytes() + nameBytes
		if shared && d.RetainedBytes()+binder.NameBytes() > cfg.StreamInputDictionaryLimit() {
			fail(errors.New("stream input dictionary allowance exhausted"))
			return
		}
		ms.StreamInputDictionaryBytes.Add(float64(current - retained))
		retained = current
		if info.Kind == codec.ResetFrame {
			sink.Reset()
			ms.StreamInputResets.Inc()
		}
		if limiters != nil && len(pending) > 0 {
			if !ratelimiter.RateLimitWithStop(limiters, len(pending), time.Duration(cfg.RateLimiterRetryDurationMs)*time.Millisecond, term) {
				return
			}
		}
		ms.StreamInputFrames.Inc()
		ms.StreamInputPoints.Add(float64(len(pending)))
		ms.InRecs.Add(float64(len(pending)))
		for _, p := range pending {
			sink.Point(p.id, p.name, p.value, p.timestamp, p.slot)
		}
		sink.Flush()
		clear(pending) // No decoder names remain retained across the next reset.
	}
}

func detachStream(c *inputConn, r *bufio.Reader, d *codec.Decoder, flags byte) {
	c.detach(func(w io.Writer) error {
		prefix, err := codec.AppendPreamble(nil, flags)
		if err == nil {
			prefix, err = d.Snapshot(prefix)
		}
		if err != nil {
			return err
		}
		if _, err = w.Write([]byte{0}); err != nil {
			return err
		}
		if _, err = w.Write(prefix); err != nil {
			return err
		}
		if _, err = w.Write(d.Pending()); err != nil {
			return err
		}
		return writeBuffered(w, c, r)
	})
}
