package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

//go:embed research-sources.json
var researchCatalogJSON []byte

type ResearchSource struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Category string   `json:"category"`
	Hosts    []string `json:"hosts"`
	Sample   string   `json:"sample,omitempty"`
	Kind     string   `json:"kind,omitempty"`
	SHA256   string   `json:"sha256,omitempty"`
	Note     string   `json:"note"`
}

var researchSources = func() []ResearchSource {
	var s []ResearchSource
	if err := json.Unmarshal(researchCatalogJSON, &s); err != nil {
		panic(err)
	}
	return s
}()

func sourceByID(id string) (ResearchSource, bool) {
	for _, s := range researchSources {
		if s.ID == id {
			return s, true
		}
	}
	return ResearchSource{}, false
}
func researchHost(host string) bool {
	host = norm(host)
	for _, s := range researchSources {
		for _, h := range s.Hosts {
			if norm(h) == host {
				return true
			}
		}
	}
	return false
}
func sourceForURL(raw string) (ResearchSource, *url.URL, error) {
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil || u == nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") || len(raw) > 8192 {
		return ResearchSource{}, nil, errors.New("请使用已列来源的 HTTPS 文件链接（不含用户名、密码或片段）")
	}
	// Only documented public buckets and regions. Preserve signed URLs exactly;
	// anonymous object URLs may use that same bucket's official dual-stack endpoint.
	dualstack := map[string]string{"pmc-oa-opendata.s3.amazonaws.com": "pmc-oa-opendata.s3.dualstack.us-east-1.amazonaws.com", "pmc-oa-opendata.s3.us-east-1.amazonaws.com": "pmc-oa-opendata.s3.dualstack.us-east-1.amazonaws.com", "noaa-goes16.s3.amazonaws.com": "noaa-goes16.s3.dualstack.us-east-1.amazonaws.com"}
	if target, known := dualstack[norm(u.Hostname())]; known {
		signed := false
		for k := range u.Query() {
			if strings.HasPrefix(strings.ToLower(k), "x-amz-") || strings.EqualFold(k, "signature") || strings.EqualFold(k, "awsaccesskeyid") {
				signed = true
			}
		}
		if !signed {
			u.Host = target
		}
	}
	for _, s := range researchSources {
		for _, h := range s.Hosts {
			if norm(h) == norm(u.Hostname()) {
				return s, u, nil
			}
		}
	}
	return ResearchSource{}, nil, errors.New("下载域名尚未接入，请在来源检测中查看已登记域名")
}

type HostHealth struct {
	ResolveHost string       `json:"resolveHost,omitempty"`
	Host        string       `json:"host"`
	IPv6        []string     `json:"ipv6,omitempty"`
	Error       string       `json:"error,omitempty"`
	Code        string       `json:"code,omitempty"`
	DNS         []DNSAttempt `json:"dns,omitempty"`
}
type SourceHealth struct {
	ID        string       `json:"id"`
	Status    string       `json:"status"`
	Detail    string       `json:"detail"`
	Checked   string       `json:"checked"`
	Hosts     []HostHealth `json:"hosts"`
	Bytes     int          `json:"bytes"`
	HTTP      int          `json:"http"`
	SHA256    string       `json:"sha256,omitempty"`
	FinalHost string       `json:"finalHost,omitempty"`
	Code      string       `json:"code,omitempty"`
	Advice    string       `json:"advice,omitempty"`
}
type ResearchJob struct {
	ResumedFrom int64  `json:"resumedFrom"`
	HTTP        int    `json:"http"`
	ID          string `json:"id"`
	Source      string `json:"source"`
	Name        string `json:"name"`
	Host        string `json:"host"`
	State       string `json:"state"`
	Detail      string `json:"detail"`
	Path        string `json:"path"`
	Done        int64  `json:"done"`
	Total       int64  `json:"total"`
	SHA256      string `json:"sha256"`
	Expected    string `json:"expected"`
	Created     string `json:"created"`
	URL         string `json:"url,omitempty"`
	Validator   string `json:"validator,omitempty"`
	FinalURL    string `json:"finalURL,omitempty"`
}
type researchManager struct {
	a         *App
	transport http.RoundTripper
	mu        sync.Mutex
	persistMu sync.Mutex
	health    map[string]SourceHealth
	jobs      map[string]*ResearchJob
	cancels   map[string]context.CancelFunc
	checking  bool
	closed    bool
	wg        sync.WaitGroup
	slots     chan struct{}
	root      string
}

