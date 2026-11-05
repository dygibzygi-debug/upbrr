// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/autobrr/upbrr/internal/zulea/dupeaccept"
	"github.com/autobrr/upbrr/internal/zulea/pttdupe"
	"github.com/autobrr/upbrr/internal/zulea/pttplan"
	"github.com/autobrr/upbrr/internal/zulea/zuleatest"
)

const acceptReason = "REMUX obok enkodu tej samej rozdzielczości, decyzja operatora"

func blockNetwork(t *testing.T) func() {
	t.Helper()
	prev := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("dupe-accept contacted the network: %s", r.URL.Host)
		return nil, errors.New("network blocked")
	})
	return func() { http.DefaultTransport = prev }
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func confirmCalls(t *testing.T, answer string, called *bool) func(string) (string, error) {
	t.Helper()
	return func(string) (string, error) {
		*called = true
		return answer, nil
	}
}

func plantPlan(t *testing.T, work string, dupe *pttdupe.Result, name string) string {
	t.Helper()
	dir := filepath.Join(work, "reports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(Outcome{
		Release: zuleatest.Release,
		Tracker: "PTT",
		Status:  StatusStop,
		Dupe:    dupe,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, zuleatest.Release+".ptt."+name+".json")
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func dupeResult(status string, cands ...pttdupe.Candidate) *pttdupe.Result {
	return &pttdupe.Result{
		Status:         status,
		Message:        status,
		Candidates:     cands,
		SearchComplete: true,
		CandidateCount: len(cands),
	}
}

func grpCandidate(id, name string, size int64) pttdupe.Candidate {
	return pttdupe.Candidate{
		ID:         id,
		Name:       name,
		Type:       "Encode",
		Resolution: "1080p",
		SizeBytes:  size,
		Relation:   "same_slot",
		Reasons:    []string{"same_tracker_slot: same slot"},
	}
}

func TestDupeAcceptOpensGateAndChangesFingerprint(t *testing.T) {
	_, fake, runner := uploadSetup(t)
	fake.filterRows = sameSlotRow
	var logs bytes.Buffer
	logger := NewLogger(&logs, true)
	logger.AddSecret(pttKey)
	runner.Logger = logger

	blocked, err := runner.DryRun(context.Background(), []string{zuleatest.Release})
	if err != nil || blocked[0].Status != StatusStop || blocked[0].Canary.Ready || blocked[0].Dupe == nil || blocked[0].Dupe.Status != pttdupe.StatusDuplicate {
		t.Fatalf("without acceptance the release must STOP: %+v %v", blocked, err)
	}
	fpBefore := blocked[0].Plan.Fingerprint
	sha, err := dupeaccept.SetSHA(blocked[0].Dupe.Candidates)
	if err != nil {
		t.Fatal(err)
	}
	phrase, err := dupeaccept.AcceptPhrase(zuleatest.Release, sha)
	if err != nil {
		t.Fatal(err)
	}
	var preview bytes.Buffer
	restore := blockNetwork(t)
	rec, err := runner.DupeAccept(DupeAcceptRequest{
		Release: zuleatest.Release,
		Reason:  acceptReason,
		Preview: &preview,
		Confirm: typed(phrase),
	})
	restore()
	if err != nil {
		t.Fatal(err)
	}
	text := preview.String()
	for _, want := range []string{"typ: REMUX", "rozdzielczość: 1080p", "rozmiar:", "id=7", "same_tracker_slot", sha, phrase} {
		if !strings.Contains(text, want) {
			t.Fatalf("preview missing %q", want)
		}
	}
	if strings.Contains(logs.String(), pttKey) || strings.Contains(logs.String(), acceptReason) {
		t.Fatal("acceptance or secret reached the log")
	}
	if !strings.Contains(logs.String(), "decision=dupe_accept") || !strings.Contains(logs.String(), sha) {
		t.Fatal("log missing the accept decision")
	}
	path := dupeaccept.Path(runner.Config.WorkDir, zuleatest.Release)
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v err %v", st, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(raw), pttKey) || !strings.Contains(string(raw), acceptReason) {
		t.Fatalf("record leaked or dropped the reason: %v", err)
	}
	if rec.Version != Version || len(rec.History) != 1 || rec.CandidateSHA != sha {
		t.Fatalf("record %+v", rec)
	}

	opened, err := runner.DryRun(context.Background(), []string{zuleatest.Release})
	if err != nil || opened[0].Status != StatusReady || !opened[0].Canary.Ready || opened[0].Acceptance == nil {
		t.Fatalf("accepted duplicate must open the gate: status=%s canary=%+v acc=%+v err=%v reason=%s",
			opened[0].Status, opened[0].Canary, opened[0].Acceptance, err, opened[0].Reason)
	}
	if opened[0].Plan.Fingerprint == fpBefore || opened[0].Acceptance.SHA != sha {
		t.Fatalf("fingerprint did not include the acceptance: before %s after %s", fpBefore, opened[0].Plan.Fingerprint)
	}
	var shown bytes.Buffer
	Render(&shown, opened, nil)
	shownText := shown.String()
	if !strings.Contains(shownText, "DUPLIKAT ZAAKCEPTOWANY PRZEZ CZŁOWIEKA: "+acceptReason) ||
		!strings.Contains(shownText, opened[0].Acceptance.At) ||
		!strings.Contains(shownText, "gotowość do canary: TAK") ||
		!strings.Contains(shownText, "7 ") {
		t.Fatalf("plan preview:\n%s", shownText)
	}

	logs.Reset()
	revokePhrase, err := dupeaccept.RevokePhrase(zuleatest.Release, sha)
	if err != nil {
		t.Fatal(err)
	}
	var revokePreview bytes.Buffer
	restore = blockNetwork(t)
	revoked, err := runner.DupeAccept(DupeAcceptRequest{
		Release: zuleatest.Release,
		Reason:  "wycofuję akceptację testową",
		Revoke:  true,
		Preview: &revokePreview,
		Confirm: typed(revokePhrase),
	})
	restore()
	if err != nil {
		t.Fatal(err)
	}
	if revoked.Active() != nil || len(revoked.History) != 2 || revoked.History[0].Reason != acceptReason || revoked.Action != dupeaccept.ActionRevoke {
		t.Fatalf("revoke %+v", revoked)
	}
	if strings.Contains(logs.String(), pttKey) || strings.Contains(logs.String(), "wycofuję akceptację testową") {
		t.Fatal("revoke log contains the reason or a secret")
	}
	closed, err := runner.DryRun(context.Background(), []string{zuleatest.Release})
	if err != nil || closed[0].Status != StatusStop || closed[0].Canary.Ready || closed[0].Plan.Fingerprint != fpBefore {
		t.Fatalf("revoke must restore the block and the fingerprint: %+v fp %s want %s err %v",
			closed[0].Canary, closed[0].Plan.Fingerprint, fpBefore, err)
	}
}

