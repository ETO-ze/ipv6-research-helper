package main

import (
	"context"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const nodeA = "2a04:4e42::223"
const nodeB = "2a04:4e42:200::223"

func seedNodes(a *App, host string) {
	a.cache[host] = cacheItem{ips: []net.IP{net.ParseIP(nodeA), net.ParseIP(nodeB)}, expires: time.Now().Add(time.Hour)}
}
func TestNodeFilteringFailsClosed(t *testing.T) {
	ips := []net.IP{net.ParseIP(nodeA), net.ParseIP(nodeB), net.ParseIP(nodeA), net.ParseIP("127.0.0.1"), net.ParseIP("::1"), net.ParseIP("fd00::1")}
	got, e := filterNodeIPs(ips, NodePolicy{Blocked: []string{nodeA}})
	if e != nil || len(got) != 1 || got[0].String() != nodeB {
		t.Fatal(got, e)
	}
	got, e = filterNodeIPs(ips, NodePolicy{Pinned: nodeA})
	if e != nil || len(got) != 1 || got[0].String() != nodeA {
		t.Fatal(got, e)
	}
	for _, p := range []NodePolicy{{Pinned: "2606:4700::1111"}, {Blocked: []string{nodeA, nodeB}}, {Pinned: nodeA, Blocked: []string{nodeA}}} {
		if _, e = filterNodeIPs(ips, p); e == nil {
			t.Fatal("should fail closed", p)
		}
	}
}
func TestNodePolicyPersistsAndStaysDomainScoped(t *testing.T) {
	a := testApp(t)
	seedNodes(a, "pypi.org")
	seedNodes(a, "files.pythonhosted.org")
	if e := a.setNodePolicy("pypi.org", nodeA, "pin"); e != nil {
		t.Fatal(e)
	}
	if e := a.initNodePolicies(); e != nil || a.nodePolicies["pypi.org"].Pinned != nodeA {
		t.Fatal("pin not persisted", e)
	}
	if e := a.setNodePolicy("pypi.org", nodeA, "block"); e != nil {
		t.Fatal(e)
	}
	if a.nodePolicies["pypi.org"].Pinned != "" {
		t.Fatal("blocking pin must clear pin")
	}
	if e := a.initNodePolicies(); e != nil {
		t.Fatal(e)
	}
	ips, e := a.nodeIPs(context.Background(), "pypi.org")
	if e != nil || len(ips) != 1 || ips[0].String() != nodeB {
		t.Fatal(ips, e)
	}
	ips, e = a.nodeIPs(context.Background(), "files.pythonhosted.org")
	if e != nil || len(ips) != 2 {
		t.Fatal("other domain changed", ips, e)
	}
	if e = a.setNodePolicy("pypi.org", nodeA, "pin"); e == nil {
		t.Fatal("blocked pin accepted")
	}
	if e = a.setNodePolicy("pypi.org", "", "auto"); e != nil || !blockedIP(a.nodePolicies["pypi.org"], nodeA) {
		t.Fatal("auto erased block", e)
	}
	if e = a.setNodePolicy("pypi.org", nodeA, "unblock"); e != nil {
		t.Fatal(e)
	}
	if len(a.nodePolicies["pypi.org"].Blocked) != 0 {
		t.Fatal("unblock failed")
	}
}
func TestNodeRejectsInvalidAndStalePins(t *testing.T) {
	a := testApp(t)
	seedNodes(a, "pypi.org")
	for _, q := range []struct{ host, ip, action string }{{"unknown.invalid", nodeA, "pin"}, {"pypi.org", "1.1.1.1", "pin"}, {"pypi.org", "::1", "pin"}, {"pypi.org", "fd00::1", "block"}, {"pypi.org", "2606:4700::1111", "pin"}, {"pypi.org", "2606:4700::1111", "block"}} {
		if e := a.setNodePolicy(q.host, q.ip, q.action); e == nil {
			t.Fatal("invalid selection", q)
		}
	}
	a.cache["pypi.org"] = cacheItem{ips: []net.IP{net.ParseIP(nodeA)}, expires: time.Now().Add(-time.Second)}
	if e := a.setNodePolicy("pypi.org", nodeA, "pin"); e == nil {
		t.Fatal("expired DNS pin accepted")
	}
}
func TestNodePersistenceFailureDoesNotApply(t *testing.T) {
	a := testApp(t)
	seedNodes(a, "pypi.org")
	os.Mkdir(filepath.Join(a.dir, "upstream-policies.json.tmp"), 0700)
	if e := a.setNodePolicy("pypi.org", nodeA, "block"); e == nil {
		t.Fatal("write should fail")
	}
	if len(a.nodePolicies) != 0 {
		t.Fatal("failed write applied policy")
	}
}
func TestNodeBlockClosesOnlyAffectedConnection(t *testing.T) {
	a := testApp(t)
	seedNodes(a, "pypi.org")
	var entries []*Entry
	for i, host := range []string{"pypi.org", "files.pythonhosted.org"} {
		left, right := net.Pipe()
		t.Cleanup(func() { right.Close() })
		en := &Entry{ID: uint64(i + 1), Host: host, Remote: net.JoinHostPort(nodeA, "443"), Active: true}
		cc := &countedConn{Conn: left, a: a, e: en}
		a.entries = append(a.entries, en)
		a.active[en.ID] = cc
		entries = append(entries, en)
	}
	if e := a.setNodePolicy("pypi.org", nodeA, "block"); e != nil {
		t.Fatal(e)
	}
	if entries[0].Active || !entries[1].Active {
		t.Fatal("closure escaped domain scope")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, e := a.connect(ctx, "pypi.org", net.ParseIP(nodeA), "443", "download6"); e == nil || !strings.Contains(e.Error(), "拉黑") {
		t.Fatal("blocked address was dialed", e)
	}
}
func TestNodeAPIRequiresAuth(t *testing.T) {
	a := testApp(t)
	seedNodes(a, "pypi.org")
	body := `{"host":"pypi.org","ip":"` + nodeA + `"}`
	r := httptest.NewRequest("POST", "http://127.0.0.1:17890/api/upstreams/block", strings.NewReader(body))
	w := httptest.NewRecorder()
	a.ui(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("POST", "http://127.0.0.1:17890/api/upstreams/block", strings.NewReader(body))
	r.Header.Set("X-Token", a.token)
	w = httptest.NewRecorder()
	a.ui(w, r)
	if w.Code != 200 || !blockedIP(a.nodePolicies["pypi.org"], nodeA) {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestAllCatalogSourcesHaveRoutePlans(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range routePlans() {
		if seen[p.ID] {
			t.Fatal("duplicate", p.ID)
		}
		seen[p.ID] = true
	}
	if len(seen) != len(researchSources)+2 {
		t.Fatal(len(seen))
	}
	for _, s := range researchSources {
		if !seen[s.ID] {
			t.Fatal("missing", s.ID)
		}
	}
}

func TestUnavailableNodeSelectionPreservesExistingRoute(t *testing.T) {
	a := testApp(t)
	seedNodes(a, "files.pythonhosted.org")
	for _, ip := range []string{nodeA, nodeB} {
		if e := a.setNodePolicy("files.pythonhosted.org", ip, "block"); e != nil {
			t.Fatal(e)
		}
	}
	path := filepath.Join(a.dir, "clash-state.json")
	before := []byte(`{"sourceID":"githubraw","hosts":["raw.githubusercontent.com"]}`)
	if e := os.WriteFile(path, before, 0600); e != nil {
		t.Fatal(e)
	}
	p, _ := routeSelection("pypi", "files.pythonhosted.org")
	if e := a.switchRoute(p); e == nil || !strings.Contains(e.Error(), "已保留当前接管") {
		t.Fatal(e)
	}
	after, e := os.ReadFile(path)
	if e != nil || string(after) != string(before) {
		t.Fatal("previous route was modified", e)
	}
	// Prevent testApp cleanup from treating this synthetic marker as live config.
	os.Remove(path)
}
