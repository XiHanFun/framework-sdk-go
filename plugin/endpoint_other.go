//go:build !windows

package plugin

import (
	"context"
	"errors"
	"net"
)

// callbackNetworks 是本 SDK 在非 Windows 平台上能连的回调端点 network（handshake.md §4）：没有命名管道。
var callbackNetworks = []Network{NetworkUnix, NetworkTCP}

// listenNamedPipe 在非 Windows 平台不可用。
func listenNamedPipe(string) (net.Listener, error) {
	return nil, errors.New("plugin: npipe endpoints are only supported on Windows")
}

// namedPipeDialer 在非 Windows 平台不可用。
func namedPipeDialer(string) (func(context.Context) (net.Conn, error), error) {
	return nil, errors.New("plugin: npipe endpoints are only supported on Windows")
}