func TestDupeAcceptRejectsChangedCandidateSet(t *testing.T) {
	cases := map[string]string{
		"new candidate": sameSlotRow + `,{"id":8,"attributes":{"name":"Second Film 2018 MULTi 1080p BluRay Encode x264-GRP","tmdb_id":100001,"category":"Movie","type":"Encode","resolution":"1080p","size":40}}`,
		"changed size":  strings.Replace(sameSlotRow, `"size":40`, `"size":41`, 1),
		"changed name":  strings.Replace(sameSlotRow, "Atmos 7.1-GRP", "Atmos 7.1-OTHER", 1),
	}
	for name, rows := range cases {
		t.Run(name, func(t *testing.T) {
			_, fake, runner := uploadSetup(t)
			fake.filterRows = sameSlotRow
			if _, err := acceptFromSearch(t, runner); err != nil {
				t.Fatal(err)
			}
			fake.filterRows = rows
			outcomes, err := runner.DryRun(context.Background(), []string{zuleatest.Release})
			if err != nil || outcomes[0].Status != StatusStop || outcomes[0].Canary.Ready || outcomes[0].Acceptance != nil {
				t.Fatalf("changed set must STOP: %+v err %v", outcomes[0], err)
			}
			if !strings.Contains(outcomes[0].Reason, "nie pasuje") {
				t.Fatalf("reason %q", outcomes[0].Reason)
			}
		})
	}
}

