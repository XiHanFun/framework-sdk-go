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

	"github.com/xihanfun/framework-spec/conformance/rules"
)

// Endpoint 是一个本机 gRPC 端点。
type Endpoint struct {
	Network Network
	Address string
}

// String 返回 <network> <address>。
func (e Endpoint) String() string { return string(e.Network) + " " + e.Address }

// ParseEndpoint 解析 <network> <address>（handshake.md §2：握手行去掉前缀 xihan-plugin/1 之后的部分，也是
// XIHAN_PLUGIN_HOST_ADDRESS 的一项）：network 是 unix、tcp 或 npipe，与地址之间恰好一个空格；地址是一个或多个空白之外的字符
// （空白的集合同 .NET 正则的 \s，含 U+0085、U+00A0、U+2028 等），tcp 的地址只认 127.0.0.0/8 的点分十进制或 [::1] 加 1–65535 的端口。
// 规则就是一致性模块的 rules.ParseEndpoint，不去掉首尾空白。
func ParseEndpoint(text string) (Endpoint, bool) {
	endpoint, ok := rules.ParseEndpoint(text)
	if !ok {
		return Endpoint{}, false
	}
	return Endpoint{Network: Network(endpoint.Network), Address: endpoint.Address}, true
}

// handshakeLine 写出握手行（不含行尾）：xihan-plugin/1 <network> <address>。
func handshakeLine(endpoint Endpoint) string {
	return HandshakeLineVersion + " " + endpoint.String()
}

// selectHostEndpoint 按 handshake.md §4 选回调端点：XIHAN_PLUGIN_HOST_ADDRESS 合法时按宿主的偏好取第一个 network 在 supported 里的项，
// 之后不换；缺失（空串）、不合法或没有能连的项时返回假，回调一律以 plugins.host.unavailable 失败、不发请求。
func selectHostEndpoint(value string, supported []Network) (Endpoint, bool) {
	networks := make([]string, 0, len(supported))
	for _, network := range supported {
		networks = append(networks, string(network))
	}
	endpoint, ok := rules.SelectHostEndpoint(value, networks)
	if !ok {
		return Endpoint{}, false
	}
	return Endpoint{Network: Network(endpoint.Network), Address: endpoint.Address}, true
}

// listen 按选项监听：unix 套接字只允许当前用户（没给地址时放进新建的私有目录），tcp 只绑定 127.0.0.1，npipe 只在 Windows 上可用、
// 只允许当前用户并拒绝远程客户端。写进握手行的地址不能含空白（handshake.md §2）：给定的地址含空白即拒绝；自动生成的套接字路径含空白
// （临时目录在带空格的用户目录下）时，没指定网络类型就改用 tcp，指定了 unix 则拒绝。
// 返回的清理函数在服务停止后调用，删除自动新建的私有目录；其余情况什么都不做。
func listen(network Network, address string) (net.Listener, Endpoint, func(), error) {
	explicit := network != networkUnset
	if !explicit {
		network = NetworkUnix
	}
	if strings.ContainsFunc(address, rules.IsWhitespace) {
		return nil, Endpoint{}, nil, fmt.Errorf("plugin: the listen address %q must not contain whitespace", address)
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
			if strings.ContainsFunc(address, rules.IsWhitespace) {
				cleanup()
				if explicit {
					return nil, Endpoint{}, nil, fmt.Errorf("plugin: the socket path %q contains whitespace and cannot be written into the handshake line; set Options.Address or listen on tcp", address)
				}
				return listen(NetworkTCP, "")
			}
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
		// 只写 127.0.0.1:<端口>：握手行的 tcp 地址只认回环地址的固定写法（handshake.md §2），主机名与 [::1] 以外的写法都不合法，
		// 本 SDK 只用其中最常见的一种
		host, _, err := net.SplitHostPort(address)
		if err != nil || host != "127.0.0.1" {
			return nil, Endpoint{}, nil, fmt.Errorf("plugin: the tcp address %q must be 127.0.0.1:<port>", address)
		}
		listener, err := net.Listen("tcp", address)
		if err != nil {
			return nil, Endpoint{}, nil, fmt.Errorf("plugin: listen on %q: %w", address, err)
		}
		bound := listener.Addr().(*net.TCPAddr)
		return listener, Endpoint{Network: NetworkTCP, Address: "127.0.0.1:" + strconv.Itoa(bound.Port)}, cleanup, nil
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

// dial 连接一个本机端点（宿主回调服务）；options 追加在默认选项之后。连接是惰性的，首次调用时才真正建立。
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
