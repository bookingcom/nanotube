package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"go.uber.org/ratelimit"
	"go.uber.org/zap"
)

type metrics struct {
	outRecs prometheus.Counter
}

func setupMetrics() *metrics {
	ms := metrics{
		outRecs: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sender",
			Name:      "out_records_total",
			Help:      "Outgoing records counter.",
		}),
	}
	err := prometheus.Register(ms.outRecs)
	if err != nil {
		log.Fatalf("error registering the in_records_total metric: %v", err)
	}

	return &ms
}

func connectAndSendUDP(destination string, cycle bool, cycles int, messages [][]byte, rate int,
	wg *sync.WaitGroup, ms *metrics, lg *zap.Logger) {

	limiter := ratelimit.New(rate)

	var conn net.Conn
	var err error

	conn, err = net.Dial("udp", destination)
	if err != nil {
		lg.Fatal("could not connect to target host", zap.Error(err))
	}

	defer func() {
		err = conn.Close()
		if err != nil {
			lg.Fatal("error closing connection", zap.Error(err))
		}
	}()

	for i := 0; ; i++ {
		for _, message := range messages {
			limiter.Take()
			_, err := conn.Write(message)
			if err != nil {
				lg.Error("error sending data", zap.Error(err))
			}
			ms.outRecs.Inc()
		}

		if !cycle {
			break
		}

		if cycles != 0 && i >= cycles-1 {
			break
		}
	}

	wg.Done()
}

func parseRateIncreaseFlag(rateIncrease string) (low int, step int, period int, high int, retError error) {
	parts := strings.Split(rateIncrease, ":")
	if len(parts) != 4 {
		retError = fmt.Errorf("error parsing rateIncrease flag. Got %s", rateIncrease)
		return
	}

	low, err := strconv.Atoi(parts[0])
	if err != nil {
		retError = errors.Wrapf(err, "failed to convert low value to int: %s", parts[0])
		return
	}
	step, err = strconv.Atoi(parts[1])
	if err != nil {
		retError = errors.Wrapf(err, "failed to convert step value to int: %s", parts[1])
		return
	}
	period, err = strconv.Atoi(parts[2])
	if err != nil {
		retError = errors.Wrapf(err, "failed to convert period value to int: %s", parts[2])
		return
	}
	high, err = strconv.Atoi(parts[3])
	if err != nil {
		retError = errors.Wrapf(err, "failed to convert high value to int: %s", parts[3])
		return
	}

	return
}

func connectAndSendTCP(destination string, retryTCP bool, TCPBufSize int, cycle bool, ncycles int, messages [][]byte, rate int, rateIncrease string,
	wg *sync.WaitGroup, ms *metrics, lg *zap.Logger) {

	conn := openTCPConnection(destination, retryTCP, lg)
	bufConn := bufio.NewWriterSize(conn, TCPBufSize)

	defer func() {
		err := conn.Close()
		if err != nil {
			lg.Fatal("error closing connection", zap.Error(err))
		}
	}()

	if rateIncrease != "" {

		low, step, period, high, err := parseRateIncreaseFlag(rateIncrease)
		if err != nil {
			lg.Fatal("could not parse rateIncrease", zap.Error(err), zap.String("flag", rateIncrease))
		}

		lg.Info("Starting sending data with incearsing load", zap.Int("low", low), zap.Int("high", high),
			zap.Int("step", step), zap.Int("period, s", period))

		for rate = low; rate <= high; rate += step {
			startTime := time.Now()
			limiter := ratelimit.New(rate / 100)

			done := false
			for {
				i := 0
				for _, message := range messages {
					if time.Since(startTime).Seconds() >= float64(period) {
						done = true
						break
					}

					if i%100 == 0 {
						limiter.Take()
					}
					_, err := bufConn.Write(message)
					if err != nil {
						lg.Error("error sending data", zap.Error(err))
						err := conn.Close()
						if err != nil {
							lg.Fatal("error closing connection", zap.Error(err))
						}
						conn = openTCPConnection(destination, retryTCP, lg)
						bufConn = bufio.NewWriterSize(conn, TCPBufSize)
					}
					ms.outRecs.Inc()
					i++
				}

				if done {
					break
				}
			}
		}
	} else {
		limiter := ratelimit.New(rate / 100)

		for i := 0; ; i++ {
			j := 0
			for _, message := range messages {
				if j%100 == 0 {
					limiter.Take()
				}
				j++

				_, err := bufConn.Write(message)
				if err != nil {
					lg.Error("error sending data", zap.Error(err))
					err := conn.Close()
					if err != nil {
						lg.Fatal("error closing connection", zap.Error(err))
					}
					conn = openTCPConnection(destination, retryTCP, lg)
					bufConn = bufio.NewWriterSize(conn, TCPBufSize)
				}
				ms.outRecs.Inc()
			}
			err := bufConn.Flush()
			if err != nil {
				lg.Error("could not flush connection buffer", zap.String("destination", destination), zap.Error(err))
			}

			if !cycle {
				break
			}

			if ncycles != 0 && i >= ncycles-1 {
				break
			}
		}
	}

	wg.Done()
}

