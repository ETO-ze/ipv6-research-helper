package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"gopkg.in/yaml.v3"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClashInjectionPreservesExistingRules(t *testing.T) {
	for _, indent := range []string{"", " ", "  "} {
		src := "proxies:\n" + indent + "- {name: old, type: direct}\nrules:\n" + indent + "- DOMAIN,old.example,DIRECT\nmode: Rule\n"
		changed := injectClash(src, "  - {name: helper, type: http}\n", "  - DOMAIN,new.example,helper\n")
		var m map[string]any
		if e := yaml.Unmarshal([]byte(changed), &m); e != nil {
			t.Fatal(e)
		}
		if len(m["proxies"].([]any)) != 2 || len(m["rules"].([]any)) != 2 || m["rules"].([]any)[1] != "DOMAIN,old.example,DIRECT" {
			t.Fatal("existing rule changed")
		}
	}
}
func TestRealUEChunkIntegrity(t *testing.T) {
	raw, e := os.ReadFile("testdata/ue-reference.chunk")
	if os.IsNotExist(e) {
		t.Skip("optional official UE fixture absent; run scripts/fetch-ue-fixture.ps1")
	}
	if e != nil {
		t.Fatal(e)
	}
	if e = validateUEChunk(raw); e != nil {
		t.Fatal(e)
	}
	raw[len(raw)-1] ^= 1
	if validateUEChunk(raw) == nil {
		t.Fatal("corrupted file accepted")
	}
	if validateUEChunk(raw[:100]) == nil {
		t.Fatal("truncated file accepted")
	}
}
func TestIPv6FailureDoesNotFallback(t *testing.T) {
	a := testApp(t)
	a.doh = &http.Client{Transport: mockRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"Status":0,"Answer":[{"Type":1,"TTL":60,"Data":"1.1.1.1"}]}`))}, nil
	})}
	c, e := a.dial(context.Background(), "tcp", "egs-cloudfront-chunks.epicgamescdn.com:443")
	if e == nil || c != nil {
		t.Fatal("IPv4-only DNS must fail")
	}
	if a.v6.Load() != 0 {
		t.Fatal("unexpected upstream")
	}
}
func TestDNSTransientEmptyAnswerRetriesIPv6(t *testing.T) {
	a := testApp(t)
	calls := 0
	a.doh = &http.Client{Transport: mockRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		body := `{"Status":0,"Answer":[]}`
		if calls == 2 {
			body = `{"Status":0,"Answer":[{"Type":28,"TTL":5,"Data":"2600:9000::1"}]}`
		}
		if r.URL.Host != "dns.alidns.com" {
			t.Fatal("left reachable resolver before retry")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	ips, e := a.resolve(context.Background(), "cdn.example")
	if e != nil || len(ips) != 1 || calls != 2 {
		t.Fatalf("retry: %v %v calls=%d", ips, e, calls)
	}
}
func TestHTTPInsideCONNECTRemapsToIPv6Transport(t *testing.T) {
	a := testApp(t)
	a.transport = &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return nil, io.EOF }}
	// Exercise the real nested HTTP server, with a mock IPv6 transport destination.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "chunk") }))
	defer origin.Close()
	a.transport = &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "egs-cloudfront-chunks.epicgamescdn.com:80" {
			t.Errorf("unexpected destination: %s", addr)
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(origin.URL, "http://"))
	}}
	server := httptest.NewServer(http.HandlerFunc(a.proxy))
	defer server.Close()
	c, e := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	io.WriteString(c, "CONNECT egs-cloudfront-chunks.epicgamescdn.com:80 HTTP/1.1\r\nHost: egs-cloudfront-chunks.epicgamescdn.com:80\r\n\r\n")
	br := bufio.NewReader(c)
	res, e := http.ReadResponse(br, nil)
	if e != nil || res.StatusCode != 200 {
		t.Fatalf("CONNECT: %v %v", res, e)
	}
	io.WriteString(c, "GET /chunk HTTP/1.1\r\nHost: egs-cloudfront-chunks.epicgamescdn.com\r\nConnection: close\r\n\r\n")
	res, e = http.ReadResponse(br, nil)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(b) != "chunk" {
		t.Fatalf("unexpected body %q", b)
	}
}

type mockRoundTripper func(*http.Request) (*http.Response, error)

func (f mockRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestDNSFollowsCNAMEWithoutIPv4(t *testing.T) {
	a := testApp(t)
	calls := 0
	a.doh = &http.Client{Transport: mockRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		body := `{"Status":0,"Answer":[{"Type":5,"TTL":30,"Data":"edge.example."}]}`
		if r.URL.Query().Get("name") == "edge.example" {
			body = `{"Status":0,"Answer":[{"Type":28,"TTL":30,"Data":"2600:9000::1"}]}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	ips, e := a.resolve(context.Background(), "cdn.example")
	if e != nil || len(ips) != 1 || calls != 2 {
		t.Fatalf("CNAME resolution %v %v calls=%d", ips, e, calls)
	}
}

