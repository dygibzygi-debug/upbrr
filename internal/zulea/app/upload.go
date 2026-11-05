// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/autobrr/upbrr/internal/trackers/submission"
	"github.com/autobrr/upbrr/internal/zulea/pttplan"
	"github.com/autobrr/upbrr/internal/zulea/tm"
)

// ErrUploadRefused marks an upload that did not start. Nothing was sent.
var ErrUploadRefused = errors.New("zulea: upload refused")

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUploadRefused, fmt.Sprintf(format, args...))
}

var fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Receipt states.
const (
	ReceiptAttempted = "attempted"
	ReceiptAccepted  = "accepted"
	ReceiptRejected  = "rejected"
	ReceiptNotSent   = "not_sent"
	ReceiptUncertain = "uncertain"
)

// Receipt records one upload attempt. It is written before submission, so an
// interrupted or uncertain attempt can never be retried automatically.
type Receipt struct {
	Release     string `json:"release"`
	Tracker     string `json:"tracker"`
	Fingerprint string `json:"fingerprint"`
	// InfoHash is the TM torrent v1 infohash. PTT rewrites the info dictionary
	// (source and entropy), so the copy that qBittorrent actually seeds has a
	// different infohash. Seed matches that registered file, never this field.
	InfoHash string `json:"tm_info_hash"`
	// SeedSource is "manual" when qBittorrent already had the registered
	// torrent (path and category left unchanged) or "cli" when seed added it.
	SeedSource string `json:"seed_source,omitempty"`
	State      string `json:"state"`
	Message    string `json:"message,omitempty"`
	TorrentID  string `json:"torrent_id,omitempty"`
	TorrentURL string `json:"torrent_url,omitempty"`
	// RegisteredTorrent is the torrent downloaded from the tracker (0600, work_dir).
	RegisteredTorrent string `json:"registered_torrent,omitempty"`
	DataMatch         *bool  `json:"data_match,omitempty"`
	StartedAt         string `json:"started_at"`
	FinishedAt        string `json:"finished_at,omitempty"`
	// Moderation is the last ptt-status verdict (APPROVED, PENDING, NOT_VISIBLE).
	Moderation          string `json:"moderation,omitempty"`
	ModerationCheckedAt string `json:"moderation_checked_at,omitempty"`
	// Resolutions is the history of human decisions made with resolve.
	Resolutions []Resolution `json:"resolutions,omitempty"`
	// DupeAcceptPath and DupeAcceptSHA point at the human acceptance that
	// opened the gate for this upload. They are empty when the search was CLEAR.
	DupeAcceptPath string `json:"dupe_accept_path,omitempty"`
	DupeAcceptSHA  string `json:"dupe_accept_sha,omitempty"`
}

// UploadRequest is one human-initiated canary upload.
type UploadRequest struct {
	Release string
	// Fingerprint is the full fingerprint of the dry-run the human reviewed.
	Fingerprint string
	// Preview receives the full fresh preview before confirmation.
	Preview io.Writer
	// Confirm shows prompt and returns what the human typed.
	Confirm func(prompt string) (string, error)
}

// UploadResult is the outcome of a submitted upload.
type UploadResult struct {
	Receipt     Receipt
	ReceiptPath string
	Comparison  *tm.Comparison
}

// ConfirmationPhrase is what the human must type to send the plan.
func ConfirmationPhrase(fingerprint string) string {
	return "WYSYLAM " + fingerprint[:12]
}

func (r *Runner) receiptPath(release string) string {
	return filepath.Join(r.Config.WorkDir, "receipts", release+".ptt.json")
}

