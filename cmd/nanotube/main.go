package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/ratelimiter"
	"github.com/bookingcom/nanotube/pkg/rewrites"
	"github.com/bookingcom/nanotube/pkg/rules"
	"github.com/bookingcom/nanotube/pkg/target"
	"github.com/facebookgo/pidfile"
	"github.com/pkg/errors"

	"github.com/facebookgo/grace/gracenet"
	"github.com/libp2p/go-reuseport"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var version string

func main() {
	cfgPath, validateConfig, versionInfo := parseFlags()

	if versionInfo {
		fmt.Println(version)
		return
	}

	cfg, clustersConf, rulesConf, rewritesConf, hash, clustersHash, err := readConfigs(cfgPath)
	if err != nil {
		log.Fatalf("error reading and compiling config: %v", err)
	}

	if validateConfig {
		return
	}

	if cfg.PidFilePath != "" {
		pidfile.SetPidfilePath(cfg.PidFilePath)
		err = pidfile.Write()
		if err != nil {
			log.Fatalf("error writing pidfile: %v", err)
		}
	}

	lg, err := buildLogger(&cfg)
	if err != nil {
		log.Fatalf("error building logger config: %v", err)
	}

	ms := metrics.New(&cfg)
	metrics.Register(ms, &cfg)

	lg.Info("GOMAXPROCS value", zap.Int("GOMAXPROCS", runtime.GOMAXPROCS(0)))

	clusters, rules, rewrites, err := buildPipeline(&cfg, &clustersConf, &rulesConf, rewritesConf, ms, lg)
	if err != nil {
		log.Fatalf("error building pipline components: %v", err)
	}

	ms.Version.WithLabelValues(version).Inc()
	ms.ConfVersion.WithLabelValues(hash).Inc()
	ms.ClustersVersion.WithLabelValues(clustersHash).Inc()

	if cfg.PprofPort != -1 {
		go func() {
			l, err := reuseport.Listen("tcp", net.JoinHostPort("localhost", strconv.Itoa(cfg.PprofPort)))
			if err != nil {
				lg.Error("opening TCP port for pprof failed", zap.Error(err))
			}

			err = http.Serve(l, nil)
			if err != nil {
				lg.Error("pprof server failed", zap.Error(err))
			}
		}()
	}

	go func() {
		l, err := reuseport.Listen("tcp", fmt.Sprintf(":%d", cfg.PromPort))
		if err != nil {
			lg.Error("opening TCP port for Prometheus failed", zap.Error(err))
		}
		err = http.Serve(l, http.AllowQuerySemicolons(promhttp.Handler()))
		if err != nil {
			lg.Error("Prometheus server failed", zap.Error(err))
		}
	}()

	stop := make(chan struct{})

	var rateLimiters []*ratelimiter.SlidingWindow
	if cfg.RateLimiterGlobalRecordLimit > 0 {
		windowSize := time.Duration(cfg.RateLimiterWindowSizeSec) * time.Second
		globalRateLimiter := ratelimiter.NewSlidingWindowRateLimiter(windowSize,
			int64(cfg.RateLimiterGlobalRecordLimit), ms.GlobalRateLimiterBlockedReaders)
		rateLimiters = append(rateLimiters, globalRateLimiter)
	}
	n := gracenet.Net{}
	queue, err := Listen(&n, rateLimiters, &cfg, stop, lg, ms)
	if err != nil {
		log.Fatalf("error launching listener, %v", err)
	}

	procDone := ProcessBuf(queue, rules, rewrites, cfg.WorkerPoolSize, cfg.NormalizeRecords, cfg.LogSpecialRecords, lg, ms)
	done := clusters.Send(procDone)

	// SIGTERM gracefully terminates with timeout
	// SIGKILL terminates immediately
	// SIGUSR2 triggers zero-downtime restart
	sgn := make(chan os.Signal, 1)
	signal.Notify(sgn, os.Interrupt, syscall.SIGTERM, syscall.SIGUSR2)

	// TODO: Make seamless restart work for GRPC
	for {
		s := <-sgn

		if s == syscall.SIGUSR2 {
			lg.Info("Reload: Got signal for reload. Checking config.")
			_, _, _, _, _, _, err = readConfigs(cfgPath)
			if err != nil {
				lg.Error("Reload: Cannot reload: config invalid", zap.Error(err))
				continue
			} else {
				lg.Info("Reload: Config OK. Starting a new instance.")
			}
			pid, err := n.StartProcess()
			if err != nil {
				lg.Error("Reload: Failed to start new process", zap.Error(err))
			} else {
				lg.Info("Reload: Started new process. Moved FDs.", zap.Int("pid", pid))
			}
			lg.Info("Termination: Staring termination sequence.")
			close(stop)
		} else {
			lg.Info("Termination: Staring termination sequence.")
			close(stop)
		}

		break
	}

	select {
	case <-time.After(time.Second * time.Duration(cfg.TermTimeoutSec)):
		log.Fatalf("Termination: Force quit due to timeout. Queue not fully flushed.")
	case <-done:
		lg.Info("Terminated gracefully.")
	}
}

