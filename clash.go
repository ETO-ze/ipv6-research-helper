package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

const clashName = "EpicIPv6Helper-local"

type clashClient struct {
	base   string
	secret string
	client *http.Client
}

func (c clashClient) call(method, path string, body any, out any) error {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	r, e := http.NewRequest(method, c.base+path, bytes.NewReader(b))
	if e != nil {
		return e
	}
	r.Header.Set("Content-Type", "application/json")
	if c.secret != "" {
		r.Header.Set("Authorization", "Bearer "+c.secret)
	}
	res, e := c.client.Do(r)
	if e != nil {
		return errors.New("Clash controller unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("Clash API %s returned %d", path, res.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}

type clashState struct {
	Data          string
	Profile       string
	Original      []byte
	AdditionProxy string
	AdditionRules string
	BaseConfig    []byte
	Selections    map[string]string
	Enabled       string
	SourceID      string
	Hosts         []string
}

func readYAML(p string) (map[string]any, []byte, error) {
	b, e := os.ReadFile(p)
	if e != nil {
		return nil, nil, e
	}
	var m map[string]any
	e = yaml.Unmarshal(b, &m)
	return m, b, e
}
func locateClash() (string, error) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", `$p=Get-CimInstance Win32_Process -Filter "Name='clash-win64.exe'"; if(@($p).Count -ne 1){exit 1}; if($p.CommandLine -match '-d\s+"([^\"]+)"'){$Matches[1]}elseif($p.CommandLine -match '-d\s+(\S+)'){$Matches[1]}else{exit 1}`)
	hideCommand(cmd)
	b, e := cmd.Output()
	if e != nil {
		return "", errors.New("One running Clash for Windows core is required")
	}
	return strings.TrimSpace(string(b)), nil
}
func clashAPI(data string) (clashClient, map[string]any, error) {
	cfg, _, e := readYAML(filepath.Join(data, "config.yaml"))
	if e != nil {
		return clashClient{}, nil, e
	}
	addr, _ := cfg["external-controller"].(string)
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		return clashClient{}, nil, errors.New("Controller must use 127.0.0.1")
	}
	secret, _ := cfg["secret"].(string)
	c := clashClient{"http://" + addr, secret, &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil}}}
	return c, cfg, nil
}
func (a *App) clashAction(action string) error {
	a.networkMu.Lock()
	defer a.networkMu.Unlock()
	return a.clashActionLocked(action, "epic", hostnames())
}
func (a *App) clashActionLocked(action, sourceID string, hosts []string) error {
	statePath := filepath.Join(a.dir, "clash-state.json")
	if action == "restore" {
		raw, e := os.ReadFile(statePath)
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		var s clashState
		if e = json.Unmarshal(raw, &s); e != nil {
			return e
		}
		c, _, e := clashAPI(s.Data)
		if e != nil {
			return e
		}
		current, e := os.ReadFile(s.Profile)
		if e != nil {
			return e
		}
		expected := injectClash(string(s.Original), s.AdditionProxy, s.AdditionRules)
		if string(current) != expected {
			// A subscription refresh may already have removed our profile additions
			// and live rules. Preserve that refreshed configuration instead of
			// reloading a stale backup. Any unresolved helper reference fails closed.
			if e = clashExternallyRemoved(c, current); e != nil {
				return fmt.Errorf("Clash profile changed after enabling; keep backup and restore manually to preserve your edits: %w", e)
			}
			latest, readErr := os.ReadFile(s.Profile)
			if readErr != nil {
				return readErr
			}
			if !bytes.Equal(latest, current) {
				return errors.New("Clash profile changed during restore verification; keep recovery record")
			}
			return os.Rename(statePath, filepath.Join(a.dir, "clash-restored-external-"+time.Now().Format("20060102-150405.000000000")+".json"))
		}
		// Reload the original merged configuration before removing persistent changes.
		path := filepath.Join(s.Data, "epic-ipv6-restore.yaml")
		if e = os.WriteFile(path, s.BaseConfig, 0600); e != nil {
			return e
		}
		if e = c.call("PUT", "/configs", map[string]string{"path": path}, nil); e != nil {
			return e
		}
		if e = os.WriteFile(s.Profile, s.Original, 0600); e != nil {
			return e
		}
		restoreSelections(c, s.Selections)
		os.Remove(path)
		os.Remove(filepath.Join(s.Data, "epic-ipv6-active.yaml"))
		return os.Rename(statePath, filepath.Join(a.dir, "clash-restored-"+time.Now().Format("20060102-150405")+".json"))
	}
	if _, e := os.Stat(statePath); e == nil {
		return errors.New("Clash integration already managed; inspect live rules or restore first")
	}
	data, e := locateClash()
	if e != nil {
		return e
	}
	c, base, e := clashAPI(data)
	if e != nil {
		return e
	}
	var live map[string]any
	if e = c.call("GET", "/configs", nil, &live); e != nil {
		return e
	}
	if strings.ToLower(fmt.Sprint(live["mode"])) != "rule" {
		return errors.New("Clash must already be in Rule mode")
	}
	lb, e := os.ReadFile(filepath.Join(data, "profiles", "list.yml"))
	if e != nil {
		return e
	}
	var list struct {
		Index int `yaml:"index"`
		Files []struct {
			Time string `yaml:"time"`
		} `yaml:"files"`
	}
	if e = yaml.Unmarshal(lb, &list); e != nil {
		return e
	}
	if list.Index < 0 || list.Index >= len(list.Files) {
		return errors.New("Invalid selected profile")
	}
	file := list.Files[list.Index].Time
	if filepath.Base(file) != file || !strings.HasSuffix(file, ".yml") {
		return errors.New("Invalid profile filename")
	}
	path := filepath.Join(data, "profiles", file)
	profile, original, e := readYAML(path)
	if e != nil {
		return e
	}
	var rules struct {
		Rules []struct{ Type, Payload, Proxy string }
	}
	if e = c.call("GET", "/rules", nil, &rules); e != nil {
		return e
	}
	pr, _ := profile["rules"].([]any)
	if len(pr) != len(rules.Rules) {
		return fmt.Errorf("Active rule count %d differs from selected profile %d; refusing broad reload", len(rules.Rules), len(pr))
	}
	for i, r := range rules.Rules {
		parts := strings.Split(fmt.Sprint(pr[i]), ",")
		if len(parts) < 2 {
			return errors.New("Invalid profile rule")
		}
		if parts[len(parts)-1] == "no-resolve" {
			parts = parts[:len(parts)-1]
		}
		if parts[len(parts)-1] != r.Proxy || (len(parts) > 2 && parts[1] != r.Payload) {
			return fmt.Errorf("Active rule differs at index %d", i)
		}
	}
	var pp struct {
		Proxies map[string]struct{ Type, Now string }
	}
	if e = c.call("GET", "/proxies", nil, &pp); e != nil {
		return e
	}
	if _, ok := pp.Proxies[clashName]; ok {
		return errors.New("Managed proxy name already exists")
	}
	selections := map[string]string{}
	for n, p := range pp.Proxies {
		if p.Type == "Selector" {
			selections[n] = p.Now
		}
	}
	// Preserve selected profile content and effective base settings. Reject unknown proxy inventory.
	expectedNames := map[string]bool{"DIRECT": true, "REJECT": true, "GLOBAL": true}
	for _, key := range []string{"proxies", "proxy-groups"} {
		v, _ := profile[key].([]any)
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				return errors.New("Invalid proxy config")
			}
			expectedNames[fmt.Sprint(m["name"])] = true
		}
	}
	for n := range pp.Proxies {
		if !expectedNames[n] {
			return errors.New("Active proxy inventory differs from selected profile")
		}
	}
	merged := map[string]any{}
	for k, v := range profile {
		merged[k] = v
	}
	for k, v := range base {
		if k != "proxies" && k != "proxy-groups" && k != "rules" {
			merged[k] = v
		}
	}
	for k, v := range live {
		merged[k] = v
	}
	originalMerged, e := yaml.Marshal(merged)
	if e != nil {
		return e
	}
	additionProxy := "  # BEGIN EpicIPv6Helper\n  - {name: " + clashName + ", type: http, server: 127.0.0.1, port: 17891}\n  # END EpicIPv6Helper\n"
	additionRules := "  # BEGIN EpicIPv6Helper\n"
	for _, h := range hosts {
		additionRules += "  - DOMAIN," + h + "," + clashName + "\n"
	}
	additionRules += "  # END EpicIPv6Helper\n"
	changed := injectClash(string(original), additionProxy, additionRules)
	var check map[string]any
	if e = yaml.Unmarshal([]byte(changed), &check); e != nil {
		return e
	}
	if reflect.DeepEqual(check, profile) {
		return errors.New("Profile uses unsupported YAML layout")
	}
	merged["proxies"] = check["proxies"]
	merged["rules"] = check["rules"]
	active, e := yaml.Marshal(merged)
	if e != nil {
		return e
	}
	s := clashState{Data: data, Profile: path, Original: original, AdditionProxy: additionProxy, AdditionRules: additionRules, BaseConfig: originalMerged, Selections: selections, Enabled: time.Now().Format(time.RFC3339), SourceID: sourceID, Hosts: hosts}
	sb, _ := json.Marshal(s)
	if e = os.WriteFile(statePath, sb, 0600); e != nil {
		return e
	}
	activePath := filepath.Join(data, "epic-ipv6-active.yaml")
	if e = os.WriteFile(activePath, active, 0600); e != nil {
		return e
	}
	if e = os.WriteFile(path, []byte(changed), 0600); e != nil {
		return e
	}
	if e = c.call("PUT", "/configs", map[string]string{"path": activePath}, nil); e != nil {
		os.WriteFile(path, original, 0600)
		os.Remove(statePath)
		return e
	}
	restoreSelections(c, selections)
	// Only close existing downloads for managed hosts so Epic reconnects through the new rule.
	var con struct {
		Connections []struct {
			ID       string
			Metadata struct{ Host string }
		}
	}
	if c.call("GET", "/connections", nil, &con) == nil {
		for _, x := range con.Connections {
			if containsHost(hosts, x.Metadata.Host) {
				c.call("DELETE", "/connections/"+x.ID, nil, nil)
			}
		}
	}
	return nil
}

