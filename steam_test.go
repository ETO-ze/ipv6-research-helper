package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func steamFakeApp(t *testing.T, original []byte) *App {
	t.Helper()
	// Hosts roundtrip tests must not invoke the real system ipconfig/reg helpers.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("IPV6_HELPER_STEAM_PATH", t.TempDir())
	a := testApp(t)
	a.hostsPath = filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(a.hostsPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	return a
}

func steamExpectedTestBlock(hosts []string) []byte {
	b := []byte(steamMarkerStart + "\r\n")
	for _, host := range hosts {
		b = append(b, []byte(bindIP+" "+host+"\r\n::1 "+host+"\r\n")...)
	}
	return append(b, []byte(steamMarkerEnd+"\r\n")...)
}

func steamReadFake(t *testing.T, a *App) []byte {
	t.Helper()
	b, err := os.ReadFile(a.hostsPath)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type steamRestoreObservedConn struct {
	net.Conn
	onClose func()
	closed  chan struct{}
	once    sync.Once
}

func (c *steamRestoreObservedConn) Close() error {
	c.once.Do(func() {
		if c.onClose != nil {
			c.onClose()
		}
		close(c.closed)
	})
	return c.Conn.Close()
}

func TestSteamRestoreClosesOnlyManagedUpstreamsWhileDialsBlocked(t *testing.T) {
	original := []byte("# original UHE preserved\r\n240e:928:801::e dl.steam.clngaa.com.z.ngaagslb.net #UHE_")
	a := steamFakeApp(t, original)
	// A valid recovery record may use DNS case variations; closure still must
	// match the normalized host used by connect.
	if err := a.steamApplyLocked([]string{"CACHE7-HKG1.STEAMCONTENT.COM"}); err != nil {
		t.Fatal(err)
	}
	var observed []*steamRestoreObservedConn
	var entries []*Entry
	for index, row := range []struct{ host, kind string }{
		{"cache7-hkg1.steamcontent.com", "download6"},
		{"cache8-hkg1.steamcontent.com", "download6"},
		{"download.epicgames.com", "download6"},
		{"cache7-hkg1.steamcontent.com", "dns6"},
	} {
		left, right := net.Pipe()
		t.Cleanup(func() { right.Close() })
		conn := &steamRestoreObservedConn{Conn: left, closed: make(chan struct{})}
		if index == 0 {
			conn.onClose = func() {
				if !a.steamRestoring.Load() || a.steamRouteEpoch.Load() != 1 {
					t.Error("managed connection closed before restore blocked old/new dials")
				}
				if _, err := a.connect(context.Background(), "cache7-hkg1.steamcontent.com", net.ParseIP("2001:4860:4860::8888"), "443", "download6"); err == nil || !strings.Contains(err.Error(), "正在恢复") {
					t.Error("new Steam dial was not blocked before reaching the network", err)
				}
			}
		}
		entry := &Entry{ID: a.serial.Add(1), Host: row.host, Kind: row.kind, Active: true}
		a.mu.Lock()
		a.entries = append(a.entries, entry)
		a.active[entry.ID] = &countedConn{Conn: conn, a: a, e: entry}
		a.mu.Unlock()
		observed = append(observed, conn)
		entries = append(entries, entry)
	}
	if err := a.restoreSteam(); err != nil {
		t.Fatal(err)
	}
	for index, conn := range observed {
		select {
		case <-conn.closed:
			if index != 0 {
				t.Error("restore closed an unrelated upstream", index)
			}
		default:
			if index == 0 {
				t.Error("restore retained the managed Steam upstream")
			}
		}
	}
	a.mu.Lock()
	for index, entry := range entries {
		_, active := a.active[entry.ID]
		if entry.Active != (index != 0) || active != (index != 0) {
			t.Error("closure was not reflected in connection accounting", index)
		}
	}
	a.mu.Unlock()
	if a.steamRestoring.Load() || a.steamRouteEpoch.Load() != 1 || a.v6.Load() != 0 {
		t.Fatal("restore left dial gate active, lost its epoch, or opened real network")
	}
	if !bytes.Equal(steamReadFake(t, a), original) {
		t.Fatal("managed closure prevented exact original hosts restoration")
	}
}

func TestSteamStopFailureKeepsRoutingActiveUntilSuccessfulRetry(t *testing.T) {
	original := []byte("# original\r\n240e:928:801::e dl.steam.clngaa.com.z.ngaagslb.net #UHE_")
	a := steamFakeApp(t, original)
	if err := a.steamApplyLocked([]string{"cache7-hkg1.steamcontent.com"}); err != nil {
		t.Fatal(err)
	}
	applied := steamReadFake(t, a)
	journalPath := filepath.Join(a.dir, "steam-route.json")
	journal, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(a.hostsPath, 0444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(a.hostsPath, 0600) })
	stopRequest := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:17890/api/stop", nil)
		r.Header.Set("X-Token", a.token)
		w := httptest.NewRecorder()
		a.ui(w, r)
		return w
	}
	if w := stopRequest(); w.Code != 409 || !strings.Contains(w.Body.String(), "已保留恢复记录及备份") {
		t.Fatal("failed restoration was reported as a successful stop", w.Code, w.Body.String())
	}
	status := a.routingStatus()
	if !status.Active || status.SourceID != "steam" || status.Mode != "hosts" || !a.steamActive() {
		t.Fatal("failed restoration reported disabled routing despite retained hosts", status)
	}
	if a.steamRestoring.Load() || a.steamRouteEpoch.Load() != 1 || !bytes.Equal(steamReadFake(t, a), applied) {
		t.Fatal("failed stop left the gate active, lost its epoch, or changed hosts")
	}
	if saved, err := os.ReadFile(journalPath); err != nil || !bytes.Equal(saved, journal) {
		t.Fatal("failed stop discarded or changed recovery data", err)
	}
	if err := os.Chmod(a.hostsPath, 0600); err != nil {
		t.Fatal(err)
	}
	if w := stopRequest(); w.Code != 200 {
		t.Fatal("stop could not recover after its failure cause was removed", w.Code, w.Body.String())
	}
	if status := a.routingStatus(); status.Active || a.steamActive() || a.steamRestoring.Load() {
		t.Fatal("successful retry retained active routing or its gate", status)
	}
	if a.steamRouteEpoch.Load() != 2 || !bytes.Equal(steamReadFake(t, a), original) {
		t.Fatal("successful retry lost its epoch or exact original hosts bytes")
	}
	if _, err := os.Stat(journalPath); !os.IsNotExist(err) {
		t.Fatal("successful retry retained the active recovery journal", err)
	}
}

