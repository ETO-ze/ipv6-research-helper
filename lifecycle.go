package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"time"
)

const appVersion = "0.7.0"
const productID = "EpicIPv6Helper"

func openDashboard() {
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", "http://127.0.0.1:17890/")
	hideCommand(cmd)
	cmd.Start()
}
func existingService(action string) bool {
	c := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	res, e := c.Get("http://127.0.0.1:17890/api/state")
	if e != nil {
		return false
	}
	var state struct{ Application string }
	e = json.NewDecoder(res.Body).Decode(&state)
	res.Body.Close()
	if e != nil || state.Application != productID {
		return false
	}
	if action == "open" {
		openDashboard()
		return true
	}
	if action == "restore" {
		res, e = c.Get("http://127.0.0.1:17890/")
		if e != nil {
			return false
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		matches := regexp.MustCompile(`const token='([a-f0-9]+)'`).FindSubmatch(b)
		if len(matches) != 2 {
			return false
		}
		r, _ := http.NewRequest("POST", "http://127.0.0.1:17890/api/shutdown", nil)
		r.Header.Set("X-Token", string(matches[1]))
		c.Timeout = 45 * time.Second
		res, e = c.Do(r)
		if e != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == 200
	}
	return true
}
func (a *App) restoreAll() error {
	if e := a.clashAction("restore"); e != nil {
		return e
	}
	for _, action := range []string{"RestoreEpicProxy", "RestoreBypass", "RestoreGuard"} {
		if e := a.networkAction(action); e != nil {
			return e
		}
	}
	return a.hosts(false)
}
func run() error {
	dir := flag.String("data", "", "Runtime directory")
	noOpen := flag.Bool("no-open", false, "Don't open dashboard")
	restore := flag.Bool("restore", false, "Restore managed routing and exit")
	clashMode := flag.String("clash", "", "enable or restore local Clash routing")
	manual := flag.Bool("manual", false, "Start without automatically changing routing")
	audit := flag.Bool("research-audit", false, "Audit research sources without starting servers or changing routing")
	flag.Parse()
	if *dir == "" {
		root := os.Getenv("LOCALAPPDATA")
		if root == "" {
			return errors.New("LOCALAPPDATA unavailable")
		}
		*dir = filepath.Join(root, productID, "runtime")
	}
	if e := os.MkdirAll(*dir, 0700); e != nil {
		return e
	}
	lf, e := os.OpenFile(filepath.Join(*dir, "application.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer lf.Close()
	log.SetOutput(lf)
	if *audit {
		a, e := newApp(*dir)
		if e != nil {
			return e
		}
		defer a.journal.Close()
		defer a.stop()
		return researchAudit(a)
	}
	if *restore && existingService("restore") {
		return nil
	}
	if *clashMode == "" && !*restore {
		h, exists, e := acquireInstance()
		if e != nil {
			return e
		}
		defer releaseInstance(h)
		if exists {
			for i := 0; i < 15; i++ {
				action := "check"
				if !*noOpen {
					action = "open"
				}
				if existingService(action) {
					return nil
				}
				time.Sleep(200 * time.Millisecond)
			}
			return errors.New("已有助手正在启动，或管理端口被占用。请稍后重试。")
		}
	}
	a, e := newApp(*dir)
	if e != nil {
		return e
	}
	defer a.journal.Close()
	if *clashMode != "" {
		if *clashMode != "enable" && *clashMode != "restore" {
			return errors.New("Invalid Clash action")
		}
		return a.clashAction(*clashMode)
	}
	if *restore {
		return a.restoreAll()
	}
	specs := []string{"127.0.0.1:17890", "127.0.0.1:17891", bindIP + ":80", bindIP + ":443"}
	for _, s := range specs {
		l, e := net.Listen("tcp4", s)
		if e != nil {
			a.stop()
			return fmt.Errorf("无法监听 %s，请先退出旧版助手或检查端口占用：%w", s, e)
		}
		a.listeners = append(a.listeners, l)
	}
	defer a.stop()
	go (&http.Server{Handler: http.HandlerFunc(a.ui), ReadHeaderTimeout: 5 * time.Second}).Serve(a.listeners[0])
	for _, l := range a.listeners[1:3] {
		go (&http.Server{Handler: http.HandlerFunc(a.proxy), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 65536}).Serve(l)
	}
	go func() {
		for {
			c, e := a.listeners[3].Accept()
			if e != nil {
				return
			}
			go a.tlsTunnel(c)
		}
	}()
	if !*manual {
		var activateErr error
		if a.clashActive() {
			activateErr = a.checkClashRoute()
		} else if _, e := locateClash(); e == nil {
			activateErr = a.clashAction("enable")
		} else {
			activateErr = a.hosts(true)
		}
		if activateErr != nil {
			a.mu.Lock()
			a.startupError = "自动接入未完成：" + activateErr.Error() + "。直连 hosts 模式需要右键 EXE，以管理员身份运行。"
			a.mu.Unlock()
			log.Print(activateErr)
		}
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	go func() {
		select {
		case <-sig:
			if e := a.restoreAll(); e != nil {
				log.Print(e)
				return
			}
			a.stop()
		case <-a.done:
		}
	}()
	log.Printf("Version %s started, PID %d", appVersion, os.Getpid())
	if !*noOpen {
		openDashboard()
	}
	<-a.done
	return nil
}
func main() {
	if e := run(); e != nil {
		log.Print(e)
		showError(e.Error())
		os.Exit(1)
	}
}
