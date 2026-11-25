// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/autobrr/upbrr/internal/zulea/pttdupe"
	"github.com/autobrr/upbrr/internal/zulea/pttplan"
	"github.com/autobrr/upbrr/internal/zulea/zuleatest"
)

func completedCanaryDecision() *CanaryCompleteConfig {
	return &CanaryCompleteConfig{
		Confirmed:   true,
		DecidedBy:   "Example Operator",
		Date:        "2026-11-25",
		MinApproved: 3,
	}
}

func addApprovedReceipts(t *testing.T, runner *Runner, count int) {
	t.Helper()
	for i := range count {
		release := "Approved." + string(rune('A'+i)) + ".2026-GRP"
		if err := writeReceipt(runner.receiptPath(release), Receipt{
			Release:    release,
			Tracker:    "PTT",
			State:      ReceiptAccepted,
			Moderation: "APPROVED",
		}, false); err != nil {
			t.Fatal(err)
		}
	}
}

func completeWithoutModerationQueue(cfg Config) Config {
	cfg = completeCanaryConfig(cfg)
	cfg.PTT.ModQueue = false
	cfg.PTT.CanaryComplete = completedCanaryDecision()
	return cfg
}

func TestCanaryCompleteConfigValidation(t *testing.T) {
	_, cfg, _ := setup(t)
	cases := map[string]*CanaryCompleteConfig{
		"missing decided_by": {
			Confirmed:   true,
			Date:        "2026-11-25",
			MinApproved: 3,
		},
		"bad date": {
			Confirmed:   true,
			DecidedBy:   "operator",
			Date:        "25.11.2026",
			MinApproved: 3,
		},
		"minimum too low": {
			Confirmed:   true,
			DecidedBy:   "operator",
			Date:        "2026-11-25",
			MinApproved: 2,
		},
	}
	for name, decision := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := cfg
			candidate.PTT.CanaryComplete = decision
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid canary_complete must fail validation")
			}
		})
	}

	cfg.PTT.CanaryComplete = completedCanaryDecision()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	ptt, ok := doc["ptt"].(map[string]any)
	if !ok {
		t.Fatalf("encoded ptt object has type %T", doc["ptt"])
	}
	decision, ok := ptt["canary_complete"].(map[string]any)
	if !ok {
		t.Fatalf("encoded canary_complete object has type %T", ptt["canary_complete"])
	}
	decision["unknown"] = true
	data, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "zulea.json")
	writePrivate(t, path, string(data))
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown canary_complete field must fail closed: %v", err)
	}

	delete(decision, "unknown")
	decision["confirmed"] = "yes"
	data, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	writePrivate(t, path, string(data))
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "cannot unmarshal") {
		t.Fatalf("invalid canary_complete type must fail closed: %v", err)
	}
}

func TestCanaryCompleteRequiresApprovedEvidence(t *testing.T) {
	_, cfg, _ := setup(t)
	cfg = completeWithoutModerationQueue(cfg)
	runner := &Runner{Config: cfg, MediaInfo: fakeMediaInfo{dir: filepath.Join(cfg.WorkDir, "mi")}}
	dupes := &pttdupe.Result{Status: pttdupe.StatusClear, SearchComplete: true}
	gate := runner.canaryGate(zuleatest.Release, &pttplan.Plan{TaxonomyConfirmed: true}, dupes, pttKey)
	if gate.Ready || gate.Basis.Basis != "canary_complete" || gate.Basis.ApprovedCount != 0 {
		t.Fatalf("no APPROVED evidence must close D-5: %+v", gate)
	}
	addApprovedReceipts(t, runner, 2)
	gate = runner.canaryGate(zuleatest.Release, &pttplan.Plan{TaxonomyConfirmed: true}, dupes, pttKey)
	if gate.Ready || gate.Basis.ApprovedCount != 2 {
		t.Fatalf("too little APPROVED evidence must close D-5: %+v", gate)
	}
	addApprovedReceipts(t, runner, 3)
	gate = runner.canaryGate(zuleatest.Release, &pttplan.Plan{TaxonomyConfirmed: true}, dupes, pttKey)
	if !gate.Ready || gate.Basis.ApprovedCount != 3 || gate.Basis.MinApproved != 3 || !gate.Basis.Confirmed {
		t.Fatalf("enough APPROVED evidence must open D-5: %+v", gate)
	}
}