func TestDupeAcceptRefusesOurGroup(t *testing.T) {
	_, fake, runner := uploadSetup(t)
	fake.filterRows = `{"id":8,"attributes":{"name":"Another.Film.2014.MULTi.1080p.REMUX.BluRay.AVC.DTS-HD.MA.5.1-Zulea",` +
		`"tmdb_id":100001,"category":"Movie","type":"Remux","resolution":"1080p","size":40}}`
	if _, err := runner.DryRun(context.Background(), []string{zuleatest.Release}); err != nil {
		t.Fatal(err)
	}
	called := false
	restore := blockNetwork(t)
	defer restore()
	_, err := runner.DupeAccept(DupeAcceptRequest{
		Release: zuleatest.Release,
		Reason:  acceptReason,
		Preview: &bytes.Buffer{},
		Confirm: confirmCalls(t, "AKCEPTUJE x", &called),
	})
	if err == nil || called || !strings.Contains(err.Error(), "-Zulea") {
		t.Fatalf("called %v err %v", called, err)
	}
	if _, statErr := os.Stat(dupeaccept.Path(runner.Config.WorkDir, zuleatest.Release)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("refused acceptance must not be stored")
	}
}

func TestDupeAcceptRefusesExactDuplicate(t *testing.T) {
	_, _, runner := uploadSetup(t)
	plantPlan(t, runner.Config.WorkDir, dupeResult(pttdupe.StatusDuplicate, pttdupe.Candidate{
		ID:         "9",
		Name:       "Example Film 2019 MULTi 1080p BluRay Encode x264-GRP",
		Type:       "Encode",
		Resolution: "1080p",
		SizeBytes:  40,
		Relation:   "exact_duplicate",
		Reasons:    []string{"exact_identity"},
	}), "exact")
	called := false
	restore := blockNetwork(t)
	defer restore()
	_, err := runner.DupeAccept(DupeAcceptRequest{
		Release: zuleatest.Release,
		Reason:  acceptReason,
		Preview: &bytes.Buffer{},
		Confirm: confirmCalls(t, "no", &called),
	})
	if err == nil || called || !strings.Contains(err.Error(), "exact_duplicate") {
		t.Fatalf("called %v err %v", called, err)
	}
}

func TestDupeAcceptRefusesIncompleteOrEmptySearch(t *testing.T) {
	_, _, runner := uploadSetup(t)
	partial := dupeResult(pttdupe.StatusReview, grpCandidate("7", "Partial Encode-GRP", 40))
	partial.SearchComplete = false
	partial.Message = "wyszukiwanie niekompletne — wymagana ręczna kontrola"
	incompletePath := plantPlan(t, runner.Config.WorkDir, partial, "incomplete")
	called := false
	restore := blockNetwork(t)
	_, err := runner.DupeAccept(DupeAcceptRequest{
		Release: zuleatest.Release,
		Reason:  acceptReason,
		Preview: &bytes.Buffer{},
		Confirm: confirmCalls(t, "no", &called),
	})
	restore()
	if err == nil || called || !strings.Contains(err.Error(), "niekompletne") {
		t.Fatalf("incomplete called=%v err=%v", called, err)
	}

	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(incompletePath, past, past); err != nil {
		t.Fatal(err)
	}
	empty := dupeResult(pttdupe.StatusReview)
	empty.SearchComplete = true
	empty.Message = "kandydaci wymagają ręcznej oceny"
	plantPlan(t, runner.Config.WorkDir, empty, "empty")
	called = false
	restore = blockNetwork(t)
	_, err = runner.DupeAccept(DupeAcceptRequest{
		Release: zuleatest.Release,
		Reason:  acceptReason,
		Preview: &bytes.Buffer{},
		Confirm: confirmCalls(t, "no", &called),
	})
	restore()
	if err == nil || called || !strings.Contains(err.Error(), "pusta") {
		t.Fatalf("empty called=%v err=%v", called, err)
	}

	sha, err := dupeaccept.SetSHA(partial.Candidates)
	if err != nil {
		t.Fatal(err)
	}
	rec := dupeaccept.Record{Release: zuleatest.Release}
	rec.Append(dupeaccept.Decision{
		Action:       dupeaccept.ActionAccept,
		At:           "2026-11-05T00:00:00Z",
		Reason:       acceptReason,
		CandidateSHA: sha,
		Candidates:   partial.Candidates,
		Status:       pttdupe.StatusReview,
		Version:      Version,
	})
	if err := dupeaccept.Save(dupeaccept.Path(runner.Config.WorkDir, zuleatest.Release), rec); err != nil {
		t.Fatal(err)
	}
	gate := runner.canaryGate(zuleatest.Release, &pttplan.Plan{TaxonomyConfirmed: true}, partial, "k")
	if gate.Ready || !strings.Contains(strings.Join(gate.Blockers, "|"), "niekompletne") {
		t.Fatalf("incomplete gate %+v", gate)
	}

	emptySHA, err := dupeaccept.SetSHA(nil)
	if err != nil {
		t.Fatal(err)
	}
	emptyRec := dupeaccept.Record{Release: zuleatest.Release}
	emptyRec.Append(dupeaccept.Decision{
		Action:       dupeaccept.ActionAccept,
		At:           "2026-11-05T00:00:01Z",
		Reason:       acceptReason,
		CandidateSHA: emptySHA,
		Candidates:   nil,
		Status:       pttdupe.StatusReview,
		Version:      Version,
	})
	if err := dupeaccept.Save(dupeaccept.Path(runner.Config.WorkDir, zuleatest.Release), emptyRec); err != nil {
		t.Fatal(err)
	}
	emptyGate := runner.canaryGate(zuleatest.Release, &pttplan.Plan{TaxonomyConfirmed: true}, empty, "k")
	if emptyGate.Ready || !strings.Contains(strings.Join(emptyGate.Blockers, "|"), "pusta") {
		t.Fatalf("empty gate %+v", emptyGate)
	}
}

