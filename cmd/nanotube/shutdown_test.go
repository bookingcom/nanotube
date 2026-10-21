package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Exercise the real signal handler, listener, routing workers, target queues
// and final output flush. In particular, the producer never closes its socket.
func TestShutdownSignalsDrainPipeline(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "nanotube")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build nanotube: %v\n%s", err, output)
	}
	for _, size := range []int{4096, 65536} {
		for _, signal := range []syscall.Signal{syscall.SIGTERM, syscall.SIGUSR2} {
			t.Run(fmt.Sprintf("buffer-%d/%s", size, signal), func(t *testing.T) {
				receiver, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer receiver.Close()
				received := make(chan string, 100)
				go func() {
					for {
						conn, err := receiver.Accept()
						if err != nil {
							return
						}
						go func() {
							defer conn.Close()
							scanner := bufio.NewScanner(conn)
							for scanner.Scan() {
								received <- scanner.Text()
							}
						}()
					}
				}()
				input, metricsAddr, config := shutdownConfig(t, receiver.Addr().String(), size)
				if output, err := exec.Command(binary, "-config", config, "-validate").CombinedOutput(); err != nil {
					t.Fatalf("validate configuration: %v\n%s", err, output)
				}
				logPath := filepath.Join(t.TempDir(), "nanotube.log")
				logFile, err := os.Create(logPath)
				if err != nil {
					t.Fatal(err)
				}
				defer logFile.Close()
				command := exec.Command(binary, "-config", config)
				command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				command.Stdout, command.Stderr = logFile, logFile
				if err := command.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL) })
				wait := make(chan error, 1)
				go func() { wait <- command.Wait() }()
				waitForShutdownMetric(t, metricsAddr, "nanotube_in_records_total", 0, logPath)
				producer, err := net.DialTimeout("tcp", input, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer producer.Close()
				samples := []string{"first.metric 1 100", "second.metric 2 101", "third.metric 3 102"}
				for _, sample := range samples {
					if _, err := fmt.Fprintln(producer, sample); err != nil {
						t.Fatal(err)
					}
				}
				waitForShutdownMetric(t, metricsAddr, "nanotube_in_records_total", len(samples), logPath)
				if err := command.Process.Signal(signal); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-wait:
					if err != nil {
						logs, _ := os.ReadFile(logPath)
						t.Fatalf("shutdown failed: %v\n%s", err, logs)
					}
				case <-time.After(4 * time.Second):
					t.Fatal("process did not finish its bounded shutdown")
				}
				logs, _ := os.ReadFile(logPath)
				if !strings.Contains(string(logs), "Terminated gracefully.") {
					t.Fatalf("missing graceful completion: %s", logs)
				}
				for _, expected := range samples {
					select {
					case got := <-received:
						if got != expected {
							t.Fatalf("got record %q, want %q", got, expected)
						}
					case <-time.After(time.Second):
						t.Fatalf("final output buffer lost record %q", expected)
					}
				}
				if signal == syscall.SIGUSR2 {
					var childPID int
					for _, line := range strings.Split(string(logs), "\n") {
						if strings.Contains(line, "Started new process. Moved FDs.") {
							index := strings.LastIndex(line, `"pid":`)
							if index >= 0 {
								childPID, _ = strconv.Atoi(strings.TrimSuffix(line[index+6:], "}"))
							}
						}
					}
					if childPID == 0 {
						t.Fatalf("missing replacement PID: %s", logs)
					}
					child, err := os.FindProcess(childPID)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = child.Kill() })
					waitForShutdownMetric(t, metricsAddr, "nanotube_in_records_total", 0, logPath)
					nextProducer, err := net.DialTimeout("tcp", input, time.Second)
					if err != nil {
						t.Fatal(err)
					}
					defer nextProducer.Close()
					if _, err := fmt.Fprintln(nextProducer, "replacement.metric 4 103"); err != nil {
						t.Fatal(err)
					}
					waitForShutdownMetric(t, metricsAddr, "nanotube_in_records_total", 1, logPath)
					if err := child.Signal(syscall.SIGTERM); err != nil {
						t.Fatal(err)
					}
					select {
					case got := <-received:
						if got != "replacement.metric 4 103" {
							t.Fatalf("replacement produced %q", got)
						}
					case <-time.After(4 * time.Second):
						t.Fatal("replacement did not accept and drain records")
					}
				}
			})
		}
	}
	t.Run("unavailable-target-keeps-shutdown-bounded", func(t *testing.T) {
		receiver, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		target := receiver.Addr().String()
		receiver.Close() // Dialing this target must fail.
		input, metricsAddr, config := shutdownConfig(t, target, 65536)
		logPath := filepath.Join(t.TempDir(), "nanotube.log")
		logFile, err := os.Create(logPath)
		if err != nil {
			t.Fatal(err)
		}
		defer logFile.Close()
		command := exec.Command(binary, "-config", config)
		command.Stdout, command.Stderr = logFile, logFile
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = command.Process.Kill() })
		wait := make(chan error, 1)
		go func() { wait <- command.Wait() }()
		waitForShutdownMetric(t, metricsAddr, "nanotube_in_records_total", 0, logPath)
		producer, err := net.DialTimeout("tcp", input, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer producer.Close()
		if _, err := fmt.Fprintln(producer, "pending.metric 1 100"); err != nil {
			t.Fatal(err)
		}
		waitForShutdownMetric(t, metricsAddr, "nanotube_in_records_total", 1, logPath)
		if err := command.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-wait:
			if err == nil {
				t.Fatal("an unavailable target must not be reported as a lossless drain")
			}
		case <-time.After(4 * time.Second):
			t.Fatal("unavailable target exceeded the termination budget")
		}
		logs, _ := os.ReadFile(logPath)
		for _, message := range []string{"All incoming connections closed. Draining the main queue.", "Queue not fully flushed."} {
			if !strings.Contains(string(logs), message) {
				t.Fatalf("missing %q: %s", message, logs)
			}
		}
	})
}

