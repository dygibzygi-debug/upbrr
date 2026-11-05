// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/autobrr/upbrr/internal/zulea/freeze"
	"github.com/autobrr/upbrr/internal/zulea/input"
)

// CheckResult is the offline chain verdict for one release.
type CheckResult struct {
	Release  string `json:"release"`
	Status   string `json:"status"` // OK or STOP
	Reason   string `json:"reason,omitempty"`
	Title    string `json:"title,omitempty"`
	InfoHash string `json:"info_hash,omitempty"`
	RunID    string `json:"tm_run_id,omitempty"`
	Images   int    `json:"images,omitempty"`
}

// CheckSummary is the result of Check.
type CheckSummary struct {
	FreezeSHA256 string        `json:"freeze_sha256"`
	Results      []CheckResult `json:"results"`
	OK           int           `json:"ok"`
	Stop         int           `json:"stop"`
	// Skipped counts freeze releases without an OK record (e.g. WYJATEK), when checking all.
	Skipped    int    `json:"skipped"`
	ReportPath string `json:"report_path,omitempty"`
}

func (r *Runner) openAdapter() (*input.Adapter, error) {
	adapter, err := input.Open(input.Sources{
		LibraryRoot:  r.Config.LibraryRoot,
		FreezePath:   r.Config.FreezePath,
		FreezeSHA256: r.Config.FreezeSHA256,
		TitlesPath:   r.Config.TitlesPath,
		ManifestPath: r.Config.ManifestPath,
		TorrentDir:   r.Config.TMOutputDir,
		TMRunsDir:    r.Config.TMRunsDir,
		TMOutputBase: r.Config.TMOutputBase,
		Disk:         r.Config.DiskCheck(),
	})
	if err != nil {
		return nil, fmt.Errorf("zulea: open inputs: %w", err)
	}
	return adapter, nil
}

// Check verifies the whole chain of evidence (freeze, disk, directory, NFO,
// TM manifest and torrent, title) for releases, without any network access.
// With no releases it checks every freeze release that has an OK record.
func (r *Runner) Check(releases []string) (CheckSummary, error) {
	adapter, err := r.openAdapter()
	if err != nil {
		return CheckSummary{}, err
	}
	summary := CheckSummary{FreezeSHA256: adapter.Freeze().SHA256}
	if len(releases) == 0 {
		all, _ := adapter.Freeze().Releases()
		for _, rel := range all {
			if slices.Contains(adapter.Freeze().Status(rel), freeze.StatusOK) {
				releases = append(releases, rel)
			} else {
				summary.Skipped++
			}
		}
	}
	for _, rel := range releases {
		result := CheckResult{Release: rel, Status: "STOP"}
		prepared, perr := adapter.Prepare(rel, input.PTT)
		if perr != nil {
			result.Reason = perr.Error()
			summary.Stop++
		} else {
			result.Status = "OK"
			result.Title = prepared.Title
			result.InfoHash = prepared.Torrent.InfoHash
			result.RunID = prepared.TMRunID
			result.Images = len(prepared.ImageURLs)
			summary.OK++
		}
		summary.Results = append(summary.Results, result)
	}
	path, err := r.writeJSON("reports", "check-"+time.Now().UTC().Format("20060102-150405")+".json", summary)
	if err != nil {
		return summary, err
	}
	summary.ReportPath = path
	return summary, nil
}

