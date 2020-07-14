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

func connectAndSend(destination string, useUDP bool, cycle bool, ncycles int, messages [][]byte, limiter ratelimit.Limiter,
	wg *sync.WaitGroup, s *stats.Stats) {

	var conn net.Conn
	var err error

	if useUDP {
		conn, err = net.Dial("udp", destination)
	} else {
		conn, err = net.Dial("tcp", destination)
	}

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
				log.Fatalf("error sending data: %v", err)
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

func main() {
	path := flag.String("data", "", "path to records file to be sent to the relay")
	targetHost := flag.String("host", "", "target hostname")
	targetPort := flag.Int("port", 0, "target port")
	useUDP := flag.Bool("udp", false, "use UDP instead of TCP? Default - false.")
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
	s := stats.NewStats(time.Second, 0, "sender", func() {})
	limiter := ratelimit.NewUnlimited()
	if *rate != 0 {
		limiter = ratelimit.New(*rate)
	}
	var wg sync.WaitGroup
	destination := net.JoinHostPort(*targetHost, strconv.Itoa(*targetPort))
	for i := 0; i < *connections; i++ {
		wg.Add(1)
		go connectAndSend(destination, *useUDP, *cycle, *nCycles, messages, limiter, &wg, s)
	}

	wg.Wait()
}