func shutdownConfig(t *testing.T, target string, buffer int) (string, string, string) {
	t.Helper()
	freeAddress := func() string {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		return listener.Addr().String()
	}
	input, metricsAddr := freeAddress(), freeAddress()
	_, metricsPort, _ := net.SplitHostPort(metricsAddr)
	host, targetPort, _ := net.SplitHostPort(target)
	dir := t.TempDir()
	config := filepath.Join(dir, "config.toml")
	clusters := filepath.Join(dir, "clusters.toml")
	rules := filepath.Join(dir, "rules.toml")
	files := map[string]string{
		config:   fmt.Sprintf("ClustersConfig = %q\nRulesConfig = %q\nListenTCP = %q\nPromPort = %s\nPromLocal = true\nPprofPort = -1\nTermTimeoutSec = 2\nIncomingConnIdleTimeoutSec = 3600\nMainQueueSize = 100\nMainQueueBatchSize = 0\nBatchFlushPerdiodSec = 3600\nWorkerPoolSize = 1\nHostQueueSize = 100\nTCPOutBufSize = %d\nTCPOutBufFlushPeriodSec = 3600\nSendTimeoutSec = 3601\n", clusters, rules, input, metricsPort, buffer),
		clusters: fmt.Sprintf("[[cluster]]\nname = \"receiver\"\ntype = \"toall\"\n[[cluster.hosts]]\nname = %q\nport = %s\n", host, targetPort),
		rules:    "[[rule]]\nregexs = [\".*\"]\nclusters = [\"receiver\"]\n",
	}
	for path, data := range files {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return input, metricsAddr, config
}

func waitForShutdownMetric(t *testing.T, address, name string, value int, logPath string) {
	t.Helper()
	client := &http.Client{Timeout: 200 * time.Millisecond}
	defer client.CloseIdleConnections()
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, err := client.Get("http://" + address + "/metrics")
		if err == nil {
			data, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr == nil {
				for _, line := range strings.Split(string(data), "\n") {
					if strings.HasPrefix(line, name+" ") {
						got, _ := strconv.ParseFloat(strings.TrimPrefix(line, name+" "), 64)
						if got == float64(value) {
							return
						}
					}
				}
			}
		}
		select {
		case <-ticker.C:
		case <-deadline:
			logs, _ := os.ReadFile(logPath)
			t.Fatalf("waiting for %s=%d\n%s", name, value, logs)
		}
	}
}
