// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package pttfetch downloads one registered torrent from PTT (read only) with
// upbrr's Unit3D API headers and registered-torrent validation.
package pttfetch

import (
	"context"
	"errors"
	"fmt"
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
