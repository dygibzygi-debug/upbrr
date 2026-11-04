// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package tmdb is the narrow TMDB lookup allowed by decision D-2: one movie
// read by the TMDB_ID recorded in the freeze. There is no search by name, no
// other provider, and the returned id must equal the frozen id or the release
// stops. The API key is never placed in errors or logs.
package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the TMDB v3 API root.
const DefaultBaseURL = "https://api.themoviedb.org/3"

// ErrMismatch marks a response whose id differs from the frozen TMDB_ID (STOP).
var ErrMismatch = errors.New("tmdb: returned id differs from freeze")

var (
	idPattern   = regexp.MustCompile(`^[0-9]{1,10}$`)
	imdbPattern = regexp.MustCompile(`^tt[0-9]{1,10}$`)
)

// Movie is the subset of a TMDB movie record the PTT payload needs.
type Movie struct {
	ID               int
	IMDbID           int
	OriginalLanguage string
	Title            string
	OriginalTitle    string
	ReleaseYear      int
	// Keywords uses upbrr's Unit3D format: names joined by ", " with commas replaced by spaces.
	Keywords string
}

// Client reads one movie by id.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	apiKey  string
}

// NewClient returns a client for apiKey with a 15-second timeout.
func NewClient(apiKey string) *Client {
	return &Client{
		BaseURL: DefaultBaseURL,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
		apiKey:  strings.TrimSpace(apiKey),
	}
}

type movieResponse struct {
	ID               int    `json:"id"`
	IMDbID           string `json:"imdb_id"`
	OriginalLanguage string `json:"original_language"`
	Title            string `json:"title"`
	OriginalTitle    string `json:"original_title"`
	ReleaseDate      string `json:"release_date"`
	Keywords         struct {
		Keywords []struct {
			Name string `json:"name"`
		} `json:"keywords"`
	} `json:"keywords"`
}

// Movie fetches /movie/{id} with keywords, by the frozen id only.
func (c *Client) Movie(ctx context.Context, frozenID string) (Movie, error) {
	frozenID = strings.TrimSpace(frozenID)
	if !idPattern.MatchString(frozenID) || strings.TrimLeft(frozenID, "0") == "" {
		return Movie{}, fmt.Errorf("tmdb: invalid frozen TMDB_ID %q", frozenID)
	}
	if c.apiKey == "" {
		return Movie{}, errors.New("tmdb: api key missing")
	}
	want, _ := strconv.Atoi(frozenID)
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/movie/" + strconv.Itoa(want) + "?" +
		url.Values{"api_key": {c.apiKey}, "append_to_response": {"keywords"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Movie{}, errors.New("tmdb: build request failed")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// The transport error may embed the URL (and key); report only the class.
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Timeout() {
			return Movie{}, errors.New("tmdb: request timed out")
		}
		return Movie{}, errors.New("tmdb: request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Movie{}, fmt.Errorf("tmdb: http %d for movie %d", resp.StatusCode, want)
	}
	var body movieResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return Movie{}, errors.New("tmdb: decode response failed")
	}
	if body.ID != want {
		return Movie{}, fmt.Errorf("%w: got %d, frozen %d", ErrMismatch, body.ID, want)
	}
	movie := Movie{
		ID:               body.ID,
		OriginalLanguage: strings.TrimSpace(body.OriginalLanguage),
		Title:            body.Title,
		OriginalTitle:    body.OriginalTitle,
	}
	if imdb := strings.TrimSpace(body.IMDbID); imdb != "" {
		if !imdbPattern.MatchString(imdb) {
			return Movie{}, fmt.Errorf("tmdb: malformed imdb_id %q", imdb)
		}
		movie.IMDbID, _ = strconv.Atoi(strings.TrimPrefix(imdb, "tt"))
	}
	if len(body.ReleaseDate) >= 4 {
		movie.ReleaseYear, _ = strconv.Atoi(body.ReleaseDate[:4])
	}
	parts := make([]string, 0, len(body.Keywords.Keywords))
	for _, k := range body.Keywords.Keywords {
		if name := strings.TrimSpace(k.Name); name != "" {
			parts = append(parts, strings.ReplaceAll(name, ",", " "))
		}
	}
	movie.Keywords = strings.Join(parts, ", ")
	return movie, nil
}
