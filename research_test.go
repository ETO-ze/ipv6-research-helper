package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResearchHealthReload(t *testing.T) {
	passed := SourceHealth{ID: "pypi", Status: "样本通过", Code: "sample_passed", Checked: time.Now().Add(-time.Minute).Format(time.RFC3339), Bytes: 11050, HTTP: 200, SHA256: strings.Repeat("a", 64), FinalHost: "files.pythonhosted.org"}
	for _, name := range []string{"passed", "interrupted", "null", "malformed", "incomplete-pass", "unknown-source"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			h := passed
			if name == "interrupted" {
				h.Status = "检测中"
			}
			if name == "incomplete-pass" {
				h.Bytes = 0
			}
			if name == "unknown-source" {
				h.ID = "unknown"
			}
			b, _ := json.Marshal(map[string]SourceHealth{h.ID: h})
			if name == "null" {
				b = []byte("null")
			} else if name == "malformed" {
				b = []byte(`{"pypi":`)
			}
			if e := os.WriteFile(filepath.Join(dir, "research-status.json"), b, 0600); e != nil {
				t.Fatal(e)
			}
			a, e := newApp(dir)
			if e != nil {
				t.Fatal(e)
			}
			defer a.journal.Close()
			if a.research.health == nil {
				t.Fatal("nil health map would panic on the next check")
			}
			got := a.research.health["pypi"]
			if name == "passed" {
				if got.Status != passed.Status || got.Checked != passed.Checked || got.SHA256 != passed.SHA256 {
					t.Fatal("complete saved sample lost", got)
				}
			} else if name == "interrupted" {
				if got.Status != "上次检测中断" || got.Code != "check_interrupted" || a.research.checking {
					t.Fatal("interrupted check still appears running", got)
				}
			} else if len(a.research.health) != 0 {
				t.Fatal("invalid saved sample appeared as verified", a.research.health)
			}
		})
	}
}

