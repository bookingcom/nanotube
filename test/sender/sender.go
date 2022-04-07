package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"

	"go.uber.org/ratelimit"
	"go.uber.org/zap"
)

func connectAndSendUDP(destination string, cycle bool, ncycles int, messages [][]byte, rate int,
	wg *sync.WaitGroup, lg *zap.Logger) {

	s := NewStats(time.Second*5, 0, lg)

	limiter := ratelimit.New(rate)

	var conn net.Conn
	var err error

	conn, err = net.Dial("udp", destination)
	if err != nil {
		log.Fatalf("could not connect to target host : %v", err)
	}

	defer func() {
		err = conn.Close()
		if err != nil {
			log.Fatalf("error closing connection: %v", err)
		}
	}()

	for i := 0; ; i++ {
		for _, message := range messages {
			limiter.Take()
			_, err := conn.Write(message)
			if err != nil {
				log.Printf("error sending data: %v", err)
			}
			s.Inc()
		}

		if !cycle {
			break
		} else {
			if ncycles != 0 && i >= ncycles-1 {
				break
			}
		}

		log.Printf("Finished passage through the file for cycle %d. Cycling...\n", i)
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

func connectAndSendTCP(destination string, retryTCP bool, cycle bool, ncycles int, messages [][]byte, rate int, rateIncrease string,
	wg *sync.WaitGroup, lg *zap.Logger) {

	conn := openTCPConnection(destination, retryTCP)

	defer func() {
		err := conn.Close()
		if err != nil {
			lg.Fatal("error closing connection", zap.Error(err))
		}
	}()

	s := NewStats(time.Second*5, 0, lg)

	if rateIncrease != "" {

		low, step, period, high, err := parseRateIncreaseFlag(rateIncrease)
		if err != nil {
			lg.Fatal("could not parse rateIncrease", zap.Error(err), zap.String("flag", rateIncrease))
		}

		lg.Info("Starting sending data with incearsing load", zap.Int("low", low), zap.Int("high", high),
			zap.Int("step", step), zap.Int("period, s", period))

		for rate = low; rate <= high; rate += step {
			startTime := time.Now()
			limiter := ratelimit.New(rate)

			done := false
			for {
				for _, message := range messages {
					if time.Since(startTime).Seconds() >= float64(period) {
						done = true
						break
					}

					limiter.Take()
					_, err := conn.Write(message)
					if err != nil {
						lg.Info("error sending data", zap.Error(err))
						err := conn.Close()
						if err != nil {
							lg.Fatal("error closing connection", zap.Error(err))
						}
						conn = openTCPConnection(destination, retryTCP)
					}
					s.Inc()
				}

				if done {
					break
				}
			}
		}
	} else {
		limiter := ratelimit.New(rate)

		for i := 0; ; i++ {
			for _, message := range messages {
				limiter.Take()
				_, err := conn.Write(message)
				if err != nil {
					log.Printf("error sending data: %v", err)
					err := conn.Close()
					if err != nil {
						log.Fatalf("error closing connection: %v", err)
					}
					conn = openTCPConnection(destination, retryTCP)
				}
				s.Inc()
			}

			if !cycle {
				break
			} else {
				if ncycles != 0 && i >= ncycles-1 {
					break
				}
			}

			log.Printf("Finished passage through the file for cycle %d. Cycling...\n", i)
		}
	}

	wg.Done()
}

func openTCPConnection(destination string, retryTCP bool) net.Conn {
	conn, err := net.Dial("tcp", destination)

	if retryTCP {
		for {
			if err != nil {
				log.Printf("could open TCP connection to NT on %s, error %v. Retrying...", destination, err)
			} else {
				break
			}
			time.Sleep(time.Second * 10)
			conn, err = net.Dial("tcp", destination)
		}
	} else {
		if err != nil {
			log.Fatalf("could not connect to target host : %v", err)
		}
	}

	return conn
}

func readPlaybackData(path string) [][]byte {
	content, err := ioutil.ReadFile(path)
	if err != nil {
		log.Fatalf("could not open file : %v", err)
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

	flag.Parse()

	lg, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("failed to create logger: %v", err)
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
	if *connections != 1 && !*cycle {
		lg.Fatal("We can use >1 connection only with the cycle option")
	}
	if *profiler != "" {
		go func() {
			log.Println(http.ListenAndServe(*profiler, nil))
		}()

	}

	data := readPlaybackData(*path)

	var wg sync.WaitGroup

	for i := 0; i < *connections; i++ {
		wg.Add(1)
		destination := net.JoinHostPort(*targetHost, strconv.Itoa(*targetPort))
		if *useUDP {
			go connectAndSendUDP(destination, *cycle, *nCycles, data, *rate, &wg, lg)
		} else {
			go connectAndSendTCP(destination, *retryTCP, *cycle, *nCycles, data, *rate, *rateIncrease, &wg, lg)
		}
	}

	wg.Wait()
}
