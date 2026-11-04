package conf

import (
	"strings"
	"testing"
)

func TestTCPMainQueueBudgetConfig(t *testing.T) {
	for _, tc := range []struct {
		text string
		want uint64
	}{{"", 256 << 20}, {"TCPMainQueueBytes = 0", 0}, {"TCPMainQueueBytes = 67108864", 64 << 20}} {
		cfg, err := ReadMain(strings.NewReader("ClustersConfig = \"clusters.toml\"\nRulesConfig = \"rules.toml\"\n" + tc.text))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.TCPMainQueueBytes != tc.want {
			t.Fatalf("budget = %d, want %d", cfg.TCPMainQueueBytes, tc.want)
		}
	}
}
