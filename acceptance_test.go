package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func acceptanceFixture(id string, finished time.Time, passed bool) Verification {
	var names []string
	scope := "Epic 独立 UE 数据块与助手 IPv6 链路；不证明整套 UE 下载或所有客户端连接。"
	if id == "steam" {
		scope = "Steam 独立内容块与本机 TLS 转发；未启用 hosts，不证明 Steam 客户端已经使用本链路。"
		names = []string{
			"官方 SteamCache HTTPS / cache7-hkg1.steamcontent.com",
			"官方 SteamCache HTTPS / cache8-hkg1.steamcontent.com",
			"Steam 本地 IPv6 TLS 监听",
			"Steam 本地 TLS 原样转发",
			"当前接管状态",
			"未知或 IPv4 目标拒绝 http://not-steam.example/chunk",
			"未知或 IPv4 目标拒绝 http://1.1.1.1/chunk",
			"真实 IPv6 上游",
		}
	} else {
		names = []string{
			"本地下载代理",
			"UE 数据块 HTTP → Akamai IPv6",
			"UE 数据块 HTTPS → CloudFront IPv6",
			"拒绝非授权目标 http://not-an-epic-host.invalid/",
			"拒绝非授权目标 http://1.1.1.1/",
			"系统 TCP 出站采样",
			"真实 IPv6 上游建立",
		}
	}
	checks := make([]Check, 0, len(names))
	for _, name := range names {
		checks = append(checks, Check{Name: name, Passed: true, Detail: "isolated test fixture"})
	}
	if !passed {
		checks = []Check{{Name: names[0], Passed: false, Detail: "new test failure"}}
	}
	return Verification{
		Version: appVersion, Started: finished.Add(-time.Minute).Format(time.RFC3339Nano),
		Finished: finished.Format(time.RFC3339Nano), Passed: passed, Scope: scope, Checks: checks,
	}
}

func acceptanceFixtureTime() time.Time {
	return time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
}