type steamConcurrentCloseConn struct {
	net.Conn
	calls                  atomic.Int32
	first, second, release chan struct{}
}

func (c *steamConcurrentCloseConn) Close() error {
	switch c.calls.Add(1) {
	case 1:
		close(c.first)
	case 2:
		close(c.second)
	}
	<-c.release
	return c.Conn.Close()
}

func TestSteamRestoreAndStopCanConcurrentlyCloseSameUpstream(t *testing.T) {
	original := []byte("# original without trailing newline")
	a := steamFakeApp(t, original)
	if err := a.steamApplyLocked([]string{"cache7-hkg1.steamcontent.com"}); err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	t.Cleanup(func() { right.Close() })
	conn := &steamConcurrentCloseConn{Conn: left, first: make(chan struct{}), second: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(conn.release) }) }
	t.Cleanup(release)
	entry := &Entry{ID: a.serial.Add(1), Host: "cache7-hkg1.steamcontent.com", Kind: "download6", Active: true}
	a.mu.Lock()
	a.entries = append(a.entries, entry)
	a.active[entry.ID] = &countedConn{Conn: conn, a: a, e: entry}
	a.mu.Unlock()
	restoreResult := make(chan error, 1)
	go func() { restoreResult <- a.restoreSteam() }()
	select {
	case <-conn.first:
	case <-time.After(2 * time.Second):
		t.Fatal("restore did not close the managed upstream")
	}
	if !a.mu.TryLock() {
		t.Fatal("restore held the connection accounting lock while calling Close")
	}
	a.mu.Unlock()
	stopDone := make(chan struct{})
	go func() { a.stop(); close(stopDone) }()
	select {
	case <-conn.second:
	case <-time.After(2 * time.Second):
		t.Fatal("stop could not collect and close the same upstream during restoration")
	}
	release()
	select {
	case err := <-restoreResult:
		if err != nil {
			t.Fatal("concurrent shutdown prevented hosts restoration", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("restore deadlocked during concurrent Close")
	}
	select {
	case <-stopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("stop deadlocked during concurrent Close")
	}
	a.mu.Lock()
	active := len(a.active)
	closed := !entry.Active
	a.mu.Unlock()
	if active != 0 || !closed || a.steamRestoring.Load() || !bytes.Equal(steamReadFake(t, a), original) {
		t.Fatal("concurrent Close retained active connection/gate or changed original hosts")
	}
}

func TestSteamHostsRoundtripPreservesUHEAndExactBytes(t *testing.T) {
	for _, ending := range []string{"", "\n", "\r\n"} {
		t.Run(strings.ReplaceAll(ending, "\n", "LF"), func(t *testing.T) {
			original := []byte("# 用户原有规则\r\n240e:928:801::e dl.steam.clngaa.com.z.ngaagslb.net #UHE_\r\n127.0.0.1 unrelated.local" + ending)
			a := steamFakeApp(t, original)
			hosts := []string{"cache7-hkg1.steamcontent.com", "cache8-hkg1.steamcontent.com"}
			if err := a.steamApplyLocked(hosts); err != nil {
				t.Fatal(err)
			}
			applied := steamReadFake(t, a)
			if !bytes.HasPrefix(applied, original) || !bytes.Contains(applied, steamExpectedTestBlock(hosts)) {
				t.Fatal("original rules changed or exact IPv4/IPv6 loopback block missing")
			}
			backups, err := filepath.Glob(filepath.Join(a.dir, "steam-hosts-before-*.bak"))
			if err != nil || len(backups) != 1 {
				t.Fatal("missing unique pre-change backup", backups, err)
			}
			backup, err := os.ReadFile(backups[0])
			if err != nil || !bytes.Equal(backup, original) {
				t.Fatal("backup does not preserve original bytes")
			}
			if err := a.restoreSteam(); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(steamReadFake(t, a), original) {
				t.Fatal("roundtrip changed original bytes or final newline")
			}
			if _, err := os.Stat(filepath.Join(a.dir, "steam-route.json")); !os.IsNotExist(err) {
				t.Fatal("successful restore retained active journal")
			}
			if err := a.restoreSteam(); err != nil {
				t.Fatal("restore is not idempotent", err)
			}
		})
	}
}

func TestSteamRestorePreservesExternalEditsAroundManagedBlock(t *testing.T) {
	original := []byte("127.0.0.1 original.local")
	a := steamFakeApp(t, original)
	if err := a.steamApplyLocked([]string{"cache7-hkg1.steamcontent.com"}); err != nil {
		t.Fatal(err)
	}
	prefix := []byte("# external prefix\r\n")
	suffix := []byte("# external suffix\r\n127.0.0.1 later.local\r\n")
	changed := append(append(append([]byte{}, prefix...), steamReadFake(t, a)...), suffix...)
	if err := os.WriteFile(a.hostsPath, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.restoreSteam(); err != nil {
		t.Fatal(err)
	}
	expected := append(append(append([]byte{}, prefix...), original...), suffix...)
	if !bytes.Equal(steamReadFake(t, a), expected) {
		t.Fatal("external user edits were overwritten")
	}
}

func TestSteamRestoreRejectsChangedDuplicatedOrDeletedBlock(t *testing.T) {
	for _, kind := range []string{"edited", "duplicated", "deleted", "changed-separator"} {
		t.Run(kind, func(t *testing.T) {
			a := steamFakeApp(t, []byte("127.0.0.1 original.local"))
			if err := a.steamApplyLocked([]string{"cache7-hkg1.steamcontent.com"}); err != nil {
				t.Fatal(err)
			}
			state, err := a.steamState()
			if err != nil {
				t.Fatal(err)
			}
			changed := steamReadFake(t, a)
			switch kind {
			case "edited":
				changed = bytes.Replace(changed, []byte(bindIP+" cache7"), []byte("192.0.2.8 cache7"), 1)
			case "duplicated":
				changed = append(changed, state.Block...)
			case "deleted":
				changed = []byte("# user replaced file\n")
			case "changed-separator":
				changed = bytes.Replace(changed, append(append([]byte{}, state.Separator...), state.Block...), append([]byte("\n"), state.Block...), 1)
			}
			if err := os.WriteFile(a.hostsPath, changed, 0600); err != nil {
				t.Fatal(err)
			}
			if err := a.restoreSteam(); err == nil {
				t.Fatal("modified or ambiguous block was accepted")
			}
			if !bytes.Equal(steamReadFake(t, a), changed) {
				t.Fatal("failed restore overwrote user changes")
			}
			if _, err := os.Stat(filepath.Join(a.dir, "steam-route.json")); err != nil {
				t.Fatal("failed restore discarded recovery journal", err)
			}
		})
	}
}

func TestSteamRestoreRejectsMalformedRecoveryRecord(t *testing.T) {
	host := "cache7-hkg1.steamcontent.com"
	valid := steamRouteState{Hosts: []string{host}, Block: steamExpectedTestBlock([]string{host}), BeforeSHA256: bytesSHA256([]byte("# original\r\n"))}
	for _, kind := range []string{"invalid-json", "empty-hosts", "non-steam-host", "wrong-block-host", "unrelated-line-in-block", "arbitrary-separator", "missing-hash", "non-hex-hash", "short-hash", "long-hash"} {
		t.Run(kind, func(t *testing.T) {
			s := valid
			s.Hosts = append([]string{}, valid.Hosts...)
			s.Block = append([]byte{}, valid.Block...)
			switch kind {
			case "empty-hosts":
				s.Hosts = nil
			case "non-steam-host":
				s.Hosts = []string{"example.com"}
			case "wrong-block-host":
				s.Block = steamExpectedTestBlock([]string{"cache8-hkg1.steamcontent.com"})
			case "unrelated-line-in-block":
				s.Block = bytes.Replace(s.Block, []byte(steamMarkerEnd), []byte("127.0.0.1 private.user.local\r\n"+steamMarkerEnd), 1)
			case "arbitrary-separator":
				s.Separator = []byte("127.0.0.1 private.user.local\r\n")
			case "missing-hash":
				s.BeforeSHA256 = ""
			case "non-hex-hash":
				s.BeforeSHA256 = strings.Repeat("z", 64)
			case "short-hash":
				s.BeforeSHA256 = strings.Repeat("ab", 31)
			case "long-hash":
				s.BeforeSHA256 = strings.Repeat("ab", 33)
			}
			original := append(append([]byte("# before\r\n"), s.Separator...), s.Block...)
			a := steamFakeApp(t, original)
			raw, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "invalid-json" {
				raw = []byte(`{"hosts":`)
			}
			path := filepath.Join(a.dir, "steam-route.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := a.steamState(); err == nil {
				t.Fatal("invalid recovery record accepted")
			}
			if err := a.restoreSteam(); err == nil {
				t.Fatal("invalid recovery record permitted deletion")
			}
			if !bytes.Equal(steamReadFake(t, a), original) {
				t.Fatal("invalid record changed hosts")
			}
			if saved, err := os.ReadFile(path); err != nil || !bytes.Equal(saved, raw) {
				t.Fatal("invalid record was discarded")
			}
		})
	}
}

func TestSteamEnsureAfterShutdownCannotStartListeners(t *testing.T) {
	a := steamFakeApp(t, []byte("# original\n"))
	a.stop()
	if err := a.ensureSteamListenersLocked(); err == nil {
		t.Fatal("stopped helper reopened listeners")
	}
	a.listenerMu.Lock()
	started, count := a.steamListenersStarted, len(a.listeners)
	a.listenerMu.Unlock()
	if started || count != 0 {
		t.Fatal("shutdown was bypassed to create listeners", started, count)
	}
	select {
	case <-a.done:
	default:
		t.Fatal("shutdown did not signal completion")
	}
}

func TestSteamConcurrentEnsureAndStopCloseAllExistingListeners(t *testing.T) {
	a := steamFakeApp(t, []byte("# original\n"))
	// Exercise listener ownership using an ephemeral local port. Pretend Steam
	// listeners already exist so no test can bind the user's real port 80/443.
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a.listenerMu.Lock()
	a.listeners = append(a.listeners, listener)
	a.steamListenersStarted = true
	a.listenerMu.Unlock()
	start := make(chan struct{})
	var workers sync.WaitGroup
	for worker := 0; worker < 24; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for step := 0; step < 32; step++ {
				a.networkMu.Lock()
				_ = a.ensureSteamListenersLocked()
				a.networkMu.Unlock()
			}
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		<-start
		a.stop()
	}()
	close(start)
	workers.Wait()
	if _, err := listener.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatal("shutdown retained an open listener", err)
	}
	for repeat := 0; repeat < 3; repeat++ {
		if err := a.ensureSteamListenersLocked(); err == nil {
			t.Fatal("ensure reported listening after concurrent shutdown")
		}
		a.stop() // Repeated stop must stay idempotent.
	}
	a.listenerMu.Lock()
	stopping, count := a.stopping, len(a.listeners)
	a.listenerMu.Unlock()
	if !stopping || count != 1 {
		t.Fatal("concurrent shutdown lost ownership or added listeners", stopping, count)
	}
}

