package android

import (
	"os"

	"github.com/netbirdio/netbird/client/internal/lazyconn"
	"github.com/netbirdio/netbird/client/internal/peer"
	"github.com/netbirdio/netbird/client/net"
)

var (
	// EnvKeyNBForceRelay Exported for Android java client to force relay connections
	EnvKeyNBForceRelay = peer.EnvKeyNBForceRelay

	// EnvKeyNBLazyConn Exported for Android java client to configure lazy connection
	EnvKeyNBLazyConn = lazyconn.EnvEnableLazyConn

	// EnvKeyNBInactivityThreshold Exported for Android java client to configure connection inactivity threshold
	EnvKeyNBInactivityThreshold = lazyconn.EnvInactivityThreshold
)

// EnvList wraps a Go map for export to Java
type EnvList struct {
	data map[string]string
}

// SocketDialer lets Android create connected sockets using the platform network
// stack and pass the connected fd to Go.
type SocketDialer interface {
	DialTCP(host string, port int32) (int32, error)
}

// NewEnvList creates a new EnvList
func NewEnvList() *EnvList {
	return &EnvList{data: make(map[string]string)}
}

// Put adds a key-value pair
func (el *EnvList) Put(key, value string) {
	el.data[key] = value
}

// Get retrieves a value by key
func (el *EnvList) Get(key string) string {
	return el.data[key]
}

func (el *EnvList) AllItems() map[string]string {
	return el.data
}

// SetEnv updates Go's process environment for Android callers that need a value
// before Client.Run receives an EnvList, such as setup/auth connectivity checks.
func SetEnv(key, value string) error {
	return os.Setenv(key, value)
}

// SetSocketDialer configures the Android-native TCP dialer used by gRPC.
func SetSocketDialer(dialer SocketDialer) {
	if dialer == nil {
		net.SetAndroidSocketDialerFn(nil)
		return
	}
	net.SetAndroidSocketDialerFn(dialer.DialTCP)
}
