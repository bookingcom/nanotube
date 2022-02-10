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

func BenchmarkProcessREs(b *testing.B) {
	b.StopTimer()
	var benchMetrics = [...]string{
		"aaa 1 1",
		"abcabc 1 1",
		"xxx 1 1",
		"1234lkjsljfdlaskdjfskdjf 1 1",
		"123kjkj 1 1",
		"123 1 1",
		"jkn 1 1",
		"lkjlkjlksjdlkfjalskdjfewifjlsdkmnflksdjflskdjfloskjeoifjklsjdflkjsdl 1 1",
		"lkmnxlkhjfgkshdioufhewoiabclkjlkjabclkjl;kjaaaaaaaaaaalkjljabcalkjlkjabc 1 1",
		"lkjsdlkjfaljbajlkjlkjabcabc 1 1",
		"kkkkkkkkkkkkkkkkkkkjjjjjjjjjjjjjjjjjjjjjjj 1 1",
		"aaaaaaaaaaajabcabcabcabcabclkjljk 1 1",
		"a 1 1",
		"abc 1 1",
		"abcabcabcabc 1 1",
		"abcabcabcabcabcabc 1 1",
	}
	var REs = []string{
		"^a.*",
		"^abc.*",
		"^abcabcabcabc.*",
		"^abcabcabcabcabcabc.*",
	}

	lg := zap.NewNop()
	defaultConfig := conf.MakeDefault()
	ms := metrics.New(&defaultConfig)

	cls := map[string]*target.TestTarget{
		"1": {Name: "1"},
		"2": {Name: "2"},
		"3": {Name: "3"},
		"4": {Name: "4"},
	}

	rulesConf := conf.Rules{Rule: []conf.Rule{
		{
			Regexs: REs,
			Clusters: []string{
				"1",
			},
		},
	},
	}

	rules, err := rules.TestBuild(rulesConf, cls, false, ms)
	if err != nil {
		b.Fatalf("rules building failed: %v", err)
	}
	var emptyRewrites rewrites.Rewrites

	b.StartTimer()
	for i := 0; i < b.N; i++ {
		for _, m := range benchMetrics {
			proc(m, rules, emptyRewrites, true, false, lg, ms)
		}
	}
}

func BenchmarkProcessPrefix(b *testing.B) {
	b.StopTimer()
	var benchMetrics = [...]string{
		"aaa 1 1",
		"abcabc 1 1",
		"xxx 1 1",
		"1234lkjsljfdlaskdjfskdjf 1 1",
		"123kjkj 1 1",
		"123 1 1",
		"jkn 1 1",
		"lkjlkjlksjdlkfjalskdjfewifjlsdkmnflksdjflskdjfloskjeoifjklsjdflkjsdl 1 1",
		"lkmnxlkhjfgkshdioufhewoiabclkjlkjabclkjl;kjaaaaaaaaaaalkjljabcalkjlkjabc 1 1",
		"lkjsdlkjfaljbajlkjlkjabcabc 1 1",
		"kkkkkkkkkkkkkkkkkkkjjjjjjjjjjjjjjjjjjjjjjj 1 1",
		"aaaaaaaaaaajabcabcabcabcabclkjljk 1 1",
		"a 1 1",
		"abc 1 1",
		"abcabcabcabc 1 1",
		"abcabcabcabcabcabc 1 1",
	}
	var prefixes = []string{
		"a",
		"abc",
		"abcabcabcabc",
		"abcabcabcabcabcabc",
	}

	lg := zap.NewNop()
	defaultConfig := conf.MakeDefault()
	ms := metrics.New(&defaultConfig)

	cls := map[string]*target.TestTarget{
		"1": {Name: "1"},
		"2": {Name: "2"},
		"3": {Name: "3"},
		"4": {Name: "4"},
	}

	rulesConf := conf.Rules{Rule: []conf.Rule{
		{
			Prefixes: prefixes,
			Clusters: []string{
				"1",
			},
		},
	},
	}

	rules, err := rules.TestBuild(rulesConf, cls, false, ms)
	if err != nil {
		b.Fatalf("rules building failed: %v", err)
	}
	var emptyRewrites rewrites.Rewrites

	b.StartTimer()
	for i := 0; i < b.N; i++ {
		for i := 0; i < 10000; i++ {
			for _, m := range benchMetrics {
				proc(m, rules, emptyRewrites, true, false, lg, ms)
			}
		}
	}
}

