package t3

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchLockDeniesConcurrentAndRecoversAfterInterruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launch.lock")
	first, err := launchLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := launchLock(path); err == nil {
		second.Close()
		t.Fatal("concurrent startup accepted")
	}
	// File close also occurs when a process exits or exec replaces it.
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := launchLock(path)
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
}

func TestProcessIdentityIncludesKernelStartTime(t *testing.T) {
	first, err := processStartTime(os.Getpid())
	if err != nil || first == "" {
		t.Fatal("current process start time unavailable", err)
	}
	second, err := processStartTime(os.Getpid())
	if err != nil || first != second {
		t.Fatal("start identity changed for same process", err)
	}
	// Command names can contain whitespace and parentheses. Field 22 remains
	// relative to the final command delimiter, rather than raw word positions.
	stat := "42 (name with ) spaces) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 98765 20"
	parsed, err := parseProcessStartTime(stat)
	if err != nil || parsed != "98765" {
		t.Fatal("process identity parsed incorrectly", parsed, err)
	}
	for _, bad := range []string{"", "42 (short) S 1", strings.Replace(stat, "98765", "not-a-start-time", 1)} {
		if _, err := parseProcessStartTime(bad); err == nil {
			t.Fatal("malformed process identity accepted")
		}
	}
}

func TestPIDReuseDoesNotAdoptOrSignalAnotherProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.pid")
	write := func(record processRecord) {
		raw, _ := json.Marshal(record)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(processRecord{PID: os.Getpid(), Instance: "old", StartTime: "0"})
	if record, err := activeRecordAt(path); err != nil || record != nil {
		t.Fatal("stale process start identity blocked recovery", err)
	}
	current, err := processStartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	write(processRecord{PID: os.Getpid(), Instance: "old", StartTime: current})
	if _, err := activeRecordAt(path); err == nil {
		t.Fatal("non-T3 executable adopted despite matching PID")
	}
	write(processRecord{PID: 1, Instance: "old", StartTime: current})
	if _, err := activeRecordAt(path); err == nil {
		t.Fatal("PID 1 accepted")
	}
}
