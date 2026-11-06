// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/autobrr/upbrr/internal/zulea/dupeaccept"
	"github.com/autobrr/upbrr/internal/zulea/seed"
)

func TestPublicJSONHidesSecrets(t *testing.T) {
	const (
		passkey = "PASSKEY-SENTINEL-PANEL"
		apiKey  = "APIKEY-SENTINEL-PANEL"
		qbit    = "QBIT-PASSWORD-SENTINEL-PANEL"
	)
	receipt := Receipt{
		Release:           "Example-GRP",
		State:             ReceiptAccepted,
		RegisteredTorrent: "/work/registered/Example-GRP.ptt.torrent",
		TorrentURL:        "https://tracker.example.invalid/torrents/77",
		Message: "key " + apiKey + " pw " + qbit +
			" https://tracker.example.invalid/announce/" + passkey +
			" https://img.example.invalid/delete/SECRET",
		Confirmation: ConfirmationStamp{
			Channel: ChannelPanel,
			Who:     "operator",
			IP:      "203.0.113.8",
			Session: "sess-1",
		},
	}
	var buf bytes.Buffer
	logger := NewLogger(&buf, true)
	logger.AddSecret(apiKey)
	logger.AddSecret(qbit)
	err := WritePublicJSON(&buf, "receipts", 0, []Receipt{receipt}, nil, logger.MaskKnown)
	if err != nil {
		t.Fatal(err)
	}
	text := buf.String()
	var doc JSONDocument
	if jerr := json.Unmarshal([]byte(text), &doc); jerr != nil || doc.SchemaVersion != SchemaVersion || doc.Command != "receipts" || !doc.OK {
		t.Fatalf("document rejected: %v", jerr)
	}
	for _, secret := range []string{passkey, apiKey, qbit, receipt.RegisteredTorrent, "/delete/SECRET"} {
		if strings.Contains(text, secret) {
			t.Fatal("json leaked a secret or a passkey torrent path")
		}
	}
	if !strings.Contains(text, ChannelPanel) || !strings.Contains(text, "operator") {
		t.Fatal("confirmation identity missing")
	}
}