func TestCanaryBasisUnreadableReceiptOnlyBlocksCompletedCanaryPath(t *testing.T) {
	_, cfg, _ := setup(t)
	runner := &Runner{Config: completeCanaryConfig(cfg)}
	path := runner.receiptPath("Broken.Receipt.2026-GRP")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	basis, err := runner.CanaryBasis()
	if err != nil || basis.Basis != "mod_queue" {
		t.Fatalf("mod_queue path must not depend on receipt evidence: basis=%+v err=%v", basis, err)
	}

	runner.Config = completeWithoutModerationQueue(cfg)
	if _, err := runner.CanaryBasis(); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("completed-canary path must fail closed on unreadable receipt: %v", err)
	}
}

func TestCanaryBasisChangesFingerprint(t *testing.T) {
	_, cfg, _ := setup(t)
	cfg = completeWithoutModerationQueue(cfg)
	runner := &Runner{Config: cfg, MediaInfo: fakeMediaInfo{dir: filepath.Join(cfg.WorkDir, "mi")}}
	addApprovedReceipts(t, runner, 3)
	first, err := runner.DryRun(context.Background(), []string{zuleatest.Release})
	if err != nil || !first[0].Canary.Ready || first[0].Plan.Payload["mod_queue_opt_in"] != "0" {
		t.Fatalf("completed canary plan: %+v err=%v", first, err)
	}
	if first[0].Canary.Basis != first[0].Plan.CanaryBasis {
		t.Fatalf("visible and fingerprinted basis differ: gate=%+v plan=%+v", first[0].Canary.Basis, first[0].Plan.CanaryBasis)
	}

	before := first[0].Plan.Fingerprint
	addApprovedReceipts(t, runner, 4)
	second, err := runner.DryRun(context.Background(), []string{zuleatest.Release})
	if err != nil || second[0].Plan.Fingerprint == before || second[0].Canary.Basis.ApprovedCount != 4 {
		t.Fatalf("APPROVED count must change fingerprint: before=%s after=%+v err=%v", before, second, err)
	}
	runner.Config.PTT.CanaryComplete.Date = "2026-11-26"
	third, err := runner.DryRun(context.Background(), []string{zuleatest.Release})
	if err != nil || third[0].Plan.Fingerprint == second[0].Plan.Fingerprint {
		t.Fatalf("changed owner decision must change fingerprint: second=%+v third=%+v err=%v", second, third, err)
	}
}

func TestCanaryCompleteUploadPaths(t *testing.T) {
	for _, channel := range []string{ChannelTerminal, ChannelPanel} {
		t.Run(channel, func(t *testing.T) {
			if channel == ChannelPanel && runtime.GOOS == "windows" {
				t.Skip("panel confirmation mode is a Linux filesystem contract")
			}
			_, fake, runner := uploadSetup(t)
			runner.Config = completeWithoutModerationQueue(runner.Config)
			addApprovedReceipts(t, runner, 3)
			fingerprint := dryRunFingerprint(t, runner)
			preview := new(bytes.Buffer)
			req := UploadRequest{
				Release:     zuleatest.Release,
				Fingerprint: fingerprint,
				Preview:     preview,
			}
			if channel == ChannelTerminal {
				req.Confirm = typed(ConfirmationPhrase(fingerprint))
			} else {
				dir := filepath.Join(t.TempDir(), "confirmations")
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, "upload.json")
				writeConfirm(t, path, ConfirmationFile{
					Command:     "upload",
					Release:     zuleatest.Release,
					Fingerprint: fingerprint,
					Phrase:      ConfirmationPhrase(fingerprint),
					Who:         "operator",
				}, 0o600)
				req.ConfirmationPath = path
			}
			result, err := runner.Upload(context.Background(), req)
			if err != nil || fake.uploads != 1 || fake.fields["mod_queue_opt_in"] != "0" {
				t.Fatalf("completed canary upload: result=%+v err=%v uploads=%d fields=%+v", result, err, fake.uploads, fake.fields)
			}
			if result.Receipt.Confirmation.Channel != channel {
				t.Fatalf("confirmation channel=%q want=%q", result.Receipt.Confirmation.Channel, channel)
			}
		})
	}
}

