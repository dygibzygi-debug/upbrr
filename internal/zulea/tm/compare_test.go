// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package tm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func downloadedVariant(mutate func(map[string]any)) []byte {
	info := fixtureInfo()
	info["source"] = "PTT"
	if mutate != nil {
		mutate(info)
	}
	return enc(map[string]any{
		"announce":   "https://tracker.example.invalid/announce/PASSKEY-SENTINEL-0123456789",
		"created by": "tracker",
		"info":       info,
	})
}

func TestCompareTrackerAddsSource(t *testing.T) {
	c, err := Compare(fixture(fixtureInfo()), downloadedVariant(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !c.OK() || !c.DataMatch || !c.NamesMatch || !c.InfoHashChanged || !c.PrivateOK {
		t.Fatalf("comparison %+v", c)
	}
	report := c.Report()
	for _, want := range []string{"DANE_ZGODNE=TAK", "NAZWY_ZGODNE=TAK", "INFOHASH_ZMIENIONY_PRZEZ_TRACKER=TAK", "PRIVATE=1",
		"source        ROZNE  oryginal: None", "https://tracker.example.invalid  (dodatkowy element w adresie: TAK)"} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "PASSKEY-SENTINEL") {
		t.Fatal("report must not expose the passkey")
	}
}

func TestCompareVerdicts(t *testing.T) {
	renamed, err := Compare(fixture(fixtureInfo()), downloadedVariant(func(i map[string]any) { i["name"] = "Renamed" }))
	if err != nil || !renamed.OK() || renamed.NamesMatch {
		t.Fatalf("renamed: %+v %v", renamed, err)
	}
	broken, err := Compare(fixture(fixtureInfo()), downloadedVariant(func(i map[string]any) { i["pieces"] = strings.Repeat("B", 60) }))
	if err != nil || broken.OK() || broken.DataMatch {
		t.Fatalf("data mismatch must fail: %+v %v", broken, err)
	}
	public, err := Compare(fixture(fixtureInfo()), downloadedVariant(func(i map[string]any) { delete(i, "private") }))
	if err != nil || public.OK() || !strings.Contains(public.Report(), "PRIVATE=BRAK/0 — STOP") {
		t.Fatalf("missing private must fail: %+v %v", public, err)
	}
	unsorted := []byte("d4:infod4:name1:a12:piece lengthi16e6:pieces20:AAAAAAAAAAAAAAAAAAAA6:lengthi5e7:privatei1eee")
	if _, err := Describe(unsorted); err != nil {
		t.Fatalf("lenient decode must accept unsorted keys: %v", err)
	}
	if _, err := Describe([]byte("d4:infod1:ai1e1:ai2eee")); err == nil {
		t.Fatal("duplicate keys must be rejected")
	}
}

// TestCompareWritesFixturesForParity writes the scenario files when
// ZULEA_COMPARE_OUT is set, so the reference Python script can be run on them.
func TestCompareWritesFixturesForParity(t *testing.T) {
	dir := os.Getenv("ZULEA_COMPARE_OUT")
	if dir == "" {
		t.Skip("parity fixtures not requested")
	}
	files := map[string][]byte{
		"original.torrent": fixture(fixtureInfo()),
		"source.torrent":   downloadedVariant(nil),
		"renamed.torrent":  downloadedVariant(func(i map[string]any) { i["name"] = "Renamed" }),
		"broken.torrent":   downloadedVariant(func(i map[string]any) { i["pieces"] = strings.Repeat("B", 60) }),
		"public.torrent":   downloadedVariant(func(i map[string]any) { delete(i, "private") }),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if name == "original.torrent" {
			continue
		}
		c, err := Compare(files["original.torrent"], data)
		if err != nil {
			t.Fatal(err)
		}
		report := c.Report()
		idx := strings.Index(report, "WNIOSEK:")
		if idx < 0 {
			t.Fatal("report has no verdict")
		}
		if err := os.WriteFile(filepath.Join(dir, name+".go.txt"), []byte(report[idx:]), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