func acceptanceTestApp(t *testing.T, dir string) *App {
	t.Helper()
	// No registry, Steam log, system hosts, downloads, or network helpers are
	// touched while exercising persistence and GET-only API snapshots.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("IPV6_HELPER_STEAM_PATH", t.TempDir())
	a, err := newApp(dir)
	if err != nil {
		t.Fatal(err)
	}
	a.hostsPath = filepath.Join(dir, "fake-hosts")
	if _, err := os.Stat(a.hostsPath); os.IsNotExist(err) {
		if err := os.WriteFile(a.hostsPath, []byte("# untouched fake hosts\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { a.stop(); a.journal.Close() })
	return a
}

func acceptanceWriteFixture(t *testing.T, path string, value any) {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func acceptanceRoutingSnapshot(t *testing.T, a *App) (map[string]AcceptanceStatus, RoutingStatus) {
	t.Helper()
	w := httptest.NewRecorder()
	a.ui(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:17890/api/routing", nil))
	if w.Code != 200 {
		t.Fatal("GET routing snapshot failed", w.Code, w.Body.String())
	}
	var response struct {
		Acceptance map[string]AcceptanceStatus `json:"acceptance"`
		Routing    RoutingStatus               `json:"routing"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Acceptance, response.Routing
}

func acceptanceStoredSnapshot(a *App) map[string]Verification {
	a.acceptanceMu.Lock()
	defer a.acceptanceMu.Unlock()
	result := make(map[string]Verification, len(a.acceptance))
	for id, v := range a.acceptance {
		result[id] = v
	}
	return result
}

func acceptanceAssertFailed(t *testing.T, a *App, id string) {
	t.Helper()
	stored, ok := acceptanceStoredSnapshot(a)[id]
	if !ok || stored.Passed {
		t.Fatal("current memory did not retain failed verification", id, ok, stored.Passed)
	}
	status, _ := acceptanceRoutingSnapshot(t, a)
	row, ok := status[id]
	if !ok || row.Passed || strings.Contains(row.Status, "已验收") || row.Checked == "" || row.Detail == "" {
		t.Fatal("API hid the failed verification or showed a green pass", id, ok, row)
	}
}

func TestAcceptancePersistsRestartAndSeparatesServices(t *testing.T) {
	dir := t.TempDir()
	a := acceptanceTestApp(t, dir)
	steam := a.finishVerification("steam", acceptanceFixture("steam", acceptanceFixtureTime(), true))
	epic := a.finishVerification("epic", acceptanceFixture("epic", acceptanceFixtureTime(), false))
	if !steam.Passed || epic.Passed || steam.Report == epic.Report {
		t.Fatal("service verification results were mixed or did not save", steam.Passed, epic.Passed)
	}
	for _, v := range []Verification{steam, epic} {
		var report Verification
		if err := readAcceptanceJSON(v.Report, &report); err != nil || report.Passed != v.Passed || report.Finished != v.Finished {
			t.Fatal("durable report differs from the returned result", err)
		}
	}
	before, routing := acceptanceRoutingSnapshot(t, a)
	if !before["steam"].Passed || before["epic"].Passed || routing.Active || a.steamActive() || a.managed {
		t.Fatal("historical link verification enabled routing or mixed services", before, routing)
	}
	if !strings.Contains(before["steam"].Status, "链路") || before["steam"].Scope != steam.Scope || before["steam"].Checked != steam.Finished {
		t.Fatal("Steam pass lacks link scope or its actual check time", before["steam"])
	}
	// In particular, a successful independent link test while hosts is disabled
	// must not turn into a claim that the Steam client itself was accepted.
	if strings.Contains(before["steam"].Status, "客户端已验收") {
		t.Fatal("independent sample was incorrectly promoted to client acceptance")
	}
	a.stop()
	a.journal.Close()
	restarted := acceptanceTestApp(t, dir)
	after, current := acceptanceRoutingSnapshot(t, restarted)
	if after["steam"] != before["steam"] || after["epic"] != before["epic"] || current.Active || restarted.steamActive() {
		t.Fatal("restart reset saved acceptance or enabled routing", before, after, current)
	}
	if restarted.v6.Load() != 0 || restarted.steamListenersStarted || len(restarted.listeners) != 0 {
		t.Fatal("acceptance restore opened network connections or listeners")
	}
}

func TestAcceptanceLatestFailureOverridesPassOnlyForThatService(t *testing.T) {
	dir := t.TempDir()
	a := acceptanceTestApp(t, dir)
	if !a.finishVerification("steam", acceptanceFixture("steam", acceptanceFixtureTime(), true)).Passed {
		t.Fatal("initial Steam pass did not save")
	}
	epic := a.finishVerification("epic", acceptanceFixture("epic", acceptanceFixtureTime(), true))
	failure := a.finishVerification("steam", acceptanceFixture("steam", acceptanceFixtureTime(), false))
	if !epic.Passed || failure.Passed {
		t.Fatal("a failed follow-up run did not replace the previous pass")
	}
	acceptanceAssertFailed(t, a, "steam")
	status, _ := acceptanceRoutingSnapshot(t, a)
	if !status["epic"].Passed || status["epic"].Checked != epic.Finished {
		t.Fatal("Steam failure cleared Epic acceptance", status)
	}
	a.stop()
	a.journal.Close()
	restarted := acceptanceTestApp(t, dir)
	acceptanceAssertFailed(t, restarted, "steam")
	after, _ := acceptanceRoutingSnapshot(t, restarted)
	if !after["epic"].Passed || after["epic"].Checked != epic.Finished {
		t.Fatal("failure persistence corrupted the other service", after)
	}
}

func TestAcceptanceMigratesNewestRealResultIncludingLegacyFailure(t *testing.T) {
	dir := t.TempDir()
	base := acceptanceFixtureTime()
	old := acceptanceFixture("steam", base, true)
	acceptanceWriteFixture(t, filepath.Join(dir, "acceptance-status.json"), map[string]Verification{"steam": old})
	// Names and mtimes deliberately disagree with Finished. Neither may be used
	// as evidence that an older pass supersedes the actual latest failure.
	olderPath := filepath.Join(dir, "steam-acceptance-20991231-235959.json")
	older := acceptanceFixture("steam", base.Add(time.Hour), true)
	acceptanceWriteFixture(t, olderPath, older)
	latestPath := filepath.Join(dir, "steam-acceptance-20000101-000000.json")
	latest := acceptanceFixture("steam", base.Add(2*time.Hour), false)
	acceptanceWriteFixture(t, latestPath, latest)
	if err := os.Chtimes(olderPath, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(latestPath, base, base); err != nil {
		t.Fatal(err)
	}
	epic := acceptanceFixture("epic", base.Add(3*time.Hour), true)
	epic.Version = "0.7.0"
	acceptanceWriteFixture(t, filepath.Join(dir, "acceptance-20261001-030000.json"), epic)
	forged := acceptanceFixture("steam", base.Add(4*time.Hour), true)
	forged.Checks = forged.Checks[:1]
	acceptanceWriteFixture(t, filepath.Join(dir, "steam-acceptance-20991231-235958.json"), forged)
	ignored := acceptanceFixture("steam", base.Add(5*time.Hour), true)
	acceptanceWriteFixture(t, filepath.Join(dir, "unrelated-result.json"), ignored)
	docs := filepath.Join(dir, "docs")
	if err := os.Mkdir(docs, 0700); err != nil {
		t.Fatal(err)
	}
	acceptanceWriteFixture(t, filepath.Join(docs, "steam-acceptance-20261001-050000.json"), ignored)
	a := acceptanceTestApp(t, dir)
	acceptanceAssertFailed(t, a, "steam")
	status, _ := acceptanceRoutingSnapshot(t, a)
	if status["steam"].Checked != latest.Finished || !status["epic"].Passed || status["epic"].Version != "0.7.0" || status["epic"].Checked != epic.Finished {
		t.Fatal("migration did not choose real timestamps, legacy versions, and service scopes", status)
	}
	var saved map[string]Verification
	if err := readAcceptanceJSON(filepath.Join(dir, "acceptance-status.json"), &saved); err != nil || saved["steam"].Passed || saved["steam"].Finished != latest.Finished || !saved["epic"].Passed {
		t.Fatal("migration was not persisted consistently", saved, err)
	}
}

func TestAcceptanceEqualTimestampFailureWins(t *testing.T) {
	dir := t.TempDir()
	finished := acceptanceFixtureTime()
	pass := acceptanceFixture("steam", finished, true)
	fail := acceptanceFixture("steam", finished, false)
	acceptanceWriteFixture(t, filepath.Join(dir, "acceptance-status.json"), map[string]Verification{"steam": pass})
	acceptanceWriteFixture(t, filepath.Join(dir, "steam-acceptance-20261001-000000.json"), fail)
	a := acceptanceTestApp(t, dir)
	acceptanceAssertFailed(t, a, "steam")
	if acceptanceNewer(pass, fail) || !acceptanceNewer(fail, pass) {
		t.Fatal("tie-breaker can restore a pass over a recorded failure")
	}
}

func TestAcceptanceValidatesRealServiceProofAndAggregate(t *testing.T) {
	for _, id := range []string{"steam", "epic"} {
		t.Run(id, func(t *testing.T) {
			valid := acceptanceFixture(id, acceptanceFixtureTime(), true)
			if !validAcceptanceReport(id, valid) {
				t.Fatal("complete service proof was rejected")
			}
			failure := acceptanceFixture(id, acceptanceFixtureTime(), false)
			if !validAcceptanceReport(id, failure) {
				t.Fatal("legitimate early failure was rejected")
			}
			other := "steam"
			if id == "steam" {
				other = "epic"
			}
			if validAcceptanceReport(other, valid) || validAcceptanceReport("unknown", valid) {
				t.Fatal("proof was accepted for another service")
			}
			for index := range valid.Checks {
				partial := valid
				partial.Checks = append(append([]Check{}, valid.Checks[:index]...), valid.Checks[index+1:]...)
				if validAcceptanceReport(id, partial) {
					t.Error("pass without a necessary check was accepted", valid.Checks[index].Name)
				}
			}
			for _, kind := range []string{"empty-version", "empty-scope", "missing-started", "malformed-finished", "time-reversed", "future-time", "no-checks", "empty-check-name", "duplicate-check-name", "passed-with-failed-check", "failed-with-all-passed-checks"} {
				t.Run(kind, func(t *testing.T) {
					v := valid
					v.Checks = append([]Check{}, valid.Checks...)
					switch kind {
					case "empty-version":
						v.Version = "  "
					case "empty-scope":
						v.Scope = "\n\t"
					case "missing-started":
						v.Started = ""
					case "malformed-finished":
						v.Finished = "recently"
					case "time-reversed":
						v.Finished = acceptanceFixtureTime().Add(-2 * time.Minute).Format(time.RFC3339)
					case "future-time":
						v.Finished = time.Now().Add(time.Hour).Format(time.RFC3339)
					case "no-checks":
						v.Checks = nil
					case "empty-check-name":
						v.Checks[0].Name = " "
					case "duplicate-check-name":
						v.Checks = append(v.Checks, v.Checks[0])
					case "passed-with-failed-check":
						v.Checks[0].Passed = false
					case "failed-with-all-passed-checks":
						v.Passed = false
					}
					if validAcceptanceReport(id, v) {
						t.Fatal("invalid or inconsistent report was accepted")
					}
				})
			}
		})
	}
}

func TestAcceptanceCorruptNullAndForgedRecordsNeverShowPass(t *testing.T) {
	for _, kind := range []string{"null", "corrupt-cache", "forged-cache", "forged-report", "corrupt-report", "wrong-service-report", "unknown-source-cache", "oversized-report"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			cachePath := filepath.Join(dir, "acceptance-status.json")
			reportPath := filepath.Join(dir, "steam-acceptance-20261001-000000.json")
			forged := acceptanceFixture("steam", acceptanceFixtureTime(), true)
			forged.Checks = forged.Checks[:1]
			switch kind {
			case "null":
				if err := os.WriteFile(cachePath, []byte("null"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(reportPath, []byte("null"), 0600); err != nil {
					t.Fatal(err)
				}
			case "corrupt-cache":
				if err := os.WriteFile(cachePath, []byte(`{"steam":`), 0600); err != nil {
					t.Fatal(err)
				}
			case "forged-cache":
				acceptanceWriteFixture(t, cachePath, map[string]Verification{"steam": forged})
			case "forged-report":
				acceptanceWriteFixture(t, reportPath, forged)
			case "corrupt-report":
				if err := os.WriteFile(reportPath, []byte(`{"passed":true`), 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong-service-report":
				acceptanceWriteFixture(t, reportPath, acceptanceFixture("epic", acceptanceFixtureTime(), true))
			case "unknown-source-cache":
				acceptanceWriteFixture(t, cachePath, map[string]Verification{"pypi": acceptanceFixture("steam", acceptanceFixtureTime(), true)})
			case "oversized-report":
				large := acceptanceFixture("steam", acceptanceFixtureTime(), true)
				large.Checks[0].Detail = strings.Repeat("x", 256*1024)
				acceptanceWriteFixture(t, reportPath, large)
			}
			a := acceptanceTestApp(t, dir)
			status, routing := acceptanceRoutingSnapshot(t, a)
			if len(acceptanceStoredSnapshot(a)) != 0 || len(status) != 0 || routing.Active {
				t.Fatal("invalid optional history displayed green acceptance or enabled routing", status, routing)
			}
		})
	}
}

func TestAcceptanceStatusSaveFailureReturnsAndPersistsFailedReport(t *testing.T) {
	dir := t.TempDir()
	a := acceptanceTestApp(t, dir)
	statusPath := filepath.Join(dir, "acceptance-status.json")
	if err := os.Mkdir(statusPath, 0700); err != nil {
		t.Fatal(err)
	}
	out := a.finishVerification("steam", acceptanceFixture("steam", acceptanceFixtureTime(), true))
	if out.Passed {
		t.Fatal("status save failure was returned as a successful verification")
	}
	acceptanceAssertFailed(t, a, "steam")
	var report Verification
	if err := readAcceptanceJSON(out.Report, &report); err != nil || report.Passed || !validAcceptanceReport("steam", report) {
		t.Fatal("recoverable report still claims a pass after status persistence failed", err, report.Passed)
	}
	var failedCheck bool
	for _, check := range out.Checks {
		failedCheck = failedCheck || (!check.Passed && strings.Contains(check.Name, "保存"))
	}
	if !failedCheck {
		t.Fatal("save failure was not explained in the returned checks")
	}
	a.stop()
	a.journal.Close()
	if err := os.Remove(statusPath); err != nil {
		t.Fatal(err)
	}
	restarted := acceptanceTestApp(t, dir)
	acceptanceAssertFailed(t, restarted, "steam")
}

func TestAcceptanceReportSaveFailureNeverShowsPassInMemoryOrAPI(t *testing.T) {
	a := acceptanceTestApp(t, t.TempDir())
	// Only the fake runtime path becomes a regular file; no ACL or system
	// configuration changes are needed to make report creation fail reliably.
	blockedPath := filepath.Join(t.TempDir(), "runtime-is-file")
	original := []byte("do not overwrite this fake obstruction")
	if err := os.WriteFile(blockedPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	a.dir = blockedPath
	out := a.finishVerification("epic", acceptanceFixture("epic", acceptanceFixtureTime(), true))
	if out.Passed {
		t.Fatal("report creation failure was returned as a successful verification")
	}
	acceptanceAssertFailed(t, a, "epic")
	var reportFailure bool
	for _, check := range out.Checks {
		reportFailure = reportFailure || (!check.Passed && check.Name == "保存报告")
	}
	if !reportFailure {
		t.Fatal("failed report creation was not included in returned checks")
	}
	if after, err := os.ReadFile(blockedPath); err != nil || !bytes.Equal(after, original) {
		t.Fatal("failed report creation overwrote the obstruction file", err)
	}
}
