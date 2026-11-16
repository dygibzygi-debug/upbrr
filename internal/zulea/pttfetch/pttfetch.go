// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package pttfetch downloads one registered torrent from PTT (read only) with
// upbrr's Unit3D API headers and registered-torrent validation.
package pttfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/autobrr/upbrr/internal/trackers"
	trackerdata "github.com/autobrr/upbrr/internal/trackers/data"
	"github.com/autobrr/upbrr/internal/trackers/impl/unit3d/sites/ptt"
)

var idPattern = regexp.MustCompile(`^[0-9]{1,12}$`)

// BaseURL is the PTT origin from the upbrr PTT profile.
func BaseURL() string { return strings.TrimRight(ptt.Profile().BaseURL, "/") }

// Download fetches /torrent/download/<id> into outputPath (mode 0600). Redirects
// may not leave the PTT origin. The download URL and key are never returned.
func Download(ctx context.Context, apiKey, id, outputPath string) error {
	if strings.TrimSpace(apiKey) == "" {
		return errors.New("pttfetch: PTT API key is required")
	}
	if !idPattern.MatchString(id) {
		return fmt.Errorf("pttfetch: invalid PTT torrent id %q", id)
	}
	origin, err := url.Parse(BaseURL())
	if err != nil {
		return fmt.Errorf("pttfetch: base URL: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, BaseURL()+"/torrent/download/"+id, nil)
	if err != nil {
		return errors.New("pttfetch: build request failed")
	}
	trackerdata.SetUnit3DAPIHeaders(req, apiKey)
	client := &http.Client{
		Timeout: 40 * time.Second,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if !strings.EqualFold(r.URL.Host, origin.Host) || r.URL.Scheme != origin.Scheme || len(via) >= 5 {
				return errors.New("pttfetch: redirect leaves the PTT origin")
			}
			return nil
		},
	}
	if err := trackers.DownloadRegisteredTorrent(ctx, client, req, outputPath); err != nil {
		return fmt.Errorf("pttfetch: download torrent %s: %w", id, err)
	}
	return nil
}

// Moderation states reported by Status.
const (
	// StateApproved: the torrent is visible through the standard API (approved).
	StateApproved = "APPROVED"
	// StatePending: PTT exposes /api/torrents/pending and lists the torrent there.
	StatePending = "PENDING"
	// StateNotVisible: hidden from the API — pending moderation, rejected or
	// removed. The standard Unit3D API cannot tell these apart.
	StateNotVisible = "NOT_VISIBLE"
)

// TorrentStatus is the read-only view of one PTT torrent.
type TorrentStatus struct {
	State    string `json:"state"`
	Name     string `json:"name,omitempty"`
	InfoHash string `json:"info_hash,omitempty"`
	// PendingEndpoint reports whether PTT answered /api/torrents/pending.
	PendingEndpoint bool `json:"pending_endpoint"`
}

type showResponse struct {
	Data       *resourceBody `json:"data"`
	Attributes *attributes   `json:"attributes"`
}

type resourceBody struct {
	Attributes attributes `json:"attributes"`
}

type attributes struct {
	Name     string `json:"name"`
	InfoHash string `json:"info_hash"`
}

type pendingResponse struct {
	Data []struct {
		ID json.Number `json:"id"`
	} `json:"data"`
}

func apiGet(ctx context.Context, apiKey, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, BaseURL()+path, nil)
	if err != nil {
		return nil, errors.New("pttfetch: build request failed")
	}
	trackerdata.SetUnit3DAPIHeaders(req, apiKey)
	client := &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pttfetch: request %s failed: %w", path, err)
	}
	return resp, nil
}

// Status reads the moderation-visible state of torrent id (read only).
func Status(ctx context.Context, apiKey, id string) (TorrentStatus, error) {
	if strings.TrimSpace(apiKey) == "" {
		return TorrentStatus{}, errors.New("pttfetch: PTT API key is required")
	}
	if !idPattern.MatchString(id) {
		return TorrentStatus{}, fmt.Errorf("pttfetch: invalid PTT torrent id %q", id)
	}
	resp, err := apiGet(ctx, apiKey, "/api/torrents/"+id)
	if err != nil {
		return TorrentStatus{}, err
	}
	defer resp.Body.Close()
	var status TorrentStatus
	switch resp.StatusCode {
	case http.StatusOK:
		var body showResponse
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
			return TorrentStatus{}, errors.New("pttfetch: torrent details are not valid JSON")
		}
		attrs := body.Attributes
		if body.Data != nil {
			attrs = &body.Data.Attributes
		}
		status.State = StateApproved
		if attrs != nil {
			status.Name, status.InfoHash = attrs.Name, strings.ToLower(attrs.InfoHash)
		}
		return status, nil
	case http.StatusNotFound, http.StatusForbidden:
		status.State = StateNotVisible
	default:
		return TorrentStatus{}, fmt.Errorf("pttfetch: torrent details returned HTTP %d", resp.StatusCode)
	}
	pending, err := apiGet(ctx, apiKey, "/api/torrents/pending")
	if err != nil {
		return status, nil //nolint:nilerr // The optional extension is unavailable; the standard verdict stands.
	}
	defer pending.Body.Close()
	if pending.StatusCode != http.StatusOK {
		return status, nil
	}
	status.PendingEndpoint = true
	var list pendingResponse
	if json.NewDecoder(io.LimitReader(pending.Body, 8<<20)).Decode(&list) == nil {
		for _, item := range list.Data {
			if item.ID.String() == id {
				status.State = StatePending
				break
			}
		}
	}
	return status, nil
}