func testApp(t *testing.T) *App {
	t.Helper()
	a, e := newApp(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.stop(); a.journal.Close() })
	return a
}
func TestRejectIPv4AndNonPublic(t *testing.T) {
	a := testApp(t)
	for _, s := range []string{"1.1.1.1", "::ffff:1.1.1.1", "::1", "fe80::1", "fd00::1", "::"} {
		if _, e := a.connect(context.Background(), "test", net.ParseIP(s), "443", "test"); e == nil {
			t.Fatalf("allowed %s", s)
		}
	}
	if a.v6.Load() != 0 {
		t.Fatal("connected unexpectedly")
	}
}
func TestUnknownHostBlocked(t *testing.T) {
	a := testApp(t)
	for _, u := range []string{"http://evil.example/file", "http://127.0.0.1/file", "http://download.epicgames.com.evil.example/file"} {
		r := httptest.NewRequest("GET", u, nil)
		w := httptest.NewRecorder()
		a.proxy(w, r)
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
}
func TestHostsPreservesUnrelated(t *testing.T) {
	a := testApp(t)
	a.hostsPath = filepath.Join(t.TempDir(), "hosts")
	original := []byte("# comment\r\n\r\n127.0.0.1 localhost\r\n192.0.2.1 example.org\r\n")
	os.WriteFile(a.hostsPath, original, 0600)
	if e := a.hosts(true); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(a.hostsPath)
	if !bytes.Contains(b, []byte("egs-cloudfront-chunks.epicgamescdn.com")) {
		t.Fatal("CloudFront missing")
	}
	if e := a.hosts(false); e != nil {
		t.Fatal(e)
	}
	b, _ = os.ReadFile(a.hostsPath)
	if !bytes.Equal(b, original) {
		t.Fatal("unrelated hosts changed")
	}
}
func TestHostsConflictAndMalformedBlock(t *testing.T) {
	a := testApp(t)
	a.hostsPath = filepath.Join(t.TempDir(), "hosts")
	b := []byte("127.0.0.19 download.epicgames.com #UsbEAm\r\n")
	os.WriteFile(a.hostsPath, b, 0600)
	if e := a.hosts(true); e == nil {
		t.Fatal("conflict should fail")
	}
	after, _ := os.ReadFile(a.hostsPath)
	if !bytes.Equal(b, after) {
		t.Fatal("modified conflict")
	}
	if _, e := removeBlock([]byte(markerStart + "\nno end")); e == nil {
		t.Fatal("accepted broken block")
	}
}
func TestUIRejectCSRF(t *testing.T) {
	a := testApp(t)
	r := httptest.NewRequest("POST", "http://127.0.0.1:17890/api/start", nil)
	w := httptest.NewRecorder()
	a.ui(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("GET", "http://attacker.example/api/state", nil)
	w = httptest.NewRecorder()
	a.ui(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func hello(host string) []byte {
	b := make([]byte, 34)
	b[0] = 3
	b[1] = 3
	b = append(b, 0, 0, 2, 0x13, 1, 1, 0)
	ext := []byte{0, 0, 0, 0}
	v := []byte{0, 0, 0, byte(len(host) >> 8), byte(len(host))}
	v = append(v, host...)
	binary.BigEndian.PutUint16(v, uint16(len(v)-2))
	binary.BigEndian.PutUint16(ext[2:], uint16(len(v)))
	ext = append(ext, v...)
	b = append(b, byte(len(ext)>>8), byte(len(ext)))
	b = append(b, ext...)
	return append([]byte{1, byte(len(b) >> 16), byte(len(b) >> 8), byte(len(b))}, b...)
}
func record(b []byte) []byte { return append([]byte{22, 3, 1, byte(len(b) >> 8), byte(len(b))}, b...) }
func TestTLSFragmentedSNI(t *testing.T) {
	h := "egs-cloudfront-chunks.epicgamescdn.com"
	b := hello(h)
	raw := append(record(b[:12]), record(b[12:])...)
	got, copy, e := readHello(bytes.NewReader(raw))
	if e != nil || got != h || !bytes.Equal(raw, copy) {
		t.Fatalf("%s %v", got, e)
	}
}
func TestTLSRejectUnknownAndTruncated(t *testing.T) {
	for _, b := range [][]byte{record(hello("evil.example")), record([]byte{1, 0, 1, 0}), []byte("GET / HTTP/1.1\r\n")} {
		if _, _, e := readHello(bytes.NewReader(b)); e == nil {
			t.Fatal("invalid hello accepted")
		}
	}
	if _, e := helloSNI([]byte(strings.Repeat("x", 20))); e == nil {
		t.Fatal("short hello accepted")
	}
}
