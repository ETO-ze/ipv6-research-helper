package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

type DNSAttempt struct {
	Provider string `json:"provider"`
	Host     string `json:"host"`
	Code     string `json:"code"`
}
type DNSFailure struct {
	Code     string
	Attempts []DNSAttempt
	Cause    error
}

func (e *DNSFailure) Error() string { return "IPv6 DNS: " + e.Code }
func (e *DNSFailure) Unwrap() error { return e.Cause }

// Follow aliases within the same resolver and time budget. A broken CNAME chain
// must not consume the budgets reserved for the other DNS services.
func (a *App) queryAAAA(ctx context.Context, base, host string, depth int) ([]net.IP, int, []DNSAttempt, error) {
	endpoint, _ := url.Parse(base)
	attempt := DNSAttempt{Provider: endpoint.Hostname(), Host: host}
	fail := func(code string, cause error) ([]net.IP, int, []DNSAttempt, error) {
		attempt.Code = code
		return nil, 0, []DNSAttempt{attempt}, &DNSFailure{Code: code, Cause: cause}
	}
	if depth > 8 {
		return fail("dns_cname_loop", nil)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"?name="+url.QueryEscape(host)+"&type=AAAA", nil)
	req.Header.Set("Accept", "application/dns-json")
	req.Header.Set("Cache-Control", "no-cache")
	res, err := a.doh.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fail("dns_timeout", err)
		}
		return fail("dns_error", err)
	}
	var answer struct {
		Status int
		Answer []struct {
			Type int
			TTL  int
			Data string
		}
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&answer)
	res.Body.Close()
	if err != nil || res.StatusCode != 200 {
		return fail("dns_error", err)
	}
	if answer.Status == 3 {
		return fail("dns_nxdomain", nil)
	}
	if answer.Status != 0 {
		return fail(fmt.Sprintf("dns_rcode_%d", answer.Status), nil)
	}
	var ips []net.IP
	cname := ""
	ttl := 300
	for _, record := range answer.Answer {
		if record.Type == 5 {
			cname = norm(record.Data)
		}
		if (record.Type == 5 || record.Type == 28) && record.TTL < ttl {
			ttl = record.TTL
		}
		if record.Type == 28 {
			if ip := net.ParseIP(record.Data); publicV6(ip) {
				ips = append(ips, ip)
			}
		}
	}
	if len(ips) > 0 {
		attempt.Code = "dns_ipv6"
		return ips, ttl, []DNSAttempt{attempt}, nil
	}
	if cname != "" {
		if cname == host {
			return fail("dns_cname_loop", nil)
		}
		var childTTL int
		var trace []DNSAttempt
		ips, childTTL, trace, err = a.queryAAAA(ctx, base, cname, depth+1)
		if childTTL < ttl {
			ttl = childTTL
		}
		attempt.Code = "dns_cname"
		return ips, ttl, append([]DNSAttempt{attempt}, trace...), err
	}
	return fail("dns_no_aaaa", nil)
}
func (a *App) resolve(ctx context.Context, host string) ([]net.IP, error) {
	host = norm(host)
	a.mu.Lock()
	item, ok := a.cache[host]
	a.mu.Unlock()
	if ok && time.Now().Before(item.expires) {
		return item.ips, nil
	}
	var attempts []DNSAttempt
	var cause error
	noAAAA, nonexistent, timeout, loop := false, false, false, false
	for _, base := range []string{"https://dns.alidns.com/resolve", "https://dns.alidns.com/resolve", "https://cloudflare-dns.com/dns-query", "https://dns.google/resolve"} {
		if ctx.Err() != nil {
			cause = ctx.Err()
			break
		}
		queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		ips, ttl, trace, err := a.queryAAAA(queryCtx, base, host, 0)
		cancel()
		attempts = append(attempts, trace...)
		if err == nil && len(ips) > 0 {
			if ttl < 1 {
				ttl = 1
			}
			a.mu.Lock()
			a.cache[host] = cacheItem{ips: ips, expires: time.Now().Add(time.Duration(ttl) * time.Second), attempts: attempts}
			a.mu.Unlock()
			return ips, nil
		}
		cause = err
		var dns *DNSFailure
		if errors.As(err, &dns) {
			switch dns.Code {
			case "dns_no_aaaa":
				noAAAA = true
			case "dns_nxdomain":
				nonexistent = true
			case "dns_timeout":
				timeout = true
			case "dns_cname_loop":
				loop = true
			}
		}
	}
	code := "dns_error"
	if timeout || errors.Is(cause, context.DeadlineExceeded) {
		code = "dns_timeout"
	}
	if loop {
		code = "dns_cname_loop"
	}
	// Preserve valid negative answers even when a later service is unreachable.
	if nonexistent {
		code = "dns_nxdomain"
	}
	if noAAAA {
		code = "dns_no_aaaa"
	}
	if ctx.Err() != nil {
		cause = ctx.Err()
		if errors.Is(ctx.Err(), context.Canceled) {
			code = "canceled"
		}
	}
	return nil, &DNSFailure{Code: code, Attempts: attempts, Cause: cause}
}
