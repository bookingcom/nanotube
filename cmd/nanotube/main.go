package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"nanotube/pkg/conf"
	"nanotube/pkg/metrics"
	"nanotube/pkg/rules"
	"nanotube/pkg/target"
	"os"
	"os/signal"
	"syscall"
	"time"

	"net/http"
	_ "net/http/pprof"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

var version string

func main() {
	config := zap.NewProductionConfig()
	config.OutputPaths = []string{"stdout"}
	lg, err := config.Build()
	if err != nil {
		log.Fatalf("error building logger config: %v", err)
	}
	defer func() {
		err := lg.Sync()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error when syncing logger: %v", err)
		}
	}()

	cfgPath, clPath, rulesPath, validateConfig, versionInfo := parseFlags()

	if versionInfo {
		fmt.Println(version)
		return
	}

	cfg, clusters, rules, ms := loadBuildRegister(cfgPath, clPath, rulesPath, lg)

	if validateConfig {
		return
	}

	go func() {
		err := http.ListenAndServe( // shadow
			fmt.Sprintf("localhost:%d", cfg.PprofPort), nil)
		if err != nil {
			lg.Error("pprof server failed", zap.Error(err))
		}
	}()

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
	procDone := Process(queue, rules, cfg.WorkerPoolSize, cfg.NormalizeRecords, cfg.LogSpecialRecords, lg, ms)
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

func parseFlags() (string, string, string, bool, bool) {
	cfgPath := flag.String("config", "", "Path to config file.")
	clPath := flag.String("clusters", "", "Path to clusters file.")
	rulesPath := flag.String("rules", "", "Path to rules file.")
	testConfig := flag.Bool("validate", false, "Validate configuration files.")
	versionInfo := flag.Bool("version", false, "Print version info.")

	flag.Parse()

	// if --version is specified, only print the version, nothing else matters
	if *versionInfo {
		return *cfgPath, *clPath, *rulesPath, *testConfig, true
	}

	if cfgPath == nil || *cfgPath == "" {
		log.Fatal("config file path not specified")
	}
	if clPath == nil || *clPath == "" {
		log.Fatal("clusters file not specified")
	}
	if rulesPath == nil || *rulesPath == "" {
		log.Fatal("rules file not specified")
	}

	return *cfgPath, *clPath, *rulesPath, *testConfig, false
}

func loadBuildRegister(cfgPath, clPath, rulesPath string,
	lg *zap.Logger) (conf.Main, target.Clusters, rules.Rules, *metrics.Prom) {

	bs, err := ioutil.ReadFile(cfgPath)
	if err != nil {
		log.Fatalf("error reading config file: %v", err)
	}
	cfg, err := conf.ReadMain(bytes.NewReader(bs))
	if err != nil {
		log.Fatalf("error reading main config: %v", err)
	}

	ms := metrics.New(&cfg)
	metrics.Register(ms)
	ms.Version.WithLabelValues(version).Inc()

	bs, err = ioutil.ReadFile(clPath)
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

	bs, err = ioutil.ReadFile(rulesPath)
	if err != nil {
		log.Fatalf("error reading rules file: %v", err)
	}
	rulesConf, err := conf.ReadRules(bytes.NewReader(bs))
	if err != nil {
		log.Fatalf("error reading rules config: %v", err)
	}
	rules, err := rules.Build(rulesConf, clusters)
	if err != nil {
		log.Fatalf("error while compiling rules: %v", err)
	}

	return cfg, clusters, rules, ms
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