func TestDupeAcceptRefusesGroupIgnoringCase(t *testing.T) {
	for _, name := range []string{
		"Another.Film.2014.MULTi.1080p.REMUX.BluRay.AVC.DTS-HD.MA.5.1-zulea",
		"Another.Film.2014.MULTi.1080p.REMUX.BluRay.AVC.DTS-HD.MA.5.1-ZULEA",
	} {
		t.Run(name[len(name)-6:], func(t *testing.T) {
			_, _, runner := uploadSetup(t)
			found := dupeResult(pttdupe.StatusDuplicate, pttdupe.Candidate{
				ID:         "8",
				Name:       name,
				Type:       "Remux",
				Resolution: "1080p",
				SizeBytes:  40,
				Relation:   "same_slot",
				Reasons:    []string{"same_tracker_slot: same slot"},
			})
			plantPlan(t, runner.Config.WorkDir, found, "group")
			called := false
			restore := blockNetwork(t)
			defer restore()
			_, err := runner.DupeAccept(DupeAcceptRequest{
				Release: zuleatest.Release,
				Reason:  acceptReason,
				Preview: &bytes.Buffer{},
				Confirm: confirmCalls(t, "no", &called),
			})
			if err == nil || called || !strings.Contains(strings.ToLower(err.Error()), "-zulea") {
				t.Fatalf("called %v err %v", called, err)
			}
		})
	}
}

