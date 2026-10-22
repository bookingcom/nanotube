package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	reloadReadyEnv = "NANOTUBE_RELOAD_READY"
	systemdEnv     = "NANOTUBE_SYSTEMD"
)

// startReplacement waits for the inherited listener and routing/output pipeline
// to be initialized before retiring the old process. The private socket also
// distinguishes the new child from the old process's shared metrics listener.
func startReplacement(ctx context.Context, start func() (int, error), timeout time.Duration) (int, error) {
	if os.Getenv(systemdEnv) == "1" && os.Getenv("NOTIFY_SOCKET") == "" {
		return 0, fmt.Errorf("systemd reload requires NotifyAccess=main")
	}
	dir, err := os.MkdirTemp("", "nt-reload-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "ready")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stopAccept := context.AfterFunc(readyCtx, func() { _ = listener.Close() })
	defer stopAccept()

	old, had := os.LookupEnv(reloadReadyEnv)
	if err := os.Setenv(reloadReadyEnv, path); err != nil {
		return 0, err
	}
	pid, err := start()
	if had {
		_ = os.Setenv(reloadReadyEnv, old)
	} else {
		_ = os.Unsetenv(reloadReadyEnv)
	}
	if err != nil {
		return 0, err
	}
	child, err := os.FindProcess(pid)
	if err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if committed {
			_ = child.Release()
			return
		}
		terminateReplacement(ctx, child, timeout)
	}()
	conn, err := listener.AcceptUnix()
	if err != nil {
		return 0, fmt.Errorf("waiting for replacement readiness: %w", err)
	}
	defer conn.Close()
	stopRead := context.AfterFunc(readyCtx, func() { _ = conn.Close() })
	defer stopRead()
	var announced int
	if _, err := fmt.Fscanln(conn, &announced); err != nil || announced != pid {
		return 0, fmt.Errorf("invalid replacement readiness: pid=%d, want=%d, error=%v", announced, pid, err)
	}
	if err := readyCtx.Err(); err != nil {
		return 0, err
	}
	if os.Getenv(systemdEnv) == "1" {
		if err := handoffService(ctx, pid, timeout); err != nil {
			return 0, err
		}
	}
	committed = true
	return pid, nil
}

// Called only after signal handling, inherited input listeners, routing workers
// and output senders are initialized. This is pipeline readiness, not a claim
// that every remote target is reachable or has acknowledged a delivery.
func announceReplacementReady() error {
	path := os.Getenv(reloadReadyEnv)
	_ = os.Unsetenv(reloadReadyEnv)
	if path == "" {
		return nil
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	_, err = fmt.Fprintln(conn, os.Getpid())
	return err
}

// notifySocket accepts systemd's filesystem and Linux abstract socket forms.
func notifySocket() string {
	path := os.Getenv("NOTIFY_SOCKET")
	if strings.HasPrefix(path, "@") {
		return "\x00" + path[1:]
	}
	return path
}

// Cleanup can wait during a rejected reload, while the original keeps serving.
// If termination was requested, drain both processes concurrently rather than
// spending the original process's deadline waiting for the failed replacement.
func terminateReplacement(ctx context.Context, child *os.Process, timeout time.Duration) {
	_ = child.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		_, _ = child.Wait()
		close(done)
	}()
	force := func() {
		select {
		case <-done:
		case <-time.After(timeout + time.Second):
			_ = child.Kill()
			<-done
		}
	}
	select {
	case <-done:
	case <-ctx.Done():
		go force()
	case <-time.After(timeout + time.Second):
		_ = child.Kill()
		<-done
	}
}
