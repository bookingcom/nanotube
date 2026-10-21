package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSystemdHandoffWaitsForMainPIDBarrier(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify")
	listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("NOTIFY_SOCKET", path)
	reached := make(chan struct{})
	release := make(chan struct{})
	manager := make(chan error, 1)
	go func() {
		for _, want := range []string{"BARRIER=1", "MAINPID=1234", "BARRIER=1"} {
			data, control := make([]byte, 128), make([]byte, syscall.CmsgSpace(4))
			n, cn, _, _, err := listener.ReadMsgUnix(data, control)
			if err != nil {
				manager <- err
				return
			}
			if string(data[:n]) != want {
				manager <- fmt.Errorf("notification %q, want %q", data[:n], want)
				return
			}
			if want == "MAINPID=1234" {
				close(reached)
				continue
			}
			messages, err := syscall.ParseSocketControlMessage(control[:cn])
			if err != nil || len(messages) != 1 {
				manager <- fmt.Errorf("invalid barrier control messages: %v", err)
				return
			}
			fds, err := syscall.ParseUnixRights(&messages[0])
			if err != nil || len(fds) != 1 {
				manager <- fmt.Errorf("invalid barrier descriptor: %v", err)
				return
			}
			select {
			case <-reached:
				<-release
			default:
			}
			_ = syscall.Close(fds[0])
		}
		manager <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- handoffService(ctx, 1234, time.Second) }()
	select {
	case <-reached:
	case err := <-manager:
		t.Fatalf("notification protocol failed: %v", err)
	case <-ctx.Done():
		t.Fatal("MAINPID was not sent")
	}
	select {
	case err := <-done:
		t.Fatalf("handoff returned before systemd processed MAINPID: %v", err)
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-manager; err != nil {
		t.Fatal(err)
	}
}

func TestSystemdUnavailableDoesNotTransferOwnership(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", filepath.Join(t.TempDir(), "missing"))
	if err := handoffService(context.Background(), os.Getpid(), time.Second); err == nil {
		t.Fatal("missing notification endpoint accepted")
	}
}
