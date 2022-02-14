package main

import (
	"bufio"
	"log"
	"os"
	"path/filepath"
	"sync"
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

func BenchmarkProcWithoutConcurrentWorkers(b *testing.B) {
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
	var regexs = []string{
		"lk.*kj.*",
		"abc.*a+.*",
		"a",
		".*23.*",
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
	var regexs = []string{
		"lk.*kj.*",
		"abc.*a+.*",
		"a",
		".*23.*",
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

	// var benchMetrics []string

	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		benchMetrics = append(benchMetrics, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		log.Fatalf("error while scan-reading the samplie in metrics %v", err)
	}

	cfgPath := filepath.Join(fixturesPath, "config.toml")

	cfg, clustersConf, rulesConf, rewritesConf, _, err := readConfigs(cfgPath)
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

func BenchmarkRealistic1(b *testing.B)  { realisticBench(b, 1) }
func BenchmarkRealistic4(b *testing.B)  { realisticBench(b, 4) }
func BenchmarkRealistic8(b *testing.B)  { realisticBench(b, 8) }
func BenchmarkRealistic16(b *testing.B) { realisticBench(b, 16) }
func BenchmarkRealistic32(b *testing.B) { realisticBench(b, 32) }
func BenchmarkRealistic64(b *testing.B) { realisticBench(b, 64) }

func realisticBench(b *testing.B, nWorkers uint16) {
	b.StopTimer()

	benchMetrics, clusters, rules, rewrites, ms, lg := setupRealisticBench(b)

	for i := 0; i < b.N; i++ {
		queue := make(chan string, 1000000)
		for i := 0; i < 10; i++ {
			for _, m := range benchMetrics {
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

// This set of benchmarks investigates impact of channel contention by making reads/write on it less frequent.

func BenchmarkRealisticBuff1(b *testing.B)  { realisticBenchBuff(b, 1) }
func BenchmarkRealisticBuff4(b *testing.B)  { realisticBenchBuff(b, 4) }
func BenchmarkRealisticBuff8(b *testing.B)  { realisticBenchBuff(b, 8) }
func BenchmarkRealisticBuff16(b *testing.B) { realisticBenchBuff(b, 16) }
func BenchmarkRealisticBuff32(b *testing.B) { realisticBenchBuff(b, 32) }
func BenchmarkRealisticBuff64(b *testing.B) { realisticBenchBuff(b, 64) }

func realisticBenchBuff(b *testing.B, nWorkers uint16) {
	b.StopTimer()

	benchMetrics, clusters, rules, rewrites, ms, lg := setupRealisticBench(b)

	for i := 0; i < b.N; i++ {
		q := make(chan []string, 1000000)

		for i := 0; i < 10; i++ {
			ss := []string{}
			for _, m := range benchMetrics {
				ss = append(ss, m)

				if len(ss) > 20 {
					q <- ss

					ss = []string{}
				}
			}
			if len(ss) > 0 {
				q <- ss
			}
		}
		close(q)

		b.StartTimer()

		done := ProcessBuff(q, rules, rewrites, nWorkers, true, false, lg, ms)
		_ = clusters.Send(done)

		<-done

		b.StopTimer()
	}
}

// This is a set of tests to investigate channel contention impact on performance.

func BenchmarkRealisticHighThroughput1(b *testing.B)  { realisticBenchHighThroughput(b, 1) }
func BenchmarkRealisticHighThroughput4(b *testing.B)  { realisticBenchHighThroughput(b, 4) }
func BenchmarkRealisticHighThroughput8(b *testing.B)  { realisticBenchHighThroughput(b, 8) }
func BenchmarkRealisticHighThroughput16(b *testing.B) { realisticBenchHighThroughput(b, 16) }
func BenchmarkRealisticHighThroughput32(b *testing.B) { realisticBenchHighThroughput(b, 32) }
func BenchmarkRealisticHighThroughput64(b *testing.B) { realisticBenchHighThroughput(b, 64) }

func realisticBenchHighThroughput(b *testing.B, nWorkers uint16) {
	b.StopTimer()

	benchMetrics, clusters, rules, rewrites, ms, lg := setupRealisticBench(b)

	for i := 0; i < b.N; i++ {
		qs := [4](chan string){
			make(chan string, 100000),
			make(chan string, 100000),
			make(chan string, 100000),
			make(chan string, 100000),
		}

		go func() {
			for i := 0; i < 10; i++ {
				j := 0
				for _, m := range benchMetrics {
					qs[j] <- m
					j++
					j %= 4
				}
			}
			close(qs[0])
			close(qs[1])
			close(qs[2])
			close(qs[3])
		}()

		b.StartTimer()

		done := ProcessHighThroughput(qs, rules, rewrites, nWorkers, true, false, lg, ms)
		_ = clusters.Send(done)

		<-done

		b.StopTimer()
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

// ProcessBuff is a test variation of main.Process
func ProcessBuff(q chan []string, rules rules.Rules, rewrites rewrites.Rewrites, workerPoolSize uint16, shouldValidate bool, shouldLog bool, lg *zap.Logger, metrics *metrics.Prom) chan struct{} {
	done := make(chan struct{})
	var wg sync.WaitGroup
	for w := 1; w <= int(workerPoolSize); w++ {
		wg.Add(1)
		go workerBuff(&wg, q, rules, rewrites, shouldValidate, shouldLog, lg, metrics)
	}
	go func() {
		wg.Wait()
		close(done)
	}()

	return done
}

// workerBuff is a test variation of main.worker
func workerBuff(wg *sync.WaitGroup, queue chan []string, rules rules.Rules, rewrites rewrites.Rewrites, shouldValidate bool, shouldLog bool, lg *zap.Logger, metrics *metrics.Prom) {
	defer wg.Done()

	for ss := range queue {
		for _, s := range ss {
			proc(s, rules, rewrites, shouldValidate, shouldLog, lg, metrics)
		}
	}
}

// ProcessHighThroughput is a test variation of main.Process
func ProcessHighThroughput(qs [4](chan string), rules rules.Rules, rewrites rewrites.Rewrites, workerPoolSize uint16, shouldValidate bool, shouldLog bool, lg *zap.Logger, metrics *metrics.Prom) chan struct{} {
	done := make(chan struct{})
	var wg sync.WaitGroup
	for w := 1; w <= int(workerPoolSize); w++ {
		wg.Add(1)
		go workerHighThroughput(&wg, qs, rules, rewrites, shouldValidate, shouldLog, lg, metrics)
	}
	go func() {
		wg.Wait()
		close(done)
	}()

	return done
}

// workerHighThroughput is a test variation of same function
func workerHighThroughput(wg *sync.WaitGroup, queue [4](chan string), rules rules.Rules, rewrites rewrites.Rewrites, shouldValidate bool, shouldLog bool, lg *zap.Logger, metrics *metrics.Prom) {
	defer wg.Done()

	ok0 := true
	ok1 := true
	ok2 := true
	ok3 := true

	for {
		var s string
		select {
		case s, ok0 = <-queue[0]:
			proc(s, rules, rewrites, shouldValidate, shouldLog, lg, metrics)
		case s, ok1 = <-queue[1]:
			proc(s, rules, rewrites, shouldValidate, shouldLog, lg, metrics)
		case s, ok2 = <-queue[2]:
			proc(s, rules, rewrites, shouldValidate, shouldLog, lg, metrics)
		case s, ok3 = <-queue[3]:
			proc(s, rules, rewrites, shouldValidate, shouldLog, lg, metrics)
		}

		if !ok0 && !ok1 && !ok2 && !ok3 {
			return
		}
	}
}
