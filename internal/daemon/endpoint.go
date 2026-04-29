package daemon

import (
	"context"
	"fmt"
	"net"
	"strings"
)

// DaemonEndpoint abstracts the listen / dial pair for either a Unix socket or
// TCP loopback. Address() returns a stable string representation.
//
//nolint:revive // DaemonEndpoint name is fixed by Plan 1 §2.2 public spec.
type DaemonEndpoint interface {
	Listen() (net.Listener, error)
	Dial(ctx context.Context) (net.Conn, error)
	Address() string
	Kind() string // "unix" | "tcp"
}

type unixEndpoint struct{ path string }

func (u unixEndpoint) Listen() (net.Listener, error) {
	return net.Listen("unix", u.path)
}

func (u unixEndpoint) Dial(ctx context.Context) (net.Conn, error) {
	d := net.Dialer{}
	return d.DialContext(ctx, "unix", u.path)
}

func (u unixEndpoint) Address() string { return "unix://" + u.path }
func (u unixEndpoint) Kind() string    { return "unix" }

// UnixEndpoint constructs a Unix-socket endpoint at the given path.
func UnixEndpoint(path string) DaemonEndpoint { return unixEndpoint{path: path} }

type tcpEndpoint struct{ addr string }

func (t tcpEndpoint) Listen() (net.Listener, error) {
	if err := requireLoopback(t.addr); err != nil {
		return nil, err
	}
	return net.Listen("tcp", t.addr)
}

func (t tcpEndpoint) Dial(ctx context.Context) (net.Conn, error) {
	if err := requireLoopback(t.addr); err != nil {
		return nil, err
	}
	d := net.Dialer{}
	return d.DialContext(ctx, "tcp", t.addr)
}

func (t tcpEndpoint) Address() string { return t.addr }
func (t tcpEndpoint) Kind() string    { return "tcp" }

// TCPEndpoint constructs a TCP-loopback endpoint at the given host:port.
func TCPEndpoint(addr string) DaemonEndpoint { return tcpEndpoint{addr: addr} }

func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse host:port: %w", err)
	}
	if host == "127.0.0.1" || host == "::1" || host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("address %q is not loopback", addr)
}

// ParseAddress decodes a serialized form (unix:///path or host:port).
func ParseAddress(s string) (DaemonEndpoint, error) {
	if strings.HasPrefix(s, "unix://") {
		return UnixEndpoint(strings.TrimPrefix(s, "unix://")), nil
	}
	if strings.Contains(s, ":") {
		return TCPEndpoint(s), nil
	}
	return nil, fmt.Errorf("unrecognized address: %q", s)
}
