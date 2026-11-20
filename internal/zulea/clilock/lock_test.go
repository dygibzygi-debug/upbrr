// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package clilock

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLockHelperProcess(_ *testing.T) {
	if os.Getenv("ZULEA_LOCK_HELPER") != "1" {
		return
	}
	lock, err := Acquire(os.Getenv("ZULEA_LOCK_WORK"))
	if errors.Is(err, ErrBusy) {
		_, _ = os.Stdout.WriteString("busy")
		return
	}
	if err != nil {
		_, _ = os.Stdout.WriteString("error: " + err.Error())
		os.Exit(2)
	}
	defer lock.Close()
	_, _ = os.Stdout.WriteString("acquired")
}

func TestAcquireSerializesProcessesAndReleasesOnClose(t *testing.T) {
	work := t.TempDir()
	lock, err := Acquire(work)
	if err != nil {
		t.Fatal(err)
	}
	runHelper := func() string {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestLockHelperProcess$")
		cmd.Env = append(os.Environ(), "ZULEA_LOCK_HELPER=1", "ZULEA_LOCK_WORK="+work)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("helper: %v: %s", err, out)
		}
		return string(out)
	}
	if got := runHelper(); !strings.Contains(got, "busy") {
		t.Fatalf("second process=%q want busy", got)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if got := runHelper(); !strings.Contains(got, "acquired") {
		t.Fatalf("after close=%q want acquired", got)
	}
}

func TestAcquireRejectsSymlink(t *testing.T) {
	work := t.TempDir()
	target := filepath.Join(work, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(work, fileName)
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Acquire(work); err == nil {
		t.Fatalf("symlink error=%v", err)
	}
}

func TestAcquireRejectsUnsafeMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX permission bits")
	}
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, fileName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(work); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("mode error=%v", err)
	}
}
