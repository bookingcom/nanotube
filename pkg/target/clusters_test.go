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
	cfg.MaxHostReconnectPeriodMs = 50000
	cls, _ := conf.ReadClustersConfig(strings.NewReader(clsConfig))

	ms := metrics.New(&cfg)

	logger, _ := zap.NewProductionConfig().Build()

	clusters, _ := NewClusters(cfg, cls, logger, ms)

	for _, cluster := range clusters {
		if cluster.getAvailableHostCount() != 0 {
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
	cfg.MaxHostReconnectPeriodMs = 50000

	cls, _ := conf.ReadClustersConfig(strings.NewReader(clsConfig))

	ms := metrics.New(&cfg)

	logger, _ := zap.NewProductionConfig().Build()

	clusters, _ := NewClusters(cfg, cls, logger, ms)
	for _, cluster := range clusters {
		if cluster.getAvailableHostCount() != 0 {
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
		availableHostCount := cluster.getAvailableHostCount()
		if availableHostCount != 2 {
			t.Fatalf("expected 2 available hosts, found %d", availableHostCount)
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
	cfg.MaxHostReconnectPeriodMs = 50000
	cls, _ := conf.ReadClustersConfig(strings.NewReader(clsConfig))

	ms := metrics.New(&cfg)

	logger, _ := zap.NewProductionConfig().Build()

	clusters, _ := NewClusters(cfg, cls, logger, ms)
	for _, cluster := range clusters {
		if cluster.getAvailableHostCount() != 0 {
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
	availableHostCount := cluster.getAvailableHostCount()
	if availableHostCount != 1 {
		t.Fatalf("expected 1 available hosts, found %d", availableHostCount)
	}

	ch = make(chan struct{})
	cluster.UpdateHostHealthStatus <- &HostStatus{host, false, ch}
	<-ch
	availableHostCount = cluster.getAvailableHostCount()
	if availableHostCount != 0 {
		t.Fatalf("expected 0 available hosts, found %d", availableHostCount)
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
	cfg.MaxHostReconnectPeriodMs = 50000

	cls, _ := conf.ReadClustersConfig(strings.NewReader(clsConfig))

	ms := metrics.New(&cfg)

	logger, _ := zap.NewProductionConfig().Build()

	clusters, _ := NewClusters(cfg, cls, logger, ms)
	for _, cluster := range clusters {
		if cluster.getAvailableHostCount() != 0 {
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
		availableHostCount := cluster.getAvailableHostCount()
		if availableHostCount != n {
			t.Fatalf("expected 6 available hosts, found %d", availableHostCount)
		}
	}

	for _, host := range shuffledHosts {
		ch := make(chan struct{})
		cluster.UpdateHostHealthStatus <- &HostStatus{host, false, ch}
		<-ch
		n--
		availableHostCount := cluster.getAvailableHostCount()
		if availableHostCount != n {
			t.Fatalf("expected %d available hosts", availableHostCount)
		}
	}
}

func (cl *Cluster) getAvailableHostCount() int {
	cl.Hm.RLock()
	defer cl.Hm.RUnlock()
	return len(cl.AvailableHosts)
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
