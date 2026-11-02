package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestSharedNameMetrics(t *testing.T) {
	m := newStreamMetrics()
	if err := m.ConfigureNames(16<<10, false); err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewRegistry()
	if err := m.register(reg); err != nil {
		t.Fatal(err)
	}
	m.For(prometheus.Labels{"cluster": "example", "upstream_host": "localhost"}).Unshared.Add(7)
	table := m.NameTable(16<<10, false)
	token := table.Resolve([]byte("held"), 0)
	if !table.Acquire(token, 0) {
		t.Fatal("could not pin name")
	}
	defer table.Release(token)
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{"nanotube_stream_name_table_capacity": float64(table.Stats().Capacity), "nanotube_stream_name_table_retained_names": 1, "nanotube_stream_names_unshared_total": 7, "nanotube_stream_name_table_generation_exhaustions_total": 0}
	for _, f := range families {
		if expected, ok := want[f.GetName()]; ok {
			metric := f.Metric[0]
			got := metric.GetGauge().GetValue() + metric.GetCounter().GetValue()
			if got != expected {
				t.Errorf("%s = %v, want %v", f.GetName(), got, expected)
			}
			delete(want, f.GetName())
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing metrics: %v", want)
	}
}