func TestSteamCheckedWriteRejectsStaleExternalSnapshot(t *testing.T) {
	a := steamFakeApp(t, []byte("# user edited file after snapshot\n"))
	current := steamReadFake(t, a)
	if err := writeSteamHostsChecked(a.hostsPath, []byte("# old snapshot\n"), []byte("# would overwrite user edit\n")); err == nil {
		t.Fatal("stale snapshot overwrote concurrent user edit")
	}
	if !bytes.Equal(steamReadFake(t, a), current) {
		t.Fatal("failed checked write changed current hosts")
	}
	staged, err := filepath.Glob(filepath.Join(filepath.Dir(a.hostsPath), ".ipv6helper-steam-*.tmp"))
	if err != nil || len(staged) != 0 {
		t.Fatal("failed checked write left staging data beside hosts", staged, err)
	}
}

func TestSteamFailedWriteRetainsRecoveryAndCanCleanUnappliedRecord(t *testing.T) {
	original := []byte("# original read-only file\n")
	a := steamFakeApp(t, original)
	if err := os.Chmod(a.hostsPath, 0444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(a.hostsPath, 0600) })
	if err := a.steamApplyLocked([]string{"cache7-hkg1.steamcontent.com"}); err == nil {
		t.Fatal("write to read-only hosts unexpectedly succeeded")
	}
	if !bytes.Equal(steamReadFake(t, a), original) {
		t.Fatal("failed activation altered original hosts")
	}
	if _, err := a.steamState(); err != nil {
		t.Fatal("failed write discarded or corrupted recovery journal", err)
	}
	if a.steamActive() {
		t.Fatal("unapplied recovery journal reported active hosts")
	}
	if err := a.restoreSteam(); err != nil {
		t.Fatal("unapplied journal was not safely cleared against exact original hash", err)
	}
	if !bytes.Equal(steamReadFake(t, a), original) {
		t.Fatal("cleanup of unapplied state changed hosts")
	}
}

