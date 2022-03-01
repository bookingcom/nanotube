package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/facebookgo/grace/gracenet"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"

	"go.uber.org/zap"
)

func TestMain(t *testing.T) {
	setup(t)

	sample := "test 123 123"
	// stop := make(chan struct{})
	var g errgroup.Group
	listening := make(chan bool)
	g.Go(func() error { return listen(12003, sample, listening) })

	<-listening

	err := send(12123, sample)
	// close(stop)
	if err != nil {
		t.Fatalf("sending to nanotube failed: %v", err)
	}

	err = g.Wait()
	if err != nil {
		t.Fatalf("receiving from nanotube failed: %v", err)
	}
}

func setup(t *testing.T) {
	lg := zap.NewNop()

	fixturesPath := "testdata/"

	cfgPath := filepath.Join(fixturesPath, "config.toml")

	cfg, clustersConf, rulesConf, rewritesConf, _, err := readConfigs(cfgPath)
	if err != nil {
		t.Fatalf("error reading and compiling config: %v", err)
	}
	ms := metrics.New(&cfg)
	metrics.Register(ms, &cfg)
	clusters, rules, rewrites, err := buildPipeline(&cfg, &clustersConf, &rulesConf, rewritesConf, ms, lg)
	if err != nil {
		t.Fatalf("error building pipline components: %v", err)
	}

	term := make(chan struct{})
	n := gracenet.Net{}
	pipe, err := Listen(&n, &cfg, term, lg, ms)
	if err != nil {
		t.Fatalf("error launching listener, %v", err)
	}
	done := Process(pipe, rules, rewrites, cfg.WorkerPoolSize, true, true, lg, ms)
	_ = clusters.Send(done)
}

func listen(port int, sample string, listening chan<- bool) (e error) {
	l, err := net.Listen("tcp", net.JoinHostPort("localhost", strconv.Itoa(port)))
	if err != nil {
		return errors.Wrap(err, "opening listening port failed")
	}
	listening <- true
	conn, err := l.Accept()
	if err != nil {
		return errors.Wrap(err, "failed to accept connection")
	}
	defer func() {
		cerr := conn.Close()
		if e == nil {
			e = errors.Wrap(cerr, "closing connection failed")
		}
	}()

	w := bufio.NewWriter(os.Stdout)
	defer func() {
		ferr := w.Flush()
		if e == nil {
			e = errors.Wrap(ferr, "error flushing to stdout")
		}
	}()

	// data is sent twice: once via TCP and once via UDP
	sc := bufio.NewScanner(conn)
	sc.Scan()
	comp := sc.Text()
	if sample != comp {
		return errors.Errorf("got *%s*, expected *%s*", sc.Text(), sample)
	}

	sc.Scan()
	comp = sc.Text()
	if sample != comp {
		return errors.Errorf("got *%s*, expected *%s*", sc.Text(), sample)
	}

	return nil
}

func send(port int, sample string) (e error) {
	connTCP, err := net.Dial("tcp", net.JoinHostPort("localhost", strconv.Itoa(port)))
	if err != nil {
		return errors.Wrap(err, "connection failed")
	}
	defer func() {
		cerr := connTCP.Close()
		if e == nil {
			e = errors.Wrap(cerr, "error closing connection")
		}
	}()

	_, err = fmt.Fprintln(connTCP, sample)
	if err != nil {
		return errors.Wrap(err, "error sending data via TCP")
	}

	connUDP, err := net.Dial("tcp", net.JoinHostPort("localhost", strconv.Itoa(port)))
	if err != nil {
		return errors.Wrap(err, "dialing to UDP failed")

	}
	defer func() {
		cerr := connUDP.Close()
		if e == nil {
			e = errors.Wrap(cerr, "error closing connection")
		}
	}()

	_, err = fmt.Fprintln(connUDP, sample)
	if err != nil {
		return errors.Wrap(err, "error sending data via UDP")
	}

	return nil
}
