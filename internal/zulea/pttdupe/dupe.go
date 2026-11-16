// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package pttdupe runs the upbrr duplicate check for PTT on a verified Zulea
// release: the unchanged upbrr dupe service searches PTT's Unit3D
// /api/torrents/filter by the frozen TMDB id and evaluates every candidate.
//
// It is read only (a search request). The outcome is deliberately
// conservative: only a complete search without duplicates or review reasons
// is CLEAR; everything else blocks a later upload until a human decides.
package pttdupe

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/autobrr/upbrr/internal/config"
	"github.com/autobrr/upbrr/internal/trackers/dupe"
	"github.com/autobrr/upbrr/internal/trackers/impl"
	"github.com/autobrr/upbrr/internal/zulea/input"
	"github.com/autobrr/upbrr/internal/zulea/tmdb"
	"github.com/autobrr/upbrr/pkg/api"
)

// Outcome statuses.
const (
	// StatusClear: complete search, no duplicate, nothing to review.
	StatusClear = "CLEAR"
	// StatusDuplicate: at least one candidate occupies the same slot (STOP).
	StatusDuplicate = "DUPLICATE"
	// StatusReview: candidates or policy need a human decision.
	StatusReview = "REVIEW"
	// StatusNotRun: the search did not run (e.g. no API key in dry-run).
	StatusNotRun = "NOT_RUN"
	// StatusFailed: the search was attempted and failed.
	StatusFailed = "FAILED"
)

// Candidate is one existing PTT torrent the evaluator considered.
type Candidate struct {
	ID         string   `json:"id,omitempty"`
	Name       string   `json:"name"`
	Link       string   `json:"link,omitempty"`
	Relation   string   `json:"relation"`
	Reasons    []string `json:"reasons,omitempty"`
	Type       string   `json:"type,omitempty"`
	Resolution string   `json:"resolution,omitempty"`
	SizeBytes  int64    `json:"size_bytes,omitempty"`
}

// Result is the duplicate-check outcome shown in the dry-run.
type Result struct {
	Status         string      `json:"status"`
	Message        string      `json:"message,omitempty"`
	SearchComplete bool        `json:"search_complete"`
	Pages          int         `json:"pages"`
	CandidateCount int         `json:"candidate_count"`
	Candidates     []Candidate `json:"candidates,omitempty"`
	ReviewReasons  []string    `json:"review_reasons,omitempty"`
	Notes          []string    `json:"notes,omitempty"`
}

// Blocking reports whether the result forbids an upload without a human decision.
func (r Result) Blocking() bool { return r.Status != StatusClear }

// Options configure one check.
type Options struct {
	// APIKey is the PTT API key. Without it the check is NOT_RUN.
	APIKey string
	// WorkDir anchors upbrr's private state (banned-group cache); never the library.
	WorkDir string
	Logger  api.Logger
}

// Subject builds the upbrr duplicate subject from verified inputs.
func Subject(prepared *input.Prepared, movie tmdb.Movie) api.DuplicateSubject {
	return api.DuplicateSubject{
		SourcePath:  prepared.Directory,
		SourceSize:  prepared.MKV.Size,
		VideoPath:   prepared.MKV.Path,
		FileList:    []string{prepared.MKV.Path},
		Filename:    filepath.Base(prepared.MKV.Path),
		ReleaseName: prepared.Release,
		Release: api.ReleaseInfo{
			Category:   "MOVIE",
			Resolution: prepared.Parsed.Res,
			Source:     "BluRay",
			Type:       "REMUX",
			Group:      "Zulea",
		},
		Identity: api.ExternalIdentity{
			Category: api.CanonicalCategoryMovie,
			TMDBID:   movie.ID,
			IMDBID:   movie.IMDbID,
		},
		Type:       "REMUX",
		Source:     "BluRay",
		Tag:        "Zulea",
		VideoCodec: prepared.Parsed.Video,
	}
}