func TestSteamConflictsRejectWithoutOverwritingOriginalRules(t *testing.T) {
	for _, original := range []string{
		"192.0.2.1 unrelated.local CACHE7-HKG1.STEAMCONTENT.COM # other tool\n",
		"::1 cache7-hkg1.steamcontent.com. # other tool\n",
		steamMarkerStart + "\n# incomplete managed block\n",
		steamMarkerEnd + "\n",
	} {
		a := steamFakeApp(t, []byte(original))
		if err := a.steamPreflight([]string{"cache7-hkg1.steamcontent.com"}); err == nil {
			t.Fatal("conflict accepted", original)
		}
		if err := a.steamApplyLocked([]string{"cache7-hkg1.steamcontent.com"}); err == nil {
			t.Fatal("conflict overwritten", original)
		}
		if !bytes.Equal(steamReadFake(t, a), []byte(original)) {
			t.Fatal("preflight/apply altered existing rules")
		}
	}
}

func TestSteamPreflightFailurePreservesExistingRoute(t *testing.T) {
	a := steamFakeApp(t, []byte("# original\n"))
	clash := []byte(`{"sourceID":"githubraw","hosts":["raw.githubusercontent.com"],"original":"dXNlciBvcmlnaW5hbA==","profile":"must-not-touch"}`)
	statePath := filepath.Join(a.dir, "clash-state.json")
	if err := os.WriteFile(statePath, clash, 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.switchRoute(RoutePlan{ID: "steam", Hosts: []string{"cache7-hkg1.steamcontent.com.evil.example"}}); err == nil {
		t.Fatal("invalid Steam host accepted")
	}
	if saved, err := os.ReadFile(statePath); err != nil || !bytes.Equal(saved, clash) {
		t.Fatal("invalid Steam selection disturbed previous route")
	}
	if err := os.Chmod(a.hostsPath, 0444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(a.hostsPath, 0600) })
	if err := a.switchRoute(RoutePlan{ID: "steam", Hosts: []string{"cache7-hkg1.steamcontent.com"}}); err == nil {
		t.Fatal("read-only hosts accepted")
	}
	if saved, err := os.ReadFile(statePath); err != nil || !bytes.Equal(saved, clash) {
		t.Fatal("permission failure disturbed previous route")
	}
	if !bytes.Equal(steamReadFake(t, a), []byte("# original\n")) {
		t.Fatal("permission failure altered hosts")
	}
}

