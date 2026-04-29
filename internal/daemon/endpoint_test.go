package daemon_test

import (
	"context"
	"net"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/daemon"
)

func TestUnixEndpoint_RoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets unsupported on windows")
	}
	sock := filepath.Join(t.TempDir(), "daemon.sock")
	ep := daemon.UnixEndpoint(sock)

	l, err := ep.Listen()
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	go func() {
		c, _ := l.Accept()
		if c != nil {
			_, _ = c.Write([]byte("ok"))
			_ = c.Close()
		}
	}()

	conn, err := ep.Dial(context.Background())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	buf := make([]byte, 2)
	_, err = conn.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "ok", string(buf))
	assert.Equal(t, "unix://"+sock, ep.Address())
}

func TestTCPEndpoint_RoundTrip(t *testing.T) {
	ep := daemon.TCPEndpoint("127.0.0.1:0")
	l, err := ep.Listen()
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	// Hand the actually-bound address back into a fresh endpoint for Dial.
	addr := l.Addr().(*net.TCPAddr).String()
	dialEP := daemon.TCPEndpoint(addr)
	go func() {
		c, _ := l.Accept()
		if c != nil {
			_, _ = c.Write([]byte("ok"))
			_ = c.Close()
		}
	}()

	conn, err := dialEP.Dial(context.Background())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	buf := make([]byte, 2)
	_, err = conn.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "ok", string(buf))
}

func TestTCPEndpoint_RejectsNonLoopback(t *testing.T) {
	ep := daemon.TCPEndpoint("8.8.8.8:7474")
	_, err := ep.Listen()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loopback")
}

func TestTCPEndpoint_RejectsLocalhostHostname(t *testing.T) {
	ep := daemon.TCPEndpoint("localhost:7474")
	_, err := ep.Listen()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "literal IP")
}

func TestParseAddress(t *testing.T) {
	cases := []struct {
		in   string
		kind string
	}{
		{"unix:///tmp/foo.sock", "unix"},
		{"127.0.0.1:7474", "tcp"},
		{"localhost:7474", "tcp"},
	}
	for _, tc := range cases {
		ep, err := daemon.ParseAddress(tc.in)
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.kind, ep.Kind(), tc.in)
	}
}
