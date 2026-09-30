package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDNSNegativeSurvivesAlternateTimeout(t *testing.T) {
	a := testApp(t)
	a.doh = &http.Client{Transport: mockRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "dns.alidns.com" {
			return nil, context.DeadlineExceeded
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"Status":0,"Answer":[]}`))}, nil
	})}
	_, err := a.resolve(context.Background(), "arxiv.org")
	var d *DNSFailure
	if !errors.As(err, &d) || d.Code != "dns_no_aaaa" || len(d.Attempts) != 4 {
		t.Fatalf("lost DNS evidence: %#v", err)
	}
}
func TestDNSUncertainIsNeverReportedAsNoIPv6(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		body string
		want string
	}{
		{"timeout", context.DeadlineExceeded, "", "dns_timeout"},
		{"connection", io.EOF, "", "dns_error"},
		{"nxdomain", nil, `{"Status":3}`, "dns_nxdomain"},
		{"serverfail", nil, `{"Status":2}`, "dns_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp(t)
			a.doh = &http.Client{Transport: mockRoundTripper(func(r *http.Request) (*http.Response, error) {
				if tc.err != nil {
					return nil, tc.err
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			h := a.research.probeSource(context.Background(), ResearchSource{ID: "test", Hosts: []string{"arxiv.org"}})
			if h.Code != tc.want || h.Advice == "" || len(h.Hosts[0].DNS) != 4 {
				t.Fatalf("bad diagnosis: %+v", h)
			}
		})
	}
}
func TestNetworkFailureRetainsTypedCause(t *testing.T) {
	tr := mockRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, &DNSFailure{Code: "dns_no_aaaa", Cause: context.DeadlineExceeded}
	})
	_, _, err := researchRequest(context.Background(), tr, "https://arxiv.org/pdf/test", 0, "", 65535)
	if failureCode(err) != "dns_no_aaaa" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestNOAADualstackPreservesObjectAndSignature(t *testing.T) {
	_, u, err := sourceForURL("https://noaa-goes16.s3.amazonaws.com/folder/data.nc?versionId=abc")
	if err != nil || u.Host != "noaa-goes16.s3.dualstack.us-east-1.amazonaws.com" || u.Path != "/folder/data.nc" || u.RawQuery != "versionId=abc" {
		t.Fatalf("incorrect object mapping: %v %v", u, err)
	}
	signed := "https://noaa-goes16.s3.amazonaws.com/folder/data.nc?X-Amz-Signature=abc&X-Amz-Credential=test"
	_, u, err = sourceForURL(signed)
	if err != nil || u.String() != signed {
		t.Fatal("signed URL changed")
	}
}

func TestNodePolicyAdvice(t *testing.T) {
	for _, message := range []string{"固定 IPv6 已被拉黑或不在当前 DNS 候选中", "没有可用的 IPv6：候选为空或已全部拉黑"} {
		if failureCode(errors.New(message)) != "node_policy" {
			t.Fatal("policy misreported as a network failure")
		}
	}
}

func TestResearchCDNMappingIsExactAndVisible(t *testing.T) {
 if upstreamResolveHost("arxiv.org")!="dualstack.s.sni.global.fastly.net" || upstreamResolveHost("arxiv.org.evil.example")!="arxiv.org.evil.example" {t.Fatal("mapping scope changed")}
 a:=testApp(t)
 a.doh=&http.Client{Transport:mockRoundTripper(func(r *http.Request)(*http.Response,error){
  if r.URL.Query().Get("name")!="dualstack.s.sni.global.fastly.net" {t.Fatal("unexpected lookup")}
  return response(200,[]byte(`{"Status":0,"Answer":[{"Type":28,"TTL":60,"Data":"2a04:4e42::810"}]}`),nil),nil
 })}
 h:=a.research.probeSource(context.Background(),ResearchSource{ID:"arxiv",Hosts:[]string{"arxiv.org"}})
 if h.Code!="sample_missing"||h.Hosts[0].Code!="dns_cdn_ipv6"||h.Hosts[0].ResolveHost==h.Hosts[0].Host {t.Fatal("CDN route hidden or DNS counted as a passed file")}
}
func TestProbeClassifiesHTTPAndInvalidContent(t *testing.T) {
	for _, tc := range []struct {
		http       int
		body, code string
	}{{403, "blocked", "access_required"}, {404, "missing", "sample_expired"}, {429, "wait", "rate_limited"}, {200, "<html>login</html>", "sample_format"}} {
		a := testApp(t)
		a.doh = &http.Client{Transport: mockRoundTripper(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"Status":0,"Answer":[{"Type":28,"TTL":60,"Data":"2600:9000::1"}]}`))}, nil
		})}
		a.research.transport = mockRoundTripper(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: tc.http, Body: io.NopCloser(strings.NewReader(tc.body)), Header: http.Header{}}, nil
		})
		h := a.research.probeSource(context.Background(), ResearchSource{ID: "test", Hosts: []string{"arxiv.org"}, Sample: "https://arxiv.org/pdf/test", Kind: "pdf"})
		if h.Code != tc.code || h.FinalHost != "arxiv.org" || h.Advice == "" {
			t.Fatalf("%+v", h)
		}
	}
}
