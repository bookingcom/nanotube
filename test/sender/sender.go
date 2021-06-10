package main

import (
	"flag"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bookingcom/nanotube/test/stats"

	"go.uber.org/ratelimit"
)

func connectAndSendUDP(destination string, cycle bool, ncycles int, messages [][]byte, limiter ratelimit.Limiter,
	wg *sync.WaitGroup, s *stats.Stats) {

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

func connectAndSendTCP(destination string, retryTCP bool, cycle bool, ncycles int, messages [][]byte, limiter ratelimit.Limiter,
	wg *sync.WaitGroup, s *stats.Stats) {

	conn := openTCPConnection(destination, retryTCP)

	defer func() {
		err := conn.Close()
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
				conn.Close()
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

	flag.Parse()

	if path == nil || *path == "" {
		log.Fatal("please, supply path to data file")
	}
	if targetHost == nil || *targetHost == "" {
		log.Fatal("please supply target hostname")
	}
	if targetPort == nil || *targetPort == 0 {
		log.Fatal("please supply target port")
	}
	if *connections != 1 && !*cycle {
		log.Fatal("We can use >1 connection only with the cycle option")
	}
	if *profiler != "" {
		go func() {
			log.Println(http.ListenAndServe(*profiler, nil))
		}()

	}

	content, err := ioutil.ReadFile(*path)
	if err != nil {
		log.Fatalf("could not open file : %v", err)
	}
	messageStrings := strings.SplitAfter(string(content), "\n")
	var messages [][]byte
	for _, m := range messageStrings {
		messages = append(messages, []byte(m))
	}
	s := stats.NewStats(time.Second, 0, func() {})
	limiter := ratelimit.NewUnlimited()
	if *rate != 0 {
		limiter = ratelimit.New(*rate)
	}
	var wg sync.WaitGroup

	for i := 0; i < *connections; i++ {
		wg.Add(1)
		destination := net.JoinHostPort(*targetHost, strconv.Itoa(*targetPort))
		if *useUDP {
			go connectAndSendUDP(destination, *cycle, *nCycles, messages, limiter, &wg, s)
		} else {
			go connectAndSendTCP(destination, *retryTCP, *cycle, *nCycles, messages, limiter, &wg, s)
		}
	}

	wg.Wait()
}