func TestCanaryCompleteRefusesStaleOrInsufficientEvidence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		decision *CanaryCompleteConfig
	}{
		{name: "missing decision"},
		{name: "unconfirmed decision", decision: &CanaryCompleteConfig{
			DecidedBy:   "Example Operator",
			Date:        "2026-11-25",
			MinApproved: 3,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, fake, runner := uploadSetup(t)
			runner.Config = completeCanaryConfig(runner.Config)
			runner.Config.PTT.ModQueue = false
			runner.Config.PTT.CanaryComplete = tc.decision
			addApprovedReceipts(t, runner, 3)
			outcomes, err := runner.DryRun(context.Background(), []string{zuleatest.Release})
			if err != nil || outcomes[0].Plan == nil || outcomes[0].Canary.Ready {
				t.Fatalf("expected a blocked plan: %+v err=%v", outcomes, err)
			}
			fingerprint := outcomes[0].Plan.Fingerprint
			_, err = runner.Upload(context.Background(), UploadRequest{
				Release:     zuleatest.Release,
				Fingerprint: fingerprint,
				Preview:     &bytes.Buffer{},
				Confirm:     typed(ConfirmationPhrase(fingerprint)),
			})
			if !errors.Is(err, ErrUploadRefused) || fake.uploads != 0 {
				t.Fatalf("missing decision reached upload: %v uploads=%d", err, fake.uploads)
			}
			if _, statErr := os.Stat(runner.receiptPath(zuleatest.Release)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("refusal left a receipt: %v", statErr)
			}
		})
	}

	t.Run("insufficient", func(t *testing.T) {
		_, fake, runner := uploadSetup(t)
		runner.Config = completeWithoutModerationQueue(runner.Config)
		addApprovedReceipts(t, runner, 2)
		outcomes, err := runner.DryRun(context.Background(), []string{zuleatest.Release})
		if err != nil || outcomes[0].Plan == nil || outcomes[0].Canary.Ready {
			t.Fatalf("expected a blocked plan: %+v err=%v", outcomes, err)
		}
		fingerprint := outcomes[0].Plan.Fingerprint
		_, err = runner.Upload(context.Background(), UploadRequest{
			Release:     zuleatest.Release,
			Fingerprint: fingerprint,
			Preview:     &bytes.Buffer{},
			Confirm:     typed(ConfirmationPhrase(fingerprint)),
		})
		if !errors.Is(err, ErrUploadRefused) || fake.uploads != 0 {
			t.Fatalf("insufficient evidence reached upload: %v uploads=%d", err, fake.uploads)
		}
		if _, statErr := os.Stat(runner.receiptPath(zuleatest.Release)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("refusal left a receipt: %v", statErr)
		}
	})

	t.Run("stale fingerprint", func(t *testing.T) {
		_, fake, runner := uploadSetup(t)
		runner.Config = completeWithoutModerationQueue(runner.Config)
		addApprovedReceipts(t, runner, 3)
		fingerprint := dryRunFingerprint(t, runner)
		addApprovedReceipts(t, runner, 4)
		_, err := runner.Upload(context.Background(), UploadRequest{
			Release:     zuleatest.Release,
			Fingerprint: fingerprint,
			Preview:     &bytes.Buffer{},
			Confirm:     typed(ConfirmationPhrase(fingerprint)),
		})
		if !errors.Is(err, ErrUploadRefused) || fake.uploads != 0 {
			t.Fatalf("stale fingerprint reached upload: %v uploads=%d", err, fake.uploads)
		}
	})
}
