package target

import (
	"fmt"
	"sync"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"

	"github.com/dgryski/go-jump"
	"github.com/pkg/errors"
	"github.com/segmentio/fasthash/fnv1a"
)

// Cluster represents a set of host with some kind of routing (sharding) defined by it's type.
type Cluster struct {
	Name string
	// Are ordered by index
	Hosts []*Host
	Type  string
}

// Push sends single record to cluster. Routing happens based on cluster type.
func (cl *Cluster) Push(r *rec.Rec, metrics *metrics.Prom) error {
	if cl.Type == conf.BlackholeCluster {
		metrics.BlackholedRecs.Inc()
		return nil
	}

	hs, err := cl.resolveHosts(r.Path)
	if err != nil {
		return errors.Wrapf(err, "resolving host for record %v failed", *r)
	}

	for _, h := range hs {
		h.Push(r)
	}

	return nil
}

// resolveHosts does routing inside the cluster.
func (cl *Cluster) resolveHosts(path string) ([]*Host, error) {
	switch cl.Type {
	case conf.JumpCluster:
		return []*Host{
			cl.Hosts[jumpHash(path, len(cl.Hosts))],
		}, nil
	case conf.ToallCluster:
		return cl.Hosts, nil
	case conf.BlackholeCluster:
		return make([]*Host, 0), nil
	default:
		return nil, fmt.Errorf("resolving hosts for path %s failed", path)
	}
}

// Send starts continuous process of sending data to hosts in the cluster.
// The sending is stopped on demand.
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
			close(h.Ch)
		}
	}()
}

// hashing for the rind of hosts in a cluster based on the record path
// using https://en.wikipedia.org/wiki/Fowler%E2%80%93Noll%E2%80%93Vo_hash_function and
// https://arxiv.org/abs/1406.2294
func jumpHash(path string, ringSize int) int32 {
	key := fnv1a.HashString64(path)
	return jump.Hash(key, ringSize)
}