func TestSteamHostValidationAndDiscoveryAreExact(t *testing.T) {
	for _, host := range []string{"CACHE7-HKG1.STEAMCONTENT.COM", "cache8-hkg1.steamcontent.com.", "cache12-fra2.steamcontent.com"} {
		if !steamHost(host) {
			t.Fatal("valid official cache rejected", host)
		}
	}
	for _, host := range []string{"1.1.1.1", "::1", "steamcontent.com", "cache7-hkg1.steamcontent.com.evil.example", "evil.cache7-hkg1.steamcontent.com", "cache7-hkg1.steamcontent.com:443", "cache7-hkg1.steamcontent.com\r\n", "cache7000-hkg1.steamcontent.com", "cache7-hkg1.steamcontent.com,OTHER,DIRECT"} {
		if steamHost(host) || allowedHost(host) {
			t.Fatal("non-exact host accepted", host)
		}
	}
	data := []byte("old cache7-hkg1.steamcontent.com:80\nGET https://CACHE8-HKG1.STEAMCONTENT.COM/depot/730/chunk/x\ncache7-hkg1.steamcontent.com\nevil.cache9-hkg1.steamcontent.com\ncache10-hkg1.steamcontent.com.evil.example\n")
	got := steamLogHosts(data)
	if strings.Join(got, ",") != "cache7-hkg1.steamcontent.com,cache8-hkg1.steamcontent.com" {
		t.Fatal("discovered substring rather than exact official host", got)
	}
	var lots strings.Builder
	for i := 0; i < 70; i++ {
		lots.WriteString("cache")
		lots.WriteString(strconv.Itoa(i + 1))
		lots.WriteString("-hkg1.steamcontent.com\n")
	}
	last := steamLogHosts([]byte(lots.String()))
	if len(last) != 48 || containsHost(last, "cache1-hkg1.steamcontent.com") || !containsHost(last, "cache70-hkg1.steamcontent.com") {
		t.Fatal("discovery did not bound itself to the 48 most recent unique hosts", last)
	}
}

