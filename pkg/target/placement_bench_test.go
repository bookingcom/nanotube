package target

import (
	"fmt"
	"github.com/bookingcom/nanotube/internal/placement"
	"sync"
	"testing"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/rec"
)

type campaignBenchTarget struct{ records uint64 }

func (t *campaignBenchTarget) Push(*rec.RecBytes)   { t.records++ }
func (*campaignBenchTarget) Stream(*sync.WaitGroup) {}
func (*campaignBenchTarget) Stop()                  {}
func (*campaignBenchTarget) IsAvailable() bool      { return true }

// This identical harness also runs on the fetched base. Host queue/network I/O
// are excluded to isolate any change to legacy scalar and batch routing cost.
func BenchmarkPlacementCampaignJump(b *testing.B) {
	for _, size := range []int{3, 40} {
		for _, batching := range []bool{false, true} {
			b.Run(fmt.Sprintf("hosts=%d/batch=%t", size, batching), func(b *testing.B) {
				cl := &Cluster{Type: conf.JumpCluster}
				for i := 0; i < size; i++ {
					cl.Hosts = append(cl.Hosts, &campaignBenchTarget{})
				}
				records := make([]*rec.RecBytes, 4096)
				for i := range records {
					records[i] = &rec.RecBytes{Path: []byte(fmt.Sprintf("example.application.worker.%d.requests", i))}
					records[i].PathHash()
				}
				batch := NewBatch()
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					record := records[i%len(records)]
					if batching {
						_ = cl.PushBytesBatch(record, nil, batch)
					} else {
						_ = cl.PushBytes(record, nil)
					}
				}
				batch.Flush()
			})
		}
	}
}

// A counting queued target exercises Batch's real staging without network I/O
// or retaining an ever-growing test output slice.
type campaignQueuedTarget struct {
	campaignBenchTarget
	id int
}

func (t *campaignQueuedTarget) queueID() int                { return t.id }
func (t *campaignQueuedTarget) pushBulk(rs []*rec.RecBytes) { t.records += uint64(len(rs)) }

func BenchmarkPlacementCampaignOwners(b *testing.B) {
	for _, mode := range []string{"single", "one-moving", "all-moving"} {
		for _, batching := range []bool{false, true} {
			b.Run(fmt.Sprintf("%s/batch=%t", mode, batching), func(b *testing.B) {
				d := placementDocument(b)
				if mode != "single" {
					d = nextMap(d)
					for i := range d.Groups {
						if mode == "all-moving" || i == 0 {
							d.Groups[i].Replicas[0].MovingTo = "d"
						}
					}
				}
				r, err := placement.New(d, "site-a")
				if err != nil {
					b.Fatal(err)
				}
				cl := &Cluster{Type: conf.PlacementCluster, placement: r, placementTargets: map[string]Target{}}
				for _, id := range []string{"a", "b", "c", "d"} {
					cl.placementTargets[id] = &campaignQueuedTarget{id: nextQueueID()}
				}
				records := make([]*rec.RecBytes, 4096)
				for i := range records {
					records[i] = &rec.RecBytes{Path: []byte(fmt.Sprintf("example.application.worker.%d.requests", i))}
					records[i].PathHash()
				}
				batch := NewBatch()
				route := func(i int) {
					record := records[i%len(records)]
					if batching {
						_ = cl.PushBytesBatch(record, nil, batch)
						if i%256 == 255 {
							batch.Flush()
						}
					} else {
						_ = cl.PushBytes(record, nil)
					}
				}
				for i := range 4096 {
					route(i)
				}
				batch.Flush()
				b.ReportAllocs()
				b.ResetTimer()
				for i := range b.N {
					route(i)
				}
				batch.Flush()
				b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "records/s")
			})
		}
	}
}
