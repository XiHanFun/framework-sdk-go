//go:build windows

package plugin

import (
	"context"
	"fmt"
	"net"
	"os/user"

	"github.com/Microsoft/go-winio"
)

// callbackNetworks 是本 SDK 在 Windows 上能连的回调端点 network（handshake.md §4）：go-winio 连命名管道，标准库连回环 TCP 与
// Windows 10 1803 起的 Unix 域套接字。
var callbackNetworks = []Network{NetworkPipe, NetworkUnix, NetworkTCP}

// listenNamedPipe 监听 Windows 命名管道：只允许当前用户、拒绝远程客户端（handshake.md §4）。
func listenNamedPipe(address string) (net.Listener, error) {
	// Windows 上 Uid 是当前用户的 SID；安全描述符 D:P(A;;GA;;;<SID>) 只给这个用户完全访问、不继承别的访问控制项，
	// 系统缺省的命名管道描述符还会给 Everyone 读权限。go-winio 创建管道时总带 FILE_PIPE_REJECT_REMOTE_CLIENTS，
	// 并且只新建、不打开已有的同名管道，抢先建好的管道不会被拿来用
	current, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("plugin: look up the current user for the access control of %q: %w", address, err)
	}
	listener, err := winio.ListenPipe(address, &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;" + current.Uid + ")"})
	if err != nil {
		return nil, fmt.Errorf("plugin: listen on named pipe %q: %w", address, err)
	}
	return listener, nil
}

// namedPipeDialer 连接 Windows 命名管道。
func namedPipeDialer(address string) (func(context.Context) (net.Conn, error), error) {
	return func(ctx context.Context) (net.Conn, error) { return winio.DialPipeContext(ctx, address) }, nil
}
