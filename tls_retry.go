package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"time"
)

// Some CDN IPv6 addresses accept TCP but fail during TLS. Try the remaining
// IPv6 addresses while retaining hostname verification; never retry with IPv4.
func (a *App) dialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, e := net.SplitHostPort(addr)
	if e != nil {
		return nil, e
	}
	host = norm(host)
	if !allowedHost(host) || port != "443" {
		return nil, errors.New("destination not allowed")
	}
	ips, e := a.nodeIPs(ctx, host)
	if e != nil {
		return nil, e
	}
	var last error
	for _, ip := range ips {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		raw, e := a.connect(ctx, host, ip, port, "download6")
		if e != nil {
			last = e
			continue
		}
		c := tls.Client(raw, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}})
		handshake, cancel := context.WithTimeout(ctx, 6*time.Second)
		e = c.HandshakeContext(handshake)
		cancel()
		if e == nil {
			return c, nil
		}
		c.Close()
		last = e
	}
	if last == nil {
		last = errors.New("no IPv6 TLS endpoint")
	}
	return nil, last
}
