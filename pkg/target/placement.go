package target

import (
	"fmt"
	"github.com/bookingcom/nanotube/internal/placement"
	"github.com/bookingcom/nanotube/internal/v2placement"
	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"go.uber.org/zap"
	"net"
	"os"
	"strconv"
)

func readPlacement(path string) (v2placement.Document, error) {
	f, err := os.Open(path)
	if err != nil {
		return v2placement.Document{}, err
	}
	defer f.Close()
	return v2placement.Decode(f)
}

func getPlacementCluster(cl *Cluster, cc conf.Cluster, cfg conf.Main, lg *zap.Logger, ms *metrics.Prom) (*Cluster, error) {
	reject := func(err error) (*Cluster, error) { ms.Placement.Reject(cl.Name); return cl, err }
	d, err := readPlacement(cc.PlacementMap)
	if err != nil {
		return reject(err)
	}
	sink := map[string]Target{}
	router, err := placement.New(d, cc.PlacementReplica)
	if err != nil {
		return reject(err)
	}
	// Validate the entire inventory before any host constructor can start an
	// optional connection check. An invalid map must not activate output work.
	endpoints := map[string]conf.Host{}
	for _, h := range cc.Hosts {
		port := h.Port
		if port == 0 {
			port = cfg.TargetPort
		}
		endpoint := net.JoinHostPort(h.Name, strconv.Itoa(int(port)))
		if _, ok := endpoints[endpoint]; ok {
			return reject(fmt.Errorf("duplicate placement endpoint %s", endpoint))
		}
		endpoints[endpoint] = h
	}
	for _, s := range d.Stores {
		if s.Blackhole {
			continue
		}
		if _, ok := endpoints[s.Write]; !ok {
			return reject(fmt.Errorf("placement store %s write endpoint %s has no configured host", s.ID, s.Write))
		}
	}
	targets := map[string]Target{}
	for _, h := range cc.Hosts {
		port := h.Port
		if port == 0 {
			port = cfg.TargetPort
		}
		endpoint := net.JoinHostPort(h.Name, strconv.Itoa(int(port)))
		t := NewHost(cc.Name, cfg, h, lg, ms)
		targets[endpoint] = t
		cl.Hosts = append(cl.Hosts, t)
	}
	for _, s := range d.Stores {
		if !s.Blackhole {
			sink[s.ID] = targets[s.Write]
		}
	}
	cl.placementTargets = sink
	cl.placement = router
	cl.placementPath = cc.PlacementMap
	cl.placementMetrics = ms.Placement
	ms.Placement.Attach(cl.Name, &router.Stats.Rejected)
	return cl, nil
}

// ReloadPlacement adopts only the configured map file. Routes/rules, transport
// inventory and producer identity are not changed by this control operation.
func (cl *Cluster) ReloadPlacement() error {
	if cl.placement == nil {
		return nil
	}
	d, err := readPlacement(cl.placementPath)
	if err != nil {
		cl.placementMetrics.Reject(cl.Name)
		return err
	}
	return cl.placement.Adopt(d)
}

func (cls Clusters) HasPlacement() bool {
	for _, cl := range cls {
		if cl.placement != nil {
			return true
		}
	}
	return false
}
