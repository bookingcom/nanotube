package target

import (
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"go.uber.org/zap"
)

func TestClustersWithNoAvailableHosts(t *testing.T) {
	clsConfig :=
		`[[cluster]]
		name = "aaa"
		type = "lb"
			[[cluster.hosts]]
			name = "192.0.2.1"
			port = 123
			[[cluster.hosts]]
			name = "192.0.2.2"
			port = 456`

	cfg := conf.MakeDefault()
	cfg.OutConnTimeoutSec = 1
	cls, _ := conf.ReadClustersConfig(strings.NewReader(clsConfig))

	ms := metrics.New(&cfg)

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
			name = "192.0.2.1"
			port = 123
			[[cluster.hosts]]
			name = "192.0.2.2"
			port = 456`

	cfg := conf.MakeDefault()
	cfg.OutConnTimeoutSec = 1
	cls, _ := conf.ReadClustersConfig(strings.NewReader(clsConfig))

	ms := metrics.New(&cfg)

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

func TestClustersWithFlappingHosts(t *testing.T) {
	clsConfig :=
		`[[cluster]]
		name = "aaa"
		type = "lb"
			[[cluster.hosts]]
			name = "192.0.2.1"
			port = 123
			[[cluster.hosts]]
			name = "192.0.2.2"
			port = 456`

	cfg := conf.MakeDefault()
	cfg.OutConnTimeoutSec = 1
	cls, _ := conf.ReadClustersConfig(strings.NewReader(clsConfig))

	ms := metrics.New(&cfg)

	logger, _ := zap.NewProductionConfig().Build()

	clusters, _ := NewClusters(cfg, cls, logger, ms)
	for _, cluster := range clusters {
		if len(cluster.AvailableHosts) != 0 {
			t.Fatalf("expected 0 available hosts")
		}
	}

	var host *Host
	cluster := clusters["aaa"]
	for _, h := range cluster.Hosts {
		if h.Name == "192.0.2.1" {
			host = h
		}
	}

	ch := make(chan struct{})
	cluster.UpdateHostHealthStatus <- &HostStatus{host, true, ch}
	<-ch
	if len(cluster.AvailableHosts) != 1 {
		t.Fatalf("expected 1 available hosts, found %d", len(cluster.AvailableHosts))
	}

	ch = make(chan struct{})
	cluster.UpdateHostHealthStatus <- &HostStatus{host, false, ch}
	<-ch
	if len(cluster.AvailableHosts) != 0 {
		t.Fatalf("expected 0 available hosts, found %d", len(cluster.AvailableHosts))
	}
}

func TestRemoveAvailableHosts(t *testing.T) {
	clsConfig :=
		`[[cluster]]
		name = "aaa"
		type = "lb"
			[[cluster.hosts]]
			name = "192.0.2.1"
			port = 123
			[[cluster.hosts]]
			name = "192.0.2.2"
			port = 456
			[[cluster.hosts]]
			name = "192.0.2.3"
			port = 456
			[[cluster.hosts]]
			name = "192.0.2.4"
			port = 456
			[[cluster.hosts]]
			name = "192.0.2.5"
			port = 456
			[[cluster.hosts]]
			name = "192.0.2.6"
			port = 456`

	cfg := conf.MakeDefault()
	cfg.OutConnTimeoutSec = 1
	cls, _ := conf.ReadClustersConfig(strings.NewReader(clsConfig))

	ms := metrics.New(&cfg)

	logger, _ := zap.NewProductionConfig().Build()

	clusters, _ := NewClusters(cfg, cls, logger, ms)
	for _, cluster := range clusters {
		if len(cluster.AvailableHosts) != 0 {
			t.Fatalf("expected 0 available hosts")
		}
	}

	cluster := clusters["aaa"]
	hosts := cluster.Hosts
	shuffledHosts := shuffle(hosts)

	n := len(hosts)
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
		if len(cluster.AvailableHosts) != n {
			t.Fatalf("expected 6 available hosts, found %d", len(cluster.AvailableHosts))
		}
	}

	for _, host := range shuffledHosts {
		ch := make(chan struct{})
		cluster.UpdateHostHealthStatus <- &HostStatus{host, false, ch}
		<-ch
		n--
		if len(cluster.AvailableHosts) != n {
			t.Fatalf("expected %d available hosts", n)
		}
	}
}

func shuffle(hosts []*Host) []*Host {
	r := rand.New(rand.NewSource(time.Now().Unix()))
	shuffledHosts := make([]*Host, len(hosts))
	perm := r.Perm(len(hosts))
	for i, randIndex := range perm {
		shuffledHosts[i] = hosts[randIndex]
	}
	return shuffledHosts
}