func openTCPConnection(destination string, retryTCP bool, lg *zap.Logger) net.Conn {
	conn, err := net.Dial("tcp", destination)

	if retryTCP {
		for {
			if err != nil {
				lg.Warn("could open TCP connection to NT. Retrying...", zap.String("destination", destination), zap.Error(err))
			} else {
				break
			}
			time.Sleep(time.Second)
			conn, err = net.Dial("tcp", destination)
		}
	} else {
		if err != nil {
			lg.Fatal("could not connect to target host", zap.Error(err))
		}
	}

	return conn
}

func readPlaybackData(path string, lg *zap.Logger) [][]byte {
	content, err := os.ReadFile(path)
	if err != nil {
		lg.Fatal("could not open file", zap.Error(err))
	}
	messageStrings := strings.SplitAfter(string(content), "\n")
	var messages [][]byte
	for _, m := range messageStrings {
		messages = append(messages, []byte(m))
	}

	return messages
}

func main() {
	path := flag.String("data", "", "path to records file to be sent to the relay")
	targetHost := flag.String("host", "", "target hostname")
	targetPort := flag.Int("port", 0, "target port")
	useUDP := flag.Bool("udp", false, "use UDP instead of TCP? Default - false.")
	retryTCP := flag.Bool("retryTCP", false, "retry connection for TCP - do not fail if connection fails")
	rate := flag.Int("rate", 1000, "rate to send messages(number/sec)")
	cycle := flag.Bool("cycle", false, "cycle through the traffic file indefinitely?")
	nCycles := flag.Int("ncycles", 0, "number of cycles over the data for each connection. 0 by default. 0 means infinity.")
	connections := flag.Int("connections", 1, "number of concurrent connections")
	profiler := flag.String("profiler", "", "Where should the profiler listen to?")
	rateIncrease := flag.String("gradualLoadIncerase", "", "Has the form low:step:period:high. Rate starts at *low* and increases by *step* every *period* seconds until it reaches *high*. Only works for TCP.")
	promPort := flag.Int("promPort", 0, "Prometheus server port. If 0, no server is started. Default is 0.")
	TCPBufSize := flag.Int("TCPBufSize", 0, "Size of TCP buffer. 0 by default.")

	flag.Parse()

	lg, err := zap.NewProduction()
	if err != nil {
		lg.Fatal("failed to create logger", zap.Error(err))
	}

	if path == nil || *path == "" {
		lg.Fatal("please, supply path to data file")
	}
	if targetHost == nil || *targetHost == "" {
		lg.Fatal("please supply target hostname")
	}
	if targetPort == nil || *targetPort == 0 {
		lg.Fatal("please supply target port")
	}
	if *connections != 1 && !*cycle && *rateIncrease == "" {
		lg.Fatal("We can use >1 connection only with the cycle option")
	}
	if *rate < 100 {
		lg.Fatal("rate too low, minimal rate 100", zap.Int("rate", *rate))
	}

	if *profiler != "" {
		go func() {
			lg.Info("pprof server exit status", zap.Error(http.ListenAndServe(*profiler, nil)))
		}()

	}

	ms := setupMetrics()

	if *promPort != 0 {
		go func() {
			l, err := net.Listen("tcp", fmt.Sprintf(":%d", *promPort))
			if err != nil {
				lg.Error("opening TCP port for Prometheus failed", zap.Error(err))
			}
			err = http.Serve(l, promhttp.Handler())
			if err != nil {
				lg.Error("prometheus server failed", zap.Error(err))
			}
		}()
	}

	data := readPlaybackData(*path, lg)

	var wg sync.WaitGroup

	for i := 0; i < *connections; i++ {
		wg.Add(1)
		destination := net.JoinHostPort(*targetHost, strconv.Itoa(*targetPort))
		if *useUDP {
			go connectAndSendUDP(destination, *cycle, *nCycles, data, *rate, &wg, ms, lg)
		} else {
			go connectAndSendTCP(destination, *retryTCP, *TCPBufSize, *cycle, *nCycles, data, *rate, *rateIncrease, &wg, ms, lg)
		}
	}

	wg.Wait()
}
