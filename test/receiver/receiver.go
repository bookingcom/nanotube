package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
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
	"time"

	"github.com/bookingcom/nanotube/test/stats"
)

func parsePorts(portsStr string) []int {
	var ports []int

	portStrs := strings.Fields(portsStr)
	for _, ps := range portStrs {
		ss := strings.Split(ps, "-")
		switch len(ss) {
		case 1: // single port
			p64, err := strconv.ParseUint(ss[0], 10, 32)
			if err != nil {
				log.Fatalf("could not parse port from parameters : %v", err)
			}
			ports = append(ports, int(p64))
		case 2: // ports range
			pfromUint64, err := strconv.ParseUint(ss[0], 10, 32)
			if err != nil {
				log.Fatalf("Could not parse port parameters.")
			}
			pfrom := int(pfromUint64)

			ptoUint64, err := strconv.ParseUint(ss[1], 10, 32)
			if err != nil {
				log.Fatalf("Could not parse port parameters.")
			}
			pto := int(ptoUint64)

			for i := pfrom; i <= pto; i++ {
				ports = append(ports, i)
			}
		default:
			log.Fatal("wrong ports argument")

		}
	}

	return ports
}

func main() {
	portsStr := flag.String("ports", "", `List of the ports to listen on. Has to be supplied in the from "XXXX YYYY ZZZZ AAAA-BBBB" in quotes.`)
	outPrefix := flag.String("prefix", "", "Prefix for the output files.")
	outDir := flag.String("outdir", "", "Output directory. Absolute path. Optional.")
	profiler := flag.String("profiler", "", "Where should the profiler listen?")
	exitAfter := flag.Duration("exitAfter", time.Second*10, "Exit after not receiving any message for this time. Will work only of outDir == ''. Will not exit if Duration is 0")

	flag.Parse()

	if *portsStr == "" {
		log.Fatal("please supply the ports argument")
	}

	if *profiler != "" {
		go func() {
			log.Println(http.ListenAndServe(*profiler, nil))
		}()
	}
	ports := parsePorts(*portsStr)

	fs := make(map[int]io.Writer)

	s := &stats.Stats{}
	if *outDir == "" {
		s = stats.NewStats(time.Second, *exitAfter, "receiver", func() {
			log.Printf("Exiting after %s duration of inactivity", *exitAfter)
			os.Exit(0)
		})
	}
	for _, p := range ports {
		fs[p] = ioutil.Discard
		if *outDir != "" {
			fPath := fmt.Sprintf("%s/%s%d", *outDir, *outPrefix, p)
			f, err := os.Create(fPath)
			if err != nil {
				log.Fatalf("failed to create file %s%d : %v", *outPrefix, p, err)
			}
			defer func(p int) {
				err := f.Close()
				if err != nil {
					log.Printf("could not close file for port %d : %v", p, err)
				}

			}(p)
			fs[p] = f
		}
	}

	ls := make(map[int]net.Listener)
	for _, p := range ports {
		l, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
		if err != nil {
			log.Fatalf("failed to open connection on port %d : %v", p, err)
		}
		ls[p] = l
	}

	stop := make(chan struct{})

	var portsWG sync.WaitGroup
	for _, p := range ports {
		portsWG.Add(1)

		go func(lst net.Listener, prt int, stop chan struct{}) {
			defer portsWG.Done()
			var connectionWG sync.WaitGroup
		out:
			for {
				connCh := make(chan net.Conn)
				go func() {
					conn, err := lst.Accept()
					if err != nil {
						log.Fatalf("failed to accept connection on addr %s: %v", lst.Addr(), err)
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
								log.Fatalf("connection close failed: %v", err)
							}
						}()
						scanner := bufio.NewScanner(conn)
						scanner.Buffer(make([]byte, bufio.MaxScanTokenSize*100), bufio.MaxScanTokenSize)
						if *outDir == "" {
							for scanner.Scan() {
								s.Inc()
							}
							if err := scanner.Err(); err != nil {
								log.Printf("failed reading data: %v", err)
							}
							// ignore scanner.Err()
						} else {
							nb, err := io.Copy(fs[prt], conn)
							log.Printf("wrote %d bytes to port %d", nb, prt)
							if err != nil {
								log.Printf("failed during data copy: %v", err)
							}
						}
					}(conn)
				}
			}
			connectionWG.Wait()
		}(ls[p], p, stop)
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