func (a *App) initResearch() error {
	root := filepath.Join(os.Getenv("USERPROFILE"), "Downloads", "IPv6科研下载")
	if os.Getenv("USERPROFILE") == "" {
		root = filepath.Join(a.dir, "downloads")
	}
	m := &researchManager{a: a, transport: a.transport, health: map[string]SourceHealth{}, jobs: map[string]*ResearchJob{}, cancels: map[string]context.CancelFunc{}, slots: make(chan struct{}, 2), root: root}
	a.research = m
	if b, e := os.ReadFile(filepath.Join(a.dir, "research-status.json")); e == nil {
		var saved map[string]SourceHealth
		if json.Unmarshal(b, &saved) == nil {
			for id, h := range saved {
				if _, ok := sourceByID(id); !ok || h.ID != id {
					continue
				}
				if h.Status == "检测中" {
					h.Status = "上次检测中断"
					h.Detail = "上次检测未完成，可重新检测此来源。"
					h.Code = "check_interrupted"
				}
				if h.Status == "样本通过" {
					_, checkedErr := time.Parse(time.RFC3339Nano, h.Checked)
					if checkedErr != nil || h.Code != "sample_passed" || h.Bytes <= 0 || h.Bytes > 2*1024*1024 || (h.HTTP != 200 && h.HTTP != 206) || !hashPattern.MatchString(h.SHA256) || !researchHost(h.FinalHost) {
						continue
					}
				}
				m.health[id] = h
			}
		}
	}
	var jobs []*ResearchJob
	if b, e := os.ReadFile(filepath.Join(a.dir, "research-jobs.json")); e == nil {
		if e = json.Unmarshal(b, &jobs); e != nil {
			return fmt.Errorf("任务记录损坏，已保留原文件：%w", e)
		}
	}
	for _, j := range jobs {
		if !validJobID(j.ID) {
			return errors.New("任务记录包含无效 ID")
		}
		if j.State == "下载中" || j.State == "排队中" || j.State == "校验中" {
			j.State = "已暂停"
			j.Detail = "上次运行中断，可继续下载"
		}
		m.jobs[j.ID] = j
	}
	return nil
}
func validJobID(id string) bool { return regexp.MustCompile(`^[a-f0-9]{24}$`).MatchString(id) }
func writeJSONAtomic(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	tmp := path + ".tmp"
	if e = os.WriteFile(tmp, b, 0600); e != nil {
		return e
	}
	return os.Rename(tmp, path)
}
func (m *researchManager) save() error {
	m.persistMu.Lock()
	defer m.persistMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	list := []*ResearchJob{}
	for _, j := range m.jobs {
		list = append(list, j)
	}
	if e := writeJSONAtomic(filepath.Join(m.a.dir, "research-jobs.json"), list); e != nil {
		return e
	}
	return writeJSONAtomic(filepath.Join(m.a.dir, "research-status.json"), m.health)
}
func (m *researchManager) snapshot() any {
	m.mu.Lock()
	defer m.mu.Unlock()
	jobs := []ResearchJob{}
	for _, j := range m.jobs {
		v := *j
		v.URL = ""
		v.FinalURL = ""
		v.Validator = ""
		jobs = append(jobs, v)
	}
	health := []SourceHealth{}
	for _, s := range researchSources {
		h, ok := m.health[s.ID]
		if !ok {
			h = SourceHealth{ID: s.ID, Status: "未检测", Detail: s.Note}
		}
		health = append(health, h)
	}
	return map[string]any{"sources": researchSources, "health": health, "jobs": jobs, "checking": m.checking, "downloadRoot": m.root, "proxy": "http://127.0.0.1:17891"}
}
func (m *researchManager) shutdown() {
	m.mu.Lock()
	m.closed = true
	for _, cancel := range m.cancels {
		cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
	m.save()
}

// Redirects are evaluated before every hop. Signed query strings are never logged.
func researchRequest(ctx context.Context, tr http.RoundTripper, raw string, offset int64, validator string, rangeEnd int64) (*http.Response, string, error) {
	current := raw
	for hop := 0; hop < 8; hop++ {
		_, u, e := sourceForURL(current)
		if e != nil {
			return nil, "", e
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		req.Header.Set("User-Agent", "IPv6ResearchHelper/0.4.0")
		req.Header.Set("Accept-Encoding", "identity")
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
			req.Header.Set("If-Range", validator)
		} else if rangeEnd >= 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", rangeEnd))
		}
		res, e := tr.RoundTrip(req)
		if e != nil && ctx.Err() == nil {
			res, e = tr.RoundTrip(req.Clone(ctx))
		}
		if e != nil {
			return nil, "", &networkFailure{Host: u.Hostname(), Cause: e}
		}
		if res.StatusCode >= 300 && res.StatusCode < 400 {
			next, e := res.Location()
			res.Body.Close()
			if e != nil {
				return nil, "", errors.New("下载跳转地址无效")
			}
			current = next.String()
			continue
		}
		return res, u.String(), nil
	}
	return nil, "", errors.New("下载跳转超过 8 次")
}
func safeNetworkError(e error) string {
	_, message, _ := diagnosis(failureCode(e))
	return message
}
func responseError(res *http.Response) error {
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return fmt.Errorf("HTTP %d：需要有效授权或网站访问验证", res.StatusCode)
	}
	if res.StatusCode != 200 && res.StatusCode != 206 {
		return fmt.Errorf("HTTP %d：未收到文件", res.StatusCode)
	}
	if strings.Contains(strings.ToLower(res.Header.Get("Content-Type")), "text/html") {
		return errors.New("返回网页或登录页，未当作下载文件保存")
	}
	return nil
}
func contentLooksHTML(b []byte) bool {
	b = bytes.ToLower(bytes.TrimSpace(b))
	return bytes.HasPrefix(b, []byte("<!doctype html")) || bytes.HasPrefix(b, []byte("<html"))
}
func validSample(kind string, b []byte) bool {
	if len(b) == 0 || contentLooksHTML(b) {
		return false
	}
	switch kind {
	case "file":
		return bytes.HasPrefix(b, []byte("%PDF-")) || bytes.HasPrefix(b, []byte("PK\x03\x04")) || bytes.HasPrefix(b, []byte{31, 139}) || bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) || bytes.HasPrefix(b, []byte{255, 216, 255})
	case "pdf":
		return bytes.HasPrefix(b, []byte("%PDF-"))
	case "zip":
		return bytes.HasPrefix(b, []byte("PK\x03\x04"))
	case "gzip":
		return bytes.HasPrefix(b, []byte{31, 139})
	case "bz2":
		return bytes.HasPrefix(b, []byte("BZh"))
	case "zstd":
		return bytes.HasPrefix(b, []byte{0x28, 0xb5, 0x2f, 0xfd})
	case "json":
		return json.Valid(b)
	case "fasta":
		return bytes.HasPrefix(b, []byte(">"))
	default:
		return true
	}
}
func (m *researchManager) probeSource(ctx context.Context, s ResearchSource) SourceHealth {
	h := SourceHealth{ID: s.ID, Checked: time.Now().Format(time.RFC3339)}
	usable := false
	for _, host := range s.Hosts {
		c, cancel := context.WithTimeout(ctx, 14*time.Second)
		resolveHost := upstreamResolveHost(norm(host))
		ips, e := m.a.resolve(c, resolveHost)
		cancel()
		row := HostHealth{Host: host, ResolveHost: resolveHost}
		if e != nil {
			row.Error = safeNetworkError(e)
			row.Code = failureCode(e)
			var dns *DNSFailure
			if errors.As(e, &dns) {
				row.DNS = dns.Attempts
			}
		} else {
			usable = true
			row.Code = "dns_ipv6"
			if resolveHost != norm(host) {
				row.Code = "dns_cdn_ipv6"
			}
			m.a.mu.Lock()
			row.DNS = m.a.cache[resolveHost].attempts
			m.a.mu.Unlock()
			for _, ip := range ips {
				row.IPv6 = append(row.IPv6, ip.String())
			}
		}
		h.Hosts = append(h.Hosts, row)
		if ctx.Err() != nil {
			break
		}
	}
	if !usable {
		code := "dns_nxdomain"
		for _, row := range h.Hosts {
			if row.Code == "dns_no_aaaa" {
				code = "dns_no_aaaa"
			}
		}
		for _, row := range h.Hosts {
			if row.Code != "dns_no_aaaa" && row.Code != "dns_nxdomain" {
				code = row.Code
				break
			}
		}
		setDiagnosis(&h, code, "")
		return h
	}
	setDiagnosis(&h, "sample_missing", "部分或全部登记域名已解析到 IPv6；尚未验证实际文件。"+s.Note)
	if s.Sample == "" {
		return h
	}
	c, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	end := int64(65535)
	if s.SHA256 != "" {
		end = -1
	}
	res, final, e := researchRequest(c, m.transport, s.Sample, 0, "", end)
	if e != nil {
		setDiagnosis(&h, failureCode(e), e.Error())
		return h
	}
	defer res.Body.Close()
	h.HTTP = res.StatusCode
	if u, err := url.Parse(final); err == nil {
		h.FinalHost = u.Hostname()
	}
	if e = responseError(res); e != nil {
		code := "http_error"
		switch res.StatusCode {
		case 401, 403:
			code = "access_required"
		case 404, 410:
			code = "sample_expired"
		case 429:
			code = "rate_limited"
		}
		setDiagnosis(&h, code, e.Error())
		return h
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, 2*1024*1024+1))
	if e != nil || len(raw) > 2*1024*1024 {
		setDiagnosis(&h, "sample_read", "")
		return h
	}
	if !validSample(s.Kind, raw) {
		setDiagnosis(&h, "sample_format", "")
		return h
	}
	sum := sha256.Sum256(raw)
	h.SHA256 = hex.EncodeToString(sum[:])
	h.Bytes = len(raw)
	u, _ := url.Parse(final)
	h.FinalHost = u.Hostname()
	if s.SHA256 != "" && s.SHA256 != h.SHA256 {
		setDiagnosis(&h, "sample_hash", "")
		return h
	}
	h.Status = "样本通过"
	h.Code = "sample_passed"
	h.Advice = "本次仅验证上述样本与实际文件域名；其他附件、登录和重定向需分别验证。"
	h.Detail = fmt.Sprintf("HTTP %d，%d 字节，IPv6 / TLS / 文件格式通过。", h.HTTP, h.Bytes)
	if s.SHA256 != "" {
		h.Detail += "官方 SHA256 一致。"
	}
	h.Detail += s.Note
	return h
}
func (m *researchManager) startCheck(id string) error {
	m.mu.Lock()
	if m.checking || m.closed {
		m.mu.Unlock()
		return errors.New("来源检测正在进行或服务正在退出")
	}
	if id != "" {
		if _, ok := sourceByID(id); !ok {
			m.mu.Unlock()
			return errors.New("未知来源")
		}
	}
	m.checking = true
	m.wg.Add(1)
	ctx, cancel := context.WithCancel(context.Background())
	m.cancels["check"] = cancel
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer cancel()
		sem := make(chan struct{}, 4)
		var wg sync.WaitGroup
		for _, s := range researchSources {
			if id != "" && s.ID != id {
				continue
			}
			if ctx.Err() != nil {
				break
			}
			sem <- struct{}{}
			wg.Add(1)
			go func(s ResearchSource) {
				defer wg.Done()
				defer func() { <-sem }()
				m.a.mu.Lock()
				for _, host := range s.Hosts {
					delete(m.a.cache, upstreamResolveHost(norm(host)))
				}
				m.a.mu.Unlock()
				m.mu.Lock()
				m.health[s.ID] = SourceHealth{ID: s.ID, Status: "检测中", Detail: "解析 IPv6 与检测下载样本"}
				m.mu.Unlock()
				h := m.probeSource(ctx, s)
				m.mu.Lock()
				m.health[s.ID] = h
				m.mu.Unlock()
				m.save()
			}(s)
		}
		wg.Wait()
		m.mu.Lock()
		m.checking = false
		delete(m.cancels, "check")
		m.mu.Unlock()
		m.save()
	}()
	return nil
}

