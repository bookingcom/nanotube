package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

func parsePorts(portsStr string, lg *zap.Logger) []int {
	var ports []int

	portStrs := strings.Fields(portsStr)
	for _, ps := range portStrs {
		ss := strings.Split(ps, "-")
		switch len(ss) {
		case 1: // single port
			p64, err := strconv.ParseUint(ss[0], 10, 32)
			if err != nil {
				lg.Fatal("could not parse port from parameters", zap.Error(err))
			}
			ports = append(ports, int(p64))
		case 2: // ports range
			pfromUint64, err := strconv.ParseUint(ss[0], 10, 32)
			if err != nil {
				lg.Fatal("could not parse port parameters", zap.Error(err))
			}
			pfrom := int(pfromUint64)

			ptoUint64, err := strconv.ParseUint(ss[1], 10, 32)
			if err != nil {
				lg.Fatal("could not parse port parameters", zap.Error(err))
			}
			pto := int(ptoUint64)

			for i := pfrom; i <= pto; i++ {
				ports = append(ports, i)
			}
		default:
			lg.Fatal("invalid ports argument")
		}
	}

	return ports
}

type metrics struct {
	inRecs          prometheus.Counter
	timeOfLastWrite prometheus.Gauge
	nOpenPorts      prometheus.Gauge
}

func setupMetrics(lg *zap.Logger) *metrics {
	ns := "receiver"
	ms := metrics{
		inRecs: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: ns,
			Name:      "in_records_total",
			Help:      "Incoming records counter.",
		}),
		timeOfLastWrite: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: ns,
			Name:      "time_of_last_write",
			Help:      "Time of last write to the port dump file.",
		}),
		nOpenPorts: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: ns,
			Name:      "n_open_ports",
			Help:      "Number of opened ports.",
		}),
	}
	err := prometheus.Register(ms.inRecs)
	if err != nil {
		lg.Fatal("error registering the metric", zap.String("metric", "in_records_total"),
			zap.Error(err))
	}
	err = prometheus.Register(ms.timeOfLastWrite)
	if err != nil {
		lg.Fatal("error registering the metric", zap.String("metric", "time_of_last_write"),
			zap.Error(err))
	}
	err = prometheus.Register(ms.nOpenPorts)
	if err != nil {
		lg.Fatal("error registering the metric", zap.String("metric", "n_open_ports"),
			zap.Error(err))
	}

	return &ms
}

func openFiles(outDir string, outPrefix string, ports []int, lg *zap.Logger) map[int]*os.File {
	fs := make(map[int]*os.File)

	for _, p := range ports {
		fPath := fmt.Sprintf("%s/%s%d", outDir, outPrefix, p)
		f, err := os.Create(fPath)
		if err != nil {
			lg.Fatal("failed to create file", zap.String("path", fPath), zap.Error(err))
		}
		fs[p] = f
	}

	return fs
}

func closeFiles(fs map[int]*os.File, lg *zap.Logger) {
	for p, f := range fs {
		err := f.Close()
		if err != nil {
			lg.Error("could not close file for port", zap.Int("port", p), zap.Error(err))
		}
	}
}

func main() {
	portsStr := flag.String("ports", "", `List of the ports to listen on. Has to be supplied in the from "XXXX YYYY ZZZZ AAAA-BBBB" in quotes.`)
	outPrefix := flag.String("prefix", "", "Prefix for the output files.")
	outDir := flag.String("outdir", "", "Output directory. Absolute path. Optional.")
	profPort := flag.String("profPort", "", "Where should the profiler listen?")
	promPort := flag.Int("promPort", 0, "Prometheus port. If unset, Prometheus metrics are not exposed.")

	flag.Parse()

	lg, err := zap.NewProduction()
	if err != nil {
		log.Fatal("failed to create logger: ", err)
	}

	ms := setupMetrics(lg)

	if *promPort != 0 {
		go promListen(*promPort, lg)
	}

	if *profPort != "" {
		go func() {
			lg.Info("profiler server exited", zap.Error(http.ListenAndServe(*profPort, nil)))
		}()
	}

	ports := parsePorts(*portsStr, lg)
	fs := openFiles(*outDir, *outPrefix, ports, lg)
	defer closeFiles(fs, lg)
	ls := openPorts(ports, lg)

	ms.nOpenPorts.Set(float64(len(ls)))

	stop := make(chan struct{})

	var portsWG sync.WaitGroup
	for _, p := range ports {
		portsWG.Add(1)

		go listen(ls[p], p, *outDir, stop, &portsWG, fs, ms, lg)
	}

	sgn := make(chan os.Signal, 1)
	signal.Notify(sgn, os.Interrupt, syscall.SIGTERM)
	<-sgn

	// start termination sequence
	close(stop)

	if *outDir == "" {
		os.Exit(0)
	}
	portsWG.Wait()
}

func promListen(promPort int, lg *zap.Logger) {
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", promPort))
	if err != nil {
		lg.Error("opening TCP port for Prometheus failed", zap.Error(err))
	}
	err = http.Serve(l, promhttp.Handler())
	if err != nil {
		lg.Error("prometheus server failed", zap.Error(err))
	}
}

func openPorts(ports []int, lg *zap.Logger) map[int]net.Listener {
	ls := make(map[int]net.Listener)
	for _, p := range ports {
		l, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
		if err != nil {
			lg.Fatal("failed to open connection on port", zap.Int("port", p), zap.Error(err))
		}
		ls[p] = l
	}

	return ls
}

func listen(lst net.Listener, prt int, outDir string, stop chan struct{}, portsWG *sync.WaitGroup, fs map[int]*os.File, ms *metrics, lg *zap.Logger) {
	defer portsWG.Done()
	var connectionWG sync.WaitGroup
out:
	for {
		connCh := make(chan net.Conn)
		go func() {
			conn, err := lst.Accept()
			if err != nil {
				lg.Fatal("failed to accept connection on addr %s: %v", zap.String("address", lst.Addr().String()), zap.Error(err))
			}
			connCh <- conn
		}()

		select {
		case <-stop:
			break out
		case conn := <-connCh:
			connectionWG.Add(1)

			go func(conn net.Conn) {
				defer connectionWG.Done()
				defer func() {
					err := conn.Close()
					if err != nil {
						lg.Fatal("connection close failed", zap.Error(err))
					}
				}()
				if outDir == "" {
					scanner := bufio.NewScanner(conn)
					scanner.Buffer(make([]byte, bufio.MaxScanTokenSize*100), bufio.MaxScanTokenSize)
					for scanner.Scan() {
						ms.inRecs.Inc()
					}
					if err := scanner.Err(); err != nil {
						lg.Info("failed scan when reading data", zap.Error(err))
					}
				} else {
					_, err := io.Copy(fs[prt], conn)
					if err != nil {
						lg.Error("failed when dumping data", zap.Error(err))
					}
					ms.timeOfLastWrite.SetToCurrentTime()
				}
			}(conn)
		}
	}
	connectionWG.Wait()
}
