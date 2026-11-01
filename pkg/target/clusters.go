package target

import (
	"fmt"
	"sync"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/prometheus/client_golang/prometheus"

	"go.uber.org/zap"
)

// Clusters are all clusters mapped by name.
type Clusters map[string]*Cluster

// Send continuously sends data to all clusters.
// Under the hood it delegates this task to separate clusters
// and manages the lifecycles.
//
// Returned chan is closed when everything was stopped.
func (cls Clusters) Send(finish chan struct{}) chan struct{} {
	done := make(chan struct{})

	go func() {
		var wg sync.WaitGroup
		wg.Add(len(cls))

		for _, c := range cls {
			c.Send(&wg, finish)
		}

		wg.Wait()
		close(done)
	}()

	return done
}

// NewClusters builds new set of clusters from config.
func NewClusters(mainCfg *conf.Main, cfg *conf.Clusters, lg *zap.Logger, ms *metrics.Prom) (Clusters, error) {
	// Reserve an equal share even for disconnected sockets. No connection
	// can consume another's allowance, and reconnects release their old state.
	connections := 0
	for _, cc := range cfg.Cluster {
		for _, h := range cc.Hosts {
			if h.Stream {
				connections += max(1, h.MTCP)
			}
		}
	}
	total := mainCfg.StreamDictionaryBytes
	if total == 0 {
		total = 1 << 30
	}
	if total < 0 || (connections > 0 && (total-total/2)/connections < 4096) {
		return nil, fmt.Errorf("StreamDictionaryBytes is too small for %d connections", connections)
	}
	if connections > 0 {
		if err := ms.Stream.ConfigureNames(total/2, mainCfg.StreamFullNames); err != nil {
			return nil, err
		}
	}
	limit := total
	if connections > 0 {
		limit = (total - total/2) / connections
	}
	cls := make(map[string]*Cluster)
	var err error
	for _, cc := range cfg.Cluster {
		cc.Hosts = append([]conf.Host(nil), cc.Hosts...)
		for i := range cc.Hosts {
			h := &cc.Hosts[i]
			if h.Stream {
				h.StreamOptions = h.StreamOptions.WithDefaults(mainCfg.TCPOutBufFlushPeriodSec)
				h.StreamOptions.DictionaryBytes = min(h.StreamOptions.DictionaryBytes, limit)
			}
		}
		clusterCfg := *mainCfg
		if cc.Compression != "" {
			clusterCfg.TCPOutCompression = cc.Compression
		}
		cl := &Cluster{
			Name: cc.Name,
			Type: cc.Type,
		}
		switch cc.Type {
		case conf.JumpCluster:
			cl, err = getJumpCluster(cl, cc, clusterCfg, lg, ms)
		case conf.BlackholeCluster, conf.ToallCluster, conf.LB, conf.StickyCluster:
			for _, h := range cc.Hosts {
				cl.Hosts = append(cl.Hosts, NewHost(cc.Name, clusterCfg, h, lg, ms))
			}
		default:
			return cls, fmt.Errorf("incorrect cluster type %s for cluster %s",
				cl.Type, cl.Name)
		}
		if err != nil {
			return cls, err
		}
		if cc.Type == conf.StickyCluster {
			cl.sticky, err = newStickyCluster(cl, ms)
			if err != nil {
				return cls, err
			}
		}
		ms.NumberOfTargets.With(prometheus.Labels{"cluster": cl.Name}).Set(float64(len(cl.Hosts)))

		cls[cl.Name] = cl
	}

	return cls, err
}

func getJumpCluster(cl *Cluster, cc conf.Cluster, mainCfg conf.Main, lg *zap.Logger, ms *metrics.Prom) (*Cluster, error) {
	cl.Hosts = make([]Target, len(cc.Hosts))
	for _, h := range cc.Hosts {
		if cl.Hosts[h.Index] != nil {
			return cl, fmt.Errorf("duplicate index value, or index not set for a hashed cluster %s", cc.Name)
		}

		if h.Index >= len(cl.Hosts) {
			return cl, fmt.Errorf("host %s index %d out of bounds in cluster %s (cluster size %d)", h.Name, h.Index, cl.Name, len(cl.Hosts))
		}

		cl.Hosts[h.Index] = NewHost(cc.Name, mainCfg, h, lg, ms)
	}

	for _, hst := range cl.Hosts {
		if hst == nil {
			return cl, fmt.Errorf("not all host indexes were set for cluster %v", cc.Name)
		}
	}
	return cl, nil
}
