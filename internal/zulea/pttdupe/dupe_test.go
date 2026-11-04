// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package pttdupe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/autobrr/upbrr/internal/zulea/input"
	"github.com/autobrr/upbrr/internal/zulea/tmdb"
	"github.com/autobrr/upbrr/internal/zulea/zuleatest"
)

const apiKey = "PTTAPIKEYSENTINEL0123456789"

// rewriteTransport routes polishtorrent.top to the local fake and fails the
// test on any other host. Tests never reach the real tracker.
type rewriteTransport struct {
	t      *testing.T
	target *url.URL
	mu     sync.Mutex
	seen   []*http.Request
}

func (r *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.seen = append(r.seen, req.Clone(req.Context()))
	r.mu.Unlock()
	if req.URL.Hostname() != "polishtorrent.top" {
		r.t.Errorf("unexpected host %s", req.URL.Host)
		return nil, fmt.Errorf("blocked host %s", req.URL.Host)
	}
	out := req.Clone(req.Context())
	out.URL.Scheme = r.target.Scheme
	out.URL.Host = r.target.Host
	out.Host = r.target.Host
	resp, err := http.DefaultClient.Transport.RoundTrip(out)
	if err != nil {
		return nil, fmt.Errorf("fake tracker: %w", err)
	}
	return resp, nil
}

func withFakePTT(t *testing.T, handler http.HandlerFunc) *rewriteTransport {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	original := http.DefaultTransport
	transport := &rewriteTransport{t: t, target: target}
	http.DefaultTransport = transport
	http.DefaultClient.Transport = original
	t.Cleanup(func() {
		http.DefaultTransport = original
		http.DefaultClient.Transport = nil
	})
	return transport
}

func prepared(t *testing.T) *input.Prepared {
	t.Helper()
	env := zuleatest.New(t, zuleatest.Options{})
	adapter, err := input.Open(input.Sources{
		LibraryRoot:  env.LibraryRoot,
		FreezePath:   env.FreezePath,
		FreezeSHA256: env.FreezeSHA256,
		TitlesPath:   env.TitlesPath,
		ManifestPath: env.ManifestPath,
		TorrentDir:   env.TorrentDir,
		Disk:         env.DiskCheck(),
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := adapter.Prepare(zuleatest.Release, input.PTT)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var movie = tmdb.Movie{ID: 100001, IMDbID: 456}

func row(id int, name, typ, res string) string {
	return fmt.Sprintf(`{"id":%d,"attributes":{"name":%q,"tmdb_id":100001,"imdb_id":456,"category":"Movie","type":%q,`+
		`"resolution":%q,"size":40,"details_link":"https://polishtorrent.top/torrents/%d"}}`, id, name, typ, res, id)
}

func respond(rows ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/torrents/filter" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":[%s],"links":{"next":null},"meta":{"current_page":1,"last_page":1}}`, strings.Join(rows, ","))
	}
}

func check(t *testing.T) Result {
	t.Helper()
	result, err := Check(context.Background(), prepared(t), movie, Options{APIKey: apiKey, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	t.Logf("status=%s message=%s candidates=%+v review=%q notes=%q", result.Status, result.Message, result.Candidates, result.ReviewReasons, result.Notes)
	return result
}

func TestCheckClearWhenNoCandidates(t *testing.T) {
	transport := withFakePTT(t, respond())
	result := check(t)
	if result.Status != StatusClear || result.Blocking() || !result.SearchComplete {
		t.Fatalf("expected CLEAR, got %+v", result)
	}
	if len(transport.seen) != 1 {
		t.Fatalf("expected one search request, got %d", len(transport.seen))
	}
	req := transport.seen[0]
	if req.URL.Path != "/api/torrents/filter" || req.URL.Query().Get("tmdbId") != "100001" || req.Method != http.MethodGet {
		t.Fatalf("unexpected search request %s %s", req.Method, req.URL.String())
	}
	if req.Header.Get("Authorization") != "Bearer "+apiKey {
		t.Fatal("search must authenticate with the PTT API key header")
	}
}

func TestCheckBlocksSameSlotRemux(t *testing.T) {
	withFakePTT(t, respond(row(7, "Example Film 2019 MULTi 1080p BluRay REMUX AVC TrueHD Atmos 7.1-GRP", "Remux", "1080p")))
	result := check(t)
	if !result.Blocking() || (result.Status != StatusDuplicate && result.Status != StatusReview) {
		t.Fatalf("same-slot remux must block, got %+v", result)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].ID != "7" {
		t.Fatalf("candidates %+v", result.Candidates)
	}
}

// TestCheckBlocksOnTrackerError: upbrr reports a tracker HTTP error as an
// incomplete search, which must block (REVIEW), never read as CLEAR.
func TestCheckBlocksOnTrackerError(t *testing.T) {
	withFakePTT(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	result := check(t)
	if !result.Blocking() || result.SearchComplete || (result.Status != StatusReview && result.Status != StatusFailed) {
		t.Fatalf("tracker error must block, got %+v", result)
	}
}

// TestCheckClearWhenOnlyOtherResolution: a 2160p remux coexists with a 1080p one.
func TestCheckClearWhenOnlyOtherResolution(t *testing.T) {
	withFakePTT(t, respond(row(8, "Example Film 2019 MULTi 2160p UHD BluRay REMUX HDR HEVC TrueHD Atmos 7.1-GRP", "Remux", "2160p")))
	result := check(t)
	if result.Status != StatusClear || len(result.Candidates) != 1 || result.Candidates[0].Relation != "coexists" {
		t.Fatalf("different resolution must coexist, got %+v", result)
	}
}

func TestCheckNotRunWithoutKey(t *testing.T) {
	transport := withFakePTT(t, respond())
	result, err := Check(context.Background(), prepared(t), movie, Options{WorkDir: t.TempDir()})
	if err != nil || result.Status != StatusNotRun || !result.Blocking() {
		t.Fatalf("expected NOT_RUN, got %+v err=%v", result, err)
	}
	if len(transport.seen) != 0 {
		t.Fatal("no request may be sent without a key")
	}
}
