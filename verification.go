package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const ueChunkPath = "Builds/UE5/Releases/CloudDir/ChunksV4/32/A5B3747FCABAF49B_5EA5DD4B4EEDE55022795F86E3C7EF06.chunk"
const ueChunkSHA256 = "62b08120c9ea6edccfdc02a20ea1bee202529590b628abb34cee017bd71c8d7c"
const uePayloadSHA1 = "f198ed31f74fe8603a55040f105f807d4d7da98e"

type Check struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}
type Verification struct {
	Version  string  `json:"version"`
	Started  string  `json:"started"`
	Finished string  `json:"finished"`
	Passed   bool    `json:"passed"`
	Scope    string  `json:"scope"`
	Checks   []Check `json:"checks"`
	Report   string  `json:"report"`
}

func (a *App) checkClashRoute() error {
	b, e := os.ReadFile(filepath.Join(a.dir, "clash-state.json"))
	if e != nil {
		return e
	}
	var s clashState
	if e = json.Unmarshal(b, &s); e != nil {
		return e
	}
	c, _, e := clashAPI(s.Data)
	if e != nil {
		return e
	}
	var config struct{ Mode string }
	if e = c.call("GET", "/configs", nil, &config); e != nil {
		return e
	}
	if strings.ToLower(config.Mode) != "rule" {
		return errors.New("Clash is no longer in Rule mode")
	}
	var proxies struct {
		Proxies map[string]struct{ Type string }
	}
	if e = c.call("GET", "/proxies", nil, &proxies); e != nil {
		return e
	}
	if proxies.Proxies[clashName].Type != "Http" {
		return errors.New("Managed HTTP proxy is absent")
	}
	var rules struct {
		Rules []struct{ Type, Payload, Proxy string }
	}
	if e = c.call("GET", "/rules", nil, &rules); e != nil {
		return e
	}
	hosts := s.Hosts
	if len(hosts) == 0 {
		hosts = hostnames()
	}
	if len(rules.Rules) < len(hosts) {
		return errors.New("Missing Clash download rules")
	}
	for i, h := range hosts {
		r := rules.Rules[i]
		if r.Type != "Domain" || r.Payload != h || r.Proxy != clashName {
			return fmt.Errorf("Clash download rule has changed: %s", h)
		}
	}
	return nil
}
func validateUEChunk(raw []byte) error {
	if len(raw) != 270305 {
		return fmt.Errorf("unexpected size: %d", len(raw))
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != ueChunkSHA256 {
		return errors.New("chunk SHA256 mismatch")
	}
	if binary.LittleEndian.Uint32(raw) != 0xB1FE3AA2 {
		return errors.New("chunk header invalid")
	}
	header := int(binary.LittleEndian.Uint32(raw[8:]))
	size := int(binary.LittleEndian.Uint32(raw[12:]))
	if header < 41 || header+size > len(raw) {
		return errors.New("chunk length invalid")
	}
	payload := raw[header : header+size]
	if raw[40]&1 != 0 {
		zr, e := zlib.NewReader(bytes.NewReader(payload))
		if e != nil {
			return e
		}
		payload, e = io.ReadAll(io.LimitReader(zr, 1048577))
		zr.Close()
		if e != nil {
			return e
		}
	}
	digest := sha1.Sum(payload)
	if len(payload) != 1048576 || hex.EncodeToString(digest[:]) != uePayloadSHA1 {
		return errors.New("decompressed SHA1 does not match UE manifest")
	}
	return nil
}
func (a *App) verify(ctx context.Context) Verification {
	a.verifyMu.Lock()
	defer a.verifyMu.Unlock()
	out := Verification{Version: appVersion, Started: time.Now().Format(time.RFC3339), Scope: "本次真实 UE 数据块与助手上游测试；不证明此前整套 UE 下载、其他进程或路由器之后的承载协议。"}
	add := func(name string, e error, detail string) {
		if e != nil {
			detail = e.Error()
		}
		out.Checks = append(out.Checks, Check{name, e == nil, detail})
	}
	finish := func() Verification {
		out.Finished = time.Now().Format(time.RFC3339)
		out.Passed = len(out.Checks) > 0
		for _, c := range out.Checks {
			out.Passed = out.Passed && c.Passed
		}
		out.Report = filepath.Join(a.dir, "acceptance-"+time.Now().Format("20060102-150405")+".json")
		b, _ := json.MarshalIndent(out, "", "  ")
		if e := os.WriteFile(out.Report, b, 0600); e != nil {
			out.Passed = false
			out.Checks = append(out.Checks, Check{"保存报告", false, e.Error()})
		}
		return out
	}
	proxyAddress := "http://127.0.0.1:17891"
	if a.clashActive() {
		e := a.checkClashRoute()
		if s := a.routingStatus(); s.SourceID != "epic" || len(s.Hosts) != len(hostnames()) {
			e = errors.New("请先启用 Epic 完整下载服务再运行 UE 验收")
		}
		add("Clash 下载规则完整", e, "16 个下载域名规则生效，规则模式正确")
		if e != nil {
			return finish()
		}
		b, _ := os.ReadFile(filepath.Join(a.dir, "clash-state.json"))
		var s clashState
		json.Unmarshal(b, &s)
		c, _, _ := clashAPI(s.Data)
		var config map[string]any
		if e = c.call("GET", "/configs", nil, &config); e != nil {
			add("Clash 端口", e, "")
			return finish()
		}
		port, _ := config["mixed-port"].(float64)
		if port == 0 {
			port, _ = config["port"].(float64)
		}
		if port < 1 || port > 65535 {
			add("Clash 端口", errors.New("Invalid local proxy port"), "")
			return finish()
		}
		proxyAddress = fmt.Sprintf("http://127.0.0.1:%d", int(port))
	} else {
		add("本地下载代理", nil, "通过本程序 17891 端口进行独立样本测试")
	}
	// Independent OS socket sampling. Only local process metadata is collected.
	script := fmt.Sprintf(`$ErrorActionPreference='Stop';$seen=@{};$end=(Get-Date).AddSeconds(10);do{foreach($c in @(Get-NetTCPConnection -OwningProcess %d -State Established -ErrorAction SilentlyContinue)){$ip=[Net.IPAddress]::Parse($c.RemoteAddress);if(-not [Net.IPAddress]::IsLoopback($ip)){$seen[$c.RemoteAddress]=[pscustomobject]@{remote=$c.RemoteAddress;ipv6=($ip.AddressFamily -eq [Net.Sockets.AddressFamily]::InterNetworkV6 -and -not $ip.IsIPv4MappedToIPv6)}}};Start-Sleep -Milliseconds 250}while((Get-Date) -lt $end);ConvertTo-Json -InputObject @($seen.Values) -Compress`, os.Getpid())
	sample := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command", script)
	hideCommand(sample)
	var samples bytes.Buffer
	sample.Stdout = &samples
	sample.Stderr = &samples
	sampleErr := sample.Start()
	before := a.v6.Load()
	proxyURL, _ := url.Parse(proxyAddress)
	tr := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSHandshakeTimeout: 20 * time.Second}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 40 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("test redirects denied") }}
	for _, cdn := range []struct{ url, name string }{{"http://epicgames-download1-1251447533.file.myqcloud.com/", "UE 数据块 HTTP → Akamai IPv6"}, {"https://egs-cloudfront-chunks.epicgamescdn.com/", "UE 数据块 HTTPS → CloudFront IPv6"}} {
		req, _ := http.NewRequestWithContext(ctx, "GET", cdn.url+ueChunkPath, nil)
		res, e := client.Do(req)
		var raw []byte
		if e == nil {
			if res.StatusCode != 200 {
				e = fmt.Errorf("HTTP %d", res.StatusCode)
			} else {
				raw, e = io.ReadAll(io.LimitReader(res.Body, 270306))
			}
			res.Body.Close()
		}
		if e == nil {
			e = validateUEChunk(raw)
		}
		add(cdn.name, e, "HTTP 200；270305 字节；SHA256、解压大小及清单 SHA1 全部匹配")
	}
	// Forbidden probes go straight to the local helper and must never reach the internet.
	localURL, _ := url.Parse("http://127.0.0.1:17891")
	localTransport := &http.Transport{Proxy: http.ProxyURL(localURL)}
	defer localTransport.CloseIdleConnections()
	local := &http.Client{Transport: localTransport, Timeout: 3 * time.Second}
	for _, target := range []string{"http://not-an-epic-host.invalid/", "http://1.1.1.1/"} {
		req, _ := http.NewRequestWithContext(ctx, "GET", target, nil)
		res, e := local.Do(req)
		if e == nil {
			res.Body.Close()
			if res.StatusCode != 403 {
				e = fmt.Errorf("expected 403, got %d", res.StatusCode)
			}
		}
		add("拒绝非授权目标 "+target, e, "403 拒绝，未创建外网上游")
	}
	if sampleErr == nil {
		sampleErr = sample.Wait()
	}
	var rows []struct {
		Remote string
		IPv6   bool
	}
	if sampleErr == nil {
		sampleErr = json.Unmarshal(samples.Bytes(), &rows)
	}
	v4, v6 := 0, 0
	for _, r := range rows {
		if r.IPv6 {
			v6++
		} else {
			v4++
		}
	}
	if sampleErr == nil && (v4 != 0 || v6 == 0) {
		sampleErr = fmt.Errorf("外网 IPv6 样本 %d，IPv4 样本 %d，无法通过验收", v6, v4)
	}
	add("系统 TCP 出站采样", sampleErr, fmt.Sprintf("10 秒窗口：IPv6 远端 %d 个，IPv4 远端 %d 个；快照可能漏过短连接", v6, v4))
	var e error
	if a.v6.Load() <= before {
		e = errors.New("No new IPv6 upstream connection observed")
	}
	add("真实 IPv6 上游建立", e, fmt.Sprintf("新建 IPv6 上游 %d 条；下载和 DNS 均使用 tcp6，无 IPv4 回退", a.v6.Load()-before))
	return finish()
}
