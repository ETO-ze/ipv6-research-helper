package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"

	"fmt"
	"io"

	"net"
	"net/http"
	"os"
	"os/exec"

	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed dashboard.html
var dashboard string

//go:embed network-tools.ps1
var networkTools string

const bindIP = "127.0.0.23"
const markerStart = "# BEGIN EpicIPv6Helper"
const markerEnd = "# END EpicIPv6Helper"

var allowed = map[string]string{
	"download.epicgames.com": "", "download2.epicgames.com": "", "download3.epicgames.com": "", "download4.epicgames.com": "",
	"epicgames-download1.akamaized.net": "steamusercontent-a.akamaihd.net.edgesuite.net",
	"fastly-download.epicgames.com":     "dualstack.n.sni.global.fastly.net",
	"egdownload.fastly-edge.com":        "dualstack.n.sni.global.fastly.net",
	"cloudflare.epicgamescdn.com":       "", "egs-cloudfront-chunks.epicgamescdn.com": "",
	"epicgames-download1-1251447533.file.myqcloud.com": "",
	"cdn.unrealengine.com":                             "", "cdn1.unrealengine.com": "", "cdn2.unrealengine.com": "", "cdn3.unrealengine.com": "",
	"cdn1.epicgames.com": "", "cdn2.epicgames.com": "",
}

type Entry struct {
	ID     uint64 `json:"id"`
	Time   string `json:"time"`
	Host   string `json:"host"`
	Remote string `json:"remote"`
	Kind   string `json:"kind"`
	Active bool   `json:"active"`
	Down   int64  `json:"down"`
	Up     int64  `json:"up"`
	Error  string `json:"error,omitempty"`
}
type cacheItem struct {
	ips      []net.IP
	expires  time.Time
	attempts []DNSAttempt
}
type App struct {
	nodeMu                sync.Mutex
	nodePolicies          map[string]NodePolicy
	research              *researchManager
	startupError          string
	verifyMu              sync.Mutex
	mu                    sync.Mutex
	networkMu             sync.Mutex
	listenerMu            sync.Mutex
	stopping              bool
	entries               []*Entry
	active                map[uint64]net.Conn
	cache                 map[string]cacheItem
	serial                atomic.Uint64
	down                  atomic.Int64
	up                    atomic.Int64
	v6                    atomic.Uint64
	denied                atomic.Uint64
	dir                   string
	token                 string
	hostsPath             string
	managed               bool
	transport             *http.Transport
	doh                   *http.Client
	journal               *os.File
	listeners             []net.Listener
	done                  chan struct{}
	stopOnce              sync.Once
	started               time.Time
	steamListenersStarted bool
	steamRestoring        atomic.Bool
	steamRouteEpoch       atomic.Uint64
}

func norm(h string) string { return strings.TrimSuffix(strings.ToLower(h), ".") }
func allowedHost(h string) bool {
	_, ok := allowed[norm(h)]
	return ok || researchHost(h) || steamHost(h)
}
func publicV6(ip net.IP) bool {
	return ip != nil && ip.To4() == nil && ip.IsGlobalUnicast() && !ip.IsPrivate()
}
func newApp(dir string) (*App, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "connections.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	token := make([]byte, 24)
	if _, err = rand.Read(token); err != nil {
		return nil, err
	}
	a := &App{dir: dir, token: hex.EncodeToString(token), hostsPath: filepath.Join(os.Getenv("SystemRoot"), "System32", "drivers", "etc", "hosts"), journal: f, active: map[uint64]net.Conn{}, cache: map[string]cacheItem{}, done: make(chan struct{}), started: time.Now()}
	// DNS also travels over IPv6 HTTPS, independent of system proxy/hosts.
	dt := &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		h, _, _ := net.SplitHostPort(addr)
		ip := "2606:4700:4700::1111"
		if h == "dns.google" {
			ip = "2001:4860:4860::8888"
		}
		if h == "dns.alidns.com" {
			ip = "2400:3200::1"
		}
		return a.connect(ctx, "DNS "+h, net.ParseIP(ip), "443", "dns6")
	}}
	a.doh = &http.Client{Transport: dt, Timeout: 12 * time.Second, CheckRedirect: func(r *http.Request, v []*http.Request) error { return errors.New("DNS redirect denied") }}
	a.transport = &http.Transport{Proxy: nil, DialContext: a.dial, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, MaxIdleConns: 64, MaxIdleConnsPerHost: 16, IdleConnTimeout: 60 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second, DisableCompression: true, ForceAttemptHTTP2: false}
	a.transport.DialTLSContext = a.dialTLS
	if err := a.initNodePolicies(); err != nil {
		f.Close()
		return nil, err
	}
	if err := a.initResearch(); err != nil {
		f.Close()
		return nil, err
	}
	return a, nil
}
func (a *App) record(e *Entry) { b, _ := json.Marshal(e); a.journal.Write(append(b, '\n')) }
func (a *App) errorEvent(host string, err error) {
	a.denied.Add(1)
	a.mu.Lock()
	defer a.mu.Unlock()
	e := &Entry{ID: a.serial.Add(1), Time: time.Now().Format(time.RFC3339), Host: host, Kind: "error", Error: err.Error()}
	a.entries = append(a.entries, e)
	a.trim()
	a.record(e)
}
func (a *App) trim() {
	if len(a.entries) > 250 { // Keep every active entry, plus the most recent closed entries.
		keep := make([]*Entry, 0, 250)
		for i, e := range a.entries {
			if e.Active || i >= len(a.entries)-150 {
				keep = append(keep, e)
			}
		}
		a.entries = keep
	}
}