func BenchmarkProcessSingle(b *testing.B) {
	b.StopTimer()
	var benchMetrics = [...]string{
		"aaa 1 1",
	}
	var prefixes = []string{
		"a",
	}

	lg := zap.NewNop()
	defaultConfig := conf.MakeDefault()
	ms := metrics.New(&defaultConfig)

	cls := map[string]*target.TestTarget{
		"1": {Name: "1"},
		"2": {Name: "2"},
		"3": {Name: "3"},
		"4": {Name: "4"},
	}

	rulesConf := conf.Rules{Rule: []conf.Rule{
		{
			Prefixes: prefixes,
			Clusters: []string{
				"1",
			},
		},
	},
	}

	rules, err := rules.TestBuild(rulesConf, cls, false, ms)
	if err != nil {
		b.Fatalf("rules building failed: %v", err)
	}
	var emptyRewrites rewrites.Rewrites

	b.StartTimer()
	for i := 0; i < b.N; i++ {
		queue := make(chan string, 1000)
		go func() {
			for _, m := range benchMetrics {
				queue <- m
			}
			close(queue)
		}()

		done := Process(queue, rules, emptyRewrites, 4, true, false, lg, ms)
		<-done
	}
}

func BenchmarkFullProcess(b *testing.B) {
	b.StopTimer()
	var benchMetrics = [...]string{
		"aaa 1 1",
		"abcabc 1 1",
		"xxx 1 1",
		"1234lkjsljfdlaskdjfskdjf 1 1",
		"123kjkj 1 1",
		"123 1 1",
		"jkn 1 1",
		"lkjlkjlksjdlkfjalskdjfewifjlsdkmnflksdjflskdjfloskjeoifjklsjdflkjsdl 1 1",
		"lkmnxlkhjfgkshdioufhewoiabclkjlkjabclkjl;kjaaaaaaaaaaalkjljabcalkjlkjabc 1 1",
		"lkjsdlkjfaljbajlkjlkjabcabc 1 1",
		"kkkkkkkkkkkkkkkkkkkjjjjjjjjjjjjjjjjjjjjjjj 1 1",
		"aaaaaaaaaaajabcabcabcabcabclkjljk 1 1",
		"a 1 1",
		"abc 1 1",
		"abcabcabcabc 1 1",
		"abcabcabcabcabcabc 1 1",
	}
	var prefixes = []string{
		"a",
		"abc",
		"abcabcabcabc",
		"abcabcabcabcabcabc",
	}

	lg := zap.NewNop()
	defaultConfig := conf.MakeDefault()
	ms := metrics.New(&defaultConfig)

	cls := map[string]*target.TestTarget{
		"1": {Name: "1"},
		"2": {Name: "2"},
		"3": {Name: "3"},
		"4": {Name: "4"},
	}

	rulesConf := conf.Rules{Rule: []conf.Rule{
		{
			Prefixes: prefixes,
			Clusters: []string{
				"1",
			},
		},
	},
	}

	rules, err := rules.TestBuild(rulesConf, cls, false, ms)
	if err != nil {
		b.Fatalf("rules building failed: %v", err)
	}
	var emptyRewrites rewrites.Rewrites

	b.StartTimer()
	for i := 0; i < b.N; i++ {
		queue := make(chan string, 100000)
		go func() {
			for i := 0; i < 10000; i++ {
				for _, m := range benchMetrics {
					queue <- m
				}
			}
			close(queue)
		}()

		done := Process(queue, rules, emptyRewrites, 4, true, false, lg, ms)
		<-done
	}
}

