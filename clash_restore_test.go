package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type clashRestoreFixture struct {
	a          *App
	profile    string
	state      string
	current    []byte
	journal    []byte
	activePath string
	basePath   string
	base       []byte
	server     *httptest.Server
	mu         sync.Mutex
	requests   []string
}

func newClashRestoreFixture(t *testing.T, kind string) *clashRestoreFixture {
	t.Helper()
	f := &clashRestoreFixture{a: testApp(t)}
	data := t.TempDir()
	f.profile = filepath.Join(data, "user-profile.yml")
	f.state = filepath.Join(f.a.dir, "clash-state.json")
	f.activePath = filepath.Join(data, "epic-ipv6-active.yaml")
	f.basePath = filepath.Join(data, "config.yaml")
	f.current = []byte("# 用户订阅已刷新，请保留字节和节点\r\nproxies:\n  - {name: external-updated-node, type: socks5, server: changed.example, port: 1080}\nproxy-groups:\n  - {name: MyChoice, type: select, proxies: [external-updated-node, DIRECT]}\nrules:\n  - MATCH,MyChoice\n")
	switch kind {
	case "profile-marker":
		f.current = append(f.current, []byte("# BEGIN EpicIPv6Helper\n")...)
	case "profile-reference":
		f.current = append(f.current, []byte("unused-user-value: EpicIPv6Helper-local\n")...)
	case "escaped-profile-reference":
		f.current = append(f.current, []byte("unused-user-value: \"\\u0045picIPv6Helper-local\"\n")...)
	case "invalid-profile":
		f.current = []byte("invalid: [unterminated\n")
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer fake-test-secret" {
			http.Error(w, "test forbids configuration mutation or unauthenticated request", 405)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/proxies":
			if kind == "proxies-api-failure" {
				http.Error(w, "fake controller unavailable", 503)
				return
			}
			switch kind {
			case "live-proxy":
				fmt.Fprint(w, `{"proxies":{"EpicIPv6Helper-local":{"type":"Http"},"DIRECT":{"type":"Direct"}}}`)
			case "live-group-reference":
				fmt.Fprint(w, `{"proxies":{"GLOBAL":{"type":"Selector","all":["DIRECT","EpicIPv6Helper-local"],"now":"DIRECT"}}}`)
			case "missing-proxies":
				fmt.Fprint(w, `{}`)
			case "null-proxies":
				fmt.Fprint(w, `{"proxies":null}`)
			case "malformed-proxies":
				fmt.Fprint(w, `{"proxies":`)
			default:
				fmt.Fprint(w, `{"proxies":{"DIRECT":{"type":"Direct"},"external-updated-node":{"type":"Socks5"},"MyChoice":{"type":"Selector","now":"external-updated-node","all":["external-updated-node","DIRECT"]}}}`)
			}
		case "/rules":
			if kind == "rules-api-failure" {
				http.Error(w, "fake controller unavailable", 503)
				return
			}
			switch kind {
			case "live-rule":
				fmt.Fprint(w, `{"rules":[{"type":"Domain","payload":"raw.githubusercontent.com","proxy":"EpicIPv6Helper-local"},{"type":"Match","payload":"","proxy":"MyChoice"}]}`)
			case "missing-rules":
				fmt.Fprint(w, `{}`)
			case "null-rules":
				fmt.Fprint(w, `{"rules":null}`)
			case "malformed-rules":
				fmt.Fprint(w, `{"rules":`)
			case "rule-without-target":
				fmt.Fprint(w, `{"rules":[{"type":"Match","payload":""}]}`)
			case "profile-changed-again":
				updated := append(append([]byte{}, f.current...), []byte("# user edited while API was checked\n")...)
				if err := os.WriteFile(f.profile, updated, 0600); err != nil {
					http.Error(w, "fake profile mutation failed", 500)
					return
				}
				fmt.Fprint(w, `{"rules":[{"type":"Match","payload":"","proxy":"MyChoice"}]}`)
			default:
				fmt.Fprint(w, `{"rules":[{"type":"Match","payload":"","proxy":"MyChoice"}]}`)
			}
		default:
			http.Error(w, "unexpected test controller request", 404)
		}
	}))
	t.Cleanup(f.server.Close)
	f.base = []byte("external-controller: " + strings.TrimPrefix(f.server.URL, "http://") + "\nsecret: fake-test-secret\nmode: rule\n")
	if err := os.WriteFile(f.basePath, f.base, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.profile, f.current, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.activePath, []byte("OLD MANAGED SNAPSHOT MUST BE RETAINED AS LOCAL BACKUP"), 0600); err != nil {
		t.Fatal(err)
	}
	s := clashState{Data: data, Profile: f.profile,
		Original:      []byte("proxies:\n  - {name: old-node, type: socks5, server: old.example, port: 1080}\nrules:\n  - MATCH,DIRECT\n"),
		AdditionProxy: "  # BEGIN EpicIPv6Helper\n  - {name: EpicIPv6Helper-local, type: http, server: 127.0.0.1, port: 17891}\n  # END EpicIPv6Helper\n",
		AdditionRules: "  # BEGIN EpicIPv6Helper\n  - DOMAIN,raw.githubusercontent.com,EpicIPv6Helper-local\n  # END EpicIPv6Helper\n",
		BaseConfig:    []byte("OLD BASE SNAPSHOT MUST NOT BE RELOADED"),
		Selections:    map[string]string{"MyChoice": "old-node"}, SourceID: "githubraw", Hosts: []string{"raw.githubusercontent.com"}}
	f.journal, _ = json.MarshalIndent(s, "", "  ")
	if err := os.WriteFile(f.state, f.journal, 0600); err != nil {
		t.Fatal(err)
	}
	if kind == "controller-offline" {
		f.server.Close()
	}
	return f
}

