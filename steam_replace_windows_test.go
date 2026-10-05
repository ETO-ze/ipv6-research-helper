package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// A read/write-sharing handle without FILE_SHARE_DELETE reproduces the Windows
// replacement lock without opening or changing the real system hosts file.
func steamLockWindowsTestFile(t *testing.T, path string) func() {
	t.Helper()
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil,
		syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			if err := syscall.CloseHandle(handle); err != nil {
				t.Error("closing fake hosts replacement lock", err)
			}
		})
	}
	t.Cleanup(release)
	return release
}

func steamWaitWindowsStaging(t *testing.T, path string, result <-chan error) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		staged, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".ipv6helper-steam-*.tmp"))
		if err != nil {
			t.Fatal(err)
		}
		if len(staged) == 1 {
			return
		}
		select {
		case err := <-result:
			t.Fatal("restore completed before encountering the Windows replacement lock", err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("restore did not stage a replacement for the fake hosts file")
}

func steamAssertNoWindowsStaging(t *testing.T, path string) {
	t.Helper()
	staged, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".ipv6helper-steam-*.tmp"))
	if err != nil || len(staged) != 0 {
		t.Fatal("replacement left temporary files beside fake hosts", staged, err)
	}
}

func TestSteamRestoreRetriesWindowsReplacementLock(t *testing.T) {
	original := []byte("# original\r\n240e:928:801::e dl.steam.clngaa.com.z.ngaagslb.net #UHE_")
	a := steamFakeApp(t, original)
	if err := a.steamApplyLocked([]string{"cache7-hkg1.steamcontent.com"}); err != nil {
		t.Fatal(err)
	}
	applied := steamReadFake(t, a)
	release := steamLockWindowsTestFile(t, a.hostsPath)
	result := make(chan error, 1)
	go func() { result <- a.restoreSteam() }()
	steamWaitWindowsStaging(t, a.hostsPath, result)
	time.Sleep(120 * time.Millisecond)
	select {
	case err := <-result:
		t.Fatal("temporary replacement lock was not retried", err)
	default:
	}
	if !a.steamRestoring.Load() || a.steamRouteEpoch.Load() != 1 {
		t.Fatal("Steam dials were not blocked throughout the replacement retry")
	}
	if !bytes.Equal(steamReadFake(t, a), applied) {
		t.Fatal("restore wrote into the locked hosts file instead of atomically replacing it")
	}
	if _, err := os.Stat(filepath.Join(a.dir, "steam-route.json")); err != nil {
		t.Fatal("restore removed recovery data while hosts was still locked", err)
	}
	release()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal("restoration failed after the temporary lock was released", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("restoration did not resume after the temporary lock was released")
	}
	if !bytes.Equal(steamReadFake(t, a), original) || a.steamRestoring.Load() {
		t.Fatal("restoration lost exact original hosts bytes or retained the dial gate")
	}
	if _, err := os.Stat(filepath.Join(a.dir, "steam-route.json")); !os.IsNotExist(err) {
		t.Fatal("successful restoration retained an active recovery journal", err)
	}
	steamAssertNoWindowsStaging(t, a.hostsPath)
}

func TestSteamRestoreRejectsConcurrentChangeDuringWindowsLock(t *testing.T) {
	a := steamFakeApp(t, []byte("# original\n"))
	if err := a.steamApplyLocked([]string{"cache7-hkg1.steamcontent.com"}); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(a.dir, "steam-route.json")
	journal, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	steamLockWindowsTestFile(t, a.hostsPath)
	result := make(chan error, 1)
	go func() { result <- a.restoreSteam() }()
	steamWaitWindowsStaging(t, a.hostsPath, result)
	changed := append([]byte("# concurrent external edit\r\n"), steamReadFake(t, a)...)
	if err := os.WriteFile(a.hostsPath, changed, 0600); err != nil {
		t.Fatal("fake lock should allow ordinary external writes", err)
	}
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "外部修改") {
			t.Fatal("snapshot change during retry was not explicitly rejected", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot change did not abort the replacement retry promptly")
	}
	if !bytes.Equal(steamReadFake(t, a), changed) {
		t.Fatal("retry overwrote concurrent user changes")
	}
	if saved, err := os.ReadFile(journalPath); err != nil || !bytes.Equal(saved, journal) {
		t.Fatal("failed retry discarded or changed recovery data", err)
	}
	if a.steamRestoring.Load() || a.steamRouteEpoch.Load() != 1 {
		t.Fatal("failed restoration left the dial gate active or lost its route epoch")
	}
	steamAssertNoWindowsStaging(t, a.hostsPath)
}

func TestSteamRestoreWindowsReplacementLockHasBoundedRetry(t *testing.T) {
	a := steamFakeApp(t, []byte("# original\n"))
	if err := a.steamApplyLocked([]string{"cache7-hkg1.steamcontent.com"}); err != nil {
		t.Fatal(err)
	}
	applied := steamReadFake(t, a)
	journalPath := filepath.Join(a.dir, "steam-route.json")
	journal, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	steamLockWindowsTestFile(t, a.hostsPath)
	start := time.Now()
	err = a.restoreSteam()
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "暂时占用") || !strings.Contains(err.Error(), "权限不足") {
		t.Fatal("persistent Windows lock did not report both lock and permission possibilities", err)
	}
	if elapsed < steamHostsReplaceRetry-200*time.Millisecond || elapsed > steamHostsReplaceRetry+2*time.Second {
		t.Fatal("replacement retry was not bounded around its configured duration", elapsed)
	}
	if !bytes.Equal(steamReadFake(t, a), applied) {
		t.Fatal("timeout altered the locked hosts file")
	}
	if saved, err := os.ReadFile(journalPath); err != nil || !bytes.Equal(saved, journal) {
		t.Fatal("timeout discarded or changed recovery data", err)
	}
	if a.steamRestoring.Load() || a.steamRouteEpoch.Load() != 1 {
		t.Fatal("timeout left Steam downloads blocked or lost its route epoch")
	}
	steamAssertNoWindowsStaging(t, a.hostsPath)
}

func TestSteamWindowsReplacementRetriesOnlyLockOrAccessErrors(t *testing.T) {
	for _, code := range []syscall.Errno{5, 32, 33} {
		err := &os.LinkError{Op: "rename", Old: "stage", New: "fake-hosts", Err: code}
		if !steamHostsReplaceBusy(err) {
			t.Error("transient Windows replacement error was not classified", code)
		}
	}
	for _, err := range []error{nil, syscall.Errno(2), syscall.Errno(87), io.EOF, errors.New("arbitrary error")} {
		if steamHostsReplaceBusy(err) {
			t.Error("unrelated replacement error was classified for retries", err)
		}
	}
}
