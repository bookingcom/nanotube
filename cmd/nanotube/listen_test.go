package main

import (
	"net"
	"testing"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"

	"go.uber.org/zap"
)

func BenchmarkListenUDP(b *testing.B) {
	server, conn := net.Pipe()
	q := make(chan string, 10000)
	stop := make(chan struct{})
	lg := zap.NewNop()
	cfg := conf.MakeDefault()
	ms := metrics.New(&cfg)

	go func() {
		listenUDP(conn, q, stop, 4096, lg, ms)
	}()

	rec := []byte("aaa.bbb.ccc 1 12345678\n")
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		_, err := server.Write(rec)
		if err != nil {
			b.Fatal("writing to test connection failed", err)
		}
		<-q
	}
	b.StopTimer()

	err := server.Close()
	if err != nil {
		b.Fatal("closing test server failed", err)
	}
}
