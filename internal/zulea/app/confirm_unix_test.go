// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build unix

package app

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

type foreignOwnerInfo struct{ os.FileInfo }

func (foreignOwnerInfo) Sys() any {
	return &syscall.Stat_t{Uid: uint32(os.Getuid() + 1)}
}

func TestConfirmationRejectsForeignOwnerStat(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := confirmOwner(foreignOwnerInfo{FileInfo: info}); err == nil {
		t.Fatal("foreign owner must be refused")
	}
}

func TestOpenConfirmationReadsSameDescriptorAfterPathReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "confirm.json")
	const original = `{"command":"upload","release":"Original-GRP"}`
	const replacement = `{"command":"upload","release":"Replacement-GRP"}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	file, info, err := openConfirmation(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if !info.Mode().IsRegular() || info.ModTime().After(time.Now().Add(time.Minute)) {
		t.Fatalf("unexpected fstat: %+v", info)
	}
	old := path + ".old"
	if err := os.Rename(path, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(replacement), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("descriptor followed replacement: got %q want %q", data, original)
	}
}