// Upload sends one release to PTT after every gate holds:
//  1. no earlier receipt for the release (except a proven not-sent attempt);
//  2. a fresh dry-run is READY with an open canary gate;
//  3. its fingerprint equals the fingerprint the human reviewed;
//  4. the human types the confirmation phrase;
//  5. the upload-intent plan reproduces the same fingerprint.
//
// After acceptance it compares the torrent downloaded from PTT with the TM
// torrent. It never adds anything to a torrent client.
func (r *Runner) Upload(ctx context.Context, req UploadRequest) (UploadResult, error) {
	fingerprint := strings.TrimSpace(req.Fingerprint)
	if !fingerprintPattern.MatchString(fingerprint) {
		return UploadResult{}, refuse("a full 64-character dry-run fingerprint is required")
	}
	if req.Confirm == nil || req.Preview == nil {
		return UploadResult{}, refuse("interactive confirmation is required")
	}
	if err := r.checkNoReceipt(req.Release); err != nil {
		return UploadResult{}, err
	}
	sess, err := r.open([]string{req.Release})
	if err != nil {
		return UploadResult{}, refuse("%v", err)
	}
	if sess.apiKey == "" {
		return UploadResult{}, refuse("PTT API key is not configured")
	}
	outcome, prepared := r.evaluate(ctx, sess, req.Release)
	if path, werr := r.writeReport(outcome); werr == nil {
		outcome.ReportPath = path
	}
	Render(req.Preview, []Outcome{outcome}, sess.logger.MaskKnown)
	if outcome.Status != StatusReady || outcome.Plan == nil {
		return UploadResult{}, refuse("dry-run is not READY: %s", outcome.Reason)
	}
	if !outcome.Canary.Ready {
		return UploadResult{}, refuse("canary gate is closed: %s", strings.Join(outcome.Canary.Blockers, "; "))
	}
	if outcome.Plan.Fingerprint != fingerprint {
		return UploadResult{}, refuse(
			"the preview changed since review (fingerprint %s, reviewed %s); review the new dry-run",
			outcome.Plan.Fingerprint,
			fingerprint,
		)
	}
	phrase := ConfirmationPhrase(fingerprint)
	typed, err := req.Confirm(fmt.Sprintf("Wysłać %s na PTT? Wpisz dokładnie: %s\n> ", req.Release, phrase))
	if err != nil {
		return UploadResult{}, refuse("confirmation failed: %v", err)
	}
	if strings.TrimSpace(typed) != phrase {
		return UploadResult{}, refuse("confirmation phrase did not match; nothing was sent")
	}
	opts := r.planOptions(sess)
	if outcome.Acceptance != nil {
		opts.AcceptanceSHA = outcome.Acceptance.SHA
	}
	plan, upload, err := pttplan.PrepareUpload(ctx, prepared, opts, fingerprint)
	if err != nil {
		return UploadResult{}, refuse("%v", err)
	}
	defer upload.Release()

	receipt := Receipt{
		Release:     req.Release,
		Tracker:     outcome.Tracker,
		Fingerprint: plan.Fingerprint,
		InfoHash:    plan.TMInfoHash,
		State:       ReceiptAttempted,
		StartedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	if outcome.Acceptance != nil {
		receipt.DupeAcceptPath = outcome.Acceptance.Path
		receipt.DupeAcceptSHA = outcome.Acceptance.SHA
	}
	path := r.receiptPath(req.Release)
	if err := writeReceipt(path, receipt, true); err != nil {
		return UploadResult{}, refuse("cannot record the attempt: %v", err)
	}
	sess.logger.Evidencef("zulea: tracker=PTT release=%s decision=submit fingerprint=%s", req.Release, plan.Fingerprint)
	summary, submitErr := upload.Submit(ctx)
	receipt.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	result := UploadResult{ReceiptPath: path}
	switch submission.OutcomeOf(submitErr) {
	case submission.Accepted:
		receipt.State = ReceiptAccepted
	case submission.Rejected:
		receipt.State = ReceiptRejected
		receipt.Message = sess.logger.Sanitize(submitErr.Error())
	case submission.NotSent:
		receipt.State = ReceiptNotSent
		receipt.Message = sess.logger.Sanitize(submitErr.Error())
	case submission.Unknown:
		receipt.State = ReceiptUncertain
		receipt.Message = "outcome unknown — check PTT before any retry: " + sess.logger.Sanitize(errText(submitErr))
	default:
		receipt.State = ReceiptUncertain
		receipt.Message = "outcome unknown — check PTT before any retry: " + sess.logger.Sanitize(errText(submitErr))
	}
	if receipt.State == ReceiptAccepted && len(summary.UploadedTorrents) > 0 {
		uploaded := summary.UploadedTorrents[0]
		receipt.TorrentID = uploaded.TorrentID
		receipt.TorrentURL = uploaded.TorrentURL
		receipt.RegisteredTorrent = uploaded.TorrentPath
		if uploaded.TorrentPath != "" {
			if comparison, cerr := compareRegistered(prepared.TorrentPath, uploaded.TorrentPath); cerr == nil {
				result.Comparison = &comparison
				ok := comparison.OK()
				receipt.DataMatch = &ok
			} else {
				receipt.Message = "registered torrent comparison failed: " + cerr.Error()
			}
		} else {
			receipt.Message = "registered torrent unavailable — download it from PTT and run compare-torrent"
		}
	}
	result.Receipt = receipt
	if err := writeReceipt(path, receipt, false); err != nil {
		return result, fmt.Errorf("zulea: upload state %s but receipt update failed: %w", receipt.State, err)
	}
	sess.logger.Evidencef("zulea: tracker=PTT release=%s decision=%s torrent_id=%s", req.Release, receipt.State, receipt.TorrentID)
	if receipt.State != ReceiptAccepted {
		return result, fmt.Errorf("zulea: upload %s: %s", receipt.State, receipt.Message)
	}
	return result, nil
}

func errText(err error) string {
	if err == nil {
		return "none"
	}
	return err.Error()
}

// checkNoReceipt refuses a release that already has an attempt on record. Only
// a proven not-sent attempt may be retried.
func (r *Runner) checkNoReceipt(release string) error {
	if !validName(release) {
		return refuse("invalid release name")
	}
	data, err := os.ReadFile(r.receiptPath(release))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return refuse("cannot read receipt: %v", err)
	}
	var receipt Receipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return refuse("receipt is unreadable; check PTT, then: zulea-upbrr resolve")
	}
	if receipt.State == ReceiptNotSent {
		return nil
	}
	return refuse("release already has a %s upload receipt (%s); a repeat upload is never automatic", receipt.State, r.receiptPath(release))
}

