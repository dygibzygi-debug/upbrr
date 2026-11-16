// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package tmdb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const testKey = "TMDBKEYSENTINEL0123456789"

// fakeTMDB is a local loopback server; tests never reach the real TMDB.
func fakeTMDB(t *testing.T, id int, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/movie/100001" || r.URL.Query().Get("api_key") != testKey ||
			r.URL.Query().Get("append_to_response") != "keywords" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":` + itoa(id) + `,"imdb_id":"tt0000456","original_language":"en",` +
			`"title":"Example Film","original_title":"Example Film","release_date":"2019-06-28",` +
			`"keywords":{"keywords":[{"name":"music"},{"name":"one, two"}]}}`))
	}))
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

func TestMovieByFrozenID(t *testing.T) {
	var requests atomic.Int32
	server := fakeTMDB(t, 100001, &requests)
	defer server.Close()
	client := NewClient(testKey)
	client.BaseURL = server.URL
	movie, err := client.Movie(context.Background(), "100001")
	if err != nil {
		t.Fatalf("movie: %v", err)
	}
	if movie.ID != 100001 || movie.IMDbID != 456 || movie.Keywords != "music, one  two" || movie.ReleaseYear != 2019 {
		t.Fatalf("movie %#v", movie)
	}
	if requests.Load() != 1 {
		t.Fatalf("expected exactly one request, got %d", requests.Load())
	}
}

func TestMovieStopsOnIDMismatch(t *testing.T) {
	var requests atomic.Int32
	server := fakeTMDB(t, 100002, &requests)
	defer server.Close()
	client := NewClient(testKey)
	client.BaseURL = server.URL
	if _, err := client.Movie(context.Background(), "100001"); !errors.Is(err, ErrMismatch) {
		t.Fatalf("expected ErrMismatch, got %v", err)
	}
}

func TestMovieRejectsBadInputAndHidesKey(t *testing.T) {
	client := NewClient(testKey)
	client.BaseURL = "http://127.0.0.1:1"
	for _, id := range []string{"", "0", "abc", "12345678901"} {
		if _, err := client.Movie(context.Background(), id); err == nil {
			t.Errorf("%q: expected error", id)
		}
	}
	_, err := client.Movie(context.Background(), "100001")
	if err == nil || strings.Contains(err.Error(), testKey) {
		t.Fatalf("transport error must not expose the key: %v", err)
	}
	if _, err := NewClient("").Movie(context.Background(), "100001"); err == nil {
		t.Fatal("missing key must fail")
	}
}