// Verify all three independent sources before treating a modified profile as
// already restored. No persistent or live configuration is written here.
func clashExternallyRemoved(c clashClient, current []byte) error {
	if bytes.Contains(current, []byte("EpicIPv6Helper")) {
		return errors.New("managed marker or proxy reference remains in profile")
	}
	var profile map[string]any
	if e := yaml.Unmarshal(current, &profile); e != nil || profile == nil {
		return errors.New("modified Clash profile is not a valid configuration")
	}
	if hasClashManagedReference(profile) {
		return errors.New("managed proxy reference remains in decoded profile")
	}
	var inventory map[string]any
	if e := c.call("GET", "/proxies", nil, &inventory); e != nil {
		return e
	}
	proxies, ok := inventory["proxies"].(map[string]any)
	if !ok {
		return errors.New("Clash proxy inventory is missing or invalid")
	}
	if _, exists := proxies[clashName]; exists || hasClashManagedReference(proxies) {
		return errors.New("managed proxy or group reference remains live")
	}
	var result map[string]any
	if e := c.call("GET", "/rules", nil, &result); e != nil {
		return e
	}
	rules, ok := result["rules"].([]any)
	if !ok {
		return errors.New("Clash live rule inventory is missing or invalid")
	}
	for _, item := range rules {
		rule, ok := item.(map[string]any)
		if !ok {
			return errors.New("Clash live rule is invalid")
		}
		proxy, ok := rule["proxy"].(string)
		if !ok || proxy == "" {
			return errors.New("Clash live rule target is missing or invalid")
		}
		if hasClashManagedReference(rule) {
			return errors.New("managed proxy reference remains in live rules")
		}
	}
	return nil
}

