package target

import (
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"go.uber.org/zap"
)

// tcpAccounting is protected by recQueue.mu, never by a connection mutex.
// Shutdown can therefore snapshot it even while a socket write or dial blocks.
type tcpAccounting struct {
	ms                                            *metrics.TCPOutput
	cluster, target                               string
	pendingRecords, pendingBytes                  int64
	flushedRecords, socketWrittenBytes            int64
	flushedPlaintextBytes                         int64
	writeErrors, uncertainRecords, uncertainBytes int64
}

func (q *recQueue) trackTCP(ms *metrics.TCPOutput, cluster, target string) {
	q.output = &tcpAccounting{ms: ms, cluster: cluster, target: target}
	ms.PlaintextBytes.Bind(func() float64 {
		q.mu.Lock()
		defer q.mu.Unlock()
		return float64(q.output.flushedPlaintextBytes)
	})
}

// Requires q.mu, before consumers can observe the accepted records.
func (q *recQueue) accountAccepted(rs []*rec.RecBytes) {
	if q.output == nil || len(rs) == 0 {
		return
	}
	s := q.output
	s.pendingRecords += int64(len(rs))
	s.ms.PendingRecords.Add(float64(len(rs)))
	var bytes int
	for _, r := range rs {
		bytes += len(r.Path) + len(r.Val) + len(r.Time) + 3
	}
	s.pendingBytes += int64(bytes)
	s.ms.PendingBytes.Add(float64(bytes))
}

func (q *recQueue) accountTCPWrite(r tcpWriteResult) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.output == nil {
		return
	}
	s := q.output
	s.socketWrittenBytes += int64(r.written)
	s.ms.SocketWrittenBytes.Add(float64(r.written))
	if r.frames > 0 {
		s.ms.ZstdFrames.Add(float64(r.frames))
	}
	if r.err != nil {
		s.writeErrors++
		s.ms.WriteErrors.Inc()
		s.uncertainRecords += int64(r.uncertainRecords)
		s.uncertainBytes += int64(r.written)
		s.ms.UncertainRecords.Add(float64(r.uncertainRecords))
		s.ms.UncertainBytes.Add(float64(r.written))
		return
	}
	s.pendingRecords -= int64(r.records)
	s.pendingBytes -= int64(r.bytes)
	s.flushedRecords += int64(r.records)
	s.ms.PendingRecords.Sub(float64(r.records))
	s.ms.PendingBytes.Sub(float64(r.bytes))
	s.ms.FlushedRecords.Add(float64(r.records))
	s.flushedPlaintextBytes += int64(r.bytes)
}

func (q *recQueue) reportTCPShutdown(lg *zap.Logger, abandoning bool) (records, bytes int64) {
	q.mu.Lock()
	if q.output == nil {
		q.mu.Unlock()
		return 0, 0
	}
	s := *q.output
	q.mu.Unlock()
	if abandoning {
		s.ms.AbandonedRecords.Add(float64(s.pendingRecords))
		s.ms.AbandonedBytes.Add(float64(s.pendingBytes))
	}
	if s.pendingRecords != 0 || s.writeErrors != 0 {
		lg.Info("Shutdown: TCP target summary", zap.String("cluster", s.cluster), zap.String("target", s.target),
			zap.Int64("pending_records", s.pendingRecords), zap.Int64("pending_bytes", s.pendingBytes),
			zap.Int64("flushed_records", s.flushedRecords), zap.Int64("socket_written_bytes", s.socketWrittenBytes),
			zap.Int64("write_errors", s.writeErrors), zap.Int64("uncertain_records", s.uncertainRecords), zap.Int64("uncertain_bytes", s.uncertainBytes))
	}
	return s.pendingRecords, s.pendingBytes
}

// ReportTCPShutdown reports pending target deliveries, not proven receiver loss.
// On a timeout the snapshot can overlap an in-flight write. Emit the summary to
// the process log as well as metrics: a final counter update may not be scraped.
func (cls Clusters) ReportTCPShutdown(lg *zap.Logger, abandoning bool) bool {
	var records, bytes int64
	for _, cl := range cls {
		for _, host := range cl.Hosts {
			var q *recQueue
			switch h := host.(type) {
			case *HostTCP:
				q = h.q
			case *Host:
				q = h.q
			case *HostMTCP:
				q = h.q
			}
			if q != nil {
				r, b := q.reportTCPShutdown(lg, abandoning)
				records += r
				bytes += b
			}
		}
	}
	lg.Info("Shutdown: TCP output summary", zap.Bool("abandoning", abandoning), zap.Int64("pending_records", records), zap.Int64("pending_bytes", bytes))
	return records == 0 && bytes == 0
}
