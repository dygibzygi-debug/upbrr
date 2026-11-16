// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReceiptsRemainCompatibleWith06x(t *testing.T) {
	work := t.TempDir()
	dir := filepath.Join(work, "receipts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	fixtures := map[string]string{
		"Synthetic.One.2020-GRP": `{
  "release":"Synthetic.One.2020-GRP","tracker":"PTT","fingerprint":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "tm_info_hash":"1111111111111111111111111111111111111111","state":"accepted","torrent_id":"100",
  "data_match":true,"started_at":"2026-11-01T00:00:00Z","finished_at":"2026-11-01T00:01:00Z"
}`,
		"Synthetic.Two.2021-GRP": `{
  "release":"Synthetic.Two.2021-GRP","tracker":"PTT","fingerprint":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "raw_description_sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
  "description_sha256":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
  "tm_info_hash":"2222222222222222222222222222222222222222","seed_source":"manual","state":"accepted",
  "torrent_id":"200","data_match":true,"started_at":"2026-11-15T00:00:00Z","finished_at":"2026-11-15T00:01:00Z"
}`,
	}
	for release, body := range fixtures {
		if err := os.WriteFile(filepath.Join(dir, release+".ptt.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runner := &Runner{Config: Config{WorkDir: work}}
	receipts, err := runner.Receipts()
	if err != nil || len(receipts) != 2 {
		t.Fatalf("receipts=%+v err=%v", receipts, err)
	}
	first, err := runner.readReceipt("Synthetic.One.2020-GRP")
	if err != nil || first.Confirmation.Channel != "" || first.RawDescriptionSHA256 != "" {
		t.Fatalf("0.6.1-style receipt=%+v err=%v", first, err)
	}
	second, err := runner.readReceipt("Synthetic.Two.2021-GRP")
	if err != nil || second.DescriptionSHA256 == "" || second.SeedSource != "manual" {
		t.Fatalf("0.6.3-style receipt=%+v err=%v", second, err)
	}
	second.Moderation = "APPROVED"
	second.ModerationCheckedAt = "2026-11-16T00:00:00Z"
	if err := writeReceipt(runner.receiptPath(second.Release), second, false); err != nil {
		t.Fatal(err)
	}
	after, err := runner.readReceipt(second.Release)
	if err != nil || after.RawDescriptionSHA256 != second.RawDescriptionSHA256 || after.SeedSource != second.SeedSource {
		t.Fatalf("status update lost 0.6.3 fields: %+v err=%v", after, err)
	}
}
