package metrics

import (
	"fmt"
	"sync"
	"sync/atomic"

	codec "github.com/bookingcom/nanotube/pkg/streamcodec"
	"github.com/prometheus/client_golang/prometheus"
)

// StreamMetrics counts encoding attempts, including frames re-encoded on retry.
type StreamMetrics struct {
	nameMu                                 sync.Mutex
	names                                  atomic.Pointer[codec.NameTable]
	nameBudget                             int
	nameFull                               bool
	Frames, Names, Resets, Records         *prometheus.CounterVec
	Dropped                                *prometheus.CounterVec
	RetainedBytes, RetainedNames, MapBytes *prometheus.GaugeVec
}

// StreamOutput binds counters once per destination, outside the record hot path.
type StreamOutput struct {
	Frames, Names, Resets, Records         prometheus.Counter
	Dropped                                prometheus.Counter
	RetainedBytes, RetainedNames, MapBytes prometheus.Gauge
}

func newStreamMetrics() *StreamMetrics {
	counter := func(name, help string) *prometheus.CounterVec {
		return prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "nanotube", Name: name, Help: help}, []string{"cluster", "upstream_host"})
	}
	gauge := func(name, help string) *prometheus.GaugeVec {
		return prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: "nanotube", Name: name, Help: help}, []string{"cluster", "upstream_host"})
	}
	return &StreamMetrics{
		RetainedBytes: gauge("stream_connection_dictionary_bytes", "Retained connection map and predictor capacity in bytes."), RetainedNames: gauge("stream_connection_names", "Names held by connection dictionaries."), MapBytes: gauge("stream_connection_map_bytes", "Allocated global-to-wire map capacity in bytes."),
		Frames:  counter("stream_frames_total", "Binary frames encoded, including retry attempts."),
		Names:   counter("stream_names_defined_total", "Name definitions encoded, including retry attempts."),
		Resets:  counter("stream_dictionary_resets_total", "Dictionary resets encoded after reaching the per-connection byte or name cap."),
		Records: counter("stream_records_encoded_total", "Points encoded, including retry attempts."),
		Dropped: counter("stream_records_dropped_total", "Points rejected by numeric parsing (including NaN) or codec size limits."),
	}
}

func (m *StreamMetrics) For(labels prometheus.Labels) *StreamOutput {
	return &StreamOutput{Frames: m.Frames.With(labels), Names: m.Names.With(labels), Resets: m.Resets.With(labels), Records: m.Records.With(labels), Dropped: m.Dropped.With(labels), RetainedBytes: m.RetainedBytes.With(labels), RetainedNames: m.RetainedNames.With(labels), MapBytes: m.MapBytes.With(labels)}
}

func (m *StreamMetrics) register(reg prometheus.Registerer) error {
	for _, c := range []prometheus.Collector{m.Frames, m.Names, m.Resets, m.Records, m.Dropped, m.RetainedBytes, m.RetainedNames, m.MapBytes} {
		if err := reg.Register(c); err != nil {
			return err
		}
	}
	for _, spec := range []struct {
		name, help string
		value      func(codec.NameStats) float64
	}{
		{"stream_name_table_retained_bytes", "Allocated relay-wide name-table capacity in bytes.", func(s codec.NameStats) float64 { return float64(s.Bytes) }},
		{"stream_name_table_retained_names", "Names in the relay-wide table, including the unused grace period.", func(s codec.NameStats) float64 { return float64(s.Names) }},
		{"stream_name_table_name_bytes", "Allocated name byte storage; zero in fingerprint mode.", func(s codec.NameStats) float64 { return float64(s.NameBytes) }},
		{"stream_name_table_evictions_total", "Unreferenced global names reclaimed.", func(s codec.NameStats) float64 { return float64(s.Evictions) }},
	} {
		value := spec.value
		read := func() float64 {
			if names := m.names.Load(); names != nil {
				names.Sweep()
				return value(names.Stats())
			}
			return 0
		}
		var collector prometheus.Collector
		if spec.name == "stream_name_table_evictions_total" {
			collector = prometheus.NewCounterFunc(prometheus.CounterOpts{Namespace: "nanotube", Name: spec.name, Help: spec.help}, read)
		} else {
			collector = prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: "nanotube", Name: spec.name, Help: spec.help}, read)
		}
		if err := reg.Register(collector); err != nil {
			return err
		}
	}
	return nil
}

// NameTable is owned by the relay metrics/application lifetime, across hosts.
// A config reload may lower connection shares; the existing table reservation
// remains charged until the old application has drained.
func (m *StreamMetrics) ConfigureNames(budget int, full bool) error {
	m.nameMu.Lock()
	defer m.nameMu.Unlock()
	if m.names.Load() != nil {
		if m.nameBudget != budget || m.nameFull != full {
			return fmt.Errorf("stream name-table budget or comparison mode requires an application restart")
		}
		return nil
	}
	names, err := codec.NewNameTable(budget, full)
	if err != nil {
		return err
	}
	m.nameBudget = budget
	m.nameFull = full
	m.names.Store(names)
	return nil
}
func (m *StreamMetrics) NameTable(budget int, full bool) *codec.NameTable {
	if err := m.ConfigureNames(budget, full); err != nil {
		panic(err)
	}
	return m.names.Load()
}

func (m *StreamMetrics) DictionaryStats() (int, int, int, uint64) {
	if table := m.names.Load(); table != nil {
		s := table.Stats()
		return s.Bytes, s.Names, s.NameBytes, s.Evictions
	}
	return 0, 0, 0, 0
}
