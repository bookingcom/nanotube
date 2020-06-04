package main

import (
	"testing"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rewrites"
	"github.com/bookingcom/nanotube/pkg/rules"
	"github.com/bookingcom/nanotube/pkg/target"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

func BenchmarkProcess(b *testing.B) {
	// TODO remove logging altogether to get real results
	lg := zap.NewNop()
	defaultConfig := conf.MakeDefault()
	ms := metrics.New(&defaultConfig)

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

	rulesConf := conf.Rules{Rule: []conf.Rule{
		conf.Rule{
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
			Clusters: []string{
				"1",
			},
		},
		conf.Rule{
			Regexs: []string{
				".*",
				".*",
				".*",
				"^a.*",
			},
			Clusters: []string{
				"1", "2",
			},
		},
		conf.Rule{
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
			Clusters: []string{
				"3",
			},
		},
	},
	}

	rules, err := rules.Build(rulesConf, cls, false, ms)
	if err != nil {
		b.Fatalf("rules building failed: %v", err)
	}
	var emptyRewrites rewrites.Rewrites
	for i := 0; i < b.N; i++ {
		s := "abc 123 123"
		proc(s, rules, emptyRewrites, true, false, lg, ms)
	}
}

func TestContinueRuleProcessing(t *testing.T) {
	// TODO remove logging altogether to get real results
	lg := zap.NewNop()
	defaultConfig := conf.MakeDefault()
	ms := metrics.New(&defaultConfig)

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

	rulesConf := conf.Rules{Rule: []conf.Rule{
		{
			Regexs: []string{
				"a.*",
				"ab.*",
				"ab*",
			},
			Clusters: []string{
				"1",
			},
			Continue: true,
		},
		{
			Regexs: []string{
				"zz.*",
				"ab.*",
				"ab*",
			},
			Clusters: []string{
				"2",
			},
		},
	},
	}

	rules, err := rules.Build(rulesConf, cls, false, ms)
	if err != nil {
		t.Fatalf("rules building failed: %v", err)
	}
	var emptyRewrites rewrites.Rewrites
	queue := make(chan string, 1)
	queue <- testMetric
	done := Process(queue, rules, emptyRewrites, 1, true, true, lg, ms)
	close(queue)
	<-done
	if testutil.ToFloat64(ms.BlackholedRecs) != 2 {
		t.Fatalf("Error processing rules")
	}
}

func TestStopRuleProcessing(t *testing.T) {
	// TODO remove logging altogether to get real results
	lg := zap.NewNop()
	defaultConfig := conf.MakeDefault()
	ms := metrics.New(&defaultConfig)

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

	rulesConf := conf.Rules{Rule: []conf.Rule{
		{
			Regexs: []string{
				"a.*",
				"ab.*",
				"ab*",
			},
			Clusters: []string{
				"1",
			},
			//Continue: false,
		},
		{
			Regexs: []string{
				"zz.*",
				"ab.*",
				"ab*",
			},
			Clusters: []string{
				"2",
			},
			Continue: true,
		},
	},
	}

	rules, err := rules.Build(rulesConf, cls, false, ms)
	if err != nil {
		t.Fatalf("rules building failed: %v", err)
	}
	var emptyRewrites rewrites.Rewrites
	queue := make(chan string, 1)
	queue <- testMetric
	done := Process(queue, rules, emptyRewrites, 1, true, true, lg, ms)
	close(queue)
	<-done
	if testutil.ToFloat64(ms.BlackholedRecs) != 1 {
		t.Fatal("Error processing rules")
	}
}

func TestRewriteNoCopy(t *testing.T) {
	// TODO remove logging altogether to get real results
	lg := zap.NewNop()
	defaultConfig := conf.MakeDefault()
	ms := metrics.New(&defaultConfig)

	testMetric := "ab.c 123 123"
	cls := target.Clusters{
		"1": &target.Cluster{
			Name: "1",
			Type: conf.BlackholeCluster,
		},
	}

	rulesConf := conf.Rules{Rule: []conf.Rule{
		{
			Regexs: []string{
				"de",
			},
			Clusters: []string{
				"1",
			},
		},
	},
	}

	rewritesConf := conf.Rewrites{Rewrite: []conf.Rewrite{
		{
			From: "ab.c",
			To:   "de",
			Copy: false,
		},
	},
	}

	rules, err := rules.Build(rulesConf, cls, false, ms)
	if err != nil {
		t.Fatalf("rules building failed: %v", err)
	}
	rewrites, err := rewrites.Build(rewritesConf, false, ms)
	if err != nil {
		t.Fatalf("rewrite rules building failed: %v", err)
	}
	queue := make(chan string, 1)
	queue <- testMetric
	done := Process(queue, rules, rewrites, 1, true, true, lg, ms)
	close(queue)
	<-done
	if testutil.ToFloat64(ms.BlackholedRecs) != 1 {
		t.Fatal("Error processing rules")
	}
}

func TestRewriteCopy(t *testing.T) {
	// TODO remove logging altogether to get real results
	lg := zap.NewNop()
	defaultConfig := conf.MakeDefault()
	ms := metrics.New(&defaultConfig)

	testMetric := "ab.c 123 123"
	cls := target.Clusters{
		"1": &target.Cluster{
			Name: "1",
			Type: conf.BlackholeCluster,
		},
	}

	rulesConf := conf.Rules{Rule: []conf.Rule{
		{
			Regexs: []string{
				"de",
				"ab.c",
			},
			Clusters: []string{
				"1",
			},
		},
	}}

	rewritesConf := conf.Rewrites{Rewrite: []conf.Rewrite{
		{
			From: "ab.c",
			To:   "de",
			Copy: true,
		},
	},
	}

	rules, err := rules.Build(rulesConf, cls, false, ms)
	if err != nil {
		t.Fatalf("rules building failed: %v", err)
	}
	rewrites, err := rewrites.Build(rewritesConf, false, ms)
	if err != nil {
		t.Fatalf("rewrite rules building failed: %v", err)
	}
	queue := make(chan string, 1)
	queue <- testMetric
	done := Process(queue, rules, rewrites, 1, true, true, lg, ms)
	close(queue)
	<-done
	if testutil.ToFloat64(ms.BlackholedRecs) != 2 {
		t.Fatal("Error processing rules")
	}
}
