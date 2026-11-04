//go:build unix

package in

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"time"

	"go.uber.org/zap"
)

func (c *inputConn) send(write func(io.Writer) error, workBytes ...uint64) error {
	conn := c.Conn
	if a, ok := conn.(*readAhead); ok {
		a.pause() // quiesce previous-generation read-ahead before sharing the socket
		conn = a.Conn
	}
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return errors.New("accepted input is not TCP")
	}
	f, err := tcp.File()
	if err != nil {
		return err
	}
	defer f.Close()
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()
	defer w.Close()
	work := uint64(c.input.cfg.StreamInputDictionaryLimit())
	for _, n := range workBytes {
		work = max(work, n)
	}
	deadline := time.Now().Add(5*time.Second + time.Duration(work/(16<<20))*time.Second)
	_ = c.transfer.SetDeadline(deadline)
	_ = w.SetWriteDeadline(deadline)
	if _, _, err = c.transfer.WriteMsgUnix([]byte{'C'}, syscall.UnixRights(int(f.Fd()), int(r.Fd())), nil); err != nil {
		return err
	}
	var ack [1]byte
	if _, err = io.ReadFull(c.transfer, ack[:]); err != nil {
		return err
	}
	if ack[0] != 'R' {
		return errors.New("successor rejected accepted socket")
	}
	counted := &handoffWriter{Writer: w}
	err = write(counted)
	c.stateBytes = counted.n
	if err != nil {
		return err
	}
	_ = w.Close()
	if _, err = io.ReadFull(c.transfer, ack[:]); err != nil {
		return err
	}
	if ack[0] != 'A' {
		return errors.New("successor failed input state replay")
	}
	return nil
}

// Receive reads one offer at a time, admitting stream memory before decoding.
// Pipe contents have a private versioned format; client protocols are unchanged.
func (h *TCPInput) Receive(ctx context.Context, control *net.UnixConn) error {
	if h == nil {
		return errors.New("TCP input handoff unavailable")
	}
	if _, err := control.Write([]byte("NTINPUT1")); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = control.Close() })
	defer stop()
	for {
		var tag [1]byte
		oob := make([]byte, syscall.CmsgSpace(2*4))
		n, on, flags, _, err := control.ReadMsgUnix(tag[:], oob)
		if err != nil {
			return err
		}
		files, parseErr := receivedFiles(oob[:on])
		if parseErr != nil || flags&syscall.MSG_CTRUNC != 0 || n != 1 || tag[0] != 'C' || len(files) != 2 {
			for _, f := range files {
				_ = f.Close()
			}
			if n == 1 && tag[0] == 'E' && len(files) == 0 && parseErr == nil {
				return nil
			}
			return fmt.Errorf("invalid input handoff offer: tag=%q fds=%d flags=%d: %v", tag, len(files), flags, parseErr)
		}
		conn, err := net.FileConn(files[0])
		_ = files[0].Close()
		if err != nil {
			_ = files[1].Close()
			return err
		}
		_ = conn.SetReadDeadline(time.Time{})
		h.lg.Info("Reload: input read-ahead started", zap.String("peer", conn.RemoteAddr().String()))
		ahead := newReadAhead(conn)
		// c.done is distinct from stateReady: ownership is acknowledged once all
		// state was parsed, while the live connection continues in its own reader.
		ready := make(chan error, 1)
		prefix := &handoffPrefix{ReadCloser: files[1], ready: ready, ahead: ahead}
		h.add(ahead, prefix)
		if _, err = control.Write([]byte{'R'}); err != nil {
			_ = ahead.Close()
			return err
		}
		select {
		case err = <-ready:
		case <-ctx.Done():
			err = ctx.Err()
		}
		ack := byte('A')
		if err != nil {
			ack = 'F'
			h.Fallback(err)
		}
		if _, writeErr := control.Write([]byte{ack}); writeErr != nil {
			return writeErr
		}
	}
}

func receivedFiles(oob []byte) ([]*os.File, error) {
	messages, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, err
	}
	var files []*os.File
	for _, m := range messages {
		fds, e := syscall.ParseUnixRights(&m)
		if e != nil {
			return files, e
		}
		for _, fd := range fds {
			syscall.CloseOnExec(fd)
			files = append(files, os.NewFile(uintptr(fd), "input-handoff"))
		}
	}
	return files, nil
}

type handoffPrefix struct {
	io.ReadCloser
	ready     chan error
	ahead     *readAhead
	completed bool
}

func (p *handoffPrefix) Read(b []byte) (int, error) {
	n, err := p.ReadCloser.Read(b)
	if err != nil && !p.completed {
		p.completed = true
		p.ahead.finish()
		if errors.Is(err, io.EOF) {
			p.ready <- nil
		} else {
			p.ready <- err
		}
	}
	return n, err
}
func (p *handoffPrefix) Close() error {
	if !p.completed {
		p.completed = true
		p.ready <- errors.New("input state rejected before replay completed")
	}
	return p.ReadCloser.Close()
}

type handoffWriter struct {
	io.Writer
	n int64
}

func (w *handoffWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.n += int64(n)
	return n, err
}