func TestSteamAPIRequiresTokenValidOriginAndPOST(t *testing.T) {
	a := steamFakeApp(t, []byte("# original\n"))
	for _, tc := range []struct {
		method, path, token, origin string
		want                        int
	}{
		{"POST", "/api/steam/refresh", "", "", 403},
		{"POST", "/api/steam/verify", "wrong", "", 403},
		{"POST", "/api/steam/refresh", a.token, "https://evil.example", 403},
		{"GET", "/api/steam/verify", a.token, "", 403},
		{"PUT", "/api/steam/refresh", a.token, "", 403},
		{"POST", "/api/steam/not-a-real-action", a.token, "", 404},
	} {
		r := httptest.NewRequest(tc.method, "http://127.0.0.1:17890"+tc.path, nil)
		r.Header.Set("X-Token", tc.token)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		a.ui(w, r)
		if w.Code != tc.want {
			t.Fatal(tc.method, tc.path, w.Code, w.Body.String())
		}
		if a.steamListenersStarted || a.steamActive() || a.v6.Load() != 0 {
			t.Fatal("invalid API request changed route or opened upstream/listener")
		}
	}
}

func TestSteamProxyRejectsUnknownIPv4AndLookalikeHosts(t *testing.T) {
	a := steamFakeApp(t, []byte("# original\n"))
	for _, target := range []string{"http://1.1.1.1/chunk", "http://[::1]/chunk", "http://evil.example/chunk", "http://cache7-hkg1.steamcontent.com.evil.example/chunk"} {
		w := httptest.NewRecorder()
		a.proxy(w, httptest.NewRequest("GET", target, nil))
		if w.Code != 403 || a.v6.Load() != 0 {
			t.Fatal("unregistered or IP destination was not blocked before dialing", target, w.Code)
		}
	}
}