func hasClashManagedReference(value any) bool {
	switch v := value.(type) {
	case string:
		return strings.Contains(v, "EpicIPv6Helper")
	case []any:
		for _, item := range v {
			if hasClashManagedReference(item) {
				return true
			}
		}
	case map[string]any:
		for key, item := range v {
			if hasClashManagedReference(key) || hasClashManagedReference(item) {
				return true
			}
		}
	}
	return false
}
func injectClash(src, p, r string) string {
	for _, block := range []struct{ key, addition string }{{"proxies:", p}, {"rules:", r}} {
		marker := block.key + "\n"
		i := strings.Index(src, marker)
		if i < 0 {
			continue
		}
		tail := src[i+len(marker):]
		line := strings.SplitN(tail, "\n", 2)[0]
		indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
		lines := strings.Split(block.addition, "\n")
		for j := range lines {
			if strings.HasPrefix(lines[j], "  ") {
				lines[j] = indent + lines[j][2:]
			}
		}
		src = strings.Replace(src, marker, marker+strings.Join(lines, "\n"), 1)
	}
	return src
}
func restoreSelections(c clashClient, s map[string]string) {
	for n, v := range s { // names are escaped for a single URL path segment
		c.call("PUT", "/proxies/"+url.PathEscape(n), map[string]string{"name": v}, nil)
	}
}
func (a *App) clashActive() bool {
	_, e := os.Stat(filepath.Join(a.dir, "clash-state.json"))
	return e == nil
}