func TestResearchURLBoundary(t *testing.T) {
	for _, raw := range []string{"http://zenodo.org/file", "https://zenodo.org.evil.test/x", "https://127.0.0.1/file", "https://[::1]/file", "https://user:pass@zenodo.org/x", "https://zenodo.org:444/x", "file:///C:/test", "https://unknown.test/file"} {
		if _, _, e := sourceForURL(raw); e == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, raw := range []string{"https://zenodo.org/records/1/files/a.pdf", "https://files.pythonhosted.org/a.whl?signature=secret"} {
		if _, _, e := sourceForURL(raw); e != nil {
			t.Fatal(e)
		}
	}
}

func TestPMCPublicEndpointKeepsPathButPreservesSignedURL(t *testing.T) {
	_, u, e := sourceForURL("https://pmc-oa-opendata.s3.amazonaws.com/PMC1.1/a%20b.pdf?md5=abc")
	if e != nil || u.Host != "pmc-oa-opendata.s3.dualstack.us-east-1.amazonaws.com" || u.EscapedPath() != "/PMC1.1/a%20b.pdf" || u.RawQuery != "md5=abc" {
		t.Fatal("public mapping failed", u, e)
	}
	_, u, e = sourceForURL("https://pmc-oa-opendata.s3.amazonaws.com/file?X-Amz-Signature=abc")
	if e != nil || u.Host != "pmc-oa-opendata.s3.amazonaws.com" {
		t.Fatal("signed URL was rewritten")
	}
}
func TestResearchRedirectBlocksBeforeSecondRequest(t *testing.T) {
	calls := 0
	tr := mockRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://127.0.0.1/private"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	if _, _, e := researchRequest(context.Background(), tr, "https://zenodo.org/file", 0, "", -1); e == nil || calls != 1 {
		t.Fatalf("redirect accepted: %v, calls %d", e, calls)
	}
}
func TestResearchRedirectPreservesSignedQueryAndRange(t *testing.T) {
	calls := 0
	tr := mockRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://files.pythonhosted.org/a?signature=a%2Bb"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
		if r.URL.RawQuery != "signature=a%2Bb" || r.Header.Get("Range") != "bytes=7-" || r.Header.Get("If-Range") != "\"abc\"" {
			t.Fatal("request changed")
		}
		return &http.Response{StatusCode: 206, Body: io.NopCloser(strings.NewReader("x"))}, nil
	})
	r, _, e := researchRequest(context.Background(), tr, "https://zenodo.org/file", 7, "\"abc\"", -1)
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
}
func researchFixture(t *testing.T, data []byte) (*researchManager, string) {
	t.Helper()
	a := testApp(t)
	m := a.research
	m.root = t.TempDir()
	id := strings.Repeat("a", 24)
	m.jobs[id] = &ResearchJob{ID: id, Name: "sample.bin", URL: "https://zenodo.org/file", State: "已暂停"}
	return m, id
}
func response(code int, b []byte, h http.Header) *http.Response {
	if h == nil {
		h = make(http.Header)
	}
	return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(bytes.NewReader(b)), ContentLength: int64(len(b))}
}
func TestResearchCompleteAndHashMismatch(t *testing.T) {
	data := []byte("scientific data")
	sum := sha256.Sum256(data)
	for _, correct := range []bool{true, false} {
		t.Run(fmt.Sprint(correct), func(t *testing.T) {
			m, id := researchFixture(t, data)
			j := m.jobs[id]
			j.Expected = hex.EncodeToString(sum[:])
			if !correct {
				j.Expected = strings.Repeat("0", 64)
			}
			m.transport = mockRoundTripper(func(*http.Request) (*http.Response, error) { return response(200, data, nil), nil })
			e := m.download(context.Background(), id)
			if correct {
				if e != nil || j.State != "已完成" {
					t.Fatal(e)
				}
				b, _ := os.ReadFile(j.Path)
				if !bytes.Equal(b, data) {
					t.Fatal("bad output")
				}
			} else {
				if e == nil {
					t.Fatal("hash mismatch accepted")
				}
				if _, e := os.Stat(filepath.Join(m.root, id, "sample.bin")); !os.IsNotExist(e) {
					t.Fatal("bad file published")
				}
			}
		})
	}
}
func TestResearchResumeAndChangedRange(t *testing.T) {
	data := []byte("0123456789abcdef")
	for _, mode := range []string{"resume", "range ignored", "wrong offset", "changed validator"} {
		t.Run(mode, func(t *testing.T) {
			m, id := researchFixture(t, data)
			j := m.jobs[id]
			j.Validator = "\"v1\""
			dir := filepath.Join(m.root, id)
			os.MkdirAll(dir, 0700)
			os.WriteFile(filepath.Join(dir, "download.part"), data[:6], 0600)
			m.transport = mockRoundTripper(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Range") != "bytes=6-" {
					t.Fatal("missing resume")
				}
				h := http.Header{"Etag": []string{"\"v1\""}, "Content-Range": []string{"bytes 6-15/16"}}
				if mode == "range ignored" {
					return response(200, data, h), nil
				}
				if mode == "wrong offset" {
					h.Set("Content-Range", "bytes 5-14/16")
				}
				if mode == "changed validator" {
					h.Set("ETag", "\"v2\"")
				}
				return response(206, data[6:], h), nil
			})
			e := m.download(context.Background(), id)
			if mode == "resume" || mode == "range ignored" {
				if e != nil {
					t.Fatal(e)
				}
				b, _ := os.ReadFile(j.Path)
				if !bytes.Equal(b, data) {
					t.Fatal("corrupt resume")
				}
			} else {
				if e == nil {
					t.Fatal("bad resume accepted")
				}
				b, _ := os.ReadFile(filepath.Join(dir, "download.part"))
				if !bytes.Equal(b, data[:6]) {
					t.Fatal("old data modified")
				}
			}
		})
	}
}
func TestResearchRejectHTMLAndTruncation(t *testing.T) {
	for _, mode := range []string{"html", "truncated", "empty"} {
		t.Run(mode, func(t *testing.T) {
			m, id := researchFixture(t, nil)
			m.transport = mockRoundTripper(func(*http.Request) (*http.Response, error) {
				if mode == "html" {
					return response(200, []byte("<!doctype html><html>login"), nil), nil
				}
				if mode == "empty" {
					return response(200, nil, nil), nil
				}
				r := response(200, []byte("abc"), nil)
				r.ContentLength = 100
				return r, nil
			})
			if e := m.download(context.Background(), id); e == nil {
				t.Fatal("invalid content accepted")
			}
		})
	}
}
func TestResearchNamesStayInsideTask(t *testing.T) {
	for _, s := range []string{"../../outside.exe", "..\\outside.exe", "CON", "NUL.txt", "a:b?c", ".", " "} {
		n := safeFilename(s)
		if n == "" || n == "." || strings.ContainsAny(n, `/\:`) {
			t.Fatalf("unsafe %q", n)
		}
	}
}
func TestResearchAPIRequiresToken(t *testing.T) {
	a := testApp(t)
	r := httptest.NewRequest("POST", "http://127.0.0.1:17890/api/research/download", strings.NewReader(`{"url":"https://zenodo.org/file"}`))
	w := httptest.NewRecorder()
	a.ui(w, r)
	if w.Code != 403 || len(a.research.jobs) != 0 {
		t.Fatal("unauthenticated mutation accepted")
	}
}
func TestResearchSnapshotRedactsSignedURL(t *testing.T) {
	m, id := researchFixture(t, nil)
	m.jobs[id].URL = "https://zenodo.org/file?token=SECRET"
	m.jobs[id].FinalURL = "https://zenodo.org/redirect?token=SECRET"
	out := fmt.Sprint(m.snapshot())
	if strings.Contains(out, "SECRET") {
		t.Fatal("signed URL leaked")
	}
}
func TestResearchQueuePauseAndRestartRecovery(t *testing.T) {
	m, id := researchFixture(t, nil)
	m.slots <- struct{}{}
	m.slots <- struct{}{}
	if e := m.resume(id); e != nil {
		t.Fatal(e)
	}
	if e := m.pause(id); e != nil {
		t.Fatal(e)
	}
	m.wg.Wait()
	if m.jobs[id].State != "已暂停" {
		t.Fatal("pause failed")
	}
	m.jobs[id].State = "下载中"
	if e := m.save(); e != nil {
		t.Fatal(e)
	}
	b, e := newApp(m.a.dir)
	if e != nil {
		t.Fatal(e)
	}
	defer b.journal.Close()
	defer b.stop()
	if b.research.jobs[id].State != "已暂停" {
		t.Fatal("restart did not recover")
	}
}
func TestResearchProbeChecksMagicAndHash(t *testing.T) {
	for _, kind := range []string{"pdf", "zip", "gzip", "bz2", "zstd", "json", "fasta"} {
		if validSample(kind, []byte("<html>error")) {
			t.Fatal("accepted HTML")
		}
	}
	if _, _, _, e := contentRange("bytes 5-4/10"); e == nil {
		t.Fatal("invalid range")
	}
	if responseValidator(http.Header{"Etag": []string{"W/\"weak\""}}) != "" {
		t.Fatal("weak etag allowed")
	}
}
func TestResearchIPv4OnlySourceFailsClosed(t *testing.T) {
	a := testApp(t)
	a.doh = &http.Client{Transport: mockRoundTripper(func(*http.Request) (*http.Response, error) {
		return response(200, []byte(`{"Answer":[{"Type":1,"Data":"1.1.1.1"}]}`), nil), nil
	})}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s, _ := sourceByID("pypi")
	h := a.research.probeSource(ctx, s)
	if h.Code != "dns_no_aaaa" || a.v6.Load() != 0 {
		t.Fatal("IPv4 source accepted")
	}
}
