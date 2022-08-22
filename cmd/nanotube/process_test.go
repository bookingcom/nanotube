package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rewrites"
	"github.com/bookingcom/nanotube/pkg/rules"
	"github.com/bookingcom/nanotube/pkg/target"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

func testData() (benchMetrics [][]byte, regexs []string, prefixes []string) {
	benchMetrics = [][]byte{
		[]byte("aaa 1 1"),
		[]byte("abcabc 1 1"),
		[]byte("xxx 1 1"),
		[]byte("1234lkjsljfdlaskdjfskdjf 1 1"),
		[]byte("123kjkj 1 1"),
		[]byte("123 1 1"),
		[]byte("jkn 1 1"),
		[]byte("lkjlkjlksjdlkfjalskdjfewifjlsdkmnflksdjflskdjfloskjeoifjklsjdflkjsdl 1 1"),
		[]byte("lkmnxlkhjfgkshdioufhewoiabclkjlkjabclkjl;kjaaaaaaaaaaalkjljabcalkjlkjabc 1 1"),
		[]byte("lkjsdlkjfaljbajlkjlkjabcabc 1 1"),
		[]byte("kkkkkkkkkkkkkkkkkkkjjjjjjjjjjjjjjjjjjjjjjj 1 1"),
		[]byte("aaaaaaaaaaajabcabcabcabcabclkjljk 1 1"),
		[]byte("a 1 1"),
		[]byte("abc 1 1"),
		[]byte("abcabcabcabc 1 1"),
		[]byte("abcabcabcabcabcabc 1 1"),
	}

	regexs = []string{
		"^a.*",
		"^abc.*",
		"^abcabcabcabc.*",
		"^abcabcabcabcabcabc.*",
	}

	prefixes = []string{
		"a",
		"abc",
		"abcabcabcabc",
		"abcabcabcabcabcabc",
	}

	return
}

func BenchmarkProcessREs(b *testing.B) {
	b.StopTimer()

	benchMetrics, REs, _ := testData()

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

	benchMetrics, _, prefixes := testData()

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

func BenchmarkProcWithoutConcurrentWorkers(b *testing.B) {
	b.StopTimer()

	benchMetrics, regexs, prefixes := testData()

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
			Regexs:   regexs,
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

func BenchmarkProcessFunc(b *testing.B) {
	b.StopTimer()

	benchMetrics, regexs, prefixes := testData()

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
			Regexs:   regexs,
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
		queue := make(chan []byte, 100000)
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

func setupRealisticBench(b *testing.B) (benchMetrics []string, clusters target.Clusters, rules rules.Rules, rewrites rewrites.Rewrites, ms *metrics.Prom, lg *zap.Logger) {

	fixturesPath := "testdata/bench/"

	in, err := os.Open(filepath.Join(fixturesPath, "in"))
	if err != nil {
		b.Fatalf("error opening the in data file %v", err)
	}
	defer func() {
		err := in.Close()
		if err != nil {
			b.Fatalf("error closing in data test file: %v", err)
		}
	}()

	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		benchMetrics = append(benchMetrics, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		log.Fatalf("error while scan-reading the sample in metrics %v", err)
	}

	cfgPath := filepath.Join(fixturesPath, "config.toml")

	cfg, clustersConf, rulesConf, rewritesConf, _, _, err := readConfigs(cfgPath)
	if err != nil {
		b.Fatalf("error reading and compiling config: %v", err)
	}

	lg = zap.NewNop()

	ms = metrics.New(&cfg)
	clusters, rules, rewrites, err = buildPipeline(&cfg, &clustersConf, &rulesConf, rewritesConf, ms, lg)
	if err != nil {
		b.Fatalf("error building pipline components: %v", err)
	}

	return
}

func benchmarkPowN(b *testing.B, f func(*testing.B, int)) {
	for nWorkers := 1; nWorkers <= 32; nWorkers *= 2 {
		b.Run(fmt.Sprintf("%d", nWorkers), func(b *testing.B) {
			f(b, nWorkers)
		})
	}
}

func BenchmarkRealisticBytes(b *testing.B) {
	benchmarkPowN(b, benchRealisticBytes)
}

func benchRealisticBytes(b *testing.B, nWorkers int) {
	b.StopTimer()

	benchMetrics, clusters, rules, rewrites, ms, lg := setupRealisticBench(b)
	benchMetricsBytes := [][]byte{}
	for _, m := range benchMetrics {
		benchMetricsBytes = append(benchMetricsBytes, []byte(m))
	}

	for i := 0; i < b.N; i++ {
		queue := make(chan []byte, 1000000)
		for i := 0; i < 10; i++ {
			for _, m := range benchMetricsBytes {
				queue <- m
			}
		}
		close(queue)

		b.StartTimer()

		done := Process(queue, rules, rewrites, nWorkers, true, false, lg, ms)
		_ = clusters.Send(done)

		<-done

		b.StopTimer()
	}
}

func TestContinueRuleProcessing(t *testing.T) {
	lg := zap.NewNop()
	defaultConfig := conf.MakeDefault()
	ms := metrics.New(&defaultConfig)

	testMetric := []byte("ab.c 123 123")
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
	queue := make(chan []byte, 1)
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

	testMetric := []byte(" ab.c 123 123")
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
	queue := make(chan []byte, 1)
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

	testMetric := []byte("ab.c 123 123")
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
	queue := make(chan []byte, 1)
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

	testMetric := []byte("ab.c 123 123")
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
	queue := make(chan []byte, 1)
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

	testMetrics := [][]byte{
		[]byte("ab.c 123 123"),
		[]byte("aaa 123 123"),
		[]byte("zz 1 1"),
		[]byte("klk.kjl.kjo 1.9800 8909876"),
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
	queue := make(chan []byte, len(testMetrics))
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

	testMetrics := [][]byte{
		[]byte("ab.c 123 123"),
		[]byte("aaa 123 123"),
		[]byte("zz 1 1"),
		[]byte("klk.kjl.kjo 1.9800 8909876"),
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
	queue := make(chan []byte, len(testMetrics))
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
