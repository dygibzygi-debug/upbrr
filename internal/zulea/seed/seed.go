// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package seed adds the torrent registered on PTT to qBittorrent the way the
// manual process does: the downloaded (not the TM) torrent, added stopped,
// with a full recheck, and started only when the recheck reaches 100% and
// the operator asked for it. It never deletes, moves or edits a torrent.
package seed

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	qbittorrent "github.com/autobrr/go-qbittorrent"
)

// How a torrent came to be in qBittorrent.
const (
	// SeedSourceManual: the torrent was already there. Matched by the infohash
	// of the torrent downloaded from the tracker (not the TM infohash). Its
	// save path and category are left exactly as qBittorrent has them.
	SeedSourceManual = "manual"
	// SeedSourceCLI: this command added the torrent.
	SeedSourceCLI = "cli"
)

// Torrent is the subset of qBittorrent state the step needs.
type Torrent struct {
	Hash     string
	Name     string
	Progress float64
	State    string
	SavePath string
	Category string
}

// Client is the qBittorrent surface used by the step.
type Client interface {
	Login(ctx context.Context) error
	Find(ctx context.Context, hash string) (*Torrent, error)
	Add(ctx context.Context, torrentPath string, options map[string]string) error
	Recheck(ctx context.Context, hash string) error
	Start(ctx context.Context, hash string) error
}

// Request is one seeding step.
type Request struct {
	// TorrentPath is the registered torrent downloaded from PTT.
	TorrentPath string
	// InfoHash is the v1 infohash of TorrentPath, the torrent file that is
	// added to qBittorrent (the one downloaded from the tracker). It is not
	// the TM infohash: the tracker rewrites the info dictionary, so the
	// hashes differ. Looking up the TM hash would miss the existing torrent
	// and add a second copy.
	InfoHash string
	// Name is the expected torrent name (the release directory).
	Name string
	// SavePath is the library root as qBittorrent sees it.
	SavePath string
	Category string
	Tags     string
	// Wait bounds how long to watch the recheck; zero only queues it.
	Wait time.Duration
	// Poll is the status interval (default 5s).
	Poll time.Duration
	// Start starts seeding when, and only when, the recheck reached 100%.
	Start bool
}

// Result describes what happened.
type Result struct {
	Hash          string  `json:"hash"`
	Added         bool    `json:"added"`
	AlreadyExists bool    `json:"already_exists"`
	Progress      float64 `json:"progress"`
	State         string  `json:"state"`
	Started       bool    `json:"started"`
	Message       string  `json:"message"`
	// SeedSource is SeedSourceManual or SeedSourceCLI.
	SeedSource string `json:"seed_source,omitempty"`
	// SavePath and Category are what qBittorrent currently has for an
	// existing torrent. They are not rewritten.
	SavePath string `json:"save_path,omitempty"`
	Category string `json:"category,omitempty"`
}

func checking(state string) bool {
	return strings.HasPrefix(state, "checking") || state == "moving" || state == "allocating" || state == "metaDL"
}

