// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/autobrr/upbrr/internal/zulea/dupeaccept"
	"github.com/autobrr/upbrr/internal/zulea/input"
	"github.com/autobrr/upbrr/internal/zulea/pttdupe"
)

// DupeAcceptance is a human acceptance that matches the candidate set in hand.
type DupeAcceptance struct {
	Path   string `json:"path"`
	SHA    string `json:"sha"`
	Reason string `json:"reason"`
	At     string `json:"at"`
}

// DupeAcceptRequest is one interactive dupe-accept or --revoke.
type DupeAcceptRequest struct {
	Release string
	Reason  string
	Revoke  bool
	Preview io.Writer
	Confirm func(prompt string) (string, error)
	// ConfirmationPath replaces the terminal prompt for the panel.
	ConfirmationPath string
	// Confirmation is stored on the decision. Empty channel means terminal.
	Confirmation ConfirmationStamp
}

type searchSnapshot struct {
	Status         string
	Message        string
	Candidates     []pttdupe.Candidate
	Path           string
	SearchComplete bool
}

type releaseFacts struct {
	Type       string
	Resolution string
	Size       int64
}

// DupeAccept records or withdraws a human duplicate acceptance. It reads the
// latest scan-ptt or plan report and never contacts a tracker.
func (r *Runner) DupeAccept(req DupeAcceptRequest) (dupeaccept.Record, error) {
	if req.Preview == nil || (req.Confirm == nil && req.ConfirmationPath == "") {
		return dupeaccept.Record{}, refuse("interactive confirmation is required")
	}
	if !validName(req.Release) {
		return dupeaccept.Record{}, refuse("invalid release name")
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" || len(reason) > dupeaccept.MaxReasonBytes {
		return dupeaccept.Record{}, refuse("--reason is required and must be at most %d bytes", dupeaccept.MaxReasonBytes)
	}
	if req.Revoke {
		return r.revokeAcceptance(req, reason)
	}
	return r.recordAcceptance(req, reason)
}

func (r *Runner) recordAcceptance(req DupeAcceptRequest, reason string) (dupeaccept.Record, error) {
	snap, err := r.latestDupeSnapshot(req.Release)
	if err != nil {
		return dupeaccept.Record{}, err
	}
	facts, err := r.releaseFacts(req.Release)
	if err != nil {
		return dupeaccept.Record{}, refuse("%v", err)
	}
	writeAcceptPreview(req.Preview, req.Release, facts, snap, reason)
	if err := dupeaccept.AcceptableStatus(snap.Status); err != nil {
		return dupeaccept.Record{}, refuse("%v", err)
	}
	if err := dupeaccept.SearchRefused(snap.SearchComplete, snap.Candidates); err != nil {
		return dupeaccept.Record{}, refuse("%v", err)
	}
	if err := dupeaccept.HardRefuse(snap.Candidates); err != nil {
		return dupeaccept.Record{}, refuse("%v", err)
	}
	sha, err := dupeaccept.SetSHA(snap.Candidates)
	if err != nil {
		return dupeaccept.Record{}, refuse("%v", err)
	}
	phrase, err := dupeaccept.AcceptPhrase(req.Release, sha)
	if err != nil {
		return dupeaccept.Record{}, refuse("%v", err)
	}
	_, _ = fmt.Fprintf(req.Preview, "Fraza: %s\n", phrase)
	path := dupeaccept.Path(r.Config.WorkDir, req.Release)
	before, missing, err := readAcceptFile(path)
	if err != nil {
		return dupeaccept.Record{}, err
	}
	typed, stamp, err := takePhrase(req.ConfirmationPath, ConfirmationExpect{
		Command: "dupe-accept",
		Release: req.Release,
		SHA:     sha,
		Phrase:  phrase,
	}, fmt.Sprintf("Zapisać akceptację duplikatu dla %s? Wpisz dokładnie: %s\n> ", req.Release, phrase), req.Confirm)
	if err != nil {
		return dupeaccept.Record{}, refuse("confirmation failed: %v", err)
	}
	req.Confirmation = stamp
	if strings.TrimSpace(typed) != phrase {
		return dupeaccept.Record{}, refuse("confirmation phrase did not match; nothing was saved")
	}
	if err := acceptFileUnchanged(path, before, missing); err != nil {
		return dupeaccept.Record{}, err
	}
	rec, err := nextAcceptRecord(path, req.Release, missing)
	if err != nil {
		return dupeaccept.Record{}, err
	}
	rec.Append(decisionWithConfirmation(dupeaccept.Decision{
		Action:       dupeaccept.ActionAccept,
		At:           time.Now().UTC().Format(time.RFC3339),
		Reason:       reason,
		CandidateSHA: sha,
		Candidates:   snap.Candidates,
		Status:       snap.Status,
		SourceReport: snap.Path,
		Version:      Version,
	}, req.Confirmation))
	if err := dupeaccept.Save(path, rec); err != nil {
		return dupeaccept.Record{}, fmt.Errorf("zulea: dupe-accept: %w", err)
	}
	r.logDupeDecision("dupe_accept", req.Release, sha)
	return rec, nil
}

