package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

var steamCachePattern = regexp.MustCompile(`^cache[0-9]{1,3}-[a-z]{3}[0-9]{1,2}\.steamcontent\.com$`)
var steamHostPattern = regexp.MustCompile(`(?i)\b(cache[0-9]{1,3}-[a-z]{3}[0-9]{1,2}\.steamcontent\.com)\b`)
var steamDiscoveryCache struct {
	sync.Mutex
	checked time.Time
	hosts   []string
	found   bool
}

func steamHost(host string) bool { return steamCachePattern.MatchString(norm(host)) }
func steamLogHosts(b []byte) []string {
	seen := map[string]bool{}
	matches := steamHostPattern.FindAllSubmatchIndex(b, -1)
	var hosts []string
	for i := len(matches) - 1; i >= 0 && len(hosts) < 48; i-- {
		start, end := matches[i][2], matches[i][3]
		isDNS := func(c byte) bool {
			return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
		}
		if start > 0 && isDNS(b[start-1]) || end < len(b) && isDNS(b[end]) {
			continue
		}
		h := norm(string(b[start:end]))
		if !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	sort.Strings(hosts)
	return hosts
}
func steamLogPath() string {
	if root := os.Getenv("IPV6_HELPER_STEAM_PATH"); root != "" {
		return filepath.Join(root, "logs", "content_log.txt")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "reg.exe", "query", `HKCU\Software\Valve\Steam`, "/v", "SteamPath")
	hideCommand(cmd)
	b, err := cmd.Output()
	if err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if i := strings.Index(line, "REG_SZ"); i >= 0 {
				root := strings.TrimSpace(line[i+len("REG_SZ"):])
				if root != "" {
					return filepath.Join(root, "logs", "content_log.txt")
				}
			}
		}
	}
	return filepath.Join(os.Getenv("ProgramFiles(x86)"), "Steam", "logs", "content_log.txt")
}
func discoverSteamHosts(refresh bool) ([]string, bool) {
	steamDiscoveryCache.Lock()
	defer steamDiscoveryCache.Unlock()
	if !refresh && time.Since(steamDiscoveryCache.checked) < 15*time.Second {
		return append([]string{}, steamDiscoveryCache.hosts...), steamDiscoveryCache.found
	}
	var data []byte
	f, err := os.Open(steamLogPath())
	if err == nil {
		defer f.Close()
		if s, e := f.Stat(); e == nil {
			if s.Size() > 1<<20 {
				f.Seek(s.Size()-(1<<20), io.SeekStart)
			}
			data, _ = io.ReadAll(io.LimitReader(f, 1<<20))
		}
	}
	hosts := steamLogHosts(data)
	steamDiscoveryCache.hosts, steamDiscoveryCache.found, steamDiscoveryCache.checked = hosts, err == nil, time.Now()
	return append([]string{}, hosts...), err == nil
}
func steamHosts() []string {
	seen := map[string]bool{}
	hosts, _ := discoverSteamHosts(false)
	if len(hosts) == 0 {
		hosts = []string{"cache7-hkg1.steamcontent.com", "cache8-hkg1.steamcontent.com"}
	}
	for _, h := range hosts {
		seen[h] = true
	}
	var out []string
	for h := range seen {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

const steamMarkerStart = "# BEGIN IPv6Helper-Steam"
const steamMarkerEnd = "# END IPv6Helper-Steam"

type steamRouteState struct {
	Hosts        []string `json:"hosts"`
	Block        []byte   `json:"block"`
	Separator    []byte   `json:"separator"`
	Enabled      string   `json:"enabled"`
	BeforeSHA256 string   `json:"beforeSHA256"`
}

func steamBlock(hosts []string) []byte {
	b := []byte(steamMarkerStart + "\r\n")
	for _, h := range hosts {
		b = append(b, []byte(bindIP+" "+h+"\r\n::1 "+h+"\r\n")...)
	}
	return append(b, []byte(steamMarkerEnd+"\r\n")...)
}
func bytesSHA256(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

const steamHostsReplaceRetry = 3 * time.Second

func steamHostsReplaceBusy(err error) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	var code syscall.Errno
	if !errors.As(err, &code) {
		return false
	}
	// Windows can report ACCESS_DENIED for a transient replacement lock as well
	// as a persistent permission error. Both get only a bounded retry; no write
	// directly into the existing hosts file is allowed.
	return code == 5 || code == 32 || code == 33 // ACCESS_DENIED / SHARING_VIOLATION / LOCK_VIOLATION
}

// Stage beside the hosts file and atomically replace only after a second read
// confirms the original snapshot. Failed staging/replacement retains recovery data.
func writeSteamHostsChecked(path string, before, after []byte) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".ipv6helper-steam-*.tmp")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(after); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	deadline := time.Now().Add(steamHostsReplaceRetry)
	for {
		current, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		if !bytes.Equal(current, before) {
			return errors.New("hosts 在操作期间被外部修改，已保留现有文件和备份")
		}
		e = os.Rename(name, path)
		if e == nil || !steamHostsReplaceBusy(e) {
			return e
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("hosts 替换失败：文件可能被系统或安全软件暂时占用，或权限不足：%w", e)
		}
		time.Sleep(min(100*time.Millisecond, remaining))
	}
}

