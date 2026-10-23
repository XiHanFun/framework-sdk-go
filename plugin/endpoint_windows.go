//go:build windows

package plugin

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

// listenNamedPipe 监听 Windows 命名管道，只允许当前用户连接。
func listenNamedPipe(address string) (net.Listener, error) {
	return winio.ListenPipe(address, &winio.PipeConfig{MessageMode: false})
}

// namedPipeDialer 连接 Windows 命名管道。
func namedPipeDialer(address string) (func(context.Context) (net.Conn, error), error) {
	return func(ctx context.Context) (net.Conn, error) { return winio.DialPipeContext(ctx, address) }, nil
}
