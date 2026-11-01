package metrics

import "github.com/prometheus/client_golang/prometheus"

// StreamMetrics counts encoding attempts, including frames re-encoded on retry.
type StreamMetrics struct {
	Frames, Names, Resets, Records *prometheus.CounterVec
	Dropped                        *prometheus.CounterVec
}

// StreamOutput binds counters once per destination, outside the record hot path.
type StreamOutput struct {
	Frames, Names, Resets, Records prometheus.Counter
	Dropped                        prometheus.Counter
}

func newStreamMetrics() *StreamMetrics {
	counter := func(name, help string) *prometheus.CounterVec {
		return prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "nanotube", Name: name, Help: help}, []string{"cluster", "upstream_host"})
	}
	return &StreamMetrics{
		Frames:  counter("stream_frames_total", "Binary frames encoded, including retry attempts."),
		Names:   counter("stream_names_defined_total", "Name definitions encoded, including retry attempts."),
		Resets:  counter("stream_dictionary_resets_total", "Dictionary resets encoded after reaching the per-connection name cap."),
		Records: counter("stream_records_encoded_total", "Points encoded, including retry attempts."),
		Dropped: counter("stream_records_dropped_total", "Points rejected by numeric parsing (including NaN) or codec size limits."),
	}
}

func (m *StreamMetrics) For(labels prometheus.Labels) *StreamOutput {
	return &StreamOutput{Frames: m.Frames.With(labels), Names: m.Names.With(labels), Resets: m.Resets.With(labels), Records: m.Records.With(labels), Dropped: m.Dropped.With(labels)}
}

func (m *StreamMetrics) register(reg prometheus.Registerer) error {
	for _, c := range []prometheus.Collector{m.Frames, m.Names, m.Resets, m.Records, m.Dropped} {
		if err := reg.Register(c); err != nil {
			return err
		}
	}
	return nil
}