func TestDupeAcceptRefusesFailedAndNotRun(t *testing.T) {
	_, _, runner := uploadSetup(t)
	firstFailed := plantPlan(t, runner.Config.WorkDir, dupeResult(pttdupe.StatusFailed, grpCandidate("7", "Encode-GRP", 40)), "failed")
	called := false
	restore := blockNetwork(t)
	_, err := runner.DupeAccept(DupeAcceptRequest{
		Release: zuleatest.Release,
		Reason:  acceptReason,
		Preview: &bytes.Buffer{},
		Confirm: confirmCalls(t, "no", &called),
	})
	restore()
	if err == nil || called || !strings.Contains(err.Error(), "FAILED") {
		t.Fatalf("called %v err %v", called, err)
	}

	runner.Config.PTT.APIKeyFile = ""
	// A newer plan report must be the one dupe-accept reads.
	outcomes, err := runner.DryRun(context.Background(), []string{zuleatest.Release})
	if err != nil || outcomes[0].Dupe == nil || outcomes[0].Dupe.Status != pttdupe.StatusNotRun {
		t.Fatalf("missing key must be NOT_RUN: %+v %v", outcomes, err)
	}
	failedReport := plantPlan(t, runner.Config.WorkDir, dupeResult(pttdupe.StatusFailed), "older-failed")
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(failedReport, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(firstFailed, past, past); err != nil {
		t.Fatal(err)
	}
	called = false
	restore = blockNetwork(t)
	_, err = runner.DupeAccept(DupeAcceptRequest{
		Release: zuleatest.Release,
		Reason:  acceptReason,
		Preview: &bytes.Buffer{},
		Confirm: confirmCalls(t, "no", &called),
	})
	restore()
	if err == nil || called || !strings.Contains(err.Error(), "NOT_RUN") {
		t.Fatalf("called %v err %v", called, err)
	}

	cands := []pttdupe.Candidate{grpCandidate("7", "Encode-GRP", 40)}
	sha, err := dupeaccept.SetSHA(cands)
	if err != nil {
		t.Fatal(err)
	}
	rec := dupeaccept.Record{Release: zuleatest.Release}
	rec.Append(dupeaccept.Decision{
		Action:       dupeaccept.ActionAccept,
		At:           "2026-11-05T00:00:00Z",
		Reason:       acceptReason,
		CandidateSHA: sha,
		Candidates:   cands,
		Status:       pttdupe.StatusDuplicate,
		Version:      Version,
	})
	if err := dupeaccept.Save(dupeaccept.Path(runner.Config.WorkDir, zuleatest.Release), rec); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{pttdupe.StatusFailed, pttdupe.StatusNotRun} {
		gate := runner.canaryGate(zuleatest.Release, &pttplan.Plan{TaxonomyConfirmed: true}, dupeResult(status, cands...), "k")
		if gate.Ready || !strings.Contains(strings.Join(gate.Blockers, "|"), status) {
			t.Fatalf("%s gate %+v", status, gate)
		}
	}
}

func TestDupeAcceptUsesNewestReport(t *testing.T) {
	_, _, runner := uploadSetup(t)
	oldPath := filepath.Join(runner.Config.WorkDir, "reports", "scan-20261105-000000.json")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o700); err != nil {
		t.Fatal(err)
	}
	old := ScanSummary{Results: []ScanResult{{
		Release:    zuleatest.Release,
		Chain:      "OK",
		Dupe:       pttdupe.StatusDuplicate,
		Candidates: []pttdupe.Candidate{grpCandidate("1", "Old Encode-GRP", 10)},
	}}}
	oldData, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, append(oldData, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	newer := plantPlan(t, runner.Config.WorkDir, dupeResult(pttdupe.StatusDuplicate, grpCandidate("2", "New Encode-GRP", 20)), "newer")
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldPath, past, past); err != nil {
		t.Fatal(err)
	}
	sha, err := dupeaccept.SetSHA([]pttdupe.Candidate{grpCandidate("2", "New Encode-GRP", 20)})
	if err != nil {
		t.Fatal(err)
	}
	phrase, err := dupeaccept.AcceptPhrase(zuleatest.Release, sha)
	if err != nil {
		t.Fatal(err)
	}
	restore := blockNetwork(t)
	defer restore()
	rec, err := runner.DupeAccept(DupeAcceptRequest{
		Release: zuleatest.Release,
		Reason:  acceptReason,
		Preview: &bytes.Buffer{},
		Confirm: typed(phrase),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.CandidateSHA != sha || rec.SourceReport != newer || rec.Candidates[0].ID != "2" {
		t.Fatalf("bound the wrong report: %+v", rec)
	}
}

func TestDupeAcceptWrongPhraseWritesNothing(t *testing.T) {
	_, fake, runner := uploadSetup(t)
	fake.filterRows = sameSlotRow
	if _, err := runner.DryRun(context.Background(), []string{zuleatest.Release}); err != nil {
		t.Fatal(err)
	}
	restore := blockNetwork(t)
	defer restore()
	_, err := runner.DupeAccept(DupeAcceptRequest{
		Release: zuleatest.Release,
		Reason:  acceptReason,
		Preview: &bytes.Buffer{},
		Confirm: typed("AKCEPTUJE nie ten"),
	})
	if err == nil || !errors.Is(err, ErrUploadRefused) {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(dupeaccept.Path(runner.Config.WorkDir, zuleatest.Release)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("mismatched phrase must not create the file")
	}
	_, err = runner.DupeAccept(DupeAcceptRequest{
		Release: zuleatest.Release,
		Reason:  strings.Repeat("x", dupeaccept.MaxReasonBytes+1),
		Preview: &bytes.Buffer{},
		Confirm: typed("x"),
	})
	if err == nil {
		t.Fatal("overlong reason must be refused")
	}
}

func TestAcceptedDuplicateEntersQueueAndReceipt(t *testing.T) {
	_, fake, runner := uploadSetup(t)
	fake.filterRows = sameSlotRow
	summary, err := runner.ScanPTT(context.Background(), []string{zuleatest.Release}, 0)
	if err != nil || summary.Results[0].Dupe != pttdupe.StatusDuplicate {
		t.Fatalf("scan %+v %v", summary, err)
	}
	if queue, _, err := runner.Queue(); err != nil || len(queue) != 0 {
		t.Fatalf("duplicate must stay out of the queue: %+v %v", queue, err)
	}
	sha, err := dupeaccept.SetSHA(summary.Results[0].Candidates)
	if err != nil {
		t.Fatal(err)
	}
	phrase, err := dupeaccept.AcceptPhrase(zuleatest.Release, sha)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	runner.Logger = NewLogger(&logs, true)
	runner.Logger.AddSecret(pttKey)
	restore := blockNetwork(t)
	if _, err := runner.DupeAccept(DupeAcceptRequest{
		Release: zuleatest.Release,
		Reason:  acceptReason,
		Preview: &bytes.Buffer{},
		Confirm: typed(phrase),
	}); err != nil {
		restore()
		t.Fatal(err)
	}
	restore()
	queue, _, err := runner.Queue()
	if err != nil || len(queue) != 1 || queue[0].Release != zuleatest.Release {
		t.Fatalf("matching acceptance must enter the queue: %+v %v", queue, err)
	}
	fake.filterRows = strings.Replace(sameSlotRow, `"size":40`, `"size":99`, 1)
	if _, err := runner.ScanPTT(context.Background(), []string{zuleatest.Release}, 0); err != nil {
		t.Fatal(err)
	}
	if queue, _, err = runner.Queue(); err != nil || len(queue) != 0 {
		t.Fatalf("a scan with a different size must drop the release: %+v %v", queue, err)
	}
	fake.filterRows = sameSlotRow
	if _, err := runner.ScanPTT(context.Background(), []string{zuleatest.Release}, 0); err != nil {
		t.Fatal(err)
	}
	fp := dryRunFingerprint(t, runner)
	result, err := runner.Upload(context.Background(), UploadRequest{
		Release:     zuleatest.Release,
		Fingerprint: fp,
		Preview:     &bytes.Buffer{},
		Confirm:     typed(ConfirmationPhrase(fp)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.DupeAcceptSHA != sha || result.Receipt.DupeAcceptPath != dupeaccept.Path(runner.Config.WorkDir, zuleatest.Release) {
		t.Fatalf("receipt missing acceptance reference: %+v", result.Receipt)
	}
	if strings.Contains(logs.String(), pttKey) {
		t.Fatal("log leaked the key")
	}
}

func acceptFromSearch(t *testing.T, runner *Runner) (dupeaccept.Record, error) {
	t.Helper()
	outcomes, err := runner.DryRun(context.Background(), []string{zuleatest.Release})
	if err != nil || outcomes[0].Dupe == nil {
		t.Fatalf("dry-run: %+v %v", outcomes, err)
	}
	sha, err := dupeaccept.SetSHA(outcomes[0].Dupe.Candidates)
	if err != nil {
		t.Fatal(err)
	}
	phrase, err := dupeaccept.AcceptPhrase(zuleatest.Release, sha)
	if err != nil {
		t.Fatal(err)
	}
	restore := blockNetwork(t)
	defer restore()
	return runner.DupeAccept(DupeAcceptRequest{
		Release: zuleatest.Release,
		Reason:  acceptReason,
		Preview: &bytes.Buffer{},
		Confirm: typed(phrase),
	})
}
