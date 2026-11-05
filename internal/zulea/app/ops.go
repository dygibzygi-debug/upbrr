// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/autobrr/upbrr/internal/zulea/freeze"
	"github.com/autobrr/upbrr/internal/zulea/input"
	"github.com/autobrr/upbrr/internal/zulea/pttdupe"
	"github.com/autobrr/upbrr/internal/zulea/pttfetch"
	"github.com/autobrr/upbrr/internal/zulea/seed"
	"github.com/autobrr/upbrr/internal/zulea/tm"
)

// ---------------------------------------------------------------- scan-ptt

// ScanResult is the PTT presence verdict for one release.
type ScanResult struct {
	Release     string              `json:"release"`
	Chain       string              `json:"chain"` // OK or STOP
	ChainReason string              `json:"chain_reason,omitempty"`
	Title       string              `json:"title,omitempty"`
	Dupe        string              `json:"dupe,omitempty"` // CLEAR, DUPLICATE, REVIEW, NOT_RUN, FAILED
	Message     string              `json:"message,omitempty"`
	Candidates  []pttdupe.Candidate `json:"candidates,omitempty"`
}

// ScanSummary is the result of ScanPTT.
type ScanSummary struct {
	FreezeSHA256 string         `json:"freeze_sha256"`
	StartedAt    string         `json:"started_at"`
	Results      []ScanResult   `json:"results"`
	Counts       map[string]int `json:"counts"`
	ReportPath   string         `json:"report_path,omitempty"`
}

// ScanPTT checks which releases are already on PTT: the offline chain, one
// TMDB read by id and the read-only upbrr duplicate search per release, with
// delay between releases. Without releases it scans every OK freeze release.
func (r *Runner) ScanPTT(ctx context.Context, releases []string, delay time.Duration) (ScanSummary, error) {
	if len(releases) == 0 {
		all, err := r.okReleases()
		if err != nil {
			return ScanSummary{}, err
		}
		releases = all
	}
	sess, err := r.open(releases)
	if err != nil {
		return ScanSummary{}, err
	}
	if sess.apiKey == "" {
		return ScanSummary{}, errors.New("scan-ptt needs the PTT API key (ptt.api_key_file)")
	}
	summary := ScanSummary{
		FreezeSHA256: sess.adapter.Freeze().SHA256,
		StartedAt:    time.Now().UTC().Format(time.RFC3339),
		Counts:       map[string]int{},
	}
	for i, rel := range releases {
		if i > 0 && delay > 0 {
			select {
			case <-ctx.Done():
				return summary, fmt.Errorf("scan canceled: %w", ctx.Err())
			case <-time.After(delay):
			}
		}
		result := ScanResult{Release: rel, Chain: "STOP"}
		prepared, perr := sess.adapter.Prepare(rel, input.PTT)
		if perr != nil {
			result.ChainReason = perr.Error()
			summary.Counts["CHAIN_STOP"]++
			summary.Results = append(summary.Results, result)
			continue
		}
		result.Chain, result.Title = "OK", prepared.Title
		movie, merr := sess.movies.Movie(ctx, prepared.Record.TMDBID)
		if merr != nil {
			result.Dupe, result.Message = pttdupe.StatusFailed, "TMDB: "+merr.Error()
		} else if dupe, derr := pttdupe.Check(ctx, prepared, movie, pttdupe.Options{
			APIKey:  sess.apiKey,
			WorkDir: r.Config.WorkDir,
			Logger:  sess.logger,
		}); derr != nil {
			result.Dupe, result.Message = pttdupe.StatusFailed, derr.Error()
		} else {
			result.Dupe, result.Message, result.Candidates = dupe.Status, dupe.Message, dupe.Candidates
		}
		summary.Counts[result.Dupe]++
		summary.Results = append(summary.Results, result)
		sess.logger.Evidencef("zulea: scan release=%s chain=%s dupe=%s", rel, result.Chain, result.Dupe)
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	path, err := r.writeJSON("reports", "scan-"+stamp+".json", summary)
	if err != nil {
		return summary, err
	}
	summary.ReportPath = path
	if err := r.writeText("reports", "scan-"+stamp+".md", ScanMarkdown(summary)); err != nil {
		return summary, err
	}
	return summary, nil
}

func (r *Runner) okReleases() ([]string, error) {
	frozen, err := freeze.Load(r.Config.FreezePath, r.Config.FreezeSHA256)
	if err != nil {
		return nil, fmt.Errorf("zulea: freeze: %w", err)
	}
	all, _ := frozen.Releases()
	var out []string
	for _, rel := range all {
		if slices.Contains(frozen.Status(rel), freeze.StatusOK) {
			out = append(out, rel)
		}
	}
	return out, nil
}

// RenderScan writes the scan table.
func RenderScan(w io.Writer, s ScanSummary) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ŁAŃCUCH\tPTT\tWYDANIE\tSZCZEGÓŁY")
	for _, r := range s.Results {
		var detail strings.Builder
		if r.Chain != "OK" {
			detail.WriteString(r.ChainReason)
		} else {
			detail.WriteString(r.Message)
		}
		for _, c := range r.Candidates {
			_, _ = fmt.Fprintf(&detail, " | [%s] %s", c.Relation, c.Name)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Chain, dash(r.Dupe), r.Release, detail.String())
	}
	_ = tw.Flush()
	keys := make([]string, 0, len(s.Counts))
	for k := range s.Counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, s.Counts[k]))
	}
	_, _ = fmt.Fprintf(w, "\nPODSUMOWANIE: %s (tylko odczyt; nic nie wysłano)\n", strings.Join(parts, " "))
	if s.ReportPath != "" {
		_, _ = fmt.Fprintf(w, "raport: %s (+ .md)\n", s.ReportPath)
	}
}

