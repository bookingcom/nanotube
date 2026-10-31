package metrics

import "github.com/prometheus/client_golang/prometheus"

// TCPMetrics describes local output progress, not receiver acknowledgement.
type TCPMetrics struct {
	PendingRecords, PendingBytes                  *prometheus.GaugeVec
	FlushedRecords, SocketWrittenBytes            *prometheus.CounterVec
	PlaintextBytes                                *readCounterVec
	ZstdFrames                                    *prometheus.CounterVec
	WriteErrors, UncertainRecords, UncertainBytes *prometheus.CounterVec
	AbandonedRecords, AbandonedBytes              *prometheus.CounterVec
	ShutdownTimeouts                              prometheus.Counter
}

// TCPOutput binds the metrics to one target, outside the record hot path.
type TCPOutput struct {
	PendingRecords, PendingBytes                  prometheus.Gauge
	FlushedRecords, SocketWrittenBytes            prometheus.Counter
	PlaintextBytes                                *readCounter
	ZstdFrames                                    prometheus.Counter
	WriteErrors, UncertainRecords, UncertainBytes prometheus.Counter
	AbandonedRecords, AbandonedBytes              prometheus.Counter
}

func newTCPMetrics() *TCPMetrics {
	labels := []string{"cluster", "upstream_host"}
	counter := func(name, help string) *prometheus.CounterVec {
		return prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "nanotube", Name: name, Help: help}, labels)
	}
	gauge := func(name, help string) *prometheus.GaugeVec {
		return prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: "nanotube", Name: name, Help: help}, labels)
	}
	return &TCPMetrics{
		PlaintextBytes:     &readCounterVec{},
		ZstdFrames:         counter("tcp_zstd_frames_total", "Independent zstd frames fully written to TCP, including replays."),
		PendingRecords:     gauge("tcp_pending_records", "Accepted target deliveries awaiting a successful full socket write, including queue, in-flight and buffered records."),
		PendingBytes:       gauge("tcp_pending_bytes", "Serialized bytes awaiting a successful full socket write."),
		FlushedRecords:     counter("tcp_flushed_records_total", "Target records completed by a successful full socket write; not receiver acknowledgements."),
		SocketWrittenBytes: counter("tcp_socket_written_bytes_total", "Bytes reported written by TCP, including errored prefixes and replays."),
		WriteErrors:        counter("tcp_write_errors_total", "Failed or short TCP writes retried with their complete pending records."),
		UncertainRecords:   counter("tcp_uncertain_records_total", "Record prefixes reported written on failed writes, which replay may duplicate; counts attempts."),
		UncertainBytes:     counter("tcp_uncertain_bytes_total", "Bytes reported written on failed writes, which replay may duplicate; counts attempts."),
		AbandonedRecords:   counter("tcp_abandoned_records_total", "Target deliveries pending at forced-exit snapshot, including uncertain in-flight writes; not proven receiver loss."),
		AbandonedBytes:     counter("tcp_abandoned_bytes_total", "Target bytes pending at forced-exit snapshot, including uncertain in-flight writes; not proven receiver loss."),
		ShutdownTimeouts:   prometheus.NewCounter(prometheus.CounterOpts{Namespace: "nanotube", Name: "shutdown_timeouts_total", Help: "Shutdowns that exceeded the application drain deadline."}),
	}
}

func (m *TCPMetrics) For(labels prometheus.Labels) *TCPOutput {
	return &TCPOutput{
		PlaintextBytes: m.PlaintextBytes.With(labels), ZstdFrames: m.ZstdFrames.With(labels),
		PendingRecords: m.PendingRecords.With(labels), PendingBytes: m.PendingBytes.With(labels),
		FlushedRecords: m.FlushedRecords.With(labels), SocketWrittenBytes: m.SocketWrittenBytes.With(labels),
		WriteErrors: m.WriteErrors.With(labels), UncertainRecords: m.UncertainRecords.With(labels), UncertainBytes: m.UncertainBytes.With(labels),
		AbandonedRecords: m.AbandonedRecords.With(labels), AbandonedBytes: m.AbandonedBytes.With(labels),
	}
}

func (m *TCPMetrics) register(reg prometheus.Registerer, details bool) error {
	collectors := []prometheus.Collector{m.ShutdownTimeouts}
	if details {
		collectors = append(collectors, m.PlaintextBytes, m.ZstdFrames, m.PendingRecords, m.PendingBytes, m.FlushedRecords, m.SocketWrittenBytes, m.WriteErrors, m.UncertainRecords, m.UncertainBytes, m.AbandonedRecords, m.AbandonedBytes)
	}
	for _, c := range collectors {
		if err := reg.Register(c); err != nil {
			return err
		}
	}
	return nil
}