func TestSteamChunkRejectsMisleadingHTTP200(t *testing.T) {
	r := &http.Response{StatusCode: 200, Header: make(http.Header)}
	r.Header.Set("Content-Type", "application/x-steam-chunk")
	r.Header.Set("X-Content-Sha", "ce09629928fe234c3785e60ce5e782a2b33c3c9f")
	if err := validateSteamChunk(make([]byte, steamChunkSize), r); err == nil || !strings.Contains(err.Error(), "SHA256") {
		t.Fatal("size and claimed headers were accepted without matching bytes", err)
	}
	for _, size := range []int{0, steamChunkSize - 1, steamChunkSize + 1} {
		if err := validateSteamChunk(make([]byte, size), r); err == nil || !strings.Contains(err.Error(), "大小") {
			t.Fatal("truncated or oversized chunk accepted", size, err)
		}
	}
	r.StatusCode = 206
	if err := validateSteamChunk(make([]byte, steamChunkSize), r); err == nil || !strings.Contains(err.Error(), "状态") {
		t.Fatal("partial response accepted as complete block", err)
	}
	r.StatusCode = 200
	r.Header.Set("Content-Type", "text/html")
	if err := validateSteamChunk(make([]byte, steamChunkSize), r); err == nil || !strings.Contains(err.Error(), "响应头") {
		t.Fatal("website/authentication response accepted", err)
	}
	r.Header.Set("Content-Type", "application/x-steam-chunk")
	r.Header.Set("X-Content-Sha", "wrong")
	if err := validateSteamChunk(make([]byte, steamChunkSize), r); err == nil || !strings.Contains(err.Error(), "响应头") {
		t.Fatal("wrong chunk identity accepted", err)
	}
}
