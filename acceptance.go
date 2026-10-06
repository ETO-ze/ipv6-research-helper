package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// A recorded pass describes the checks in its scope, independently of whether
// a download route is currently enabled. It never certifies a whole client.
type AcceptanceStatus struct {
	Status  string `json:"status"`
	Passed  bool   `json:"passed"`
	Checked string `json:"checked"`
	Scope   string `json:"scope"`
	Detail  string `json:"detail"`
	Version string `json:"version"`
}

var acceptanceReportName = regexp.MustCompile(`^(steam-)?acceptance-[0-9]{8}-[0-9]{6}(\.[0-9]{9})?\.json$`)

func validAcceptanceReport(id string, v Verification) bool {
	if (id != "steam" && id != "epic") || strings.TrimSpace(v.Version) == "" || strings.TrimSpace(v.Scope) == "" || len(v.Scope) > 8192 || len(v.Checks) == 0 || len(v.Checks) > 64 {
		return false
	}
	started, se := time.Parse(time.RFC3339Nano, v.Started)
	finished, fe := time.Parse(time.RFC3339Nano, v.Finished)
	if se != nil || fe != nil || finished.Before(started) || finished.After(time.Now().Add(5*time.Minute)) {
		return false
	}
	checks := map[string]bool{}
	passed := true
	for _, c := range v.Checks {
		if strings.TrimSpace(c.Name) == "" || len(c.Name) > 512 || len(c.Detail) > 32768 {
			return false
		}
		if _, duplicate := checks[c.Name]; duplicate {
			return false
		}
		checks[c.Name] = c.Passed
		passed = passed && c.Passed
	}
	if passed != v.Passed {
		return false
	}
	// A failed run can stop at its first failed check. A pass must contain all
	// of the actual file, rejection and IPv6 checks for that platform.
	if !v.Passed {
		return true
	}
	var required []string
	if id == "steam" {
		required = []string{"官方 SteamCache HTTPS / cache7-hkg1.steamcontent.com", "官方 SteamCache HTTPS / cache8-hkg1.steamcontent.com", "Steam 本地 IPv6 TLS 监听", "Steam 本地 TLS 原样转发", "未知或 IPv4 目标拒绝 http://not-steam.example/chunk", "未知或 IPv4 目标拒绝 http://1.1.1.1/chunk", "真实 IPv6 上游"}
		if !checks["Steam 已应用 hosts 核验"] && !checks["当前接管状态"] {
			return false
		}
	} else {
		required = []string{"UE 数据块 HTTP → Akamai IPv6", "UE 数据块 HTTPS → CloudFront IPv6", "拒绝非授权目标 http://not-an-epic-host.invalid/", "拒绝非授权目标 http://1.1.1.1/", "系统 TCP 出站采样", "真实 IPv6 上游建立"}
		if !checks["Clash 下载规则完整"] && !checks["本地下载代理"] {
			return false
		}
	}
	for _, name := range required {
		if !checks[name] {
			return false
		}
	}
	return true
}

func readAcceptanceJSON(path string, out any) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 256*1024+1))
	if e != nil {
		return e
	}
	if len(b) > 256*1024 {
		return errors.New("验收记录超过大小限制")
	}
	return json.Unmarshal(b, out)
}

func acceptanceNewer(candidate, previous Verification) bool {
	c, _ := time.Parse(time.RFC3339Nano, candidate.Finished)
	p, _ := time.Parse(time.RFC3339Nano, previous.Finished)
	return c.After(p) || (c.Equal(p) && !candidate.Passed && previous.Passed)
}

func (a *App) initAcceptance() {
	a.acceptance = map[string]Verification{}
	var stored map[string]Verification
	if e := readAcceptanceJSON(filepath.Join(a.dir, "acceptance-status.json"), &stored); e == nil {
		for id, v := range stored {
			if validAcceptanceReport(id, v) {
				a.acceptance[id] = v
			}
		}
	}
	// Import reports produced by older versions in this same runtime directory.
	// Never import public documentation or infer a pass from a filename.
	files, _ := os.ReadDir(a.dir)
	sort.Slice(files, func(i, j int) bool { return files[i].Name() > files[j].Name() })
	counts := map[string]int{}
	changed := false
	for _, f := range files {
		if f.IsDir() || !acceptanceReportName.MatchString(f.Name()) {
			continue
		}
		id := "epic"
		if strings.HasPrefix(f.Name(), "steam-") {
			id = "steam"
		}
		counts[id]++
		if counts[id] > 200 {
			continue
		}
		var v Verification
		if readAcceptanceJSON(filepath.Join(a.dir, f.Name()), &v) != nil || !validAcceptanceReport(id, v) {
			continue
		}
		if old, ok := a.acceptance[id]; !ok || acceptanceNewer(v, old) {
			a.acceptance[id] = v
			changed = true
		}
	}
	if changed {
		if e := writeJSONAtomic(filepath.Join(a.dir, "acceptance-status.json"), a.acceptance); e != nil {
			log.Printf("保存导入的验收状态失败：%v", e)
		}
	}
}

func (a *App) acceptanceSnapshot() map[string]AcceptanceStatus {
	a.acceptanceMu.Lock()
	defer a.acceptanceMu.Unlock()
	result := map[string]AcceptanceStatus{}
	for id, v := range a.acceptance {
		status := "链路验收未通过"
		detail := ""
		if v.Passed {
			status = "链路已验收"
			detail = fmt.Sprintf("%d 项检查通过；仅覆盖本次验收范围。", len(v.Checks))
		} else {
			for _, c := range v.Checks {
				if !c.Passed {
					detail = c.Name + "：" + c.Detail
					break
				}
			}
		}
		result[id] = AcceptanceStatus{Status: status, Passed: v.Passed, Checked: v.Finished, Scope: v.Scope, Detail: detail, Version: v.Version}
	}
	return result
}

func (a *App) finishVerification(id string, out Verification) Verification {
	out.Finished = time.Now().Format(time.RFC3339Nano)
	out.Passed = len(out.Checks) > 0
	for _, c := range out.Checks {
		out.Passed = out.Passed && c.Passed
	}
	prefix := "acceptance-"
	if id == "steam" {
		prefix = "steam-acceptance-"
	}
	out.Report = filepath.Join(a.dir, prefix+time.Now().Format("20060102-150405.000000000")+".json")
	if e := writeJSONAtomic(out.Report, out); e != nil {
		out.Passed = false
		out.Checks = append(out.Checks, Check{"保存报告", false, e.Error()})
	}
	a.acceptanceMu.Lock()
	defer a.acceptanceMu.Unlock()
	if a.acceptance == nil {
		a.acceptance = map[string]Verification{}
	}
	a.acceptance[id] = out
	if e := writeJSONAtomic(filepath.Join(a.dir, "acceptance-status.json"), a.acceptance); e != nil {
		out.Passed = false
		out.Checks = append(out.Checks, Check{"保存验收状态", false, e.Error()})
		a.acceptance[id] = out
		// Keep the returned status and the recoverable report consistent. If the
		// status file alone is locked, startup can recover this failure report.
		_ = writeJSONAtomic(out.Report, out)
		_ = writeJSONAtomic(filepath.Join(a.dir, "acceptance-status.json"), a.acceptance)
	}
	return out
}