// Check runs the upbrr duplicate service for PTT.
func Check(ctx context.Context, prepared *input.Prepared, movie tmdb.Movie, opts Options) (Result, error) {
	if prepared == nil || prepared.Profile.Tracker != input.PTT.Tracker {
		return Result{}, errors.New("pttdupe: prepared inputs are not for PTT")
	}
	if strings.TrimSpace(opts.WorkDir) == "" {
		return Result{}, errors.New("pttdupe: work directory is required")
	}
	if strings.TrimSpace(opts.APIKey) == "" {
		return Result{Status: StatusNotRun, Message: "brak klucza API PTT — sprawdzanie duplikatów nie zostało uruchomione"}, nil
	}
	logger := opts.Logger
	if logger == nil {
		logger = api.NopLogger{}
	}
	registry, err := impl.NewRegistry()
	if err != nil {
		return Result{}, fmt.Errorf("pttdupe: upbrr registry: %w", err)
	}
	cfg := config.Config{
		MainSettings: config.MainSettingsConfig{DBPath: filepath.Join(opts.WorkDir, "upbrr.db")},
		Trackers: config.TrackersConfig{Trackers: map[string]config.TrackerConfig{
			input.PTT.Tracker: {APIKey: strings.TrimSpace(opts.APIKey)},
		}},
	}
	service := dupe.NewServiceWithRegistry(cfg, logger, registry)
	summary, err := service.Check(ctx, Subject(prepared, movie), []string{input.PTT.Tracker})
	if err != nil {
		return Result{Status: StatusFailed, Message: "sprawdzanie duplikatów nie powiodło się"}, nil
	}
	if len(summary.Results) != 1 || !strings.EqualFold(summary.Results[0].Tracker, input.PTT.Tracker) {
		return Result{Status: StatusFailed, Message: "brak wyniku sprawdzania duplikatów dla PTT"}, nil
	}
	return interpret(summary.Results[0]), nil
}

// interpret maps the upbrr result conservatively.
func interpret(r api.DupeCheckResult) Result {
	out := Result{
		SearchComplete: r.Search.Complete,
		Pages:          r.Search.Pages,
		CandidateCount: r.Search.CandidateCount,
		Notes:          append([]string(nil), r.Notes...),
	}
	for _, reason := range r.ReviewReasons {
		out.ReviewReasons = append(out.ReviewReasons, reasonText(reason))
	}
	needsReview := len(out.ReviewReasons) > 0 || r.RequiresStaffToken
	for _, e := range r.Evaluations {
		c := Candidate{
			ID:         e.ID,
			Name:       e.Name,
			Link:       e.Link,
			Relation:   string(e.Relation),
			Type:       e.Type,
			Resolution: e.Resolution,
			SizeBytes:  e.SizeBytes,
		}
		for _, reason := range e.Reasons {
			c.Reasons = append(c.Reasons, reasonText(reason))
		}
		switch e.Relation {
		case api.DupeRelationCoexists, api.DupeRelationProposedTrumps:
		case api.DupeRelationExactDuplicate, api.DupeRelationExistingPreferred, api.DupeRelationSameSlot,
			api.DupeRelationManualReview, api.DupeRelationInsufficientEvidence:
			needsReview = true
		default:
			// Unknown future relations fail closed.
			needsReview = true
		}
		out.Candidates = append(out.Candidates, c)
	}
	sort.SliceStable(out.Candidates, func(i, j int) bool { return out.Candidates[i].Name < out.Candidates[j].Name })
	switch {
	case r.Skipped:
		out.Status = StatusNotRun
		out.Message = "wyszukiwanie pominięte: " + strings.TrimSpace(r.SkipReason)
	case strings.TrimSpace(r.Error) != "" || strings.EqualFold(r.Status, "failed") || strings.EqualFold(r.Status, "error"):
		out.Status = StatusFailed
		out.Message = "wyszukiwanie nie powiodło się"
	case r.HasDupes:
		out.Status = StatusDuplicate
		out.Message = "na PTT jest już wydanie w tym samym slocie"
	case !r.Search.Complete:
		out.Status = StatusReview
		out.Message = "wyszukiwanie niekompletne — wymagana ręczna kontrola"
	case needsReview:
		out.Status = StatusReview
		out.Message = "kandydaci wymagają ręcznej oceny"
	default:
		out.Status = StatusClear
		out.Message = "brak duplikatów"
	}
	return out
}

func reasonText(reason api.DupeReason) string {
	if strings.TrimSpace(reason.Message) == "" {
		return reason.Code
	}
	return reason.Code + ": " + reason.Message
}