var hashPattern = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

func safeFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	if name == "" || name == "." || len(name) > 150 {
		return "download.bin"
	}
	first := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if regexp.MustCompile(`^(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])$`).MatchString(first) {
		name = "file-" + name
	}
	return name
}
func (m *researchManager) newJob(raw, expected string) (*ResearchJob, error) {
	s, u, e := sourceForURL(raw)
	if e != nil {
		return nil, e
	}
	expected = strings.ToLower(strings.TrimSpace(expected))
	if expected != "" && !hashPattern.MatchString(expected) {
		return nil, errors.New("SHA256 应为 64 位十六进制")
	}
	id := make([]byte, 12)
	if _, e = rand.Read(id); e != nil {
		return nil, e
	}
	name := safeFilename(u.Path)
	j := &ResearchJob{ID: hex.EncodeToString(id), Source: s.Name, Name: name, Host: u.Hostname(), URL: u.String(), Expected: expected, State: "已暂停", Created: time.Now().Format(time.RFC3339), Total: -1}
	j.Path = filepath.Join(m.root, j.ID, name)
	m.mu.Lock()
	if len(m.jobs) >= 500 || m.closed {
		m.mu.Unlock()
		return nil, errors.New("任务已达 500 条或正在退出")
	}
	m.jobs[j.ID] = j
	m.mu.Unlock()
	if e = m.save(); e != nil {
		return nil, e
	}
	if e = m.resume(j.ID); e != nil {
		return nil, e
	}
	m.mu.Lock()
	copy := *j
	m.mu.Unlock()
	copy.URL = ""
	copy.FinalURL = ""
	copy.Validator = ""
	return &copy, nil
}
func (m *researchManager) pause(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cancel, ok := m.cancels[id]
	if !ok {
		return errors.New("任务当前未在运行")
	}
	cancel()
	return nil
}
func (m *researchManager) resume(id string) error {
	m.mu.Lock()
	j, ok := m.jobs[id]
	if !ok || m.closed {
		m.mu.Unlock()
		return errors.New("任务不存在或服务正在退出")
	}
	if _, ok = m.cancels[id]; ok || j.State == "已完成" {
		m.mu.Unlock()
		return errors.New("任务正在运行或已经完成")
	}
	if _, _, e := sourceForURL(j.URL); e != nil {
		m.mu.Unlock()
		return e
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancels[id] = cancel
	j.State = "排队中"
	j.Detail = "等待下载槽位"
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer cancel()
		var e error
		select {
		case m.slots <- struct{}{}:
			e = m.download(ctx, id)
			<-m.slots
		case <-ctx.Done():
			e = ctx.Err()
		}
		m.mu.Lock()
		delete(m.cancels, id)
		if e != nil {
			if ctx.Err() != nil {
				j.State = "已暂停"
				j.Detail = "已保留临时文件，可继续"
			} else {
				j.State = "失败"
				j.Detail = e.Error()
			}
		}
		m.mu.Unlock()
		if e = m.save(); e != nil {
			m.mu.Lock()
			j.Detail += "；任务状态保存失败"
			m.mu.Unlock()
		}
	}()
	return nil
}
func contentRange(s string) (int64, int64, int64, error) {
	var start, end, total int64
	n, e := fmt.Sscanf(s, "bytes %d-%d/%d", &start, &end, &total)
	if e != nil || n != 3 || start < 0 || end < start || total <= end {
		return 0, 0, 0, errors.New("Content-Range 不合法")
	}
	return start, end, total, nil
}
func responseValidator(h http.Header) string {
	v := h.Get("ETag")
	if v != "" && !strings.HasPrefix(v, "W/") {
		return v
	}
	v = h.Get("Last-Modified")
	if _, e := http.ParseTime(v); e == nil {
		return v
	}
	return ""
}
func (m *researchManager) download(ctx context.Context, id string) error {
	m.mu.Lock()
	j := m.jobs[id]
	job := *j
	j.State = "下载中"
	j.Detail = "正在通过 IPv6 连接"
	m.mu.Unlock()
	// Paths are derived from a random internal ID, never from response directory components.
	dir := filepath.Join(m.root, id)
	if e := os.MkdirAll(dir, 0700); e != nil {
		return errors.New("无法创建下载目录")
	}
	part := filepath.Join(dir, "download.part")
	var offset int64
	if info, e := os.Stat(part); e == nil {
		offset = info.Size()
	}
	if job.Validator == "" {
		offset = 0
	}
	res, final, e := researchRequest(ctx, m.transport, job.URL, offset, job.Validator, -1)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if e = responseError(res); e != nil {
		return e
	}
	total := res.ContentLength
	appendFile := false
	if res.StatusCode == 206 {
		start, end, n, e := contentRange(res.Header.Get("Content-Range"))
		if e != nil {
			return e
		}
		if start != offset {
			return errors.New("服务器返回错误续传偏移，未写入文件")
		}
		if res.ContentLength >= 0 && res.ContentLength != end-start+1 {
			return errors.New("分片长度不匹配")
		}
		if offset > 0 && responseValidator(res.Header) != job.Validator {
			return errors.New("远端文件标识变化，未拼接旧数据；请新建任务")
		}
		total = n
		appendFile = offset > 0
	} else {
		offset = 0
	}
	name := job.Name
	if _, params, e := mime.ParseMediaType(res.Header.Get("Content-Disposition")); e == nil && params["filename"] != "" {
		name = safeFilename(params["filename"])
	}
	path := filepath.Join(dir, name)
	if name == "download.part" {
		name = "file-download.part"
		path = filepath.Join(dir, name)
	}
	if _, e = os.Stat(path); e == nil {
		return errors.New("目标文件已存在，拒绝覆盖；请检查任务目录")
	}
	flags := os.O_WRONLY | os.O_CREATE
	if appendFile {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, e := os.OpenFile(part, flags, 0600)
	if e != nil {
		return errors.New("无法打开临时文件")
	}
	m.mu.Lock()
	j.Done = offset
	j.ResumedFrom = offset
	j.HTTP = res.StatusCode
	j.Total = total
	j.Validator = responseValidator(res.Header)
	j.FinalURL = final
	j.Name = name
	j.Path = path
	j.Detail = "IPv6 下载中"
	m.mu.Unlock()
	m.save()
	buf := make([]byte, 128*1024)
	first := true
	for {
		n, readErr := res.Body.Read(buf)
		if n > 0 {
			if first && offset == 0 && contentLooksHTML(buf[:n]) {
				f.Close()
				return errors.New("内容是网页，已停止保存")
			}
			first = false
			written, writeErr := f.Write(buf[:n])
			m.mu.Lock()
			j.Done += int64(written)
			m.mu.Unlock()
			if writeErr != nil {
				f.Close()
				return errors.New("写入失败，请检查磁盘空间")
			}
			if written != n {
				f.Close()
				return io.ErrShortWrite
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				f.Close()
				return errors.New("传输中断，临时文件已保留")
			}
			break
		}
		if ctx.Err() != nil {
			f.Close()
			return ctx.Err()
		}
	}
	if e = f.Close(); e != nil {
		return errors.New("关闭临时文件失败")
	}
	m.mu.Lock()
	done := j.Done
	m.mu.Unlock()
	if total >= 0 && done != total {
		return fmt.Errorf("长度不一致：收到 %d，预期 %d；可重试续传", done, total)
	}
	if done == 0 {
		return errors.New("收到空文件")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	m.mu.Lock()
	j.State = "校验中"
	m.mu.Unlock()
	reader, e := os.Open(part)
	if e != nil {
		return e
	}
	hash := sha256.New()
	_, e = io.Copy(hash, &contextFileReader{ctx: ctx, r: reader})
	reader.Close()
	if e != nil {
		return errors.New("无法读取文件进行哈希校验")
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	m.mu.Lock()
	j.SHA256 = sum
	m.mu.Unlock()
	if job.Expected != "" && sum != job.Expected {
		return errors.New("SHA256 与预期不符；未发布为完成文件")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if e = os.Rename(part, path); e != nil {
		return errors.New("保存完成文件失败，临时文件已保留")
	}
	m.mu.Lock()
	j.State = "已完成"
	j.Detail = "IPv6 下载完成；SHA256 已计算"
	if job.Expected != "" {
		j.Detail = "IPv6 下载完成；预期 SHA256 校验一致"
	}
	m.mu.Unlock()
	return nil
}

type contextFileReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextFileReader) Read(b []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(b)
}
func (a *App) researchAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var input struct {
		ID     string `json:"id"`
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
	}
	if r.Method == "POST" {
		r.Body = http.MaxBytesReader(w, r.Body, 16384)
		if e := json.NewDecoder(r.Body).Decode(&input); e != nil {
			http.Error(w, "Invalid JSON", 400)
			return
		}
	}
	var e error
	var result any = map[string]bool{"ok": true}
	switch r.URL.Path {
	case "/api/research":
		result = a.research.snapshot()
	case "/api/research/check":
		e = a.research.startCheck(input.ID)
	case "/api/research/download":
		result, e = a.research.newJob(input.URL, input.SHA256)
	case "/api/research/pause":
		e = a.research.pause(input.ID)
	case "/api/research/resume":
		e = a.research.resume(input.ID)
	default:
		http.NotFound(w, r)
		return
	}
	if e != nil {
		http.Error(w, e.Error(), 409)
		return
	}
	json.NewEncoder(w).Encode(result)
}

// Kept separate from the desktop server so an audit can run without disrupting existing routing.
func researchAudit(a *App) error {
	if e := a.research.startCheck(""); e != nil {
		return e
	}
	for {
		time.Sleep(time.Second)
		a.research.mu.Lock()
		running := a.research.checking
		a.research.mu.Unlock()
		if !running {
			break
		}
	}
	return writeJSONAtomic(filepath.Join(a.dir, "research-audit.json"), a.research.snapshot())
}
