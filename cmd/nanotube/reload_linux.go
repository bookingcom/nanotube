package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"syscall"
	"time"
)

// The old tracked main process sends MAINPID. NotifyAccess=main suffices;
// KillMode=control-group can remain enabled. A barrier keeps it alive until
// systemd has processed that datagram, even if its input queue drains instantly.
func handoffService(ctx context.Context, pid int, timeout time.Duration) error {
	path := notifySocket()
	if path == "" {
		return nil
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	// Probe before transferring ownership. A broken notification endpoint
	// must leave the original service running and able to reject this reload.
	probe, cancel := context.WithTimeout(ctx, timeout)
	err = notifyBarrier(probe, conn)
	cancel()
	if err != nil {
		return fmt.Errorf("systemd notification preflight: %w", err)
	}
	_ = conn.SetWriteDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(mainPIDMessage(pid)); err != nil {
		return err
	}
	// Once MAINPID is queued, killing the child on an acknowledgement timeout
	// could make systemd stop the entire unit. Keep both live until the barrier
	// completes. SIGTERM/interrupt still cancels this wait and begins draining.
	for ctx.Err() == nil {
		if err := notifyBarrier(ctx, conn); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	return nil // termination was requested after ownership transfer was queued
}

func notifyBarrier(ctx context.Context, conn *net.UnixConn) error {
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	defer reader.Close()
	defer writer.Close()
	stop := context.AfterFunc(ctx, func() { _ = reader.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(deadline)
	} else {
		_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	}
	// WriteMsgUnix rejects connected datagram sockets. RawConn.Write keeps
	// the connected destination and netpoll/deadline behavior while sending
	// the barrier's descriptor as ancillary data.
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var sendErr error
	err = raw.Write(func(fd uintptr) bool {
		sendErr = syscall.Sendmsg(int(fd), []byte("BARRIER=1"), syscall.UnixRights(int(writer.Fd())), nil, 0)
		return sendErr != syscall.EAGAIN && sendErr != syscall.EWOULDBLOCK
	})
	if err != nil {
		return err
	}
	if sendErr != nil {
		return sendErr
	}
	_ = writer.Close()
	var b [1]byte
	_, err = reader.Read(b[:])
	if err == io.EOF {
		return nil
	}
	return fmt.Errorf("waiting for systemd notification barrier: %w", err)
}

func mainPIDMessage(pid int) []byte { return []byte("MAINPID=" + strconv.Itoa(pid)) }
