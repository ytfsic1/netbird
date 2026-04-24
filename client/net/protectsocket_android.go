package net

import (
	"fmt"
	"sync"
	"syscall"

	log "github.com/sirupsen/logrus"

	"github.com/netbirdio/netbird/client/iface/netstack"
)

var (
	androidProtectSocketLock sync.Mutex
	androidProtectSocket     func(fd int32) bool
	androidProtectSocketOnce sync.Once
)

func SetAndroidProtectSocketFn(fn func(fd int32) bool) {
	androidProtectSocketLock.Lock()
	androidProtectSocket = fn
	androidProtectSocketLock.Unlock()
}

// ControlProtectSocket is a Control function that sets the fwmark on the socket
func ControlProtectSocket(_, _ string, c syscall.RawConn) error {
	if netstack.IsEnabled() {
		return nil
	}
	var aErr error
	err := c.Control(func(fd uintptr) {
		androidProtectSocketLock.Lock()
		defer androidProtectSocketLock.Unlock()

		if androidProtectSocket == nil {
			aErr = fmt.Errorf("android socket protector is not configured")
			return
		}

		if !androidProtectSocket(int32(fd)) {
			aErr = fmt.Errorf("failed to protect socket via Android")
			return
		}
		androidProtectSocketOnce.Do(func() {
			log.Infof("Android socket protector accepted fd=%d", fd)
		})
	})

	if err != nil {
		return err
	}

	return aErr
}
