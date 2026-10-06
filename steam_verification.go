package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

const steamChunkPath = "/depot/730/chunk/ce09629928fe234c3785e60ce5e782a2b33c3c9f"
const steamChunkSHA256 = "c0c3800733df222e5264b1cee42eda79c7edbab04d77658ae3aa5325f27c6a1d"
const steamChunkSize = 745040

func validateSteamChunk(body []byte, res *http.Response) error {
	if res.StatusCode != 200 || len(body) != steamChunkSize {
		return fmt.Errorf("Steam 样本状态/大小不符：HTTP %d，%d 字节", res.StatusCode, len(body))
	}
	if res.Header.Get("Content-Type") != "application/x-steam-chunk" || res.Header.Get("X-Content-Sha") != "ce09629928fe234c3785e60ce5e782a2b33c3c9f" {
		return errors.New("Steam 内容块响应头不符")
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != steamChunkSHA256 {
		return errors.New("Steam 内容块与独立 IPv6 下载参照的 SHA256 不符")
	}
	return nil
}
func (a *App) verifySteam(ctx context.Context) Verification {
	a.verifyMu.Lock()
	defer a.verifyMu.Unlock()
	out := Verification{Version: appVersion, Started: time.Now().Format(time.RFC3339), Scope: "验证 Steam 完整传输块与本机 TLS 转发；哈希来自六个官方缓存的独立 IPv6 下载参照，不是 Valve 签名；未解密最终游戏文件。实际客户端接管需同时检查 Steam 日志和进程连接。"}
	add := func(name string, e error, detail string) {
		if e != nil {
			detail = e.Error()
		}
		out.Checks = append(out.Checks, Check{name, e == nil, detail})
	}
	finish := func() Verification {
		return a.finishVerification("steam", out)
	}
	proxyURL, _ := url.Parse("http://127.0.0.1:17891")
	tr := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSHandshakeTimeout: 15 * time.Second}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("Steam 验收不接受重定向") }}
	test := func(c *http.Client, host string) error {
		req, _ := http.NewRequestWithContext(ctx, "GET", "https://"+host+steamChunkPath, nil)
		res, e := c.Do(req)
		if e != nil {
			return e
		}
		defer res.Body.Close()
		b, e := io.ReadAll(io.LimitReader(res.Body, steamChunkSize+1))
		if e != nil {
			return e
		}
		return validateSteamChunk(b, res)
	}
	before := a.v6.Load()
	for _, h := range []string{"cache7-hkg1.steamcontent.com", "cache8-hkg1.steamcontent.com"} {
		add("官方 SteamCache HTTPS / "+h, test(client, h), "745040 字节，真实内容块头和传输参照 SHA256 一致，TLS 正常校验")
	}
	a.networkMu.Lock()
	e := a.ensureSteamListenersLocked()
	a.networkMu.Unlock()
	add("Steam 本地 IPv6 TLS 监听", e, "仅监听 ::1:80/443，与 127.0.0.23 双栈回环配合")
	if e == nil {
		local := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp6", "[::1]:443")
		}, TLSHandshakeTimeout: 15 * time.Second}
		defer local.CloseIdleConnections()
		add("Steam 本地 TLS 原样转发", test(&http.Client{Transport: local, Timeout: 30 * time.Second}, "cache7-hkg1.steamcontent.com"), "::1 → 助手 → 官方缓存 IPv6，原域名、证书和内容块校验通过；不证明 Steam 客户端已经使用此路径")
	}
	if a.steamActive() {
		s, se := a.steamState()
		if se == nil {
			var b []byte
			b, se = os.ReadFile(a.hostsPath)
			if se == nil {
				if _, se2 := findSteamBlock(b, s); se2 != nil {
					se = se2
				}
			}
		}
		add("Steam 已应用 hosts 核验", se, "本程序管理的精确域名块仍完整，其他规则未覆盖")
	} else {
		add("当前接管状态", nil, "未启用 hosts；本次为独立代理与 TLS 转发验收，客户端接管尚需管理员启用")
	}
	for _, target := range []string{"http://not-steam.example/chunk", "http://1.1.1.1/chunk"} {
		v := a.v6.Load()
		req, _ := http.NewRequestWithContext(ctx, "GET", target, nil)
		res, re := client.Do(req)
		if re == nil {
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
			if res.StatusCode != 403 || a.v6.Load() != v {
				re = errors.New("未知目标未被拒绝")
			}
		}
		add("未知或 IPv4 目标拒绝 "+target, re, "403，未创建外网上游")
	}
	if a.v6.Load() <= before {
		add("真实 IPv6 上游", errors.New("没有新建 IPv6 连接"), "")
	} else {
		add("真实 IPv6 上游", nil, fmt.Sprintf("本次新建 %d 条 IPv6 连接；下载和 DNS 使用 tcp6，无 IPv4 回退", a.v6.Load()-before))
	}
	return finish()
}
func findSteamBlock(b []byte, s steamRouteState) (int, error) {
	needle := append(append([]byte{}, s.Separator...), s.Block...)
	if i := bytes.Index(b, needle); i >= 0 && bytes.Count(b, s.Block) == 1 {
		return i, nil
	}
	return -1, errors.New("Steam hosts 块未找到或被修改")
}

// Keep public reports free of task URLs, account identifiers and local paths.
func steamPublicReport(v Verification) []byte {
	v.Report = ""
	b, _ := json.MarshalIndent(v, "", "  ")
	return b
}