// Run performs the step.
func Run(ctx context.Context, client Client, req Request) (Result, error) {
	hash := strings.ToLower(strings.TrimSpace(req.InfoHash))
	if len(hash) != 40 || req.TorrentPath == "" || req.Name == "" || strings.TrimSpace(req.SavePath) == "" {
		return Result{}, errors.New("seed: torrent path, infohash, name and save path are required")
	}
	if err := client.Login(ctx); err != nil {
		return Result{}, fmt.Errorf("seed: qBittorrent login: %w", err)
	}
	res := Result{Hash: hash}
	existing, err := client.Find(ctx, hash)
	if err != nil {
		return res, fmt.Errorf("seed: qBittorrent lookup: %w", err)
	}
	if existing != nil {
		// Already present under the tracker infohash. Do not add again and do
		// not change the save path or category, even when they differ from
		// the configuration (a category path plus auto TMM would move files).
		res.AlreadyExists = true
		res.SeedSource = SeedSourceManual
		res.Progress, res.State = existing.Progress, existing.State
		res.SavePath, res.Category = existing.SavePath, existing.Category
		res.Message = fmt.Sprintf(
			"torrent już jest w qBittorrencie (infohash %s, stan %s, postęp %.2f%%, save_path %q, kategoria %q) — ścieżka i kategoria bez zmian",
			hash, existing.State, existing.Progress*100, existing.SavePath, existing.Category,
		)
		return res, nil
	}
	// savepath is the configured path as the qBittorrent container sees the
	// library. It is never derived from the category. autoTMM is false so
	// qBittorrent does not relocate the files. No completion action is set.
	// The torrent file itself is uploaded through the WebAPI; it is not copied
	// anywhere outside the work directory.
	options := map[string]string{
		"savepath":      req.SavePath,
		"paused":        "true",
		"stopped":       "true",
		"skip_checking": "false",
		"autoTMM":       "false",
		"contentLayout": "Original",
	}
	if req.Category != "" {
		options["category"] = req.Category
	}
	if req.Tags != "" {
		options["tags"] = req.Tags
	}
	if err := client.Add(ctx, req.TorrentPath, options); err != nil {
		return res, fmt.Errorf("seed: add torrent: %w", err)
	}
	res.Added = true
	res.SeedSource = SeedSourceCLI
	added, err := waitFor(ctx, client, hash, 30*time.Second, pollOf(req))
	if err != nil {
		return res, err
	}
	if added.Name != req.Name {
		res.State = added.State
		res.Message = fmt.Sprintf("qBittorrent widzi nazwę %q zamiast %q — torrent zostaje zatrzymany, sprawdź ręcznie", added.Name, req.Name)
		return res, errors.New("seed: torrent name differs from the release directory")
	}
	if err := client.Recheck(ctx, hash); err != nil {
		return res, fmt.Errorf("seed: recheck: %w", err)
	}
	res.State, res.Progress = added.State, added.Progress
	if req.Wait <= 0 {
		res.Message = "dodano jako zatrzymany, recheck zlecony; seed uruchom po 100% (zulea-upbrr seed --start)"
		return res, nil
	}
	deadline := time.Now().Add(req.Wait)
	// qBittorrent may report the pre-check state briefly after the recheck
	// request; a finished verdict needs either an observed checking state or
	// two quiet polls.
	sawChecking, quietPolls := false, 0
	for {
		t, err := client.Find(ctx, hash)
		if err != nil {
			return res, fmt.Errorf("seed: status: %w", err)
		}
		if t != nil {
			res.State, res.Progress = t.State, t.Progress
			if checking(t.State) {
				sawChecking = true
			} else {
				quietPolls++
				if t.Progress >= 1 && (sawChecking || quietPolls >= 2) {
					break
				}
				if t.Progress < 1 && (sawChecking || quietPolls >= 2) {
					res.Message = fmt.Sprintf("recheck zakończony na %.2f%% — dane niekompletne, seed NIE uruchomiony", t.Progress*100)
					return res, nil
				}
			}
		}
		if time.Now().After(deadline) {
			res.Message = "recheck wciąż trwa — uruchom ponownie z --start po zakończeniu"
			return res, nil
		}
		select {
		case <-ctx.Done():
			return res, fmt.Errorf("seed: %w", ctx.Err())
		case <-time.After(pollOf(req)):
		}
	}
	if !req.Start {
		res.Message = "recheck 100% — torrent zatrzymany; uruchom seed: zulea-upbrr seed --start"
		return res, nil
	}
	if err := client.Start(ctx, hash); err != nil {
		return res, fmt.Errorf("seed: start: %w", err)
	}
	res.Started = true
	res.Message = "recheck 100% — seed uruchomiony"
	return res, nil
}

// StartVerified starts an existing torrent only when it is fully checked (100%).
func StartVerified(ctx context.Context, client Client, infoHash string) (Result, error) {
	hash := strings.ToLower(strings.TrimSpace(infoHash))
	if err := client.Login(ctx); err != nil {
		return Result{}, fmt.Errorf("seed: qBittorrent login: %w", err)
	}
	t, err := client.Find(ctx, hash)
	if err != nil {
		return Result{}, fmt.Errorf("seed: status: %w", err)
	}
	res := Result{Hash: hash}
	if t == nil {
		return res, errors.New("seed: torrent is not in qBittorrent")
	}
	res.State, res.Progress, res.AlreadyExists = t.State, t.Progress, true
	if checking(t.State) || t.Progress < 1 {
		res.Message = fmt.Sprintf("nie uruchamiam: stan %s, postęp %.2f%% (wymagane 100%% po rechecku)", t.State, t.Progress*100)
		return res, nil
	}
	if err := client.Start(ctx, hash); err != nil {
		return res, fmt.Errorf("seed: start: %w", err)
	}
	res.Started = true
	res.Message = "postęp 100% — seed uruchomiony"
	return res, nil
}

func pollOf(req Request) time.Duration {
	if req.Poll > 0 {
		return req.Poll
	}
	return 5 * time.Second
}

