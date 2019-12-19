package target

import (
	"fmt"
	"nanotube/pkg/conf"
	"nanotube/pkg/metrics"
	"sync"

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
func NewClusters(mainCfg conf.Main, cfg conf.Clusters, lg *zap.Logger, ms *metrics.Prom) (Clusters, error) {
	// TODO (grzkv): Add duplicate defense
	cls := make(map[string]*Cluster)
	for _, cc := range cfg.Cluster {
		cl := Cluster{
			Name: cc.Name,
		}
		switch cc.Type {
		case conf.JumpCluster:
			cl.Hosts = make([]*Host, len(cc.Hosts))
			for _, h := range cc.Hosts {
				if cl.Hosts[h.Index] != nil {
					return cls, fmt.Errorf("duplicate index value, or index not set for a hashed cluster %s", cc.Name)
				}

				if h.Index >= len(cl.Hosts) {
					return cls, fmt.Errorf("host %s index %d out of bounds in cluster %s (cluster size %d)", h.Name, h.Index, cl.Name, len(cl.Hosts))
				}

				cl.Hosts[h.Index] = NewHost(cc.Name, mainCfg, h, lg, ms)
			}

			for _, hst := range cl.Hosts {
				if hst == nil {
					return cls, fmt.Errorf("not all host indexes were set for cluster %v", cc.Name)
				}
			}
		case conf.BlackholeCluster, conf.ToallCluster:
			for _, h := range cc.Hosts {
				cl.Hosts = append(cl.Hosts, NewHost(cc.Name, mainCfg, h, lg, ms))
			}
		default:
			return cls, fmt.Errorf("incorrect cluster type %s for cluster %s",
				cl.Type, cl.Name)
		}
		cl.Type = cc.Type

		cls[cl.Name] = &cl
	}

	return cls, nil
}
