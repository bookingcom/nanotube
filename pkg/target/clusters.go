package target

import (
	"fmt"
	"sync"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"

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
	// TODO (grzkv): Add duplicate defense
	cls := make(map[string]*Cluster)
	var err error
	for _, cc := range cfg.Cluster {
		cl := &Cluster{
			Name: cc.Name,
			Type: cc.Type,
		}
		switch cc.Type {
		case conf.JumpCluster:
			cl, err = getJumpCluster(cl, cc, *mainCfg, lg, ms)
		case conf.BlackholeCluster, conf.ToallCluster, conf.LB:
			for _, h := range cc.Hosts {
				if h.GRPC {
					cl.Hosts = append(cl.Hosts, NewHostGRPC(cc.Name, *mainCfg, h, lg, ms))
				} else {
					cl.Hosts = append(cl.Hosts, NewHostTCP(cc.Name, *mainCfg, h, lg, ms))
				}
			}
		default:
			return cls, fmt.Errorf("incorrect cluster type %s for cluster %s",
				cl.Type, cl.Name)
		}

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
