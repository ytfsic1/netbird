//go:build android

package net

import (
	"context"
	"fmt"
	stdnet "net"
	"os"
	"strconv"
	"sync"
)

var (
	androidSocketDialerLock sync.RWMutex
	androidSocketDialer     func(host string, port int32) (int32, error)
)

func SetAndroidSocketDialerFn(fn func(host string, port int32) (int32, error)) {
	androidSocketDialerLock.Lock()
	androidSocketDialer = fn
	androidSocketDialerLock.Unlock()
}

func DialAndroidTCP(ctx context.Context, addr string) (stdnet.Conn, bool, error) {
	if ctx.Err() != nil {
		return nil, true, ctx.Err()
	}

	host, portString, err := stdnet.SplitHostPort(addr)
	if err != nil {
		return nil, false, nil
	}
	port, err := strconv.ParseInt(portString, 10, 32)
	if err != nil {
		return nil, false, nil
	}

	androidSocketDialerLock.RLock()
	dial := androidSocketDialer
	androidSocketDialerLock.RUnlock()
	if dial == nil {
		return nil, false, nil
	}

	fd, err := dial(host, int32(port))
	if err != nil {
		return nil, true, err
	}
	if fd < 0 {
		return nil, true, fmt.Errorf("Android socket dialer returned invalid fd %d", fd)
	}

	file := os.NewFile(uintptr(fd), "android-tcp-socket")
	if file == nil {
		return nil, true, fmt.Errorf("failed to wrap Android socket fd %d", fd)
	}
	defer file.Close()

	conn, err := stdnet.FileConn(file)
	if err != nil {
		return nil, true, fmt.Errorf("net.FileConn: %w", err)
	}
	return conn, true, nil
}
