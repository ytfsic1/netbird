//go:build !android

package net

import (
	"context"
	stdnet "net"
)

func DialAndroidTCP(_ context.Context, _ string) (stdnet.Conn, bool, error) {
	return nil, false, nil
}
