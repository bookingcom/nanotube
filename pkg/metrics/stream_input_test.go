package metrics

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestStreamInputReservations(t *testing.T) {
	m := New()
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			if m.ReserveStreamInput(80, 160) {
				m.ReleaseStreamInput(80)
			}
		})
	}
	wg.Wait()
	if !m.ReserveStreamInput(160, 160) || m.ReserveStreamInput(1, 160) {
		t.Fatal("global limit not enforced")
	}
	m.ReleaseStreamInput(160)
	if m.streamInputReserved.Load() != 0 {
		t.Fatal("reservation leak")
	}
}

func TestStreamInputDictionaryMetricRegistration(t *testing.T) {
	for _, less := range []bool{false, true} {
		t.Run(fmt.Sprint(less), func(t *testing.T) {
			reg := prometheus.NewPedanticRegistry()
			previous := prometheus.DefaultRegisterer
			prometheus.DefaultRegisterer = reg
			defer func() { prometheus.DefaultRegisterer = previous }()
			cfg := conf.MakeDefault()
			cfg.LessMetrics = less
			m := New()
			Register(m, &cfg)
			m.StreamInputDictionaryNames.Add(5)
			m.StreamInputNamesDefined.WithLabelValues("data").Add(7)
			m.StreamInputNamesDefined.WithLabelValues("state").Add(3)
			if err := testutil.GatherAndCompare(reg, strings.NewReader(`
# HELP nanotube_stream_input_dictionary_names Wire ids held by admitted stream input dictionaries, summed over connections.
# TYPE nanotube_stream_input_dictionary_names gauge
nanotube_stream_input_dictionary_names 5
# HELP nanotube_stream_input_names_defined_total Validated name definitions received by frame kind; state includes handoff snapshots.
# TYPE nanotube_stream_input_names_defined_total counter
nanotube_stream_input_names_defined_total{frame_kind="data"} 7
nanotube_stream_input_names_defined_total{frame_kind="state"} 3
`), "nanotube_stream_input_dictionary_names", "nanotube_stream_input_names_defined_total"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