// ---------------------------------------------------------------- queue

// QueueEntry is a release ready for the next human-confirmed upload.
type QueueEntry struct {
	Release string `json:"release"`
	Title   string `json:"title"`
}

// Queue lists, in scan order, releases whose latest scan says chain OK and
// PTT CLEAR and which have no upload receipt. It uploads nothing.
func (r *Runner) Queue() ([]QueueEntry, string, error) {
	scanPath, err := latestReport(filepath.Join(r.Config.WorkDir, "reports"), "scan-", ".json")
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(scanPath)
	if err != nil {
		return nil, "", fmt.Errorf("queue: %w", err)
	}
	var scan ScanSummary
	if err := json.Unmarshal(data, &scan); err != nil {
		return nil, "", fmt.Errorf("queue: scan report unreadable: %w", err)
	}
	receipts, err := r.Receipts()
	if err != nil {
		return nil, "", err
	}
	done := map[string]bool{}
	for _, rc := range receipts {
		if rc.State != ReceiptNotSent {
			done[rc.Release] = true
		}
	}
	var out []QueueEntry
	for _, res := range scan.Results {
		if res.Chain == "OK" && res.Dupe == pttdupe.StatusClear && !done[res.Release] {
			out = append(out, QueueEntry{Release: res.Release, Title: res.Title})
		}
	}
	return out, scanPath, nil
}

func latestReport(dir, prefix, suffix string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("queue: no reports yet (run scan-ptt first): %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(e.Name(), suffix) {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", errors.New("queue: no scan report yet (run scan-ptt first)")
	}
	sort.Strings(names)
	return filepath.Join(dir, names[len(names)-1]), nil
}

// RenderQueue writes the queue with the per-release next steps.
func RenderQueue(w io.Writer, entries []QueueEntry, scanPath string, limit int) {
	_, _ = fmt.Fprintf(w, "na podstawie: %s\n", scanPath)
	if len(entries) == 0 {
		_, _ = fmt.Fprintln(w, "kolejka pusta")
		return
	}
	for i, e := range entries {
		if limit > 0 && i >= limit {
			_, _ = fmt.Fprintf(w, "… i %d kolejnych\n", len(entries)-limit)
			break
		}
		_, _ = fmt.Fprintf(w, "%3d. %s\n     %s\n", i+1, e.Release, e.Title)
	}
	_, _ = fmt.Fprintln(w, "\nKażde wydanie osobno: zulea-upbrr plan --release <REL> → przegląd → zulea-upbrr upload --release <REL> --fingerprint <FP>.")
}

