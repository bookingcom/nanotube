package metrics

import (
	"fmt"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestTCPMetricsRegistration(t *testing.T) {
	for _, details := range []bool{false, true} {
		t.Run(fmt.Sprint(details), func(t *testing.T) {
			reg := prometheus.NewPedanticRegistry()
			m := newTCPMetrics()
			if err := m.register(reg, details); err != nil {
				t.Fatal(err)
			}
			target := m.For(prometheus.Labels{"cluster": "test", "upstream_host": "localhost"})
			target.PendingRecords.Add(2)
			target.PendingBytes.Add(30)
			target.FlushedRecords.Inc()
			target.SocketWrittenBytes.Add(15)
			target.WriteErrors.Inc()
			target.UncertainRecords.Inc()
			target.UncertainBytes.Add(7)
			target.AbandonedRecords.Inc()
			target.AbandonedBytes.Add(15)
			m.ShutdownTimeouts.Inc()
			families, err := reg.Gather()
			if err != nil {
				t.Fatal(err)
			}
			expected := 1
			if details {
				expected = 12
			}
			if len(families) != expected {
				t.Fatalf("registered %d metric families, want %d", len(families), expected)
			}
			for _, family := range families {
				if !details && family.GetName() != "nanotube_shutdown_timeouts_total" {
					t.Fatalf("LessMetrics exposed %s", family.GetName())
				}
			}
		})
	}
}
