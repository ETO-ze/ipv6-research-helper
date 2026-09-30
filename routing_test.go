package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoutingScopesRejectUnknownAndCrossPlatform(t *testing.T) {
	for _, q := range []struct{ id, host string }{{"unknown", ""}, {"pypi", "github.com"}, {"pypi", "files.pythonhosted.org,OTHER,DIRECT"}, {"pypi", "1.1.1.1"}} {
		if _, e := routeSelection(q.id, q.host); e == nil {
			t.Fatal("unsafe scope accepted", q)
		}
	}
	p, e := routeSelection("pypi", "FILES.PYTHONHOSTED.ORG")
	if e != nil || len(p.Hosts) != 1 || p.Hosts[0] != "files.pythonhosted.org" {
		t.Fatal(p, e)
	}
	for _, p := range routePlans() {
		for _, h := range p.Hosts {
			if !allowedHost(h) || strings.ContainsAny(h, ",\r\n*") {
				t.Fatal("unsafe registered host", h)
			}
		}
	}
}
func TestRoutingConnectionClosureStaysScoped(t *testing.T) {
	p, _ := routeSelection("pypi", "")
	if containsHost(p.Hosts, "raw.githubusercontent.com") || containsHost(p.Hosts, "notpypi.org") {
		t.Fatal("unrelated connection matched")
	}
	if !containsHost(p.Hosts, "pypi.org") {
		t.Fatal("selected host missed")
	}
}
func TestRoutingAPIRequiresTokenAndRejectsUnknownBeforeMutation(t *testing.T) {
	a := testApp(t)
	r := httptest.NewRequest("POST", "http://127.0.0.1:17890/api/routing/start", strings.NewReader(`{"id":"pypi"}`))
	w := httptest.NewRecorder()
	a.ui(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("POST", "http://127.0.0.1:17890/api/routing/start", strings.NewReader(`{"id":"not-registered"}`))
	r.Header.Set("X-Token", a.token)
	w = httptest.NewRecorder()
	a.ui(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	if a.clashActive() {
		t.Fatal("invalid scope changed config")
	}
}
func TestRoutingStatusHandlesLegacyAndDoesNotExposeBackup(t *testing.T) {
	a := testApp(t)
	b, _ := json.Marshal(clashState{Original: []byte("SECRET"), Profile: "private-path"})
	os.WriteFile(filepath.Join(a.dir, "clash-state.json"), b, 0600)
	s := a.routingStatus()
	if !s.Active || s.SourceID != "epic" || len(s.Hosts) != 16 {
		t.Fatal(s)
	}
	out, _ := json.Marshal(s)
	if strings.Contains(string(out), "SECRET") || strings.Contains(string(out), "private-path") {
		t.Fatal("private config exposed")
	}
}
