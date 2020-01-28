package rules

import (
	"testing"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/target"

	"go.uber.org/zap"
)

func TestRules(t *testing.T) {
	tests := map[string]struct {
		crules    conf.Rules
		ccls      conf.Clusters
		isErr     bool
		nRules    int
		nRegexs   []int
		nClusters []int
	}{
		"various regexs": {
			crules: conf.Rules{
				Rule: []conf.Rule{
					{
						Regexs: []string{
							"abc.*",
							"cbd.*",
							"ab[xyz]test\\.y{5}\\.a?\\.somethingx+\\.test",
						},
						Clusters: []string{
							"A",
						},
					},
					{
						Regexs: []string{
							"xyz.*xyz",
						},
						Clusters: []string{
							"A",
							"B",
							"C",
						},
					},
				},
			},
			ccls: conf.Clusters{
				Cluster: []conf.Cluster{
					{
						Name: "A",
						Type: "toall",
						Hosts: []conf.Host{
							{
								Name:  "host1",
								Index: 0,
								Port:  1234,
							},
						},
					},
					{
						Name: "B",
						Type: "jump",
						Hosts: []conf.Host{
							{
								Name:  "host2",
								Index: 1,
								Port:  2345,
							},
							{
								Name:  "host3",
								Index: 0,
								Port:  2345,
							},
						},
					},
					{
						Name: "C",
						Type: "jump",
						Hosts: []conf.Host{
							{
								Name:  "host4",
								Index: 0,
								Port:  3456,
							},
						},
					},
				},
			},
			isErr:     false,
			nRules:    2,
			nRegexs:   []int{3, 1},
			nClusters: []int{1, 3},
		},
	}

	cfg := conf.MakeDefault()
	for tname, tt := range tests {
		t.Run(tname, func(t *testing.T) {
			ms := metrics.New(&cfg)
			clusters, err := target.NewClusters(cfg, tt.ccls, zap.NewNop(), ms)
			if err != nil {
				t.Fatalf("building clusters failed: %v", err)
			}
			rs, err := Build(tt.crules, clusters)
			if err != nil {
				t.Fatalf("rules building failed: %v", err)
			}

			err = rs.Compile()
			if err != nil {
				t.Fatalf("compiling rules failed: %v", err)
			}

			if len(rs) != tt.nRules {
				t.Fatalf("expected %d rules, got %d", 2, len(rs))
			}

			for i, n := range tt.nRegexs {
				if len(rs[i].Regexs) != n {
					t.Fatalf("expected %d regexs in the rule, got %d", n, len(rs[i].Regexs))
				}
			}

			for i, n := range tt.nClusters {
				if len(rs[i].Targets) != n {
					t.Fatalf("expecte %d clusters in the rule, got %d", n, len(rs[i].Targets))
				}
			}
		})
	}
}