func validName(rel string) bool {
	return rel != "" && rel != "." && rel != ".." && !strings.ContainsAny(rel, `/\`+"\x00")
}

// writeReceipt replaces the receipt atomically: a synced temporary file in the
// same directory is renamed over the old one, so a crash leaves either the old
// or the new receipt, never a torn one. With exclusive, only a missing or
// not_sent receipt may be replaced; the not_sent one is kept as evidence.
func writeReceipt(path string, receipt Receipt, exclusive bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("receipt directory: %w", err)
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return fmt.Errorf("receipt encode: %w", err)
	}
	if exclusive {
		if existing, rerr := os.ReadFile(path); rerr == nil {
			var old Receipt
			if json.Unmarshal(existing, &old) != nil || old.State != ReceiptNotSent {
				return errors.New("receipt already exists")
			}
			superseded := path + ".superseded-" + time.Now().UTC().Format("20060102T150405Z")
			//nolint:gosec // Receipt path is work_dir/receipts/<validated release name>.
			if err := os.WriteFile(superseded, existing, 0o600); err != nil {
				return fmt.Errorf("receipt keep not_sent: %w", err)
			}
		} else if !errors.Is(rerr, fs.ErrNotExist) {
			return fmt.Errorf("receipt read: %w", rerr)
		}
	}
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("receipt temp: %w", err)
	}
	tmp := file.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("receipt chmod: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return fmt.Errorf("receipt write: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("receipt sync: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("receipt close: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("receipt rename: %w", err)
	}
	syncDir(dir)
	return nil
}

// syncDir makes a rename durable where the platform supports directory sync.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

func compareRegistered(tmPath, registeredPath string) (tm.Comparison, error) {
	original, err := tm.ReadFile(tmPath)
	if err != nil {
		return tm.Comparison{}, fmt.Errorf("read TM torrent: %w", err)
	}
	downloaded, err := tm.ReadFile(registeredPath)
	if err != nil {
		return tm.Comparison{}, fmt.Errorf("read registered torrent: %w", err)
	}
	comparison, err := tm.Compare(original, downloaded)
	if err != nil {
		return tm.Comparison{}, fmt.Errorf("compare torrents: %w", err)
	}
	return comparison, nil
}
