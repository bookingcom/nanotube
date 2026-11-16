package target

import (
	"fmt"
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
