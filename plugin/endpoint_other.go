//go:build !windows

package plugin

import (
	"context"
	"errors"
	"net"
)

// listenNamedPipe 在非 Windows 平台不可用。
func listenNamedPipe(string) (net.Listener, error) {
	return nil, errors.New("plugin: npipe endpoints are only supported on Windows")
}

// namedPipeDialer 在非 Windows 平台不可用。
func namedPipeDialer(string) (func(context.Context) (net.Conn, error), error) {
	return nil, errors.New("plugin: npipe endpoints are only supported on Windows")
}