// ---------------------------------------------------------------- verify-uploaded

// VerifyUploadedResult is the comparison for an already uploaded release.
type VerifyUploadedResult struct {
	Release    string         `json:"release"`
	PTTID      string         `json:"ptt_id"`
	Path       string         `json:"downloaded_torrent"`
	Comparison *tm.Comparison `json:"-"`
	DataMatch  bool           `json:"data_match"`
	NamesMatch bool           `json:"names_match"`
	PrivateOK  bool           `json:"private_ok"`
}

// VerifyUploaded downloads the PTT torrent of a release uploaded earlier (by
// hand) and compares it with the TM torrent. Without pttID it searches PTT and
// accepts exactly one candidate whose name equals the names.py title.
func (r *Runner) VerifyUploaded(ctx context.Context, release, pttID string) (VerifyUploadedResult, error) {
	sess, err := r.open([]string{release})
	if err != nil {
		return VerifyUploadedResult{}, err
	}
	if sess.apiKey == "" {
		return VerifyUploadedResult{}, errors.New("verify-uploaded needs the PTT API key")
	}
	prepared, err := sess.adapter.Prepare(release, input.PTT)
	if err != nil {
		return VerifyUploadedResult{}, fmt.Errorf("chain: %w", err)
	}
	if pttID == "" {
		movie, err := sess.movies.Movie(ctx, prepared.Record.TMDBID)
		if err != nil {
			return VerifyUploadedResult{}, fmt.Errorf("tmdb: %w", err)
		}
		dupe, err := pttdupe.Check(ctx, prepared, movie, pttdupe.Options{
			APIKey:  sess.apiKey,
			WorkDir: r.Config.WorkDir,
			Logger:  sess.logger,
		})
		if err != nil {
			return VerifyUploadedResult{}, fmt.Errorf("ptt search: %w", err)
		}
		var ids []string
		for _, c := range dupe.Candidates {
			if c.Name == prepared.Title && c.ID != "" {
				ids = append(ids, c.ID)
			}
		}
		if len(ids) != 1 {
			return VerifyUploadedResult{}, fmt.Errorf("found %d PTT torrents named exactly %q; pass --ptt-id", len(ids), prepared.Title)
		}
		pttID = ids[0]
	}
	out := filepath.Join(r.Config.WorkDir, "registered", release+".ptt."+pttID+".torrent")
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return VerifyUploadedResult{}, fmt.Errorf("registered dir: %w", err)
	}
	if err := pttfetch.Download(ctx, sess.apiKey, pttID, out); err != nil {
		return VerifyUploadedResult{}, fmt.Errorf("verify-uploaded: %w", err)
	}
	comparison, err := compareRegistered(prepared.TorrentPath, out)
	if err != nil {
		return VerifyUploadedResult{}, err
	}
	return VerifyUploadedResult{
		Release:    release,
		PTTID:      pttID,
		Path:       out,
		Comparison: &comparison,
		DataMatch:  comparison.DataMatch,
		NamesMatch: comparison.NamesMatch,
		PrivateOK:  comparison.PrivateOK,
	}, nil
}

// ---------------------------------------------------------------- seed

// SeedRequest is one seeding step for an accepted upload.
type SeedRequest struct {
	Release string
	Wait    time.Duration
	Poll    time.Duration
	// Start starts seeding only at 100% after the recheck.
	Start bool
}

