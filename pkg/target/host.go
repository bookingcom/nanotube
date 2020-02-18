package target

import (
	"bufio"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// Host represents a single target hosts to send records to.
type Host struct {
	Name string
	Port uint16
	// TODO (grzkv): Replace w/ circular buffer
	Ch   chan *rec.Rec
	Conn net.Conn

	W    *bufio.Writer
	CWm  sync.Mutex // this is the mutex to lock connection and writer
	stop chan int

	Lg                      *zap.Logger
	Ms                      *metrics.Prom
	SendTimeoutSec          uint32
	ConnTimeoutSec          uint32
	TCPOutBufFlushPeriodSec uint32

	outRecs            prometheus.Counter
	throttled          prometheus.Counter
	processingDuration prometheus.Histogram
	bufSize            int
}

//NewHost build new host object from config
func NewHost(clusterName string, mainCfg conf.Main, hostCfg conf.Host, lg *zap.Logger, ms *metrics.Prom) *Host {
	targetPort := mainCfg.TargetPort
	if hostCfg.Port != 0 {
		targetPort = hostCfg.Port
	}

	promLabels := prometheus.Labels{
		"cluster":       clusterName,
		"upstream_host": hostCfg.Name,
	}
	return &Host{
		Name: hostCfg.Name,
		Port: targetPort,
		Ch:   make(chan *rec.Rec, mainCfg.HostQueueSize),
		Lg:   lg,
		stop: make(chan int),

		SendTimeoutSec:          mainCfg.SendTimeoutSec,
		ConnTimeoutSec:          mainCfg.OutConnTimeoutSec,
		TCPOutBufFlushPeriodSec: mainCfg.TCPOutBufFlushPeriodSec,
		outRecs:                 ms.OutRecs.With(promLabels),
		throttled:               ms.ThrottledHosts.With(promLabels),
		processingDuration:      ms.ProcessingDuration,
		bufSize:                 mainCfg.TCPOutBufSize,
	}
}

// Push adds a new record to send to the host queue.
func (h *Host) Push(r *rec.Rec) {
	select {
	case h.Ch <- r:
	default:
		h.throttled.Inc()
	}
}

// Stream launches the the sending to target host.
// Exits when queue is closed and sending is finished.
func (h *Host) Stream(wg *sync.WaitGroup) {
	// TODO (grzkv) Maybe move (re)connection to a separate goroutine and communicate via a chan

	if h.TCPOutBufFlushPeriodSec != 0 {
		go h.Flush(time.Second * time.Duration(h.TCPOutBufFlushPeriodSec))
	}
	defer func() {
		wg.Done()
		close(h.stop)
	}()

	for r := range h.Ch {
		// retry until successful
		for {
			const maxReconnectPeriodMs = 5000
			const reconnectDeltaMs = 10
			for reconnectWait := 0; h.Conn == nil; {
				time.Sleep(time.Duration(reconnectWait) * time.Millisecond)
				if reconnectWait < maxReconnectPeriodMs {
					reconnectWait = reconnectWait*2 + reconnectDeltaMs
				}
				if reconnectWait >= maxReconnectPeriodMs {
					reconnectWait = maxReconnectPeriodMs
				}

				h.Connect()
			}

			h.CWm.Lock()
			err := h.Conn.SetWriteDeadline(time.Now().Add(
				time.Duration(h.SendTimeoutSec) * time.Second))
			h.CWm.Unlock()
			if err != nil {
				h.Lg.Warn("error setting write deadline. Renewing the connection.",
					zap.Error(err))
			}

			// this may loose one record on disconnect
			_, err = h.Write([]byte(*r.Serialize()))

			if err == nil {
				h.outRecs.Inc()
				h.processingDuration.Observe(time.Since(r.Received).Seconds())
				break
			}

			h.Lg.Warn("error sending value to host. Reconnect and retry..",
				zap.String("target", h.Name),
				zap.Uint16("port", h.Port),
				zap.Error(err),
			)
			h.CWm.Lock()
			err = h.Conn.Close()
			h.CWm.Unlock()
			if err != nil {
				// not retrying here, file descriptor may be lost
				h.Lg.Error("error closing the connection",
					zap.Error(err))
			}

			h.CWm.Lock()
			h.Conn = nil
			h.CWm.Unlock()
		}
	}
}

// Write does the remote write, i.e. sends the data
func (h *Host) Write(b []byte) (nn int, err error) {
	h.CWm.Lock()
	defer h.CWm.Unlock()
	return h.W.Write(b)
}

// Flush immediately flushes the buffer and performs a write
func (h *Host) Flush(d time.Duration) {
	t := time.NewTicker(d)
	defer t.Stop()

	for {
		select {
		case <-h.stop:
			return
		case <-t.C:
			h.CWm.Lock()
			if h.W != nil {
				if h.W.Buffered() != 0 {
					err := h.W.Flush()
					if err != nil {
						h.Lg.Error("error while flushing the host buffer", zap.Error(err), zap.String("host name", h.Name), zap.Uint16("host port", h.Port))
						// if flushing fails, the connection has to be re-established
						h.Conn = nil
						h.W = nil
					}
				}
			}
			h.CWm.Unlock()
		}
	}
}

// Connect connects to target host via TCP. If unsuccessful, sets conn to nil.
func (h *Host) Connect() {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", h.Name, h.Port),
		time.Duration(h.ConnTimeoutSec)*time.Second)
	h.CWm.Lock()
	defer h.CWm.Unlock()

	if err != nil {
		h.Lg.Warn("connection to host failed",
			zap.String("host", h.Name),
			zap.Uint16("port", h.Port))
		h.Conn = nil
		h.W = nil

		return
	}

	h.Conn = conn

	h.W = bufio.NewWriterSize(conn, h.bufSize)
}
