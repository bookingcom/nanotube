package k8s

import (
	"fmt"
	"net"
	"runtime"

	"github.com/pkg/errors"
	"github.com/vishvananda/netns"
)

// OpenTCPTunnelByPID opens an injected TCP port for listening in a container identified by PID.
func OpenTCPTunnelByPID(pid uint32, port uint16) (listener *net.TCPListener, returnErr error) {
	listener = nil
	returnErr = nil

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	ownNetns, err := netns.Get()
	if err != nil {
		returnErr = errors.Wrap(err, "failed to get own networking namespace")
		return
	}

	defer func() {
		err := ownNetns.Close()
		if err != nil {
			returnErr = errors.Wrap(err, "error while closing own network namespace")
		}
	}()

	newns, err := netns.GetFromPid(int(pid))
	if err != nil {
		returnErr = errors.Wrap(err, "failed to get container network namespace")
		return
	}

	defer func() {
		err := newns.Close()
		if err != nil {
			returnErr = errors.Wrap(err, "error while closing container net namespace")
		}
	}()

	if err = netns.Set(newns); err != nil {
		return nil, err
	}
	defer func() {
		err := netns.Set(ownNetns)
		if err != nil {
			returnErr = errors.Wrap(err, "error while setting back to the original net namespace")
		}
	}()

	addrStr := fmt.Sprintf("127.0.0.1:%d", port)
	addr, err := net.ResolveTCPAddr("tcp", addrStr)
	if err != nil {
		returnErr = errors.Wrapf(err, "error resolving TCP address from string %s", addrStr)
		return
	}

	listener, err = net.ListenTCP("tcp", addr)
	if err != nil {
		returnErr = errors.Wrapf(err, "error opening injected TCP port %d for listening", port)
		return
	}

	return
}

// OpenTCPTunnelToDocker opens an injected TCP port for listening in a container.
func OpenTCPTunnelToDocker(containerID string, port uint16) (listener *net.TCPListener, returnErr error) {
	listener = nil
	returnErr = nil

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	ownNetns, err := netns.Get()
	if err != nil {
		returnErr = errors.Wrap(err, "failed to get own networking namespace")
		return
	}

	defer func() {
		err := ownNetns.Close()
		if err != nil {
			returnErr = errors.Wrap(err, "error while closing own network namespace")
		}
	}()

	newns, err := netns.GetFromDocker(containerID)
	if err != nil {
		returnErr = errors.Wrap(err, "failed to get docker network namespace")
		return
	}

	defer func() {
		err := newns.Close()
		if err != nil {
			returnErr = errors.Wrap(err, "error while closing docker net namespace")
		}
	}()

	if err = netns.Set(newns); err != nil {
		return nil, err
	}
	defer func() {
		err := netns.Set(ownNetns)
		if err != nil {
			returnErr = errors.Wrap(err, "error while setting back to the original net namespace")
		}
	}()

	addrStr := fmt.Sprintf("127.0.0.1:%d", port)
	addr, err := net.ResolveTCPAddr("tcp", addrStr)
	if err != nil {
		returnErr = errors.Wrapf(err, "error resolving TCP address from string %s", addrStr)
		return
	}

	listener, err = net.ListenTCP("tcp", addr)
	if err != nil {
		returnErr = errors.Wrapf(err, "error opening injected TCP port %d for listening in pod with ID %s", port, containerID)
		return
	}

	return
}
