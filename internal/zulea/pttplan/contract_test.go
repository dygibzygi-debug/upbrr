// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package pttplan

import (
	"context"
	"slices"
	"sort"
	"testing"

	"github.com/autobrr/upbrr/internal/trackers/dupe"
	"github.com/autobrr/upbrr/internal/trackers/impl"
	"github.com/autobrr/upbrr/internal/zulea/zuleatest"
)

// TestUpbrrPTTContract pins the upbrr behavior the Zulea uploader relies on.
// It fails when an upstream upbrr update changes the PTT form fields, the
// endpoint, the default Unit3D IDs or the registry wiring, so the change is
// reviewed (scripts/zulea/upstream-sync.sh) before anything reaches PTT.
func TestUpbrrPTTContract(t *testing.T) {
	registry, err := impl.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := registry.Lookup("PTT")
	if !ok {
		t.Fatal("upbrr registry no longer contains PTT")
	}
	if _, ok := definition.(dupe.Factory); !ok {
		t.Fatal("upbrr PTT definition no longer provides a duplicate adapter")
	}
	env := zuleatest.New(t, zuleatest.Options{})
	opts, _ := options(t, env, &captureLogger{})
	plan, err := Build(context.Background(), prepared(t, env), opts)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Endpoint != "https://polishtorrent.top/api/torrents/upload" {
		t.Error("PTT upload endpoint changed")
	}
	fields := make([]string, 0, len(plan.Payload))
	for k := range plan.Payload {
		fields = append(fields, k)
	}
	sort.Strings(fields)
	want := []string{
		"anonymous", "bdinfo", "category_id", "description", "doubleup", "featured", "free", "igdb", "imdb",
		"internal", "keywords", "mal", "mediainfo", "mod_queue_opt_in", "name", "personal_release",
		"resolution_id", "sd", "sticky", "stream", "tmdb", "type_id",
	}
	if !slices.Equal(fields, want) {
		t.Errorf("PTT form fields changed:\n got  %q\n want %q", fields, want)
	}
	for field, value := range map[string]string{
		"category_id":   "1",
		"type_id":       "2",
		"resolution_id": "3",
	} {
		if plan.Payload[field] != value {
			t.Errorf("upbrr default %s changed: %s (was %s)", field, plan.Payload[field], value)
		}
	}
	files := make([]string, 0, len(plan.Files))
	for _, f := range plan.Files {
		files = append(files, f.Field)
	}
	if !slices.Equal(files, []string{"nfo", "torrent"}) {
		t.Errorf("PTT multipart files changed: %q", files)
	}
}