func TestContinueRuleProcessing(t *testing.T) {
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

	rules, err := rules.Build(&rulesConf, cls, false, ms)
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

	rules, err := rules.Build(&rulesConf, cls, false, ms)
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

	rules, err := rules.Build(&rulesConf, cls, false, ms)
	if err != nil {
		t.Fatalf("rules building failed: %v", err)
	}
	rewrites, err := rewrites.Build(&rewritesConf, false, ms)
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

	rules, err := rules.Build(&rulesConf, cls, false, ms)
	if err != nil {
		t.Fatalf("rules building failed: %v", err)
	}
	rewrites, err := rewrites.Build(&rewritesConf, false, ms)
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

func TestProcessing(t *testing.T) {
	lg := zap.NewNop()
	defaultConfig := conf.MakeDefault()
	ms := metrics.New(&defaultConfig)

	var testMetrics = []string{
		"ab.c 123 123",
		"aaa 123 123",
		"zz 1 1",
		"klk.kjl.kjo 1.9800 8909876",
	}

	cls := map[string]*target.TestTarget{
		"1": {Name: "1"},
		"2": {Name: "2"},
		"3": {Name: "3"},
		"4": {Name: "4"},
	}

	rulesConf := conf.Rules{Rule: []conf.Rule{
		{
			Regexs: []string{
				"a.*",
				"ab.*",
				"ab*"},
			Clusters: []string{
				"1",
			},
			Continue: true,
		},
		{
			Regexs: []string{
				"zz.*",
				"ab.*",
			},
			Clusters: []string{
				"2",
			},
		},
	},
	}

	rules, err := rules.TestBuild(rulesConf, cls, false, ms)
	if err != nil {
		t.Fatalf("rules building failed: %v", err)
	}
	var emptyRewrites rewrites.Rewrites
	queue := make(chan string, len(testMetrics))
	for _, m := range testMetrics {
		queue <- m
	}
	done := Process(queue, rules, emptyRewrites, 1, true, true, lg, ms)
	close(queue)
	<-done
	if cls["1"].ReceivedRecsNum != 2 {
		t.Fatalf("did not receive 2 rec in 1, but %d", cls["1"].ReceivedRecsNum)
	}
	if cls["2"].ReceivedRecsNum != 2 {
		t.Fatal("did not receive 2 rec in 2")
	}
}

func TestProcessingPrefix(t *testing.T) {
	lg := zap.NewNop()
	defaultConfig := conf.MakeDefault()
	ms := metrics.New(&defaultConfig)

	var testMetrics = []string{
		"ab.c 123 123",
		"aaa 123 123",
		"zz 1 1",
		"klk.kjl.kjo 1.9800 8909876",
	}

	cls := map[string]*target.TestTarget{
		"1": {Name: "1"},
		"2": {Name: "2"},
		"3": {Name: "3"},
		"4": {Name: "4"},
	}

	rulesConf := conf.Rules{Rule: []conf.Rule{
		{
			Prefixes: []string{
				"a",
				"ab",
			},
			Clusters: []string{
				"1",
			},
			Continue: true,
		},
		{
			Prefixes: []string{
				"zz",
				"ab",
			},
			Clusters: []string{
				"2",
			},
		},
	},
	}

	rules, err := rules.TestBuild(rulesConf, cls, false, ms)
	if err != nil {
		t.Fatalf("rules building failed: %v", err)
	}
	var emptyRewrites rewrites.Rewrites
	queue := make(chan string, len(testMetrics))
	for _, m := range testMetrics {
		queue <- m
	}
	done := Process(queue, rules, emptyRewrites, 1, true, true, lg, ms)
	close(queue)
	<-done
	if cls["1"].ReceivedRecsNum != 2 {
		t.Fatalf("did not receive 2 rec in 1, but %d", cls["1"].ReceivedRecsNum)
	}
	if cls["2"].ReceivedRecsNum != 2 {
		t.Fatal("did not receive 2 rec in 2")
	}
}
