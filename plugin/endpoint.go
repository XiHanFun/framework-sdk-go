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

// listen 按选项监听：unix 套接字放在临时目录且只允许当前用户，tcp 只绑定回环地址，npipe 只在 Windows 上可用。
func listen(network Network, address string) (net.Listener, Endpoint, error) {
	if network == networkUnset {
		network = NetworkUnix
	}
	switch network {
	case NetworkUnix:
		if address == "" {
			address = filepath.Join(os.TempDir(), "xihan-plugin-"+randomSuffix()+".sock")
		}
		_ = os.Remove(address)
		listener, err := net.Listen("unix", address)
		if err != nil {
			return nil, Endpoint{}, fmt.Errorf("plugin: listen on unix socket %q: %w", address, err)
		}
		_ = os.Chmod(address, 0o600)
		return listener, Endpoint{Network: NetworkUnix, Address: address}, nil
	case NetworkTCP:
		if address == "" {
			address = "127.0.0.1:0"
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil || (host != "127.0.0.1" && host != "::1") {
			return nil, Endpoint{}, fmt.Errorf("plugin: the tcp address %q must be a loopback address with a port", address)
		}
		listener, err := net.Listen("tcp", address)
		if err != nil {
			return nil, Endpoint{}, fmt.Errorf("plugin: listen on %q: %w", address, err)
		}
		bound := listener.Addr().(*net.TCPAddr)
		return listener, Endpoint{Network: NetworkTCP, Address: net.JoinHostPort(host, strconv.Itoa(bound.Port))}, nil
	case NetworkPipe:
		if address == "" {
			address = `\\.\pipe\xihan-plugin-` + randomSuffix()
		}
		listener, err := listenNamedPipe(address)
		if err != nil {
			return nil, Endpoint{}, err
		}
		return listener, Endpoint{Network: NetworkPipe, Address: address}, nil
	default:
		return nil, Endpoint{}, fmt.Errorf("plugin: unsupported network %q", network)
	}
}

// dial 连接一个本机端点（宿主回调服务）。
func dial(endpoint Endpoint) (*grpc.ClientConn, error) {
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
	return grpc.NewClient(
		"passthrough:///host",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return dialer(ctx) }),
	)
}

// randomSuffix 生成 8 字节随机数的十六进制。
func randomSuffix() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buffer)
}
