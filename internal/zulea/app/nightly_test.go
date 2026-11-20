// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/autobrr/upbrr/internal/zulea/seed"
)

func TestNightlyWarnsOnlyWhenOKSetChanges(t *testing.T) {
	work := t.TempDir()
	runner := &Runner{Config: Config{WorkDir: work}}
	buildNightly := func(r *Runner, client seed.Client) (Summary, error) {
		t.Helper()
		return r.BuildSummaryWithOptions(context.Background(), client, SummaryOptions{UpdateNightlyState: true})
	}
	writeCheck := func(name string, results []CheckResult) {
		t.Helper()
		ok, stop := 0, 0
		for _, result := range results {
			if result.Status == "OK" {
				ok++
			} else {
				stop++
			}
		}
		writeCheckFile(t, runner.Config.WorkDir, name, CheckSummary{
			Results: results,
			OK:      ok,
			Stop:    stop,
		})
	}
	writeCheck("check-20060101-000000.json", []CheckResult{
		{
			Release: "Standing-GRP",
			Status:  "STOP",
			Reason:  "tm",
		},
		{Release: "Ready-GRP", Status: "OK"},
	})
	first, err := buildNightly(runner, nil)
	if err != nil || first.Attention || !strings.Contains(first.Text, "STOP zastane") {
		t.Fatalf("baseline must not warn about standing STOPs: %+v %v", first, err)
	}
	second, err := buildNightly(runner, nil)
	if err != nil || second.Attention || !strings.Contains(second.Text, "STOP bez zmian") {
		t.Fatalf("unchanged STOPs must stay quiet: %+v %v", second, err)
	}
	goneRunner := &Runner{Config: Config{WorkDir: t.TempDir()}}
	writeCheckFile(t, goneRunner.Config.WorkDir, "check-20060101-000000.json", CheckSummary{
		Results: []CheckResult{{Release: "Ready-GRP", Status: "OK"}, {
			Release: "Standing-GRP",
			Status:  "STOP",
			Reason:  "tm",
		}},
		OK:   1,
		Stop: 1,
	})
	if base, err := buildNightly(goneRunner, nil); err != nil || base.Attention {
		t.Fatalf("baseline %+v %v", base, err)
	}
	writeCheckFile(t, goneRunner.Config.WorkDir, "check-20060102-000000.json", CheckSummary{
		Results: []CheckResult{{
			Release: "Standing-GRP",
			Status:  "STOP",
			Reason:  "tm",
		}},
		Stop: 1,
	})
	fourth, err := buildNightly(goneRunner, nil)
	if err != nil || !fourth.Attention || !strings.Contains(fourth.Text, "Zniknęło z OK: Ready-GRP") {
		t.Fatalf("a release leaving OK must warn: %+v %v", fourth, err)
	}
	writeCheck("check-20060102-000000.json", []CheckResult{
		{
			Release: "Standing-GRP",
			Status:  "STOP",
			Reason:  "tm",
		},
		{
			Release: "Ready-GRP",
			Status:  "STOP",
			Reason:  "tm",
		},
	})
	third, err := buildNightly(runner, nil)
	if err != nil || !third.Attention || !strings.Contains(third.Text, "Nowy STOP (wcześniej OK): Ready-GRP") {
		t.Fatalf("a new STOP must warn: %+v %v", third, err)
	}
	st, err := os.Stat(runner.nightlyStatePath())
	if err != nil {
		t.Fatal(err)
	}
	// Windows reports 0666 for every writable file: os.Chmod only toggles
	// the read-only attribute, so 0600 cannot be observed there. The nightly
	// state file is written on Linux, where this mode is enforced.
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("nightly state mode %v", st.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(runner.nightlyStatePath()))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("temp file left behind: %s", entry.Name())
		}
	}
}

func TestNightlyReceiptAndSeedAlerts(t *testing.T) {
	work := t.TempDir()
	runner := &Runner{Config: Config{WorkDir: work}}
	receipts := filepath.Join(work, "receipts")
	if err := os.MkdirAll(receipts, 0o700); err != nil {
		t.Fatal(err)
	}
	fresh := Receipt{
		Release:    "Fresh-GRP",
		State:      ReceiptAccepted,
		FinishedAt: time.Now().UTC().Format(time.RFC3339),
		Moderation: "PENDING",
	}
	if err := writeReceipt(filepath.Join(receipts, "Fresh-GRP.ptt.json"), fresh, true); err != nil {
		t.Fatal(err)
	}
	summary, err := runner.BuildSummary(context.Background(), nil)
	if err != nil || summary.Attention {
		t.Fatalf("a fresh moderation wait must not warn: %+v %v", summary, err)
	}
	old := fresh
	old.Release = "Old-GRP"
	old.FinishedAt = time.Now().UTC().Add(-25 * time.Hour).Format(time.RFC3339)
	if err := writeReceipt(filepath.Join(receipts, "Old-GRP.ptt.json"), old, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(receipts, "Torn-GRP.ptt.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	summary, err = runner.BuildSummary(context.Background(), failLoginQB{})
	if err != nil || !summary.Attention {
		t.Fatalf("alerts expected: %+v %v", summary, err)
	}
	for _, want := range []string{"Old-GRP: moderacja dłużej niż 24h", "Torn-GRP: nieczytelne", "Seed: błąd seed-status"} {
		if !strings.Contains(summary.Text, want) {
			t.Errorf("summary missing %q:\n%s", want, summary.Text)
		}
	}
	if strings.Contains(summary.Text, "Fresh-GRP:") {
		t.Fatalf("fresh receipt was warned:\n%s", summary.Text)
	}
}

func TestNightlyHandledElsewhereDoesNotRaiseAttention(t *testing.T) {
	prev := nightlyState{OK: []string{"Handled-GRP"}}
	lines, attention, next := nightlyChainDelta(prev, true, []CheckResult{{Release: "Handled-GRP", Status: StatusHandledElsewhere}})
	if attention || len(lines) != 0 || len(next) != 1 || next[0] != "Handled-GRP" {
		t.Fatalf("handled elsewhere must stay quiet: lines=%v attention=%t next=%v", lines, attention, next)
	}
}

func TestSummaryUpdatesNightlyStateOnlyWhenExplicit(t *testing.T) {
	work := t.TempDir()
	runner := &Runner{Config: Config{WorkDir: work}}
	writeCheckFile(t, work, "check-20060101-000000.json", CheckSummary{
		Results: []CheckResult{{Release: "Ready-GRP", Status: "OK"}},
		OK:      1,
	})
	if _, err := runner.BuildSummary(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runner.nightlyStatePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plain summary changed nightly state: %v", err)
	}
	if _, err := runner.BuildSummaryWithOptions(context.Background(), nil, SummaryOptions{UpdateNightlyState: true}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(runner.nightlyStatePath())
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("explicit nightly state update: %v mode=%v", err, info)
	}
}

func writeCheckFile(t *testing.T, work, name string, summary CheckSummary) {
	t.Helper()
	dir := filepath.Join(work, "reports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

type failLoginQB struct{}

func (failLoginQB) Login(context.Context) error { return errors.New("qBittorrent down") }
func (failLoginQB) Find(context.Context, string) (*seed.Torrent, error) {
	return nil, nil
}
func (failLoginQB) Add(context.Context, string, map[string]string) error { return nil }
func (failLoginQB) Recheck(context.Context, string) error                { return nil }
func (failLoginQB) Start(context.Context, string) error                  { return nil }
