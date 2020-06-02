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

	rules := rules.NewFromSlice([]rules.Rule{
		{
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
		{
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
		{
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
	}, ms)

	err := rules.Compile()
	if err != nil {
		b.Fatalf("rules compilation failed: %v", err)
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

	rules := rules.NewFromSlice([]rules.Rule{
		{
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
		{
			Regexs: []string{
				"zz.*",
				"ab.*",
				"ab*",
			},
			Targets: []*target.Cluster{
				cls["2"],
			},
		},
	}, ms)

	err := rules.Compile()
	if err != nil {
		t.Fatalf("rules compilation failed: %v", err)
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

	rules := rules.NewFromSlice([]rules.Rule{
		{
			Regexs: []string{
				"a.*",
				"ab.*",
				"ab*",
			},
			Targets: []*target.Cluster{cls["1"]},
			//Continue: false,
		},
		{
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
	}, ms)

	err := rules.Compile()
	if err != nil {
		t.Fatalf("rules compilation failed: %v", err)
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

	rules := rules.NewFromSlice([]rules.Rule{
		{
			Regexs: []string{
				"de",
			},
			Targets: []*target.Cluster{cls["1"]}},
	}, ms)

	rewrites := rewrites.NewFromSlice([]rewrites.Rewrite{
		{
			From: "ab.c",
			To:   "de",
			Copy: false,
		},
	}, ms)

	err := rules.Compile()
	if err != nil {
		t.Fatalf("rules compilation failed: %v", err)
	}
	err = rewrites.Compile()
	if err != nil {
		t.Fatalf("rewrite rules compilation failed: %v", err)
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

	rules := rules.NewFromSlice([]rules.Rule{
		{
			Regexs: []string{
				"de",
				"ab.c",
			},
			Targets: []*target.Cluster{cls["1"]}},
	}, ms)

	rewrites := rewrites.NewFromSlice([]rewrites.Rewrite{
		{
			From: "ab.c",
			To:   "de",
			Copy: true,
		},
	}, ms)

	err := rules.Compile()
	if err != nil {
		t.Fatalf("rules compilation failed: %v", err)
	}
	err = rewrites.Compile()
	if err != nil {
		t.Fatalf("rewrite rules compilation failed: %v", err)
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
