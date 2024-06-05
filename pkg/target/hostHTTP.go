package target

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

// Host represents a single target hosts to send records to.
type HostHTTP struct {
	Name      string
	Port      uint16
	Ch        chan *rec.RecBytes
	Available atomic.Bool
	URL       string

	stop chan int

	Lg *zap.Logger
	Ms *metrics.Prom

	conf *conf.Main

	outRecs                   prometheus.Counter
	outRecsTotal              prometheus.Counter
	throttled                 prometheus.Counter
	throttledTotal            prometheus.Counter
	stateChanges              prometheus.Counter
	stateChangesTotal         prometheus.Counter
	oldConnectionRefresh      prometheus.Counter
	oldConnectionRefreshTotal prometheus.Counter
	targetState               prometheus.Gauge
}

// String implements the Stringer interface.
func (h *HostHTTP) String() string {
	return net.JoinHostPort(h.Name, strconv.Itoa(int(h.Port)))
}

// NewHostHTTP constructs a new TCP target host.
func NewHostHTTP(clusterName string, mainCfg conf.Main, hostCfg conf.Host, lg *zap.Logger, ms *metrics.Prom) *HostHTTP {
	return ConstructHostHTTP(clusterName, mainCfg, hostCfg, lg, ms)
}

// ConstructHost builds new host object from config.
func ConstructHostHTTP(clusterName string, mainCfg conf.Main, hostCfg conf.Host, lg *zap.Logger, ms *metrics.Prom) *HostHTTP {
	targetPort := mainCfg.TargetPort
	if hostCfg.Port != 0 {
		targetPort = hostCfg.Port
	}

	promLabels := prometheus.Labels{
		"cluster":       clusterName,
		"upstream_host": hostCfg.Name,
	}
	h := HostHTTP{
		Name: hostCfg.Name,
		Port: targetPort,
		Ch:   make(chan *rec.RecBytes, mainCfg.HostQueueSize),
		stop: make(chan int),

		conf: &mainCfg,

		URL: fmt.Sprintf("http://%s:%d", hostCfg.Name, targetPort),

		outRecs:                   ms.OutRecs.With(promLabels),
		outRecsTotal:              ms.OutRecsTotal,
		throttled:                 ms.ThrottledHosts.With(promLabels),
		throttledTotal:            ms.ThrottledHostsTotal,
		stateChanges:              ms.StateChangeHosts.With(promLabels),
		stateChangesTotal:         ms.StateChangeHostsTotal,
		oldConnectionRefresh:      ms.OldConnectionRefresh.With(promLabels),
		oldConnectionRefreshTotal: ms.OldConnectionRefreshTotal,
		targetState:               ms.TargetStates.With(promLabels),
	}
	h.Ms = ms
	h.Lg = lg.With(zap.Stringer("target_host", &h))

	if mainCfg.TCPInitialConnCheck {
		h.setAvailability(false)
		go func() {
			h.ensureConnection()
		}()
	} else {
		h.setAvailability(true)
	}

	return &h
}

// Push adds a new record to send to the host queue.
func (h *HostHTTP) Push(r *rec.RecBytes) {
	select {
	case h.Ch <- r:
	default:
		h.throttled.Inc()
		h.throttledTotal.Inc()
		h.Lg.Warn("host queue is full", zap.Int("queue_size", len(h.Ch)))
	}
}

// IsAvailable tells if the host is alive.
func (h *HostHTTP) IsAvailable() bool {
	return h.Available.Load()
}

// Stop brings streaming to a halt.
func (h *HostHTTP) Stop() {
	close(h.Ch)
}

// Stream launches the sending to target host.
// Exits when queue is closed and sending is finished.
func (h *HostHTTP) Stream(wg *sync.WaitGroup) {
	defer func() {
		wg.Done()
		close(h.stop)
	}()

	const BATCHSIZE = 60 * 1024 * 1024
	// create batch of records
	records := 0
	for i := range h.Ch {
		var batch []byte
		batch = append(batch, i.Serialize()...)
		records++
	LOOP:
		// For control maximum size of batch
		for len(batch) < BATCHSIZE {
			select {
			case r := <-h.Ch:
				batch = append(batch, r.Serialize()...)
				records++
			default:
				break LOOP
			}
		}
		// Send batch
		reader := bytes.NewReader(batch)
		client := http.Client{Timeout: time.Duration(h.conf.SendTimeoutSec) * time.Second}
		_, err := client.Post(h.URL, "application/octet-stream", reader)
		if err == nil {
			h.outRecs.Add(float64(records))
			h.outRecsTotal.Add(float64(records))
		} else {
			h.Lg.Warn("error sending value to host", zap.Error(err))
		}
	}
}

// Flush periodically flushes the buffer and performs a write.
func (h *HostHTTP) flush(d time.Duration) {
	// not supported for HTTP
	return
}

func (h *HostHTTP) keepConnectionFresh() {
	// not supported for HTTP
	return
}

func (h *HostHTTP) ensureConnection() {
	for waitMs, attemptCount := uint32(0), 1; h.IsAvailable() == false; {

		time.Sleep(time.Duration(waitMs) * time.Millisecond)

		if waitMs < h.conf.MaxHostReconnectPeriodMs {
			waitMs = waitMs*2 + h.conf.HostReconnectPeriodDeltaMs
		}
		if waitMs >= h.conf.MaxHostReconnectPeriodMs {
			waitMs = h.conf.MaxHostReconnectPeriodMs
		}

		h.connect(attemptCount)
		attemptCount++
	}
}

func (h *HostHTTP) connect(attemptCount int) {
	err := h.getConnectionToHost()
	if err != nil {
		h.Lg.Warn("connection to target host failed")
		if attemptCount == 1 {
			if h.Available.Load() {
				h.setAvailability(false)
				h.stateChanges.Inc()
				h.stateChangesTotal.Inc()
			}
		}
		return
	}
	h.setAvailability(true)
}

func (h *HostHTTP) getConnectionToHost() error {
	client := http.Client{
		Timeout: time.Duration(h.conf.OutConnTimeoutSec) * time.Second,
	}
	_, err := client.Get(h.URL)
	return err
}

func (h *HostHTTP) setAvailability(val bool) {
	h.Available.Store(val)
	boolVal := 0.0
	if val {
		boolVal = 1.0
	}
	h.targetState.Set(boolVal)
}