func decisionWithConfirmation(d dupeaccept.Decision, stamp ConfirmationStamp) dupeaccept.Decision {
	if stamp.Channel == "" {
		stamp.Channel = ChannelTerminal
	}
	d.Channel = stamp.Channel
	d.Who = stamp.Who
	d.IP = stamp.IP
	d.Session = stamp.Session
	return d
}

func (r *Runner) revokeAcceptance(req DupeAcceptRequest, reason string) (dupeaccept.Record, error) {
	path := dupeaccept.Path(r.Config.WorkDir, req.Release)
	before, err := os.ReadFile(path)
	if err != nil {
		return dupeaccept.Record{}, refuse("no acceptance file for %s: %v", req.Release, err)
	}
	rec, err := dupeaccept.Load(path)
	if err != nil {
		return dupeaccept.Record{}, refuse("acceptance file is unreadable: %v", err)
	}
	if rec.Release != req.Release {
		return dupeaccept.Record{}, refuse("acceptance file is for a different release")
	}
	active := rec.Active()
	writeRevokePreview(req.Preview, path, rec, reason)
	if active == nil {
		return dupeaccept.Record{}, refuse("there is no active acceptance to revoke")
	}
	phrase, err := dupeaccept.RevokePhrase(req.Release, active.CandidateSHA)
	if err != nil {
		return dupeaccept.Record{}, refuse("%v", err)
	}
	_, _ = fmt.Fprintf(req.Preview, "Fraza: %s\n", phrase)
	typed, stamp, err := takePhrase(req.ConfirmationPath, ConfirmationExpect{
		Command: "dupe-accept",
		Release: req.Release,
		SHA:     active.CandidateSHA,
		Phrase:  phrase,
	}, fmt.Sprintf("Wycofać akceptację duplikatu dla %s? Wpisz dokładnie: %s\n> ", req.Release, phrase), req.Confirm)
	if err != nil {
		return dupeaccept.Record{}, refuse("confirmation failed: %v", err)
	}
	req.Confirmation = stamp
	if strings.TrimSpace(typed) != phrase {
		return dupeaccept.Record{}, refuse("confirmation phrase did not match; nothing was changed")
	}
	if err := acceptFileUnchanged(path, before, false); err != nil {
		return dupeaccept.Record{}, err
	}
	rec.Append(decisionWithConfirmation(dupeaccept.Decision{
		Action:       dupeaccept.ActionRevoke,
		At:           time.Now().UTC().Format(time.RFC3339),
		Reason:       reason,
		CandidateSHA: active.CandidateSHA,
		Candidates:   active.Candidates,
		Status:       active.Status,
		SourceReport: active.SourceReport,
		Version:      Version,
	}, req.Confirmation))
	if err := dupeaccept.Save(path, rec); err != nil {
		return dupeaccept.Record{}, fmt.Errorf("zulea: dupe-accept: %w", err)
	}
	r.logDupeDecision("dupe_revoke", req.Release, active.CandidateSHA)
	return rec, nil
}

func nextAcceptRecord(path, release string, missing bool) (dupeaccept.Record, error) {
	if missing {
		return dupeaccept.Record{Release: release}, nil
	}
	rec, err := dupeaccept.Load(path)
	if err != nil {
		return dupeaccept.Record{}, refuse("acceptance file is unreadable: %v", err)
	}
	if rec.Release != release {
		return dupeaccept.Record{}, refuse("acceptance file is for a different release")
	}
	return rec, nil
}

func (r *Runner) logDupeDecision(action, release, sha string) {
	if r.Logger == nil {
		return
	}
	r.Logger.Evidencef("zulea: tracker=PTT release=%s decision=%s sha=%s", release, action, sha)
}

func readAcceptFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, refuse("cannot read acceptance file: %v", err)
	}
	return data, false, nil
}

func acceptFileUnchanged(path string, before []byte, missing bool) error {
	again, err := os.ReadFile(path)
	if missing {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return refuse("the acceptance file changed during confirmation; nothing was saved")
	}
	if err != nil || string(again) != string(before) {
		return refuse("the acceptance file changed during confirmation; nothing was saved")
	}
	return nil
}

func (r *Runner) releaseFacts(release string) (releaseFacts, error) {
	adapter, err := r.openAdapter()
	if err != nil {
		return releaseFacts{}, err
	}
	prepared, err := adapter.Prepare(release, input.PTT)
	if err != nil {
		return releaseFacts{}, fmt.Errorf("chain: %w", err)
	}
	return releaseFacts{
		Type:       "REMUX",
		Resolution: prepared.Parsed.Res,
		Size:       prepared.MKV.Size,
	}, nil
}

func writeAcceptPreview(w io.Writer, release string, facts releaseFacts, snap searchSnapshot, reason string) {
	sha, err := dupeaccept.SetSHA(snap.Candidates)
	if err != nil {
		sha = ""
	}
	_, _ = fmt.Fprintf(w, "Akceptacja duplikatu (nic nie jest wysyłane, brak sieci).\nŹródło wyszukiwania: %s\n", snap.Path)
	_, _ = fmt.Fprintf(w, "Wydanie: %s\n  typ: %s\n  rozdzielczość: %s\n  rozmiar: %d B\n  skrót: %s\n",
		release, facts.Type, facts.Resolution, facts.Size, dupeaccept.ReleaseShort(release))
	_, _ = fmt.Fprintf(w, "Wynik wyszukiwania: %s — %s (kompletne: %t)\nKandydaci (%d):\n",
		snap.Status, snap.Message, snap.SearchComplete, len(snap.Candidates))
	for _, c := range snap.Candidates {
		_, _ = fmt.Fprintln(w, "  "+formatCandidate(c))
	}
	_, _ = fmt.Fprintf(w, "SHA zestawu kandydatów: %s\nPowód decyzji: %s\n", sha, reason)
}

func writeRevokePreview(w io.Writer, path string, rec dupeaccept.Record, reason string) {
	_, _ = fmt.Fprintf(w, "Wycofanie akceptacji (nic nie jest wysyłane, brak sieci).\nPlik: %s\nWydanie: %s\n", path, rec.Release)
	_, _ = fmt.Fprintf(w, "Ostatnia decyzja: %s %s\nPowód zapisany: %s\nSHA zestawu kandydatów: %s\nKandydaci (%d):\n",
		rec.Action, rec.At, rec.Reason, rec.CandidateSHA, len(rec.Candidates))
	for _, c := range rec.Candidates {
		_, _ = fmt.Fprintln(w, "  "+formatCandidate(c))
	}
	_, _ = fmt.Fprintf(w, "Powód wycofania: %s\n", reason)
}

func formatCandidate(c pttdupe.Candidate) string {
	reasons := strings.Join(c.Reasons, "; ")
	if reasons == "" {
		reasons = "-"
	}
	return fmt.Sprintf("id=%s nazwa=%s typ=%s rozdzielczość=%s rozmiar=%d B relacja=%s powód=%s",
		c.ID, c.Name, emptyField(c.Type), emptyField(c.Resolution), c.SizeBytes, emptyField(c.Relation), reasons)
}

func acceptedCandidateList(cands []pttdupe.Candidate) string {
	if len(cands) == 0 {
		return "(brak)"
	}
	parts := make([]string, len(cands))
	for i, c := range cands {
		id := c.ID
		if id == "" {
			id = "?"
		}
		parts[i] = id + " " + c.Name
	}
	return strings.Join(parts, "; ")
}

func emptyField(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}

