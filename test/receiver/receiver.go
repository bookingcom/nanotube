package main

import (
	"bufio"
	"encoding/json"
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

	"go.uber.org/zap"
)

type status struct {
	sync.Mutex
	Ready         bool
	dataProcessed bool

	timestampLastProcessed time.Time
	IdleTimeMilliSecs      int64
}

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
			lg.Fatal("wrong ports argument")

		}
	}

	return ports
}

func setupStatusServer(localAPIPort int, currentStatus *status, lg *zap.Logger) {
	http.HandleFunc("/status", func(w http.ResponseWriter, req *http.Request) {
		currentStatus.Lock()
		defer currentStatus.Unlock()
		if currentStatus.dataProcessed {
			currentStatus.IdleTimeMilliSecs = time.Since(currentStatus.timestampLastProcessed).Milliseconds()
		}
		data, err := json.Marshal(currentStatus)
		if err != nil {
			lg.Error("error when json marshaling status", zap.Any("status", currentStatus), zap.Error(err))
		}
		fmt.Fprint(w, string(data))
	})
	if localAPIPort != 0 {
		go func() {
			err := http.ListenAndServe(fmt.Sprintf(":%d", localAPIPort), nil)
			if err != nil {
				lg.Fatal("failed to start the status server", zap.Error(err))
			}
			lg.Info("status server running", zap.Int("port", localAPIPort))
		}()
	}

}

func main() {
	portsStr := flag.String("ports", "", `List of the ports to listen on. Has to be supplied in the from "XXXX YYYY ZZZZ AAAA-BBBB" in quotes.`)
	outPrefix := flag.String("prefix", "", "Prefix for the output files.")
	outDir := flag.String("outdir", "", "Output directory. Absolute path. Optional.")
	profiler := flag.String("profiler", "", "Where should the profiler listen?")
	localAPIPort := flag.Int("local-api-port", 0, "specify which port the local HTTP API should be listening on")

	flag.Parse()

	lg, err := zap.NewProduction()
	if err != nil {
		log.Fatal("failed to create logger: ", err)
	}

	if *portsStr == "" {
		lg.Fatal("please supply the ports argument")
	}

	if *profiler != "" {
		go func() {
			lg.Info("profiler server exited", zap.Error(http.ListenAndServe(*profiler, nil)))
		}()
	}
	ports := parsePorts(*portsStr, lg)

	currentStatus := &status{sync.Mutex{}, false, false, time.Now(), 0}

	if *localAPIPort != 0 {
		setupStatusServer(*localAPIPort, currentStatus, lg)
	}

	fs := make(map[int]io.Writer)

	for _, p := range ports {
		fs[p] = ioutil.Discard
		if *outDir != "" {
			fPath := fmt.Sprintf("%s/%s%d", *outDir, *outPrefix, p)
			f, err := os.Create(fPath)
			if err != nil {
				lg.Fatal("failed to create file", zap.String("path", fPath), zap.Error(err))
			}
			defer func(p int) {
				err := f.Close()
				if err != nil {
					lg.Error("could not close file for port", zap.Int("port", p), zap.Error(err))
				}

			}(p)
			fs[p] = f
		}
	}

	ls := make(map[int]net.Listener)
	for _, p := range ports {
		l, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
		if err != nil {
			lg.Fatal("failed to open connection on port", zap.Int("port", p), zap.Error(err))
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
						scanner := bufio.NewScanner(conn)
						scanner.Buffer(make([]byte, bufio.MaxScanTokenSize*100), bufio.MaxScanTokenSize)
						if *outDir == "" {
							for scanner.Scan() {
								// TODO: Add counting
							}
							if err := scanner.Err(); err != nil {
								lg.Info("failed scan when reading data", zap.Error(err))
							}
						} else {
							_, err := io.Copy(fs[prt], conn)
							if err != nil {
								lg.Error("failed when dumping data", zap.Error(err))
							}
						}

						currentStatus.Lock()
						currentStatus.dataProcessed = true
						currentStatus.timestampLastProcessed = time.Now()
						currentStatus.Unlock()
					}(conn)
				}
			}
			connectionWG.Wait()
		}(ls[p], p, stop)
	}

	currentStatus.Lock()
	currentStatus.Ready = true
	currentStatus.Unlock()

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
