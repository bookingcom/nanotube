package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rewrites"
	"github.com/bookingcom/nanotube/pkg/rules"
	"github.com/bookingcom/nanotube/pkg/target"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

var version string

func main() {
	lg, err := buildLogger()
	if err != nil {
		log.Fatalf("error building logger config: %v", err)
	}
	defer func() {
		err := lg.Sync()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error when syncing logger: %v", err)
		}
	}()

	cfgPath, _, _, _, validateConfig, versionInfo := parseFlags()

	if versionInfo {
		fmt.Println(version)
		return
	}

	cfg, clusters, rules, rewrites, ms := loadBuildRegister(cfgPath, lg)

	if validateConfig {
		return
	}

	if cfg.PprofPort != -1 {
		go func() {
			err := http.ListenAndServe( // shadow
				fmt.Sprintf("localhost:%d", cfg.PprofPort), nil)
			if err != nil {
				lg.Error("pprof server failed", zap.Error(err))
			}
		}()
	}

	go func() {
		err := http.ListenAndServe( // shadow
			fmt.Sprintf(":%d", cfg.PromPort), promhttp.Handler())
		if err != nil {
			lg.Error("Prometheus server failed", zap.Error(err))
		}
	}()

	stop := make(chan struct{})
	queue, err := Listen(&cfg, stop, lg, ms)
	if err != nil {
		log.Fatalf("error launching listener, %v", err)
	}

	gaugeQueues(queue, clusters, ms)
	procDone := Process(queue, rules, rewrites, cfg.WorkerPoolSize, cfg.NormalizeRecords, cfg.LogSpecialRecords, lg, ms)
	done := clusters.Send(procDone)

	// SIGTERM gracefully terminates with timeout
	// SIGKILL terminates immediately
	sgn := make(chan os.Signal, 1)
	signal.Notify(sgn, os.Interrupt, syscall.SIGTERM)
	<-sgn

	// start termination sequence
	close(stop)

	select {
	case <-time.After(time.Second * time.Duration(cfg.TermTimeoutSec)):
		err = lg.Sync()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error when syncing logger: %v", err)
		}

		log.Fatalf("force quit. Queue not fully flushed")
	case <-done:
	}
}

func parseFlags() (string, string, string, string, bool, bool) {
	// TODO (grzkv): Cleanup unused clPath, rulesPath, rewritesPath after migration
	cfgPath := flag.String("config", "", "Path to config file.")
	clPath := flag.String("clusters", "", "Path to clusters file.")
	rulesPath := flag.String("rules", "", "Path to rules file.")
	rewritesPath := flag.String("rewrites", "", "Path to rewrites file.")
	testConfig := flag.Bool("validate", false, "Validate configuration files.")
	versionInfo := flag.Bool("version", false, "Print version info.")

	flag.Parse()

	// if --version is specified, only print the version, nothing else matters
	if *versionInfo {
		return *cfgPath, *clPath, *rulesPath, *rewritesPath, *testConfig, true
	}

	if cfgPath == nil || *cfgPath == "" {
		log.Fatal("config file path not specified")
	}

	return *cfgPath, *clPath, *rulesPath, *rewritesPath, *testConfig, false
}

func buildLogger() (*zap.Logger, error) {
	config := zap.NewProductionConfig()
	config.OutputPaths = []string{"stdout"}
	// secondly limit the number of entries with the same level and message to `Initial`,
	// after that log every `Thereafter`s message.
	config.Sampling = &zap.SamplingConfig{
		Initial:    10,
		Thereafter: 1000,
	}
	return config.Build()
}

func loadBuildRegister(cfgPath string, lg *zap.Logger) (conf.Main, target.Clusters,
	rules.Rules, rewrites.Rewrites, *metrics.Prom) {

	bs, err := ioutil.ReadFile(cfgPath)
	if err != nil {
		log.Fatalf("error reading config file: %v", err)
	}
	cfg, err := conf.ReadMain(bytes.NewReader(bs))
	if err != nil {
		log.Fatalf("error reading main config: %v", err)
	}

	ms := metrics.New(&cfg)
	metrics.Register(ms, &cfg)
	ms.Version.WithLabelValues(version).Inc()

	bs, err = ioutil.ReadFile(cfg.ClustersConfig)
	if err != nil {
		log.Fatal("error reading clusters file")
	}
	clustersConf, err := conf.ReadClustersConfig(bytes.NewReader(bs))
	if err != nil {
		log.Fatalf("error reading clusters config: %v", err)
	}
	clusters, err := target.NewClusters(cfg, clustersConf, lg, ms)
	if err != nil {
		log.Fatalf("error building clusters")
	}

	bs, err = ioutil.ReadFile(cfg.RulesConfig)
	if err != nil {
		log.Fatalf("error reading rules file: %v", err)
	}
	rulesConf, err := conf.ReadRules(bytes.NewReader(bs))
	if err != nil {
		log.Fatalf("error reading rules config: %v", err)
	}
	rules, err := rules.Build(rulesConf, clusters, cfg.RegexDurationMetric, ms)
	if err != nil {
		log.Fatalf("error while compiling rules: %v", err)
	}

	var rewriteRules rewrites.Rewrites
	if cfg.RewritesConfig != "" {
		bs, err := ioutil.ReadFile(cfg.RewritesConfig)
		if err != nil {
			log.Fatalf("error reading rewrites config: %v", err)
		}
		rewritesConf, err := conf.ReadRewrites(bytes.NewReader(bs))
		if err != nil {
			log.Fatalf("error reading rewrites config: %v", err)
		}
		rewriteRules, err = rewrites.Build(rewritesConf, cfg.RegexDurationMetric, ms)
		if err != nil {
			log.Fatalf("error while building rewrites: %v", err)
		}
	}

	return cfg, clusters, rules, rewriteRules, ms
}

// gaugeQueue starts and maintains a goroutine to measure the main queue size.
func gaugeQueues(queue chan string, clusters target.Clusters, metrics *metrics.Prom) {
	queueGaugeIntervalMs := 1000

	ticker := time.NewTicker(time.Duration(queueGaugeIntervalMs) * time.Millisecond)
	go func() {
		for range ticker.C {
			metrics.MainQueueLength.Set(float64(len(queue)))
			for _, cl := range clusters {
				for _, h := range cl.Hosts {
					metrics.HostQueueLength.Observe(float64(len(h.Ch)))
				}
			}
		}
	}()
}
