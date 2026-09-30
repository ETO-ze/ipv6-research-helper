package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type NodePolicy struct {
	Pinned  string   `json:"pinned"`
	Blocked []string `json:"blocked"`
}
type UpstreamRow struct {
	IP          string `json:"ip"`
	DNS         bool   `json:"dns"`
	Active      int    `json:"active"`
	Connections int    `json:"connections"`
	Down        int64  `json:"down"`
	Last        string `json:"last"`
	Blocked     bool   `json:"blocked"`
	Pinned      bool   `json:"pinned"`
}
type UpstreamView struct {
	Host        string        `json:"host"`
	ResolveHost string        `json:"resolveHost"`
	Policy      NodePolicy    `json:"policy"`
	Rows        []UpstreamRow `json:"rows"`
	Expires     string        `json:"expires"`
	Error       string        `json:"error"`
}

func upstreamResolveHost(host string) string {
	// Verified with the original arXiv SNI/certificate and identical PDF bytes.
	// Only these explicit hosts use the documented Fastly dual-stack endpoint.
	if host == "arxiv.org" || host == "export.arxiv.org" {
		return "dualstack.s.sni.global.fastly.net"
	}
	if alias := allowed[host]; alias != "" {
		return alias
	}
	return host
}
func (a *App) initNodePolicies() error {
	a.nodePolicies = map[string]NodePolicy{}
	b, e := os.ReadFile(filepath.Join(a.dir, "upstream-policies.json"))
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if e = json.Unmarshal(b, &a.nodePolicies); e != nil {
		return fmt.Errorf("上游策略文件损坏，已保留：%w", e)
	}
	if a.nodePolicies == nil {
		a.nodePolicies = map[string]NodePolicy{}
	}
	for host, p := range a.nodePolicies {
		if !allowedHost(host) || host != norm(host) {
			return errors.New("上游策略包含无效域名")
		}
		for _, ip := range append(append([]string{}, p.Blocked...), p.Pinned) {
			if ip != "" && !publicV6(net.ParseIP(ip)) {
				return errors.New("上游策略仅允许公网 IPv6")
			}
		}
	}
	return nil
}
func blockedIP(p NodePolicy, ip string) bool {
	for _, v := range p.Blocked {
		if v == ip {
			return true
		}
	}
	return false
}
func (a *App) nodeAllowedLocked(host, ip string) bool {
	p := a.nodePolicies[host]
	return !blockedIP(p, ip) && (p.Pinned == "" || p.Pinned == ip)
}
func filterNodeIPs(ips []net.IP, p NodePolicy) ([]net.IP, error) {
	var out []net.IP
	seen := map[string]bool{}
	for _, ip := range ips {
		if !publicV6(ip) {
			continue
		}
		s := ip.String()
		if !seen[s] && !blockedIP(p, s) && (p.Pinned == "" || p.Pinned == s) {
			out = append(out, ip)
			seen[s] = true
		}
	}
	if len(out) == 0 {
		if p.Pinned != "" {
			return nil, errors.New("固定 IPv6 已被拉黑或不在当前 DNS 候选中，请重新选择或恢复自动")
		}
		return nil, errors.New("没有可用的 IPv6：候选为空或已全部拉黑")
	}
	return out, nil
}
func (a *App) nodeIPs(ctx context.Context, host string) ([]net.IP, error) {
	ips, e := a.resolve(ctx, upstreamResolveHost(host))
	if e != nil {
		return nil, e
	}
	a.nodeMu.Lock()
	p := a.nodePolicies[host]
	a.nodeMu.Unlock()
	return filterNodeIPs(ips, p)
}
func (a *App) upstreamView(host string) UpstreamView {
	v := UpstreamView{Host: host, ResolveHost: upstreamResolveHost(host), Rows: []UpstreamRow{}}
	rows := map[string]*UpstreamRow{}
	ensure := func(ip string) *UpstreamRow {
		if rows[ip] == nil {
			rows[ip] = &UpstreamRow{IP: ip}
		}
		return rows[ip]
	}
	a.mu.Lock()
	cache := a.cache[v.ResolveHost]
	if !cache.expires.IsZero() {
		v.Expires = cache.expires.Format(time.RFC3339)
	}
	if time.Now().Before(cache.expires) {
		for _, ip := range cache.ips {
			if publicV6(ip) {
				ensure(ip.String()).DNS = true
			}
		}
	}
	for _, en := range a.entries {
		if en.Host != host || en.Remote == "" {
			continue
		}
		ip, _, e := net.SplitHostPort(en.Remote)
		if e != nil || !publicV6(net.ParseIP(ip)) {
			continue
		}
		row := ensure(net.ParseIP(ip).String())
		row.Connections++
		row.Down += en.Down
		if en.Active {
			row.Active++
		}
		if en.Time > row.Last {
			row.Last = en.Time
		}
	}
	a.mu.Unlock()
	a.nodeMu.Lock()
	v.Policy = a.nodePolicies[host]
	v.Policy.Blocked = append([]string{}, v.Policy.Blocked...)
	a.nodeMu.Unlock()
	for _, ip := range v.Policy.Blocked {
		ensure(ip).Blocked = true
	}
	if v.Policy.Pinned != "" {
		ensure(v.Policy.Pinned).Pinned = true
	}
	for _, row := range rows {
		v.Rows = append(v.Rows, *row)
	}
	sort.Slice(v.Rows, func(i, j int) bool { return v.Rows[i].IP < v.Rows[j].IP })
	return v
}
func (a *App) setNodePolicy(host, ip, action string) error {
	host = norm(host)
	if !allowedHost(host) {
		return errors.New("未登记的下载域名")
	}
	if action != "auto" {
		parsed := net.ParseIP(ip)
		if !publicV6(parsed) {
			return errors.New("只能选择公网 IPv6 地址")
		}
		ip = parsed.String()
	}
	view := a.upstreamView(host)
	known, current := false, false
	for _, row := range view.Rows {
		if row.IP == ip {
			known = true
			current = row.DNS
		}
	}
	if action == "pin" && !current {
		return errors.New("只能固定当前 DNS 列表中的地址，请先刷新解析")
	}
	if action == "block" && !known {
		return errors.New("只能拉黑该域名的已知地址")
	}
	a.nodeMu.Lock()
	old := a.nodePolicies[host]
	p := NodePolicy{Pinned: old.Pinned, Blocked: append([]string{}, old.Blocked...)}
	switch action {
	case "pin":
		if blockedIP(p, ip) {
			a.nodeMu.Unlock()
			return errors.New("请先解除该地址的拉黑")
		}
		p.Pinned = ip
	case "auto":
		p.Pinned = ""
	case "block":
		if !blockedIP(p, ip) {
			p.Blocked = append(p.Blocked, ip)
		}
		if p.Pinned == ip {
			p.Pinned = ""
		}
	case "unblock":
		var keep []string
		for _, v := range p.Blocked {
			if v != ip {
				keep = append(keep, v)
			}
		}
		p.Blocked = keep
	default:
		a.nodeMu.Unlock()
		return errors.New("未知上游操作")
	}
	sort.Strings(p.Blocked)
	candidate := map[string]NodePolicy{}
	for h, v := range a.nodePolicies {
		candidate[h] = v
	}
	candidate[host] = p
	b, e := json.MarshalIndent(candidate, "", "  ")
	path := filepath.Join(a.dir, "upstream-policies.json")
	if e == nil {
		e = os.WriteFile(path+".tmp", b, 0600)
	}
	if e == nil {
		e = os.Rename(path+".tmp", path)
	}
	if e != nil {
		a.nodeMu.Unlock()
		return fmt.Errorf("策略保存失败，未应用：%w", e)
	}
	a.nodePolicies = candidate
	// Hold the policy lock until active connections are captured. connect uses the
	// same lock while registering, so a connection cannot slip past a block change.
	var closeList []net.Conn
	a.mu.Lock()
	for _, en := range a.entries {
		if en.Active && en.Host == host {
			remote, _, _ := net.SplitHostPort(en.Remote)
			if action == "pin" || action == "auto" || (action == "block" && remote == ip) {
				if c := a.active[en.ID]; c != nil {
					closeList = append(closeList, c)
				}
			}
		}
	}
	a.mu.Unlock()
	a.nodeMu.Unlock()
	for _, c := range closeList {
		c.Close()
	}
	if a.transport != nil {
		a.transport.CloseIdleConnections()
	}
	return nil
}
func (a *App) upstreamAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	host := norm(r.URL.Query().Get("host"))
	action, ip := "", ""
	if r.Method == "POST" {
		var in struct {
			Host string `json:"host"`
			IP   string `json:"ip"`
		}
		if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); e != nil {
			http.Error(w, "请求格式错误", 400)
			return
		}
		host = norm(in.Host)
		ip = in.IP
		action = strings.TrimPrefix(r.URL.Path, "/api/upstreams/")
	}
	if !allowedHost(host) {
		http.Error(w, "未登记的下载域名", 400)
		return
	}
	if action == "refresh" {
		a.mu.Lock()
		delete(a.cache, upstreamResolveHost(host))
		a.mu.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), 16*time.Second)
		defer cancel()
		_, e := a.resolve(ctx, upstreamResolveHost(host))
		v := a.upstreamView(host)
		if e != nil {
			v.Error = e.Error()
		}
		json.NewEncoder(w).Encode(v)
		return
	}
	if action != "" {
		if e := a.setNodePolicy(host, ip, action); e != nil {
			http.Error(w, e.Error(), 409)
			return
		}
	}
	json.NewEncoder(w).Encode(a.upstreamView(host))
}