func (f *clashRestoreFixture) assertNoConfigurationWrite(t *testing.T, wantProfile []byte) {
	t.Helper()
	profile, err := os.ReadFile(f.profile)
	if err != nil || !bytes.Equal(profile, wantProfile) {
		t.Fatal("restore overwrote the externally refreshed profile", err)
	}
	base, err := os.ReadFile(f.basePath)
	if err != nil || !bytes.Equal(base, f.base) {
		t.Fatal("external cleanup rewrote current controller configuration", err)
	}
	active, err := os.ReadFile(f.activePath)
	if err != nil || string(active) != "OLD MANAGED SNAPSHOT MUST BE RETAINED AS LOCAL BACKUP" {
		t.Fatal("external cleanup removed or rewrote old snapshot", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.profile), "epic-ipv6-restore.yaml")); !os.IsNotExist(err) {
		t.Fatal("external cleanup prepared stale configuration for reload", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, request := range f.requests {
		if request != "GET /proxies" && request != "GET /rules" {
			t.Fatal("external cleanup mutated live config or selections", request)
		}
	}
}

func TestClashRestoreExternalRefreshArchivesOnlyJournal(t *testing.T) {
	f := newClashRestoreFixture(t, "success")
	if err := f.a.clashAction("restore"); err != nil {
		t.Fatal(err)
	}
	f.assertNoConfigurationWrite(t, f.current)
	if f.a.clashActive() {
		t.Fatal("externally removed integration still marked active")
	}
	archives, err := filepath.Glob(filepath.Join(f.a.dir, "clash-restored-external-*.json"))
	if err != nil || len(archives) != 1 {
		t.Fatal("original journal was not archived exactly once", archives, err)
	}
	archived, err := os.ReadFile(archives[0])
	if err != nil || !bytes.Equal(archived, f.journal) {
		t.Fatal("recovery archive changed original journal", err)
	}
	f.mu.Lock()
	requests := append([]string{}, f.requests...)
	f.mu.Unlock()
	if strings.Join(requests, ",") != "GET /proxies,GET /rules" {
		t.Fatal("both live inventories were not explicitly checked", requests)
	}
	if err := f.a.clashAction("restore"); err != nil {
		t.Fatal("external cleanup is not idempotent", err)
	}
}

func TestClashRestoreExternalRefreshRejectsUncertainOrManagedState(t *testing.T) {
	for _, kind := range []string{"profile-marker", "profile-reference", "escaped-profile-reference", "invalid-profile", "live-proxy", "live-group-reference", "live-rule", "proxies-api-failure", "rules-api-failure", "controller-offline", "missing-proxies", "null-proxies", "malformed-proxies", "missing-rules", "null-rules", "malformed-rules", "rule-without-target", "profile-changed-again"} {
		t.Run(kind, func(t *testing.T) {
			f := newClashRestoreFixture(t, kind)
			if err := f.a.clashAction("restore"); err == nil {
				t.Fatal("uncertain or active integration was discarded", kind)
			}
			want := f.current
			if kind == "profile-changed-again" {
				want = append(append([]byte{}, f.current...), []byte("# user edited while API was checked\n")...)
			}
			f.assertNoConfigurationWrite(t, want)
			journal, err := os.ReadFile(f.state)
			if err != nil || !bytes.Equal(journal, f.journal) {
				t.Fatal("rejected cleanup discarded recovery journal", err)
			}
			archives, err := filepath.Glob(filepath.Join(f.a.dir, "clash-restored-external-*.json"))
			if err != nil || len(archives) != 0 {
				t.Fatal("rejected cleanup archived a possibly active journal", archives, err)
			}
		})
	}
}
