package main

import (
	"nanotube/pkg/conf"
	"nanotube/pkg/metrics"
	"nanotube/pkg/rules"
	"nanotube/pkg/target"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

func BenchmarkProcess(b *testing.B) {
	cls := target.Clusters{
		"1": &target.Cluster{
			Name: "1",
			Type: conf.BlackholeCluster,
		},
		"2": &target.Cluster{
			Name: "2",
			Type: conf.BlackholeCluster,
		},
		"3": &target.Cluster{
			Name: "3",
			Type: conf.BlackholeCluster,
		},
		"4": &target.Cluster{
			Name: "4",
			Type: conf.BlackholeCluster,
		},
	}
	rules := rules.Rules{
		rules.Rule{
			Regexs: []string{
				".*",
				".*",
				".*",
				".*",
				".*",
				".*",
				".*",
				".*",
				".*",
			},
			Targets: []*target.Cluster{
				cls["1"],
			},
		},
		rules.Rule{
			Regexs: []string{
				".*",
				".*",
				".*",
				"^a.*",
			},
			Targets: []*target.Cluster{
				cls["1"],
				cls["2"],
			},
		},
		rules.Rule{
			Regexs: []string{
				".*",
				".*",
				".*",
				".*",
				".*",
				".*",
				".*",
				".*",
				".*",
			},
			Targets: []*target.Cluster{
				cls["3"],
			},
		},
	}

	err := rules.Compile()
	if err != nil {
		b.Fatalf("rules compilation failed: %v", err)
	}

	lg := zap.NewNop()

	defaultConfig := conf.MakeDefault()
	ms := metrics.New(&defaultConfig)
	for i := 0; i < b.N; i++ {
		s := "abc 123 123"
		proc(&s, rules, true, true, lg, ms)
	}
}

func TestContinueRuleProcessing(t *testing.T) {
	testMetric := "ab.c 123 123"
	cls := target.Clusters{
		"1": &target.Cluster{
			Name: "1",
			Type: conf.BlackholeCluster,
		},
		"2": &target.Cluster{
			Name: "2",
			Type: conf.BlackholeCluster,
		},
		"3": &target.Cluster{
			Name: "3",
			Type: conf.BlackholeCluster,
		},
		"4": &target.Cluster{
			Name: "4",
			Type: conf.BlackholeCluster,
		},
	}
	rules := rules.Rules{
		rules.Rule{
			Regexs: []string{
				"a.*",
				"ab.*",
				"ab*",
			},
			Targets: []*target.Cluster{
				cls["1"],
			},
			Continue: true,
		},
		rules.Rule{
			Regexs: []string{
				"zz.*",
				"ab.*",
				"ab*",
			},
			Targets: []*target.Cluster{
				cls["2"],
			},
		},
	}

	err := rules.Compile()
	if err != nil {
		t.Fatalf("rules compilation failed: %v", err)
	}

	lg := zap.NewNop()
	defaultConfig := conf.MakeDefault()
	ms := metrics.New(&defaultConfig)
	queue := make(chan string, 1)
	queue <- testMetric
	done := Process(queue, rules, 1, true, true, lg, ms)
	close(queue)
	<-done
	if testutil.ToFloat64(ms.BlackholedRecs) != 2 {
		t.Fatalf("Error processing rules")
	}
}

func TestStopRuleProcessing(t *testing.T) {
	testMetric := " ab.c 123 123"
	cls := target.Clusters{
		"1": &target.Cluster{
			Name: "1",
			Type: conf.BlackholeCluster,
		},
		"2": &target.Cluster{
			Name: "2",
			Type: conf.BlackholeCluster,
		},
		"3": &target.Cluster{
			Name: "3",
			Type: conf.BlackholeCluster,
		},
		"4": &target.Cluster{
			Name: "4",
			Type: conf.BlackholeCluster,
		},
	}
	rules := rules.Rules{
		rules.Rule{
			Regexs: []string{
				"a.*",
				"ab.*",
				"ab*",
			},
			Targets: []*target.Cluster{cls["1"]},
			//Continue: false,
		},
		rules.Rule{
			Regexs: []string{
				"zz.*",
				"ab.*",
				"ab*",
			},
			Targets: []*target.Cluster{
				cls["2"],
			},
			Continue: true,
		},
	}

	err := rules.Compile()
	if err != nil {
		t.Fatalf("rules compilation failed: %v", err)
	}

	lg := zap.NewNop()
	defaultConfig := conf.MakeDefault()
	ms := metrics.New(&defaultConfig)
	queue := make(chan string, 1)
	queue <- testMetric
	done := Process(queue, rules, 1, true, true, lg, ms)
	close(queue)
	<-done
	if testutil.ToFloat64(ms.BlackholedRecs) != 1 {
		t.Fatal("Error processing rules")
	}
}