func TestConfirmationFileRules(t *testing.T) {
	if runtime.GOOS == "windows" {
		// A file written as 0600 is reported as 0666 on Windows, so the
		// acceptance path cannot be observed there. The uploader runs on Linux.
		t.Skip("confirmation file mode 0600 is a Linux contract; Windows does not store Unix permission bits")
	}
	dir := t.TempDir()
	// TempDir follows the process umask, so pin the confirmation directory
	// to the 0700 contract before any file is accepted.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "upload.json")
	fp := strings.Repeat("ab", 32)
	phrase := ConfirmationPhrase(fp)
	body := ConfirmationFile{
		Command:     "upload",
		Release:     "Example-GRP",
		Fingerprint: fp,
		Phrase:      phrase,
		Who:         "operator",
		IP:          "203.0.113.8",
		Session:     "sess-1",
	}
	writeConfirm(t, path, body, 0o600)
	got, err := ConsumeConfirmation(path, ConfirmationExpect{
		Command:     "upload",
		Release:     "Example-GRP",
		Fingerprint: fp,
		Phrase:      phrase,
	}, time.Now())
	if err != nil || got.Who != "operator" || got.Stamp().Channel != ChannelPanel {
		t.Fatalf("consume %+v %v", got, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("original confirmation file must be renamed")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), ".used-") {
		t.Fatalf("used name %v %v", entries, err)
	}
	writeConfirm(t, path, body, 0o600)
	if _, err := ConsumeConfirmation(path, ConfirmationExpect{
		Command:     "upload",
		Release:     "Example-GRP",
		Fingerprint: fp,
		Phrase:      "WYSYLAM 000000000000",
	}, time.Now()); err == nil {
		t.Fatal("wrong phrase must be refused")
	}
	writeConfirm(t, path, body, 0o600)
	if _, err := ConsumeConfirmation(path, ConfirmationExpect{
		Command:     "upload",
		Release:     "Other-GRP",
		Fingerprint: fp,
		Phrase:      phrase,
	}, time.Now()); err == nil {
		t.Fatal("wrong release must be refused")
	}
	writeConfirm(t, path, body, 0o640)
	if _, err := ConsumeConfirmation(path, ConfirmationExpect{
		Command:     "upload",
		Release:     "Example-GRP",
		Fingerprint: fp,
		Phrase:      phrase,
	}, time.Now()); err == nil {
		t.Fatal("group-readable file must be refused")
	}
	writeConfirm(t, path, body, 0o600)
	old := time.Now().Add(-6 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := ConsumeConfirmation(path, ConfirmationExpect{
		Command:     "upload",
		Release:     "Example-GRP",
		Fingerprint: fp,
		Phrase:      phrase,
	}, time.Now()); err == nil {
		t.Fatal("expired file must be refused")
	}
	sha := strings.Repeat("cd", 32)
	accept, err := dupeaccept.AcceptPhrase("Example-GRP", sha)
	if err != nil {
		t.Fatal(err)
	}
	dupePath := filepath.Join(dir, "dupe.json")
	writeConfirm(t, dupePath, ConfirmationFile{
		Command: "dupe-accept",
		Release: "Example-GRP",
		SHA:     sha,
		Phrase:  accept,
	}, 0o600)
	if _, err := ConsumeConfirmation(dupePath, ConfirmationExpect{
		Command: "dupe-accept",
		Release: "Example-GRP",
		SHA:     sha,
		Phrase:  accept,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("ef", 20)
	seedPhrase, err := SeedStartPhrase(hash)
	if err != nil {
		t.Fatal(err)
	}
	seedPath := filepath.Join(dir, "seed.json")
	writeConfirm(t, seedPath, ConfirmationFile{
		Command: "seed",
		Release: "Example-GRP",
		SHA:     hash,
		Phrase:  seedPhrase,
		Who:     "operator",
	}, 0o600)
	if _, err := ConsumeConfirmation(seedPath, ConfirmationExpect{
		Command: "seed",
		Release: "Example-GRP",
		SHA:     hash,
		Phrase:  seedPhrase,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target.json")
	writeConfirm(t, target, body, 0o600)
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ConsumeConfirmation(link, ConfirmationExpect{
		Command:     "upload",
		Release:     "Example-GRP",
		Fingerprint: fp,
		Phrase:      phrase,
	}, time.Now()); err == nil {
		t.Fatal("a symlink confirmation file must be refused")
	}
	loose := filepath.Join(dir, "loose")
	if err := os.Mkdir(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	looseFile := filepath.Join(loose, "upload.json")
	writeConfirm(t, looseFile, body, 0o600)
	if _, err := ConsumeConfirmation(looseFile, ConfirmationExpect{
		Command:     "upload",
		Release:     "Example-GRP",
		Fingerprint: fp,
		Phrase:      phrase,
	}, time.Now()); err == nil || !strings.Contains(err.Error(), "0700") {
		t.Fatalf("directory mode 0755 must be refused, got %v", err)
	}
}

func TestSeedResultJSONOmitsPassword(t *testing.T) {
	const password = "QBIT-PASSWORD-SENTINEL-PANEL"
	res := seed.Result{
		Hash:       strings.Repeat("a", 40),
		Message:    "ok " + password,
		SeedSource: seed.SeedSourceCLI,
	}
	var buf bytes.Buffer
	logger := NewLogger(io.Discard, false)
	logger.AddSecret(password)
	if err := WritePublicJSON(&buf, "seed", 0, res, nil, logger.MaskKnown); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), password) || !strings.Contains(buf.String(), `"schema_version": 1`) {
		t.Fatal("seed json leaked the password or dropped schema_version")
	}
}

func writeConfirm(t *testing.T, path string, file ConfirmationFile, mode os.FileMode) {
	t.Helper()
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
