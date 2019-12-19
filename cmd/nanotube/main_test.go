package main

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

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
	clPath := filepath.Join(fixturesPath, "clusters.toml")
	rulesPath := filepath.Join(fixturesPath, "rules.toml")

	cfg, clusters, rules, ms := loadBuildRegister(cfgPath, clPath, rulesPath, lg)

	term := make(chan struct{})
	pipe, err := Listen(&cfg, term, lg, ms)
	if err != nil {
		t.Fatalf("error launching listener, %v", err)
	}
	done := Process(pipe, rules, cfg.WorkerPoolSize, true, true, lg, ms)
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

	sc := bufio.NewScanner(conn)
	sc.Scan()

	if sample != sc.Text() {
		return errors.Errorf("got *%v*, expected *%v*", sc.Text(), sample)
	}

	return nil
}

func send(port int, sample string) (e error) {
	conn, err := net.Dial("tcp", net.JoinHostPort("localhost", strconv.Itoa(port)))
	if err != nil {
		return errors.Wrap(err, "connection failed")
	}
	defer func() {
		cerr := conn.Close()
		if e == nil {
			e = errors.Wrap(cerr, "error closing connection")
		}
	}()

	_, err = conn.Write([]byte(sample + "\n"))
	if err != nil {
		return errors.Wrap(err, "error sending data via TCP")
	}

	return nil
}
