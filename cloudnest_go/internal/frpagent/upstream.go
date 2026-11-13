package frpagent

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"time"
)

// Only the locally authorized endpoints of the running configuration are used.
// A single budget bounds the complete sample even if every dial stalls.
func probeUpstreams(ctx context.Context, proxies []proxy, localPorts []int) []byte {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	allowed := make(map[int]bool, len(localPorts))
	for _, port := range localPorts {
		allowed[port] = true
	}
	type target struct {
		addr  netip.AddrPort
		state byte
	}
	unique := make(map[netip.AddrPort]*target)
	byPosition := make([]*target, len(proxies))
	for i, p := range proxies {
		ip, err := netip.ParseAddr(p.LocalIP)
		if err != nil || !ip.IsLoopback() || ip.Zone() != "" || !port(p.LocalPort) || !allowed[p.LocalPort] {
			continue
		}
		addr := netip.AddrPortFrom(ip.Unmap(), uint16(p.LocalPort))
		if unique[addr] == nil {
			unique[addr] = &target{addr: addr}
		}
		byPosition[i] = unique[addr]
	}
	jobs := make(chan *target, len(unique))
	for _, dst := range unique {
		jobs <- dst
	}
	close(jobs)
	var workers sync.WaitGroup
	for range min(8, len(unique)) {
		workers.Go(func() {
			dialer := net.Dialer{Timeout: 250 * time.Millisecond}
			for dst := range jobs {
				if ctx.Err() != nil {
					return
				}
				conn, err := dialer.DialContext(ctx, "tcp", dst.addr.String())
				if err == nil {
					conn.Close()
					dst.state = 1
				} else if ctx.Err() == nil {
					dst.state = 2
				}
			}
		})
	}
	workers.Wait()
	states := make([]byte, (len(proxies)+3)/4)
	for i, dst := range byPosition {
		if dst != nil {
			states[i/4] |= dst.state << (2 * (i % 4))
		}
	}
	return states
}
