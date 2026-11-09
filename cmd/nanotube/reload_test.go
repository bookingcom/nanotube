package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReloadChild(_ *testing.T) {
	mode := os.Getenv("NANOTUBE_TEST_RELOAD_MODE")
	if mode == "" {
		return
	}
	if mode == "ready" {
		if err := announceReplacementReady(); err != nil {
			os.Exit(2)
		}
	}
	time.Sleep(time.Minute)
}

func TestReplacementReadiness(t *testing.T) {
	for _, mode := range []string{"ready", "silent"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(systemdEnv, "")
			t.Setenv("INVOCATION_ID", "inherited-from-another-service")
			t.Setenv("NOTIFY_SOCKET", "/unrelated-service-notify")
			t.Setenv("NANOTUBE_TEST_RELOAD_MODE", mode)
			t.Setenv(reloadReadyEnv, "original")
			var command *exec.Cmd
			start := func() (int, error) {
				command = exec.Command(os.Args[0], "-test.run=^TestReloadChild$")
				if err := command.Start(); err != nil {
					return 0, err
				}
				return command.Process.Pid, nil
			}
			pid, err := startReplacement(context.Background(), start, time.Second)
			if mode == "ready" {
				if err != nil || pid != command.Process.Pid {
					t.Fatalf("ready child rejected: pid=%d, error=%v", pid, err)
				}
				_ = command.Process.Kill()
				_ = command.Wait()
			} else {
				if err == nil {
					t.Fatal("replacement without pipeline readiness was accepted")
				}
				if err := command.Process.Signal(syscall.Signal(0)); err == nil {
					t.Fatal("failed replacement was not reaped")
				}
			}
			if got := os.Getenv(reloadReadyEnv); got != "original" {
				t.Fatalf("parent environment changed: %q", got)
			}
		})
	}
}

func TestManagedReloadRequiresNotifications(t *testing.T) {
	t.Setenv(systemdEnv, "1")
	t.Setenv("NOTIFY_SOCKET", "")
	_, err := startReplacement(context.Background(), func() (int, error) {
		t.Fatal("unsafe managed replacement was started")
		return 0, nil
	}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "NotifyAccess=main") {
		t.Fatalf("missing configuration error: %v", err)
	}
}

func TestReplacementStartFailureRestoresEnvironment(t *testing.T) {
	t.Setenv(systemdEnv, "")
	t.Setenv("INVOCATION_ID", "inherited-from-another-service")
	t.Setenv("NOTIFY_SOCKET", "")
	t.Setenv(reloadReadyEnv, "original")
	want := errors.New("start failed")
	_, err := startReplacement(context.Background(), func() (int, error) { return 0, want }, time.Second)
	if !errors.Is(err, want) || os.Getenv(reloadReadyEnv) != "original" {
		t.Fatalf("start failure changed parent state: %v", err)
	}
}

func TestTerminationDoesNotWaitForRejectedChildDrain(t *testing.T) {
	command := exec.Command("/bin/sh", "-c", `trap '' TERM; echo ready; exec sleep 30`)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	ready := make(chan error, 1)
	go func() {
		var marker [6]byte
		_, err := io.ReadFull(stdout, marker[:])
		if err == nil && string(marker[:]) != "ready\n" {
			err = errors.New("unexpected child readiness marker")
		}
		ready <- err
	}()
	// Process startup is setup, not part of the bounded-termination assertion.
	// A pipe establishes readiness without polling the filesystem on busy runners.
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("stubborn child readiness: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stubborn child did not initialize")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { terminateReplacement(ctx, command.Process, 30*time.Second); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("original termination waited for the rejected child's drain")
	}
	if err := command.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("rejected child was hard-killed instead of draining concurrently: %v", err)
	}
	_ = command.Process.Kill()
}
