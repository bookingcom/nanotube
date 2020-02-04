package target

import (
	"nanotube/pkg/conf"
	"nanotube/pkg/metrics"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestClustersWithNoAvailableHosts(t *testing.T) {
	clsConfig :=
		`[[cluster]]
		name = "aaa"
		type = "lb"
			[[cluster.hosts]]
			name = "host1"
			port = 123
			[[cluster.hosts]]
			name = "host2"
			port = 456`

	cfg := conf.MakeDefault()
	cls, _ := conf.ReadClustersConfig(strings.NewReader(clsConfig))

	ms := metrics.New(&cfg)
	metrics.Register(ms)

	logger, _ := zap.NewProductionConfig().Build()

	clusters, _ := NewClusters(cfg, cls, logger, ms)
	for _, cluster := range clusters {
		if len(cluster.AvailableHosts) != 0 {
			t.Fatalf("expected 0 available hosts")
		}
	}
}

func TestClustersWithAllAvailableHosts(t *testing.T) {
	clsConfig :=
		`[[cluster]]
		name = "aaa"
		type = "lb"
			[[cluster.hosts]]
			name = "host1"
			port = 123
			[[cluster.hosts]]
			name = "host2"
			port = 456`

	cfg := conf.MakeDefault()
	cls, _ := conf.ReadClustersConfig(strings.NewReader(clsConfig))

	ms := metrics.New(&cfg)
	metrics.Register(ms)

	logger, _ := zap.NewProductionConfig().Build()

	clusters, _ := NewClusters(cfg, cls, logger, ms)
	for _, cluster := range clusters {
		if len(cluster.AvailableHosts) != 0 {
			t.Fatalf("expected 0 available hosts")
		}
	}

	sigChs := make([]chan struct{}, 0)
	for _, cluster := range clusters {
		for _, h := range cluster.Hosts {
			ch := make(chan struct{})
			sigChs = append(sigChs, ch)
			cluster.UpdateHostHealthStatus <- &HostStatus{h, true, ch}
		}
		for _, sigCh := range sigChs {
			<-sigCh
		}
		if len(cluster.AvailableHosts) != 2 {
			t.Fatalf("expected 2 available hosts, found %d", len(cluster.AvailableHosts))
		}
	}

}