func waitFor(ctx context.Context, client Client, hash string, limit, poll time.Duration) (*Torrent, error) {
	deadline := time.Now().Add(limit)
	for {
		t, err := client.Find(ctx, hash)
		if err != nil {
			return nil, fmt.Errorf("seed: status: %w", err)
		}
		if t != nil {
			return t, nil
		}
		if time.Now().After(deadline) {
			return nil, errors.New("seed: torrent did not appear in qBittorrent after adding")
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("seed: %w", ctx.Err())
		case <-time.After(poll):
		}
	}
}

// QBit adapts go-qbittorrent (the client upbrr uses) to Client.
type QBit struct {
	client *qbittorrent.Client
}

// NewQBit creates a client for host with username/password authentication.
func NewQBit(host, username, password string) *QBit {
	return &QBit{client: qbittorrent.NewClient(qbittorrent.Config{
		Host:          host,
		Username:      username,
		Password:      password,
		Timeout:       30,
		RetryAttempts: 1,
	})}
}

// Login implements Client.
func (q *QBit) Login(ctx context.Context) error {
	if err := q.client.LoginCtx(ctx); err != nil {
		return fmt.Errorf("qbittorrent: %w", err)
	}
	return nil
}

// Find implements Client.
func (q *QBit) Find(ctx context.Context, hash string) (*Torrent, error) {
	torrents, err := q.client.GetTorrentsCtx(ctx, qbittorrent.TorrentFilterOptions{Hashes: []string{hash}})
	if err != nil {
		return nil, fmt.Errorf("qbittorrent: %w", err)
	}
	for _, t := range torrents {
		if strings.EqualFold(t.Hash, hash) {
			return &Torrent{
				Hash:     t.Hash,
				Name:     t.Name,
				Progress: t.Progress,
				State:    string(t.State),
				SavePath: t.SavePath,
				Category: t.Category,
			}, nil
		}
	}
	return nil, nil //nolint:nilnil // Absent torrent is a valid, non-error answer.
}

// Add implements Client.
func (q *QBit) Add(ctx context.Context, torrentPath string, options map[string]string) error {
	if _, err := q.client.AddTorrentFromFileCtx(ctx, torrentPath, options); err != nil {
		return fmt.Errorf("qbittorrent: %w", err)
	}
	return nil
}

// Recheck implements Client.
func (q *QBit) Recheck(ctx context.Context, hash string) error {
	if err := q.client.RecheckCtx(ctx, []string{hash}); err != nil {
		return fmt.Errorf("qbittorrent: %w", err)
	}
	return nil
}

// Start implements Client.
func (q *QBit) Start(ctx context.Context, hash string) error {
	if err := q.client.StartCtx(ctx, []string{hash}); err != nil {
		return fmt.Errorf("qbittorrent: %w", err)
	}
	return nil
}

// WebUIReport is a read-only look at the qBittorrent WebUI.
type WebUIReport struct {
	Version        string
	SavePathExists bool
	// AutoTMM is the default automatic torrent management setting.
	AutoTMM bool
}

// WebUI is the read-only surface used by check-config.
type WebUI interface {
	Login(ctx context.Context) error
	Inspect(ctx context.Context, savePath string) (WebUIReport, error)
}

// Inspect reads the WebUI version, whether savePath exists on the
// qBittorrent side, and whether default automatic torrent management is on.
// It does not add, move or reconfigure anything.
func (q *QBit) Inspect(ctx context.Context, savePath string) (WebUIReport, error) {
	var report WebUIReport
	version, err := q.client.GetAppVersionCtx(ctx)
	if err != nil {
		return report, fmt.Errorf("qbittorrent version: %w", err)
	}
	report.Version = strings.TrimSpace(version)
	prefs, err := q.client.GetAppPreferencesCtx(ctx)
	if err != nil {
		return report, fmt.Errorf("qbittorrent preferences: %w", err)
	}
	report.AutoTMM = prefs.AutoTmmEnabled
	_, err = q.client.ListDirectoryCtx(ctx, savePath, qbittorrent.DirectoryContentDirs, false)
	if err == nil {
		report.SavePathExists = true
		return report, nil
	}
	if errors.Is(err, qbittorrent.ErrUnsupportedVersion) {
		return report, fmt.Errorf("qbittorrent cannot list %q (WebAPI too old to check the save path): %w", savePath, err)
	}
	if errors.Is(err, qbittorrent.ErrUnexpectedStatus) &&
		(strings.Contains(err.Error(), "status code: 400") || strings.Contains(err.Error(), "status code: 404")) {
		report.SavePathExists = false
		return report, nil
	}
	return report, fmt.Errorf("qbittorrent save path %q: %w", savePath, err)
}
