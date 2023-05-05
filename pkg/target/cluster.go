package target

import (
	"fmt"
	"hash/fnv"
	"sync"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/dgryski/go-jump"
	"github.com/pkg/errors"
)

// ClusterTarget is abstract notion of a target to send the records to during processing.
type ClusterTarget interface {
	PushBytes(*rec.RecBytes, *metrics.Prom) error
	Send(*sync.WaitGroup, chan struct{})
	GetName() string
}

// Cluster represents a set of host with some kind of routing (sharding) defined by it's type.
type Cluster struct {
	Name  string
	Hosts []Target // ordered by index
	Hm    sync.RWMutex
	Type  string
}

func (cl *Cluster) availHostsList() []Target {
	var availHosts []Target
	for _, h := range cl.Hosts {
		if h.IsAvailable() {
			availHosts = append(availHosts, h)
		}
	}

	return availHosts
}

// PushBytes sends single record to cluster. Routing happens based on cluster type.
func (cl *Cluster) PushBytes(r *rec.RecBytes, metrics *metrics.Prom) error {
	if cl.Type == conf.BlackholeCluster {
		metrics.BlackholedRecs.Inc()
		return nil
	}

	hs, err := cl.resolveHosts(r.Path, metrics)
	if err != nil {
		return errors.Wrapf(err, "resolving host for record %v failed", *r)
	}

	for _, h := range hs {
		h.Push(r)
	}

	return nil
}

// GetName returns cluster name.
func (cl *Cluster) GetName() string {
	return cl.Name
}

// resolveHosts does routing inside the cluster.
func (cl *Cluster) resolveHosts(path []byte, ms *metrics.Prom) ([]Target, error) {
	switch cl.Type {
	case conf.JumpCluster:
		return []Target{
			cl.Hosts[jumpHashStd(path, len(cl.Hosts))],
		}, nil
	case conf.LB:
		availHosts := cl.availHostsList()
		ms.NumberOfAvailableTargets.With(prometheus.Labels{"cluster": cl.Name}).Set(float64(len(availHosts)))

		hash := fnv.New64a()
		hash.Write(path)
		key := hash.Sum64()
		if len(availHosts) == 0 {
			return []Target{
				cl.Hosts[key%uint64(len(cl.Hosts))],
			}, nil
		}
		return []Target{
			availHosts[key%uint64(len(availHosts))],
		}, nil
	case conf.ToallCluster:
		return cl.Hosts, nil
	case conf.BlackholeCluster:
		return make([]Target, 0), nil
	default:
		return nil, fmt.Errorf("resolving hosts for path %s failed", path)
	}
}

// Send starts continuous process of sending data to hosts in the cluster.
// The sending is stopped on demand. Wait-group should be marked as done after
// finished.
func (cl *Cluster) Send(cwg *sync.WaitGroup, finish chan struct{}) {
	// launch streaming on all hosts and wait until it's finished
	go func() {
		defer cwg.Done()

		var wg sync.WaitGroup
		wg.Add(len(cl.Hosts))
		for _, h := range cl.Hosts {
			go h.Stream(&wg)
		}

		wg.Wait()
	}()

	go func() {
		<-finish
		for _, h := range cl.Hosts {
			h.Stop()
		}
	}()
}

// Hashing for the ring of hosts in a cluster based on the record path
// using https://en.wikipedia.org/wiki/Fowler%E2%80%93Noll%E2%80%93Vo_hash_function and
// https://arxiv.org/abs/1406.2294
func jumpHashStd(path []byte, ringSize int) int32 {
	hash := fnv.New64a()
	hash.Write(path)
	return jump.Hash(hash.Sum64(), ringSize)
}

// TestTarget mocks a target cluster in tests.
type TestTarget struct {
	Name            string
	ReceivedRecsNum uint64
}

// PushBytes is a push in tests. It does nothing, just increases the counter.
func (tt *TestTarget) PushBytes(_ *rec.RecBytes, _ *metrics.Prom) error {
	tt.ReceivedRecsNum++
	return nil
}

// Send emulates mocks a remote sending routine. Does nothing.
func (tt *TestTarget) Send(wg *sync.WaitGroup, finish chan struct{}) {
	<-finish
	wg.Done()
}

// GetName returns cluster name.
func (tt *TestTarget) GetName() string {
	return tt.Name
}