type countedConn struct {
	net.Conn
	a    *App
	e    *Entry
	once sync.Once
}

func (c *countedConn) Read(b []byte) (int, error) {
	n, e := c.Conn.Read(b)
	c.a.down.Add(int64(n))
	c.a.mu.Lock()
	c.e.Down += int64(n)
	c.a.mu.Unlock()
	return n, e
}
func (c *countedConn) Write(b []byte) (int, error) {
	n, e := c.Conn.Write(b)
	c.a.up.Add(int64(n))
	c.a.mu.Lock()
	c.e.Up += int64(n)
	c.a.mu.Unlock()
	return n, e
}
func (c *countedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		c.a.mu.Lock()
		defer c.a.mu.Unlock()
		c.e.Active = false
		delete(c.a.active, c.e.ID)
		c.a.record(c.e)
	})
	return err
}
func (a *App) connect(ctx context.Context, host string, ip net.IP, port, kind string) (net.Conn, error) {
	if !publicV6(ip) {
		return nil, errors.New("non-public or IPv4 upstream denied")
	}
	steam := kind != "dns6" && steamHost(host)
	steamEpoch := a.steamRouteEpoch.Load()
	if steam && a.steamRestoring.Load() {
		return nil, errors.New("Steam 接管正在恢复，未建立新的下载连接")
	}
	if kind != "dns6" {
		a.nodeMu.Lock()
		ok := a.nodeAllowedLocked(host, ip.String())
		a.nodeMu.Unlock()
		if !ok {
			return nil, errors.New("上游 IPv6 已被拉黑或不是固定地址")
		}
	}
	d := net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	c, e := d.DialContext(ctx, "tcp6", net.JoinHostPort(ip.String(), port))
	if e != nil {
		return nil, e
	}
	remote, ok := c.RemoteAddr().(*net.TCPAddr)
	if !ok || !publicV6(remote.IP) {
		c.Close()
		return nil, errors.New("remote is not public IPv6")
	}
	a.nodeMu.Lock()
	defer a.nodeMu.Unlock()
	if kind != "dns6" && !a.nodeAllowedLocked(host, ip.String()) {
		c.Close()
		return nil, errors.New("连接期间上游策略已更改")
	}
	en := &Entry{ID: a.serial.Add(1), Time: time.Now().Format(time.RFC3339), Host: host, Remote: c.RemoteAddr().String(), Kind: kind, Active: true}
	cc := &countedConn{Conn: c, a: a, e: en}
	a.mu.Lock()
	if steam && (a.steamRestoring.Load() || a.steamRouteEpoch.Load() != steamEpoch) {
		a.mu.Unlock()
		c.Close()
		return nil, errors.New("Steam 接管在连接期间已恢复，下载连接已关闭")
	}
	a.v6.Add(1)
	a.entries = append(a.entries, en)
	a.active[en.ID] = cc
	a.trim()
	a.record(en)
	a.mu.Unlock()
	return cc, nil
}
func (a *App) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, e := net.SplitHostPort(addr)
	if e != nil {
		return nil, e
	}
	host = norm(host)
	if !allowedHost(host) || (port != "80" && port != "443") {
		return nil, errors.New("destination not in download allowlist")
	}
	ips, e := a.nodeIPs(ctx, host)
	if e != nil {
		return nil, e
	}
	var last error
	for _, ip := range ips {
		c, e := a.connect(ctx, host, ip, port, "download6")
		if e == nil {
			return c, nil
		}
		last = e
	}
	return nil, fmt.Errorf("all IPv6 candidates failed for %s: %v", host, last)
}
func stripHop(h http.Header) {
	for _, v := range h.Values("Connection") {
		for _, s := range strings.Split(v, ",") {
			h.Del(strings.TrimSpace(s))
		}
	}
	for _, s := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Del(s)
	}
}
func (a *App) proxy(w http.ResponseWriter, r *http.Request) {
	host := norm(r.Host)
	if h, _, e := net.SplitHostPort(host); e == nil {
		host = h
	}
	if !allowedHost(host) {
		a.errorEvent(host, errors.New("unknown destination blocked"))
		http.Error(w, "Not an allowed download host", 403)
		return
	}
	if r.Method == "CONNECT" {
		a.connectProxy(w, r, host)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		http.Error(w, "Download requests only", 405)
		return
	}
	req := r.Clone(r.Context())
	req.RequestURI = ""
	if req.URL.Scheme == "" {
		req.URL.Scheme = "http"
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		http.Error(w, "Scheme denied", 400)
		return
	}
	req.URL.Host = r.Host
	// HTTP CDN remapping is restricted to known Epic mirrors and preserves escaped path/query.
	if req.URL.Scheme == "http" && (strings.HasSuffix(host, ".myqcloud.com") || host == "download.epicgames.com" || host == "download2.epicgames.com" || host == "download3.epicgames.com" || host == "download4.epicgames.com") {
		req.URL.Scheme = "https"
		req.URL.Host = "epicgames-download1.akamaized.net"
		req.Host = req.URL.Host
	}
	stripHop(req.Header)
	res, e := a.transport.RoundTrip(req)
	if e != nil {
		a.errorEvent(host, e)
		http.Error(w, "IPv6 upstream unavailable; IPv4 fallback disabled", 502)
		return
	}
	defer res.Body.Close()
	// Redirects to unknown hosts fail closed; HTTPS tunnel redirects remain encrypted and need OS audit.
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		loc, e := res.Location()
		if e != nil || !allowedHost(loc.Hostname()) {
			a.errorEvent(host, errors.New("redirect to unknown destination blocked"))
			http.Error(w, "Redirect destination denied", 502)
			return
		}
	}
	stripHop(res.Header)
	for k, v := range res.Header {
		for _, s := range v {
			w.Header().Add(k, s)
		}
	}
	w.WriteHeader(res.StatusCode)
	io.Copy(w, res.Body)
}
func bridge(c net.Conn, r io.Reader, u net.Conn) {
	defer c.Close()
	defer u.Close()
	done := make(chan struct{}, 1)
	go func() { io.Copy(u, r); done <- struct{}{} }()
	io.Copy(c, u)
	c.Close()
	u.Close()
	<-done
}
func (a *App) connectProxy(w http.ResponseWriter, r *http.Request, host string) {
	_, port, e := net.SplitHostPort(r.Host)
	if e != nil || (port != "443" && port != "80") {
		http.Error(w, "CONNECT requires port 80 or 443", 403)
		return
	}
	if port == "80" {
		a.connectHTTP(w, r, host)
		return
	}
	u, e := a.dial(r.Context(), "tcp6", net.JoinHostPort(host, "443"))
	if e != nil {
		a.errorEvent(host, e)
		http.Error(w, "IPv6 unavailable", 502)
		return
	}
	h, ok := w.(http.Hijacker)
	if !ok {
		u.Close()
		return
	}
	c, rw, e := h.Hijack()
	if e != nil {
		u.Close()
		return
	}
	rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	rw.Flush()
	bridge(c, rw, u)
}

