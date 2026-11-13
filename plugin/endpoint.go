package plugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Endpoint 是一个本机 gRPC 端点。
type Endpoint struct {
	Network Network
	Address string
}

// String 返回 <network> <address>。
func (e Endpoint) String() string { return string(e.Network) + " " + e.Address }

// ParseEndpoint 解析 <network> <address>（环境变量 XIHAN_PLUGIN_HOST_ADDRESS 的格式）。
func ParseEndpoint(text string) (Endpoint, bool) {
	network, address, ok := strings.Cut(strings.TrimSpace(text), " ")
	if !ok || address == "" {
		return Endpoint{}, false
	}
	switch Network(network) {
	case NetworkUnix, NetworkTCP, NetworkPipe:
		return Endpoint{Network: Network(network), Address: address}, true
	default:
		return Endpoint{}, false
	}
}

// listen 按选项监听：unix 套接字只允许当前用户（没给地址时放进新建的私有目录），tcp 只绑定回环地址，npipe 只在 Windows 上可用。
// 返回的清理函数在服务停止后调用，删除自动新建的私有目录；其余情况什么都不做。
func listen(network Network, address string) (net.Listener, Endpoint, func(), error) {
	if network == networkUnset {
		network = NetworkUnix
	}
	cleanup := func() {}
	switch network {
	case NetworkUnix:
		if address == "" {
			// 套接字放进本进程新建的私有目录（handshake.md §4）：MkdirTemp 的目录名随机、权限 0700、只新建不复用，
			// 别人抢先建好的同名目录或指向别处的链接不会被拿来用；共享临时目录里的套接字在 Chmod 之前谁都连得上
			directory, err := os.MkdirTemp("", "xihan-plugin-")
			if err != nil {
				return nil, Endpoint{}, nil, fmt.Errorf("plugin: create the private socket directory: %w", err)
			}
			cleanup = func() { _ = os.RemoveAll(directory) }
			address = filepath.Join(directory, "plugin.sock")
		} else {
			_ = os.Remove(address)
		}
		listener, err := net.Listen("unix", address)
		if err != nil {
			cleanup()
			return nil, Endpoint{}, nil, fmt.Errorf("plugin: listen on unix socket %q: %w", address, err)
		}
		_ = os.Chmod(address, 0o600)
		return listener, Endpoint{Network: NetworkUnix, Address: address}, cleanup, nil
	case NetworkTCP:
		if address == "" {
			address = "127.0.0.1:0"
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil || (host != "127.0.0.1" && host != "::1") {
			return nil, Endpoint{}, nil, fmt.Errorf("plugin: the tcp address %q must be a loopback address with a port", address)
		}
		listener, err := net.Listen("tcp", address)
		if err != nil {
			return nil, Endpoint{}, nil, fmt.Errorf("plugin: listen on %q: %w", address, err)
		}
		bound := listener.Addr().(*net.TCPAddr)
		return listener, Endpoint{Network: NetworkTCP, Address: net.JoinHostPort(host, strconv.Itoa(bound.Port))}, cleanup, nil
	case NetworkPipe:
		if address == "" {
			address = `\\.\pipe\xihan-plugin-` + randomSuffix()
		}
		listener, err := listenNamedPipe(address)
		if err != nil {
			return nil, Endpoint{}, nil, err
		}
		return listener, Endpoint{Network: NetworkPipe, Address: address}, cleanup, nil
	default:
		return nil, Endpoint{}, nil, fmt.Errorf("plugin: unsupported network %q", network)
	}
}

// dial 连接一个本机端点（宿主回调服务）；options 追加在默认选项之后。
func dial(endpoint Endpoint, options ...grpc.DialOption) (*grpc.ClientConn, error) {
	var dialer func(context.Context) (net.Conn, error)
	switch endpoint.Network {
	case NetworkUnix, NetworkTCP:
		network := string(endpoint.Network)
		dialer = func(ctx context.Context) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, endpoint.Address)
		}
	case NetworkPipe:
		pipe, err := namedPipeDialer(endpoint.Address)
		if err != nil {
			return nil, err
		}
		dialer = pipe
	default:
		return nil, fmt.Errorf("plugin: unsupported network %q", endpoint.Network)
	}
	defaults := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return dialer(ctx) }),
	}
	return grpc.NewClient("passthrough:///host", append(defaults, options...)...)
}

// randomSuffix 生成 8 字节随机数的十六进制。
func randomSuffix() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buffer)
}