// RenderCheck writes the check table.
func RenderCheck(w io.Writer, s CheckSummary) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "STATUS\tWYDANIE\tRUN TM\tSCREENY\tPOWÓD / TYTUŁ PTT")
	for _, r := range s.Results {
		detail := r.Title
		if r.Status != "OK" {
			detail = r.Reason
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", r.Status, r.Release, dash(r.RunID), r.Images, detail)
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintf(w, "\nPODSUMOWANIE: OK=%d STOP=%d pominięte (bez statusu OK w zamrożeniu)=%d; bez sieci.\n", s.OK, s.Stop, s.Skipped)
	if s.ReportPath != "" {
		_, _ = fmt.Fprintf(w, "raport: %s\n", s.ReportPath)
	}
}

func dash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

// IdentityReport compares the MKV as seen through library_root with the freeze (W-3 diagnostics).
type IdentityReport struct {
	Release      string `json:"release"`
	Path         string `json:"path"`
	FreezeIno    string `json:"freeze_ino"`
	FreezeSize   string `json:"freeze_size"`
	FreezeMtime  string `json:"freeze_mtime"`
	FreezeDisk   string `json:"freeze_disk"`
	ActualIno    string `json:"actual_ino,omitempty"`
	ActualSize   string `json:"actual_size,omitempty"`
	ActualMtime  string `json:"actual_mtime,omitempty"`
	StatError    string `json:"stat_error,omitempty"`
	IdentityOK   bool   `json:"identity_ok"`
	PathModeOK   bool   `json:"path_mode_ok"`
	PathModeWhy  string `json:"path_mode_reason,omitempty"`
	XattrModeOK  bool   `json:"xattr_mode_ok"`
	XattrModeWhy string `json:"xattr_mode_reason,omitempty"`
}

// VerifyIdentity reports how the MKV identity and both disk-check modes behave
// for release through the configured library path. Read only; no network.
func (r *Runner) VerifyIdentity(release string) (IdentityReport, error) {
	if !validName(release) {
		return IdentityReport{}, errors.New("invalid release name")
	}
	frozen, err := freeze.Load(r.Config.FreezePath, r.Config.FreezeSHA256)
	if err != nil {
		return IdentityReport{}, fmt.Errorf("zulea: freeze: %w", err)
	}
	rec, ok := frozen.Record(release)
	if !ok {
		return IdentityReport{}, fmt.Errorf("zulea: release needs exactly one freeze record (statuses %v)", frozen.Status(release))
	}
	path := filepath.Join(r.Config.LibraryRoot, release, release+".mkv")
	rep := IdentityReport{
		Release:     release,
		Path:        path,
		FreezeIno:   rec.MKVIno,
		FreezeSize:  rec.MKVSize,
		FreezeMtime: rec.MKVMtime,
		FreezeDisk:  rec.Disk,
	}
	if id, err := freeze.StatIdentity(path); err != nil {
		rep.StatError = err.Error()
	} else {
		rep.ActualIno = strconv.FormatUint(id.Ino, 10)
		rep.ActualSize = strconv.FormatInt(id.Size, 10)
		rep.ActualMtime = strconv.FormatInt(id.Mtime, 10)
		rep.IdentityOK = rep.ActualIno == rec.MKVIno && rep.ActualSize == rec.MKVSize && rep.ActualMtime == rec.MKVMtime
	}
	check := r.Config.DiskCheck()
	check.Mode = freeze.DiskModePath
	if err := check.Verify(rec, path); err != nil {
		rep.PathModeWhy = err.Error()
	} else {
		rep.PathModeOK = true
	}
	check.Mode = freeze.DiskModeUnraidXattr
	if err := check.Verify(rec, path); err != nil {
		rep.XattrModeWhy = err.Error()
	} else {
		rep.XattrModeOK = true
	}
	return rep, nil
}

// RenderIdentity writes the identity diagnostics.
func RenderIdentity(w io.Writer, rep IdentityReport) {
	yes := map[bool]string{true: "TAK", false: "NIE"}
	_, _ = fmt.Fprintf(w, "wydanie:   %s\nścieżka:   %s\n", rep.Release, rep.Path)
	_, _ = fmt.Fprintf(w, "zamrożenie ino/size/mtime: %s/%s/%s (dysk %s)\n", rep.FreezeIno, rep.FreezeSize, rep.FreezeMtime, rep.FreezeDisk)
	if rep.StatError != "" {
		_, _ = fmt.Fprintf(w, "stat:      BŁĄD %s\n", rep.StatError)
	} else {
		_, _ = fmt.Fprintf(w, "stat       ino/size/mtime: %s/%s/%s\n", rep.ActualIno, rep.ActualSize, rep.ActualMtime)
	}
	_, _ = fmt.Fprintf(w, "TOŻSAMOŚĆ_ZGODNA=%s\n", yes[rep.IdentityOK])
	_, _ = fmt.Fprintf(w, "TRYB_PATH=%s %s\n", yes[rep.PathModeOK], rep.PathModeWhy)
	_, _ = fmt.Fprintf(w, "TRYB_UNRAID_XATTR=%s %s\n", yes[rep.XattrModeOK], rep.XattrModeWhy)
}

// Receipts lists upload receipts, newest file name order.
func (r *Runner) Receipts() ([]Receipt, error) {
	dir := filepath.Join(r.Config.WorkDir, "receipts")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("receipts: %w", err)
	}
	var out []Receipt
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".ptt.json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("receipts: %w", err)
		}
		var receipt Receipt
		if err := json.Unmarshal(data, &receipt); err != nil {
			receipt = Receipt{Release: strings.TrimSuffix(entry.Name(), ".ptt.json"), State: "unreadable"}
		}
		out = append(out, receipt)
	}
	return out, nil
}

// RenderReceipts writes the receipts table.
func RenderReceipts(w io.Writer, receipts []Receipt) {
	if len(receipts) == 0 {
		_, _ = fmt.Fprintln(w, "brak pokwitowań")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "STAN\tWYDANIE\tPTT ID\tDANE_ZGODNE\tKONIEC\tUWAGI")
	for _, rc := range receipts {
		match := "-"
		if rc.DataMatch != nil {
			match = map[bool]string{true: "TAK", false: "NIE"}[*rc.DataMatch]
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", rc.State, rc.Release, dash(rc.TorrentID), match, dash(rc.FinishedAt), rc.Message)
	}
	_ = tw.Flush()
}

func (r *Runner) writeJSON(sub, name string, v any) (string, error) {
	dir := filepath.Join(r.Config.WorkDir, sub)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("report: %w", err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("report: %w", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("report: %w", err)
	}
	return path, nil
}
