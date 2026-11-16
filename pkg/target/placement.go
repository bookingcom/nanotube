package target

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/bookingcom/nanotube/internal/placement"
	"github.com/bookingcom/nanotube/internal/v2placement"
	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"go.uber.org/zap"
)

// legacyPlacementSink deliberately cannot acknowledge a barrier. The original
// host queues and wire formats carry no epoch or durable sequence. This bridge
// is only for explicit bootstrap routing compatibility experiments.
type legacyPlacementSink struct{ targets map[string]Target }

func (s *legacyPlacementSink) Epochs() bool { return false }
func (s *legacyPlacementSink) Enqueue(d placement.Delivery) bool {
	if target := s.targets[d.Store]; target != nil {
		target.Push(d.Record)
	}
	return true // Legacy host overflow retains its existing drop counters.
}

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
	if !cc.PlacementLegacyWire {
		return getDurablePlacementCluster(cl, cc, lg, ms)
	}
	d, err := readPlacement(cc.PlacementMap)
	if err != nil {
		return reject(err)
	}
	sink := &legacyPlacementSink{targets: map[string]Target{}}
	router, err := placement.New(d, cc.PlacementReplica, v2placement.Lane{Producer: cc.PlacementProducer, Stream: cc.PlacementStream}, sink, cc.PlacementHoldLimit)
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
			sink.targets[s.ID] = targets[s.Write]
		}
	}
	cl.placement = router
	cl.placementPath = cc.PlacementMap
	cl.placementMetrics = ms.Placement
	ms.Placement.Attach(cl.Name, &router.Stats)
	lg.Warn("placement bootstrap uses legacy wire; epoch envelopes and moving-group activation are disabled", zap.String("cluster", cl.Name))
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
	return cl.placement.Adopt(d, nil)
}

func (cls Clusters) HasPlacement() bool {
	for _, cl := range cls {
		if cl.placement != nil {
			return true
		}
	}
	return false
}

// RejectPlacementRestart prevents an uncoordinated second producer process
// bypassing the in-memory epoch floor. Durable restart is a separate follow-up.
func (cls Clusters) RejectPlacementRestart() {
	for _, cl := range cls {
		if cl.placement != nil {
			cl.placementMetrics.Reject(cl.Name)
		}
	}
}

type placementRuntime struct {
	output *placement.Durable
	key    []byte
	log    *zap.Logger
}

func getDurablePlacementCluster(cl *Cluster, cc conf.Cluster, lg *zap.Logger, ms *metrics.Prom) (*Cluster, error) {
	reject := func(e error) (*Cluster, error) { ms.Placement.Reject(cl.Name); return cl, e }
	doc, e := readPlacement(cc.PlacementMap)
	if e != nil {
		return reject(e)
	}
	key, e := os.ReadFile(cc.PlacementKeyFile)
	if e != nil {
		return reject(e)
	}
	if len(key) < 32 || len(key) > 4096 {
		return reject(fmt.Errorf("placement key file must contain 32..4096 bytes"))
	}
	r, out, e := placement.OpenDurable(doc, cc.PlacementReplica, v2placement.Lane{Producer: cc.PlacementProducer, Stream: cc.PlacementStream}, cc.PlacementHoldLimit, placement.DurableConfig{Root: cc.PlacementSpool, MaxRecords: cc.PlacementMaxRecords, MaxBytes: cc.PlacementMaxBytes, CommitInterval: time.Duration(cc.PlacementCommitMS) * time.Millisecond, CommitBytes: cc.PlacementCommitBytes, SegmentBytes: cc.PlacementSegmentBytes})
	if e != nil {
		return reject(e)
	}
	cl.placement = r
	cl.placementPath = cc.PlacementMap
	cl.placementMetrics = ms.Placement
	cl.placementRuntime = &placementRuntime{out, key, lg}
	ms.Placement.Attach(cl.Name, &r.Stats)
	lg.Warn("experimental durable placement WAL enabled; throughput qualification required", zap.String("cluster", cl.Name))
	return cl, nil
}
func (cl *Cluster) sendPlacement(wg *sync.WaitGroup, finish chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-finish:
			cancel()
		case <-ctx.Done():
		}
	}()
	go func() {
		defer wg.Done()
		defer cancel()
		runtime := cl.placementRuntime
		if e := runtime.output.Run(ctx, runtime.key, nil); e != nil {
			runtime.log.Error("durable placement output stopped", zap.Error(e))
		}
		if e := runtime.output.Close(); e != nil {
			runtime.log.Error("durable placement spool close failed", zap.Error(e))
		}
	}()
}

// BeginPlacementMove persists the PG hold before returning its final position.
// The controller must obtain durable store fence and coverage receipts before
// calling AdoptPlacement. Configuration reload alone cannot authorize a move.
func (cl *Cluster) BeginPlacementMove(group string) (placement.Barrier, error) {
	if cl.placement == nil {
		return placement.Barrier{}, fmt.Errorf("cluster has no placement router")
	}
	return cl.placement.BeginMove(group)
}
func (cl *Cluster) AdoptPlacement(doc v2placement.Document, acts []v2placement.Activation) error {
	if cl.placement == nil {
		return fmt.Errorf("cluster has no placement router")
	}
	return cl.placement.Adopt(doc, acts)
}

func (cl *Cluster) PlacementDrainStatus(group string) (placement.Barrier, bool, error) {
	if cl.placement == nil {
		return placement.Barrier{}, false, fmt.Errorf("cluster has no placement router")
	}
	return cl.placement.DrainStatus(group)
}