// Parse fragmented TLS ClientHello records without terminating or weakening TLS.
func readHello(r io.Reader) (string, []byte, error) {
	var raw, hs []byte
	for len(raw) < 131072 {
		hdr := make([]byte, 5)
		if _, e := io.ReadFull(r, hdr); e != nil {
			return "", raw, e
		}
		n := int(binary.BigEndian.Uint16(hdr[3:]))
		if hdr[0] != 22 || n > 18432 || n == 0 {
			return "", raw, errors.New("invalid TLS handshake record")
		}
		body := make([]byte, n)
		if _, e := io.ReadFull(r, body); e != nil {
			return "", raw, e
		}
		raw = append(raw, hdr...)
		raw = append(raw, body...)
		hs = append(hs, body...)
		if len(hs) < 4 {
			continue
		}
		total := int(hs[1])<<16 | int(hs[2])<<8 | int(hs[3])
		if hs[0] != 1 || total > 65536 {
			return "", raw, errors.New("invalid ClientHello")
		}
		if len(hs) < total+4 {
			continue
		}
		host, e := helloSNI(hs[4 : total+4])
		return host, raw, e
	}
	return "", raw, errors.New("ClientHello too large")
}
func helloSNI(b []byte) (string, error) {
	bad := errors.New("missing or malformed SNI")
	if len(b) < 35 {
		return "", bad
	}
	p := 34
	p += 1 + int(b[p])
	if p+2 > len(b) {
		return "", bad
	}
	n := int(binary.BigEndian.Uint16(b[p:]))
	p += 2 + n
	if p >= len(b) {
		return "", bad
	}
	p += 1 + int(b[p])
	if p+2 > len(b) {
		return "", bad
	}
	n = int(binary.BigEndian.Uint16(b[p:]))
	p += 2
	end := p + n
	if end > len(b) {
		return "", bad
	}
	for p+4 <= end {
		typ := binary.BigEndian.Uint16(b[p:])
		n = int(binary.BigEndian.Uint16(b[p+2:]))
		p += 4
		if p+n > end {
			return "", bad
		}
		if typ == 0 {
			v := b[p : p+n]
			if len(v) < 5 {
				return "", bad
			}
			ln := int(binary.BigEndian.Uint16(v))
			if ln != len(v)-2 {
				return "", bad
			}
			for q := 2; q+3 <= len(v); {
				t := v[q]
				l := int(binary.BigEndian.Uint16(v[q+1:]))
				q += 3
				if q+l > len(v) {
					return "", bad
				}
				if t == 0 {
					h := norm(string(v[q : q+l]))
					if !allowedHost(h) {
						return "", errors.New("SNI outside allowlist")
					}
					return h, nil
				}
				q += l
			}
		}
		p += n
	}
	return "", bad
}
func (a *App) tlsTunnel(c net.Conn) {
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	h, raw, e := readHello(c)
	if e != nil {
		a.errorEvent("TLS", e)
		c.Close()
		return
	}
	c.SetReadDeadline(time.Time{})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	u, e := a.dial(ctx, "tcp6", net.JoinHostPort(h, "443"))
	if e != nil {
		a.errorEvent(h, e)
		c.Close()
		return
	}
	if _, e = u.Write(raw); e != nil {
		c.Close()
		u.Close()
		return
	}
	bridge(c, c, u)
}
func hostnames() []string {
	v := make([]string, 0, len(allowed))
	for h := range allowed {
		v = append(v, h)
	}
	sort.Strings(v)
	return v
}
func removeBlock(b []byte) ([]byte, error) {
	s := string(b)
	start := strings.Index(s, markerStart)
	if start < 0 {
		return b, nil
	}
	end := strings.Index(s[start:], markerEnd)
	if end < 0 {
		return nil, errors.New("hosts marker incomplete; manual recovery required")
	}
	end += start + len(markerEnd)
	if strings.HasPrefix(s[end:], "\r\n") {
		end += 2
	} else if strings.HasPrefix(s[end:], "\n") {
		end++
	}
	return []byte(s[:start] + s[end:]), nil
}
func (a *App) hosts(enable bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, e := os.ReadFile(a.hostsPath)
	if e != nil {
		return e
	}
	clean, e := removeBlock(b)
	if e != nil {
		return e
	}
	if enable {
		for _, line := range strings.Split(string(clean), "\n") {
			fields := strings.Fields(strings.SplitN(line, "#", 2)[0])
			if len(fields) < 2 {
				continue
			}
			for _, h := range fields[1:] {
				if allowedHost(h) {
					return fmt.Errorf("existing hosts rule for %s; stop UsbEAm / remove conflict first", h)
				}
			}
		}
		backup := filepath.Join(a.dir, "hosts-before-"+time.Now().Format("20060102-150405.000")+".bak")
		if e = os.WriteFile(backup, b, 0600); e != nil {
			return e
		}
		if len(clean) > 0 && clean[len(clean)-1] != '\n' {
			clean = append(clean, '\r', '\n')
		}
		clean = append(clean, []byte(markerStart+"\r\n")...)
		for _, h := range hostnames() {
			clean = append(clean, []byte(bindIP+" "+h+"\r\n")...)
		}
		clean = append(clean, []byte(markerEnd+"\r\n")...)
	}
	if !bytes.Equal(b, clean) {
		if e = os.WriteFile(a.hostsPath, clean, 0644); e != nil {
			return fmt.Errorf("hosts write requires Administrator: %w", e)
		}
	}
	a.managed = enable
	go exec.Command("ipconfig", "/flushdns").Run()
	return nil
}
func (a *App) state() any {
	a.mu.Lock()
	defer a.mu.Unlock()
	entries := make([]Entry, len(a.entries))
	for i, e := range a.entries {
		entries[i] = *e
	}
	_, bypassErr := os.Stat(filepath.Join(a.dir, "proxy-bypass-state.json"))
	return map[string]any{"application": productID, "version": appVersion, "startupError": a.startupError, "dataDirectory": a.dir, "started": a.started.Format(time.RFC3339), "pid": os.Getpid(), "hostsActive": a.managed, "steamActive": a.steamActive(), "proxyBypassActive": bypassErr == nil, "epicProxyActive": a.epicProxyActive(), "clashActive": a.clashActive(), "ipv6Connections": a.v6.Load(), "ipv4Connections": 0, "down": a.down.Load(), "up": a.up.Load(), "blocked": a.denied.Load(), "entries": entries, "domains": hostnames(), "scope": "Helper upstream TCP and DNS use IPv6 only. Epic bypass traffic and encrypted redirects require external audit."}
}
func (a *App) epicProxyActive() bool {
	_, e := os.Stat(filepath.Join(a.dir, "epic-install-proxy-state.json"))
	return e == nil
}
func (a *App) networkAction(action string) error {
	a.networkMu.Lock()
	defer a.networkMu.Unlock()
	if action == "RestoreGuard" || action == "RestoreBypass" || action == "RestoreEpicProxy" {
		name := "ipv4-guard-state.json"
		if action == "RestoreEpicProxy" {
			name = "epic-install-proxy-state.json"
		}
		if action == "RestoreBypass" {
			name = "proxy-bypass-state.json"
		}
		if _, e := os.Stat(filepath.Join(a.dir, name)); os.IsNotExist(e) {
			return nil
		}
	}
	p := filepath.Join(a.dir, "network-tools.generated.ps1")
	if e := os.WriteFile(p, append([]byte{0xef, 0xbb, 0xbf}, []byte(networkTools)...), 0600); e != nil {
		return e
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", p, "-Action", action, "-StateDirectory", a.dir)
	hideCommand(cmd)
	b, e := cmd.CombinedOutput()
	if e != nil {
		return fmt.Errorf("%s: %s", action, string(b))
	}
	return nil
}
func (a *App) probe(ctx context.Context) any {
	urls := []string{"https://epicgames-download1.akamaized.net/Builds/UnrealEngineLauncher/Installers/Windows/EpicInstaller-19.2.3.msi", "https://egs-cloudfront-chunks.epicgamescdn.com/Builds/UnrealEngineLauncher/Installers/Windows/EpicInstaller-19.2.3.msi"}
	out := []any{}
	for _, u := range urls {
		row := map[string]any{"url": u}
		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		req.Header.Set("Range", "bytes=0-1048575")
		start := time.Now()
		res, e := a.transport.RoundTrip(req)
		if e != nil {
			row["error"] = e.Error()
		} else {
			b, e := io.ReadAll(io.LimitReader(res.Body, 1048577))
			res.Body.Close()
			sum := sha256.Sum256(b)
			row["status"] = res.StatusCode
			row["bytes"] = len(b)
			row["sha256"] = hex.EncodeToString(sum[:])
			row["range"] = res.Header.Get("Content-Range")
			row["passed"] = e == nil && res.StatusCode == 206 && len(b) == 1048576
		}
		row["seconds"] = time.Since(start).Seconds()
		out = append(out, row)
	}
	return out
}
func (a *App) ui(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	if r.Host != "127.0.0.1:17890" {
		http.Error(w, "Invalid host", 403)
		return
	}
	if r.URL.Path == "/" && r.Method == "GET" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, strings.ReplaceAll(dashboard, "__TOKEN__", a.token))
		return
	}
	if r.URL.Path == "/api/state" && r.Method == "GET" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(a.state())
		return
	}
	if r.URL.Path == "/api/research" && r.Method == "GET" {
		a.researchAPI(w, r)
		return
	}
	if r.URL.Path == "/api/routing" && r.Method == "GET" {
		a.routingAPI(w, r)
		return
	}
	if r.URL.Path == "/api/upstreams" && r.Method == "GET" {
		a.upstreamAPI(w, r)
		return
	}
	if r.URL.Path == "/api/steam" && r.Method == "GET" {
		a.steamAPI(w, r)
		return
	}
	if r.Method != "POST" || r.Header.Get("X-Token") != a.token || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://127.0.0.1:17890") {
		http.Error(w, "Forbidden", 403)
		return
	}
	var e error
	if strings.HasPrefix(r.URL.Path, "/api/upstreams/") {
		a.upstreamAPI(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/steam/") {
		a.steamAPI(w, r)
		return
	}
	if r.URL.Path == "/api/routing/start" {
		a.routingAPI(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/research/") {
		a.researchAPI(w, r)
		return
	}
	switch r.URL.Path {
	case "/api/start":
		e = a.hosts(true)
	case "/api/stop":
		e = a.restoreSteam()
		if e == nil {
			e = a.hosts(false)
		}
	case "/api/clash":
		e = a.clashAction("enable")
	case "/api/restore-clash":
		e = a.clashAction("restore")
	case "/api/bypass":
		e = a.networkAction("EnableBypass")
	case "/api/restore-bypass":
		e = a.networkAction("RestoreBypass")
	case "/api/probe":
		ctx, cancel := context.WithTimeout(r.Context(), 70*time.Second)
		defer cancel()
		json.NewEncoder(w).Encode(a.probe(ctx))
		return
	case "/api/verify":
		ctx, cancel := context.WithTimeout(r.Context(), 100*time.Second)
		defer cancel()
		json.NewEncoder(w).Encode(a.verify(ctx))
		return
	case "/api/shutdown":
		if e = a.restoreSteam(); e == nil {
			e = a.clashAction("restore")
		}
		if e == nil {
			e = a.networkAction("RestoreEpicProxy")
		}
		if e == nil {
			e = a.networkAction("RestoreBypass")
		}
		if e == nil {
			e = a.networkAction("RestoreGuard")
		}
		if e == nil {
			e = a.hosts(false)
		}
		if e == nil {
			go func() { time.Sleep(200 * time.Millisecond); a.stop() }()
		}
	default:
		http.NotFound(w, r)
		return
	}
	if e != nil {
		http.Error(w, e.Error(), 409)
		return
	}
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
func (a *App) stop() {
	a.stopOnce.Do(func() {
		a.listenerMu.Lock()
		a.stopping = true
		listeners := append([]net.Listener{}, a.listeners...)
		a.listenerMu.Unlock()
		if a.research != nil {
			a.research.shutdown()
		}
		for _, l := range listeners {
			l.Close()
		}
		a.transport.CloseIdleConnections()
		a.doh.CloseIdleConnections()
		a.mu.Lock()
		var conns []net.Conn
		for _, c := range a.active {
			conns = append(conns, c)
		}
		a.mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
		close(a.done)
	})
}