// Seed adds the registered torrent of an accepted upload to qBittorrent,
// stopped, with a full recheck (see package seed). It requires an accepted
// receipt with DANE_ZGODNE=TAK and matching names.
func (r *Runner) Seed(ctx context.Context, req SeedRequest, client seed.Client) (seed.Result, error) {
	q := r.Config.QBittorrent
	if q == nil {
		return seed.Result{}, errors.New("seed: qbittorrent is not configured")
	}
	receipt, err := r.readReceipt(req.Release)
	if err != nil {
		return seed.Result{}, err
	}
	if receipt.State != ReceiptAccepted || receipt.DataMatch == nil || !*receipt.DataMatch || receipt.RegisteredTorrent == "" {
		return seed.Result{}, fmt.Errorf("seed: receipt must be accepted with DANE_ZGODNE=TAK (state %s)", receipt.State)
	}
	adapter, err := r.openAdapter()
	if err != nil {
		return seed.Result{}, err
	}
	prepared, err := adapter.Prepare(req.Release, input.PTT)
	if err != nil {
		return seed.Result{}, fmt.Errorf("seed: chain: %w", err)
	}
	comparison, err := compareRegistered(prepared.TorrentPath, receipt.RegisteredTorrent)
	if err != nil {
		return seed.Result{}, err
	}
	if !comparison.OK() || !comparison.NamesMatch {
		return seed.Result{}, errors.New("seed: registered torrent must match the TM data and names (compare-torrent)")
	}
	if client == nil {
		password := ""
		if q.PasswordFile != "" {
			if password, err = readKeyFile(q.PasswordFile); err != nil {
				return seed.Result{}, fmt.Errorf("seed: qbittorrent password: %w", err)
			}
		}
		client = seed.NewQBit(q.Host, q.Username, password)
	}
	res, err := seed.Run(ctx, client, seed.Request{
		TorrentPath: receipt.RegisteredTorrent,
		InfoHash:    comparison.Downloaded.InfoHash,
		Name:        req.Release,
		SavePath:    q.SavePath,
		Category:    q.Category,
		Tags:        q.Tags,
		Wait:        req.Wait,
		Poll:        req.Poll,
		Start:       req.Start,
	})
	if err != nil {
		return res, fmt.Errorf("zulea: %w", err)
	}
	return res, nil
}

func (r *Runner) readReceipt(release string) (Receipt, error) {
	if !validName(release) {
		return Receipt{}, errors.New("invalid release name")
	}
	data, err := os.ReadFile(r.receiptPath(release))
	if err != nil {
		return Receipt{}, fmt.Errorf("no upload receipt for release: %w", err)
	}
	var receipt Receipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return Receipt{}, fmt.Errorf("receipt unreadable: %w", err)
	}
	return receipt, nil
}

func (r *Runner) writeText(sub, name, text string) error {
	dir := filepath.Join(r.Config.WorkDir, sub)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("report: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
		return fmt.Errorf("report: %w", err)
	}
	return nil
}

// StartSeedVerified starts the release's torrent in qBittorrent only when it
// is fully checked (100%).
func (r *Runner) StartSeedVerified(ctx context.Context, release string) (seed.Result, error) {
	q := r.Config.QBittorrent
	if q == nil {
		return seed.Result{}, errors.New("seed: qbittorrent is not configured")
	}
	receipt, err := r.readReceipt(release)
	if err != nil {
		return seed.Result{}, err
	}
	data, err := tm.ReadFile(receipt.RegisteredTorrent)
	if err != nil {
		return seed.Result{}, fmt.Errorf("seed: registered torrent: %w", err)
	}
	hash, err := tm.InfoHashOf(data)
	if err != nil {
		hashDesc, derr := tm.Describe(data)
		if derr != nil {
			return seed.Result{}, fmt.Errorf("seed: registered torrent: %w", err)
		}
		hash = hashDesc.InfoHash
	}
	password := ""
	if q.PasswordFile != "" {
		if password, err = readKeyFile(q.PasswordFile); err != nil {
			return seed.Result{}, fmt.Errorf("seed: qbittorrent password: %w", err)
		}
	}
	res, err := seed.StartVerified(ctx, seed.NewQBit(q.Host, q.Username, password), hash)
	if err != nil {
		return res, fmt.Errorf("zulea: %w", err)
	}
	return res, nil
}
