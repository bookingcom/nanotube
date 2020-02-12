package target

import (
	"fmt"
	"sync"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"

	"github.com/cespare/xxhash"
	"github.com/dgryski/go-jump"
	"github.com/pkg/errors"
	"github.com/segmentio/fasthash/fnv1a"
)

type HostStatus struct {
	Host   *Host
	Status bool
	sigCh  chan struct{}
}

// Cluster represents a set of host with some kind of routing (sharding) defined by it's type.
type Cluster struct {
	Name string
	// Are ordered by index
	Hosts                  []*Host
	AvailableHosts         []*Host
	Hm                     sync.Mutex
	Type                   string
	UpdateHostHealthStatus chan *HostStatus
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
	case conf.LB:
		cl.Hm.Lock()
		defer cl.Hm.Unlock()
		availableHostCount := len(cl.AvailableHosts)
		if availableHostCount == 0 {
			return nil, fmt.Errorf("no available hosts left")
		}
		return []*Host{
			cl.AvailableHosts[int(xxhash.Sum64([]byte(path)))%availableHostCount],
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
			go h.Stream(&wg, cl.UpdateHostHealthStatus)
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

func (cl *Cluster) removeAvailableHost(host *Host) {
	for i, h := range cl.AvailableHosts {
		if h.Name == host.Name {
			host.stateChanges.Inc()
			cl.Hm.Lock()
			defer cl.Hm.Unlock()
			length := len(cl.AvailableHosts)

			for j := i; j < length-2; j++ {
				cl.AvailableHosts[i] = cl.AvailableHosts[i+1]
			}
			cl.AvailableHosts = cl.AvailableHosts[:length-1]

			break
		}
	}
}

func (cl *Cluster) addAvailableHost(host *Host) {
	cl.Hm.Lock()
	defer cl.Hm.Unlock()
	for _, h := range cl.AvailableHosts {
		if h.Name == host.Name && host.Port == h.Port {
			return
		}
	}
	host.stateChanges.Inc()
	cl.AvailableHosts = append(cl.AvailableHosts, host)
}

func (cl *Cluster) keepAvailableHostsUpdated() {
	for {
		h := <-cl.UpdateHostHealthStatus
		if cl.Type != conf.LB {
			continue
		}
		cl.updateHostAvailability(*h)
	}
}

func (cl *Cluster) updateHostAvailability(h HostStatus) {
	defer close(h.sigCh)
	if h.Status == false {
		cl.removeAvailableHost(h.Host)
	} else {
		cl.addAvailableHost(h.Host)
	}
}

// hashing for the rind of hosts in a cluster based on the record path
// using https://en.wikipedia.org/wiki/Fowler%E2%80%93Noll%E2%80%93Vo_hash_function and
// https://arxiv.org/abs/1406.2294
func jumpHash(path string, ringSize int) int32 {
	key := fnv1a.HashString64(path)
	return jump.Hash(key, ringSize)
}
