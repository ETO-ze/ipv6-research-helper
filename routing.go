package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type RoutePlan struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Mark    string   `json:"mark"`
	Service string   `json:"service"`
	Note    string   `json:"note"`
	Hosts   []string `json:"hosts"`
}

// Explicit download scopes, not all hosts from a publisher or a wildcard suffix.
func routePlans() []RoutePlan {
	plans := []RoutePlan{
		{"pypi", "PyPI", "Py", "Python 包与索引", "在原客户端照常下载；客户端须使用系统代理或已有 Clash 代理。", []string{"pypi.org", "files.pythonhosted.org"}},
		{"conda", "Conda main", "Co", "官方 main 软件包与索引", "覆盖 repo.anaconda.com；其他频道不在此项范围内。", []string{"repo.anaconda.com"}},
		{"condaforge", "conda-forge", "Cf", "社区软件包与索引", "覆盖 conda.anaconda.org；同一域名上的其他频道也会经过助手。", []string{"conda.anaconda.org"}},
		{"zenodo", "Zenodo", "Ze", "记录文件与附件", "同域名网页也走 IPv6；第三方附件域名不自动加入。", []string{"zenodo.org"}},
		{"ncbi", "NCBI", "Nc", "HTTPS 文件服务器", "样本为说明文件；SRA Toolkit、API 和云存储另行配置。", []string{"ftp.ncbi.nlm.nih.gov", "ftp.ncbi.nih.gov"}},
		{"githubraw", "GitHub Raw", "Gh", "原始代码与文本文件", "只接管 Raw 域名，不包含 Git clone、Release 和源码 ZIP。", []string{"raw.githubusercontent.com"}},
		{"gitlab", "GitLab.com", "Gl", "主站文件与原始代码", "同域名网页也走 IPv6；外部对象存储与自建 GitLab 不在范围内。", []string{"gitlab.com"}},
		{"cern", "CERN Open Data", "Ce", "主站公开文件", "已验证主站 PDF；EOS 和其他大数据服务器不在此项范围内。", []string{"opendata.cern.ch"}},
		{"pmc", "PMC 云文件", "Pm", "官方 IPv6 云对象地址", "仅接管官方 dualstack 地址；不会解密 HTTPS 或改写网站发出的其他地址。普通 PMC 论文页面不在范围内。", []string{"pmc-oa-opendata.s3.dualstack.us-east-1.amazonaws.com"}},
		{"epic", "Epic Games", "EP", "UE 引擎与游戏下载", "保留原有 Epic 官方 CDN 转发规则。", hostnames()},
		{"steam", "Steam", "St", "官方 SteamCache 游戏下载", "从本机 Steam 日志发现精确缓存域名，hosts → 本机 HTTP/TLS 转发 → 官方 IPv6；无需 Clash，需要管理员。启动后暂停再继续下载；新服务器请刷新并重新启用。未知缓存、登录和 UDP 不在保证范围内。", steamHosts()},
	}
	for _, source := range researchSources {
		found := false
		for _, p := range plans {
			if p.ID == source.ID {
				found = true
				break
			}
		}
		if !found {
			mark := []rune(source.Name)
			if len(mark) > 2 {
				mark = mark[:2]
			}
			plans = append(plans, RoutePlan{source.ID, source.Name, string(mark), source.Category + " · 已登记下载域名", source.Note + "。仅接管下列精确域名；DNS 可用不代表文件已验证，未知重定向域名不自动接管。", append([]string{}, source.Hosts...)})
		}
	}
	return plans
}
func containsHost(hosts []string, host string) bool {
	for _, h := range hosts {
		if h == norm(host) {
			return true
		}
	}
	return false
}
func routeSelection(id, host string) (RoutePlan, error) {
	for _, p := range routePlans() {
		if p.ID == id {
			if host != "" {
				if !containsHost(p.Hosts, host) {
					return RoutePlan{}, errors.New("所选域名不属于该服务")
				}
				p.Hosts = []string{norm(host)}
			}
			sort.Strings(p.Hosts)
			return p, nil
		}
	}
	return RoutePlan{}, errors.New("该平台尚未实现自动接管，请先在来源检测中验证")
}

type RoutingStatus struct {
	Active   bool     `json:"active"`
	SourceID string   `json:"sourceID"`
	Hosts    []string `json:"hosts"`
	Mode     string   `json:"mode,omitempty"`
}

func (a *App) routingStatus() RoutingStatus {
	if s, e := a.steamState(); e == nil && a.steamActive() {
		return RoutingStatus{true, "steam", s.Hosts, "hosts"}
	}
	var s clashState
	b, e := os.ReadFile(filepath.Join(a.dir, "clash-state.json"))
	if e != nil || json.Unmarshal(b, &s) != nil {
		return RoutingStatus{}
	}
	if s.SourceID == "" {
		s.SourceID = "epic"
	}
	if len(s.Hosts) == 0 {
		s.Hosts = hostnames()
	}
	return RoutingStatus{true, s.SourceID, s.Hosts, "clash"}
}
func (a *App) switchRoute(p RoutePlan) error {
	a.networkMu.Lock()
	defer a.networkMu.Unlock()
	if p.ID == "steam" {
		if e := a.steamPreflight(p.Hosts); e != nil {
			return e
		}
	}
	// Check before restoration: an unavailable new selection must not stop the
	// user's current route. DNS availability is not a file-download guarantee.
	if p.ID != "epic" {
		ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
		defer cancel()
		var wg sync.WaitGroup
		results := make(chan bool, len(p.Hosts))
		for _, host := range p.Hosts {
			wg.Add(1)
			go func(h string) { defer wg.Done(); _, e := a.nodeIPs(ctx, h); results <- e == nil }(host)
		}
		wg.Wait()
		close(results)
		available := false
		for ok := range results {
			available = available || ok
		}
		if !available {
			return errors.New("所选域名没有可用 IPv6（解析失败、全部拉黑或固定地址失效）；已保留当前接管，请查看上游列表与来源检测")
		}
	}
	// Restoration checks for concurrent user edits before touching any configuration.
	if p.ID == "steam" {
		if e := a.ensureSteamListenersLocked(); e != nil {
			return e
		}
	}
	if e := a.restoreSteamLocked(); e != nil {
		return e
	}
	if p.ID == "steam" {
		if e := a.hosts(false); e != nil {
			return e
		}
	}
	if e := a.clashActionLocked("restore", "", nil); e != nil {
		return e
	}
	if p.ID == "steam" {
		return a.steamApplyLocked(p.Hosts)
	}
	if e := a.clashActionLocked("enable", p.ID, p.Hosts); e != nil {
		return fmt.Errorf("启动接管失败（已停止先前接管）：%w", e)
	}
	if e := a.checkClashRoute(); e != nil {
		restoreErr := a.clashActionLocked("restore", "", nil)
		return fmt.Errorf("规则核验失败：%v；恢复结果：%v", e, restoreErr)
	}
	return nil
}
func (a *App) routingAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == "GET" {
		json.NewEncoder(w).Encode(map[string]any{"plans": routePlans(), "routing": a.routingStatus(), "acceptance": a.acceptanceSnapshot()})
		return
	}
	var input struct {
		ID   string `json:"id"`
		Host string `json:"host"`
	}
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); e != nil {
		http.Error(w, "请求格式错误", 400)
		return
	}
	p, e := routeSelection(input.ID, input.Host)
	if e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	if e = a.switchRoute(p); e != nil {
		http.Error(w, e.Error(), 409)
		return
	}
	json.NewEncoder(w).Encode(a.routingStatus())
}