// classifyDupe decides whether a fresh search is CLEAR, blocked, or covered
// by a human acceptance of this exact candidate set.
func (r *Runner) classifyDupe(release string, dupe *pttdupe.Result) (string, *DupeAcceptance, error) {
	if dupe == nil {
		return "duplikaty niesprawdzone", nil, nil
	}
	if dupe.Status == pttdupe.StatusClear {
		return "", nil, nil
	}
	if dupe.Status != pttdupe.StatusDuplicate && dupe.Status != pttdupe.StatusReview {
		return "duplikaty: " + dupe.Status + " — " + dupe.Message, nil, nil
	}
	if err := dupeaccept.SearchRefused(dupe.SearchComplete, dupe.Candidates); err != nil {
		return "duplikaty: " + dupe.Status + " — " + err.Error(), nil, nil
	}
	if err := dupeaccept.HardRefuse(dupe.Candidates); err != nil {
		return "duplikaty: " + dupe.Status + " — " + err.Error(), nil, nil
	}
	matched, stale, err := r.matchingAcceptance(release, dupe.Candidates)
	if err != nil {
		return "", nil, err
	}
	if matched == nil {
		msg := "duplikaty: " + dupe.Status + " — " + dupe.Message
		if stale {
			msg += " — zapisana akceptacja nie pasuje do tego zestawu kandydatów"
		}
		return msg, nil, nil
	}
	return "", matched, nil
}

func (r *Runner) matchingAcceptance(release string, candidates []pttdupe.Candidate) (*DupeAcceptance, bool, error) {
	if !validName(release) {
		return nil, false, nil
	}
	path := dupeaccept.Path(r.Config.WorkDir, release)
	rec, err := dupeaccept.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("dupe-accept: %w", err)
	}
	if rec.Release != release {
		return nil, false, errors.New("acceptance file is for a different release")
	}
	active := rec.Active()
	if active == nil {
		return nil, false, nil
	}
	sha, err := dupeaccept.SetSHA(candidates)
	if err != nil {
		return nil, false, fmt.Errorf("dupe-accept: %w", err)
	}
	if active.CandidateSHA != sha {
		return nil, true, nil
	}
	return &DupeAcceptance{
		Path:   path,
		SHA:    sha,
		Reason: active.Reason,
		At:     active.At,
	}, false, nil
}

func (r *Runner) latestDupeSnapshot(release string) (searchSnapshot, error) {
	dir := filepath.Join(r.Config.WorkDir, "reports")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return searchSnapshot{}, refuse("no scan-ptt or plan report yet: %v", err)
	}
	var (
		best  searchSnapshot
		when  time.Time
		found bool
	)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		path := filepath.Join(dir, name)
		info, err := entry.Info()
		if err != nil {
			return searchSnapshot{}, refuse("report %s: %v", name, err)
		}
		var (
			snap *searchSnapshot
			ok   bool
		)
		switch {
		case strings.HasPrefix(name, "scan-") && strings.HasSuffix(name, ".json"):
			snap, ok, err = snapshotFromScan(path, release)
		case strings.HasPrefix(name, release+".ptt.") && strings.HasSuffix(name, ".json"):
			snap, err = snapshotFromPlan(path, release)
			ok = err == nil
		default:
			continue
		}
		if err != nil {
			return searchSnapshot{}, refuse("report %s: %v", name, err)
		}
		if !ok {
			continue
		}
		if !found || info.ModTime().After(when) || (info.ModTime().Equal(when) && path > best.Path) {
			found = true
			when = info.ModTime()
			best = *snap
		}
	}
	if !found {
		return searchSnapshot{}, refuse("no scan-ptt or plan report for this release")
	}
	return best, nil
}

func snapshotFromPlan(path, release string) (*searchSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	var outcome Outcome
	if err := json.Unmarshal(data, &outcome); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if outcome.Release != "" && outcome.Release != release {
		return nil, errors.New("report release does not match")
	}
	snap := &searchSnapshot{Path: path}
	if outcome.Dupe != nil {
		snap.Status = outcome.Dupe.Status
		snap.Message = outcome.Dupe.Message
		snap.Candidates = outcome.Dupe.Candidates
		snap.SearchComplete = outcome.Dupe.SearchComplete
	}
	return snap, nil
}

func snapshotFromScan(path, release string) (*searchSnapshot, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("read: %w", err)
	}
	var summary ScanSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		return nil, false, fmt.Errorf("parse: %w", err)
	}
	for _, res := range summary.Results {
		if res.Release != release {
			continue
		}
		return &searchSnapshot{
			Status:         res.Dupe,
			Message:        res.Message,
			Candidates:     res.Candidates,
			Path:           path,
			SearchComplete: res.SearchComplete,
		}, true, nil
	}
	return nil, false, nil
}
