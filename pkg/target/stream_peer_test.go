package target

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestStreamPeerResetMetricsAndWarning(t *testing.T) {
	for _, tc := range []struct {
		name               string
		peer, local, names int
	}{
		{"peer", 1 << 20, 4 << 20, 4 << 20},
		{"local", 2 << 20, 4096, 4 << 20},
		{"names", 2 << 20, 4 << 20, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := &countingStreamConn{}
			_, s, _ := newStreamTestTarget(false, 0, conf.StreamOptions{PeerDictionaryBytes: tc.peer, DictionaryBytes: tc.local, MaxNames: tc.names}, wire)
			var logs bytes.Buffer
			core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&logs), zap.WarnLevel)
			s.lg = zap.New(core)
			s.connections[0].lastReset = time.Now()
			defer func() { s.connections[0].codec.close() }()
			for i := range 6000 {
				line := fmt.Appendf(nil, "example.%s.%d %d %d", strings.Repeat("component.", 30), i, i, i)
				r, err := rec.ParseRec(line, false, false, time.Now, zap.NewNop())
				if err != nil {
					t.Fatal(err)
				}
				s.dispatch([]*rec.RecBytes{r})
			}
			s.flush(&s.connections[0])
			counts := []float64{testutil.ToFloat64(s.ms.PeerResets), testutil.ToFloat64(s.ms.LocalResets), testutil.ToFloat64(s.ms.NameResets)}
			index := map[string]int{"peer": 0, "local": 1, "names": 2}[tc.name]
			if counts[index] == 0 || counts[0]+counts[1]+counts[2] != testutil.ToFloat64(s.ms.Resets) {
				t.Fatalf("reason counts %v disagree with total", counts)
			}
			for i, n := range counts {
				if i != index && n != 0 {
					t.Fatalf("wrong binding limit: %v", counts)
				}
			}
			var fields map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &fields); err != nil {
				t.Fatalf("missing or invalid rate-limited warning: %v: %s", err, &logs)
			}
			field := []string{"peer_resets", "local_resets", "name_resets"}[index]
			if fields["msg"] != "stream dictionary limit forces frequent resets" || fields[field] != float64(1) || fields["peer_dictionary_bytes"] != float64(tc.peer) || fields["dictionary_bytes"] != float64(tc.local) {
				t.Fatalf("warning lost reset cause or limits: %v", fields)
			}
		})
	}
}