func (a *App) steamState() (steamRouteState, error) {
	var s steamRouteState
	b, e := os.ReadFile(filepath.Join(a.dir, "steam-route.json"))
	if e != nil {
		return s, e
	}
	e = json.Unmarshal(b, &s)
	if e == nil {
		if len(s.Hosts) == 0 || len(s.Hosts) > 80 || !bytes.HasPrefix(s.Block, []byte(steamMarkerStart+"\r\n")) || !bytes.HasSuffix(s.Block, []byte(steamMarkerEnd+"\r\n")) {
			return s, errors.New("Steam 恢复记录无效，已保留备份")
		}
		for _, h := range s.Hosts {
			if !steamHost(h) {
				return s, errors.New("Steam 恢复记录包含非下载域名")
			}
		}
		hash, hashErr := hex.DecodeString(s.BeforeSHA256)
		if !bytes.Equal(s.Block, steamBlock(s.Hosts)) || (len(s.Separator) != 0 && !bytes.Equal(s.Separator, []byte("\r\n"))) || hashErr != nil || len(hash) != sha256.Size {
			return s, errors.New("Steam 恢复记录内容或边界无效，未修改 hosts")
		}
	}
	return s, e
}
func (a *App) steamActive() bool {
	s, e := a.steamState()
	if e != nil {
		return false
	}
	b, e := os.ReadFile(a.hostsPath)
	return e == nil && bytes.Count(b, s.Block) == 1
}
func steamHostsConflict(b []byte, hosts []string) error {
	if bytes.Contains(b, []byte(steamMarkerStart)) || bytes.Contains(b, []byte(steamMarkerEnd)) {
		return errors.New("发现已有 Steam 接管标记，请先恢复，未覆盖现有记录")
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(strings.SplitN(line, "#", 2)[0])
		if len(f) < 2 {
			continue
		}
		for _, h := range f[1:] {
			if containsHost(hosts, h) {
				return fmt.Errorf("%s 已有 hosts 规则，请先在原工具中停止该服务；未覆盖", h)
			}
		}
	}
	return nil
}
func (a *App) steamPreflight(hosts []string) error {
	if len(hosts) == 0 || len(hosts) > 80 {
		return errors.New("Steam 下载服务器列表无效")
	}
	for _, h := range hosts {
		if !steamHost(h) {
			return errors.New("非 Steam 下载服务器已拒绝")
		}
	}
	b, e := os.ReadFile(a.hostsPath)
	if e != nil {
		return e
	}
	if !a.steamActive() {
		if e = steamHostsConflict(b, hosts); e != nil {
			return e
		}
	}
	f, e := os.OpenFile(a.hostsPath, os.O_WRONLY, 0)
	if e != nil {
		return errors.New("Steam 原生接管需要管理员权限：请停止并退出助手，右键 EXE 以管理员身份运行，再选择 Steam。当前配置未改变")
	}
	return f.Close()
}
func (a *App) steamApplyLocked(hosts []string) error {
	b, e := os.ReadFile(a.hostsPath)
	if e != nil {
		return e
	}
	if e = steamHostsConflict(b, hosts); e != nil {
		return e
	}
	s := steamRouteState{Hosts: append([]string{}, hosts...), Enabled: time.Now().Format(time.RFC3339), BeforeSHA256: bytesSHA256(b)}
	if len(b) > 0 && b[len(b)-1] != '\n' {
		s.Separator = []byte("\r\n")
	}
	s.Block = steamBlock(hosts)
	if e = os.WriteFile(filepath.Join(a.dir, "steam-hosts-before-"+time.Now().Format("20060102-150405.000")+".bak"), b, 0600); e != nil {
		return e
	}
	if e = writeJSONAtomic(filepath.Join(a.dir, "steam-route.json"), s); e != nil {
		return e
	}
	updated := append(append(append([]byte{}, b...), s.Separator...), s.Block...)
	if e = writeSteamHostsChecked(a.hostsPath, b, updated); e != nil {
		return fmt.Errorf("Steam hosts 写入失败；已保留恢复记录及备份：%w", e)
	}
	go exec.Command("ipconfig", "/flushdns").Run()
	return nil
}
func (a *App) restoreSteamLocked() error {
	s, e := a.steamState()
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	b, e := os.ReadFile(a.hostsPath)
	if e != nil {
		return e
	}
	if bytes.Count(b, s.Block) != 1 {
		if bytesSHA256(b) == s.BeforeSHA256 {
			return os.Remove(filepath.Join(a.dir, "steam-route.json"))
		}
		return errors.New("Steam hosts 块已被修改或删除，未覆盖用户改动；请核对保留的备份")
	}
	needle := append(append([]byte{}, s.Separator...), s.Block...)
	i := bytes.Index(b, needle)
	if i < 0 {
		return errors.New("Steam hosts 边界已变化，保留备份待核对")
	}
	a.steamRestoring.Store(true)
	a.steamRouteEpoch.Add(1)
	defer a.steamRestoring.Store(false)
	// Pair this collection with connect's final check under a.mu. A dial begun
	// before restoration is also rejected by the changed route epoch.
	managedHosts := make(map[string]bool, len(s.Hosts))
	for _, host := range s.Hosts {
		managedHosts[norm(host)] = true
	}
	a.mu.Lock()
	var active []net.Conn
	for _, entry := range a.entries {
		if entry.Active && entry.Kind != "dns6" && managedHosts[norm(entry.Host)] {
			if c := a.active[entry.ID]; c != nil {
				active = append(active, c)
			}
		}
	}
	a.mu.Unlock()
	for _, c := range active {
		c.Close()
	}
	clean := append(append([]byte{}, b[:i]...), b[i+len(needle):]...)
	if e = writeSteamHostsChecked(a.hostsPath, b, clean); e != nil {
		return fmt.Errorf("恢复 Steam hosts 未完成；已保留恢复记录及备份：%w", e)
	}
	if e = os.Remove(filepath.Join(a.dir, "steam-route.json")); e != nil {
		return e
	}
	go exec.Command("ipconfig", "/flushdns").Run()
	return nil
}
func (a *App) restoreSteam() error {
	a.networkMu.Lock()
	defer a.networkMu.Unlock()
	return a.restoreSteamLocked()
}
func (a *App) ensureSteamListenersLocked() error {
	a.listenerMu.Lock()
	defer a.listenerMu.Unlock()
	if a.stopping {
		return errors.New("助手正在停止，未重新开启 Steam 监听")
	}
	if a.steamListenersStarted {
		return nil
	}
	l80, e := net.Listen("tcp6", "[::1]:80")
	if e != nil {
		return fmt.Errorf("Steam IPv6 本地80端口被占用：%w", e)
	}
	l443, e := net.Listen("tcp6", "[::1]:443")
	if e != nil {
		l80.Close()
		return fmt.Errorf("Steam IPv6 本地443端口被占用：%w", e)
	}
	a.listeners = append(a.listeners, l80, l443)
	a.steamListenersStarted = true
	go (&http.Server{Handler: http.HandlerFunc(a.proxy), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 90 * time.Second}).Serve(l80)
	go func() {
		for {
			c, e := l443.Accept()
			if e != nil {
				return
			}
			go a.tlsTunnel(c)
		}
	}()
	return nil
}
func (a *App) steamAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" && r.URL.Path == "/api/steam/verify" {
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		json.NewEncoder(w).Encode(a.verifySteam(ctx))
		return
	}
	if r.URL.Path != "/api/steam" && r.URL.Path != "/api/steam/refresh" {
		http.Error(w, "Unknown Steam action", 404)
		return
	}
	hosts, found := discoverSteamHosts(r.Method == "POST" && r.URL.Path == "/api/steam/refresh")
	json.NewEncoder(w).Encode(map[string]any{"hosts": steamHosts(), "detectedHosts": hosts, "clientFound": found, "active": a.steamActive(), "detail": "读取 Steam 本机日志中的官方缓存域名；只保存域名，不导入账户、令牌或游戏路径。新服务器请刷新后重新启动接管。Steam 原生模式使用 hosts，不依赖 Clash，需要管理员权限。"})
}