func parseFlags() (string, bool, bool) {
	cfgPath := flag.String("config", "", "Path to config file.")
	testConfig := flag.Bool("validate", false, "Validate configuration files.")
	versionInfo := flag.Bool("version", false, "Print version info.")

	flag.Parse()

	// if --version is specified, only print the version, nothing else matters
	if *versionInfo {
		return *cfgPath, *testConfig, true
	}

	if cfgPath == nil || *cfgPath == "" {
		log.Fatal("config file path not specified")
	}

	return *cfgPath, *testConfig, false
}

func buildLogger(cfg *conf.Main) (*zap.Logger, error) {
	config := zap.NewProductionConfig()
	config.OutputPaths = []string{"stdout"}

	config.Sampling = nil // make sure there is no sampler since we will add one by ourselves
	return config.Build(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
		return zapcore.NewSamplerWithOptions(core, time.Second*time.Duration(cfg.LogLimitWindowSec), cfg.LogLimitInitial, cfg.LogLimitThereafter)
	}))
}

func readConfigs(cfgPath string) (cfg conf.Main, clustersConf conf.Clusters, rulesConf conf.Rules, rewritesConf *conf.Rewrites, hash string, clustersHash string, retErr error) {
	bs, err := ioutil.ReadFile(cfgPath)
	if err != nil {
		retErr = errors.Wrap(err, "error reading config file")
		return
	}
	cfg, err = conf.ReadMain(bytes.NewReader(bs))
	if err != nil {
		retErr = errors.Wrap(err, "error reading main config")
		return
	}

	bs, err = ioutil.ReadFile(cfg.ClustersConfig)
	if err != nil {
		retErr = errors.Wrap(err, "error reading clusters file")
		return
	}
	clustersConf, err = conf.ReadClustersConfig(bytes.NewReader(bs))
	if err != nil {
		retErr = errors.Wrap(err, "error reading clusters config")
		return
	}

	bs, err = ioutil.ReadFile(cfg.RulesConfig)
	if err != nil {
		retErr = errors.Wrap(err, "error reading rules file")
		return
	}
	rulesConf, err = conf.ReadRules(bytes.NewReader(bs))
	if err != nil {
		retErr = errors.Wrap(err, "error reading rules config")
		return
	}

	rewritesConf = nil
	if cfg.RewritesConfig != "" {
		bs, err := ioutil.ReadFile(cfg.RewritesConfig)
		if err != nil {
			retErr = errors.Wrap(err, "error reading rewrites config")
			return
		}
		rewritesConfVal, err := conf.ReadRewrites(bytes.NewReader(bs))
		if err != nil {
			retErr = errors.Wrap(err, "error reading rewrites config")
			return
		}
		rewritesConf = &rewritesConfVal
	}

	hash, err = conf.Hash(&cfg, &clustersConf, &rulesConf, rewritesConf)
	if err != nil {
		retErr = fmt.Errorf("error calculating hash config: %w", err)
	}

	clustersHash, err = conf.ClustersHash(&clustersConf)
	if err != nil {
		retErr = fmt.Errorf("error calculating clusters config hash: %w", err)
	}

	return
}

func buildPipeline(cfg *conf.Main, clustersConf *conf.Clusters, rulesConf *conf.Rules, rewritesConf *conf.Rewrites,
	ms *metrics.Prom, lg *zap.Logger) (clusters target.Clusters, rls rules.Rules, rewriteRules rewrites.Rewrites, retErr error) {

	clusters, err := target.NewClusters(cfg, clustersConf, lg, ms)
	if err != nil {
		retErr = errors.Wrap(err, "error building clusters")
		return
	}

	rls, err = rules.Build(rulesConf, clusters, cfg.RegexDurationMetric, ms)
	if err != nil {
		retErr = errors.Wrap(err, "error while compiling rules")
		return
	}

	if rewritesConf != nil {
		rewriteRules, err = rewrites.Build(rewritesConf, cfg.RegexDurationMetric, ms)
		if err != nil {
			retErr = errors.Wrap(err, "error while building rewrites")
			return
		}
	}

	return
}
