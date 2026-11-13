package frpagent

import (
	"context"
	"net"
	"testing"
)

func TestProbeUpstreamsLoopbackOnly(t *testing.T) {
	open, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	closed, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := open.Addr().(*net.TCPAddr).Port
	refused := closed.Addr().(*net.TCPAddr).Port
	closed.Close()

	proxies := []proxy{
		{LocalIP: "127.0.0.1", LocalPort: port},
		{LocalIP: "127.0.0.1", LocalPort: refused},
		{LocalIP: "127.0.0.1", LocalPort: port},
		{LocalIP: "192.0.2.1", LocalPort: port},
		{LocalIP: "localhost", LocalPort: port},
		{LocalIP: "127.0.0.1", LocalPort: 1},
	}
	got := probeUpstreams(t.Context(), proxies, []int{port, refused})
	if len(got) != 2 || got[0] != 0x19 || got[1] != 0 {
		t.Fatalf("unexpected upstream states: %x", got)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got = probeUpstreams(ctx, proxies, []int{port, refused})
	if got[0] != 0 || got[1] != 0 {
		t.Fatalf("cancelled probes should stay unknown: %x", got)
	}
}
