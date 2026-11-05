// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package seed

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fakeQB struct {
	torrent   *Torrent
	states    []Torrent // successive states returned after the recheck
	added     map[string]string
	rechecked bool
	started   bool
	name      string
}

func (f *fakeQB) Login(context.Context) error { return nil }
func (f *fakeQB) Find(_ context.Context, hash string) (*Torrent, error) {
	if f.torrent == nil {
		return nil, nil //nolint:nilnil // fake mirrors the client contract
	}
	if f.rechecked && len(f.states) > 0 {
		next := f.states[0]
		if len(f.states) > 1 {
			f.states = f.states[1:]
		}
		next.Hash, next.Name = hash, f.torrent.Name
		return &next, nil
	}
	return f.torrent, nil
}
func (f *fakeQB) Add(_ context.Context, _ string, options map[string]string) error {
	f.added = options
	name := f.name
	if name == "" {
		name = "Example-Zulea"
	}
	f.torrent = &Torrent{
		Name:     name,
		State:    "stoppedDL",
		Progress: 0,
	}
	return nil
}
func (f *fakeQB) Recheck(context.Context, string) error { f.rechecked = true; return nil }
func (f *fakeQB) Start(context.Context, string) error   { f.started = true; return nil }

func req(start bool) Request {
	return Request{
		TorrentPath: "/work/x.torrent",
		InfoHash:    strings.Repeat("a", 40),
		Name:        "Example-Zulea",
		SavePath:    "/data/library",
		Wait:        time.Second,
		Poll:        time.Millisecond,
		Start:       start,
	}
}

func TestRunAddsStoppedRechecksAndStartsOnlyAt100(t *testing.T) {
	qb := &fakeQB{states: []Torrent{{State: "checkingUP", Progress: 0.4}, {State: "stoppedUP", Progress: 1}}}
	request := req(true)
	request.Category = "examples"
	res, err := Run(context.Background(), qb, request)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Added || !qb.rechecked || !qb.started || !res.Started || res.SeedSource != SeedSourceCLI {
		t.Fatalf("result %+v", res)
	}
	for k, v := range map[string]string{
		"stopped":       "true",
		"paused":        "true",
		"skip_checking": "false",
		"savepath":      "/data/library",
		"autoTMM":       "false",
		"category":      "examples",
	} {
		if qb.added[k] != v {
			t.Errorf("option %s=%q want %q", k, qb.added[k], v)
		}
	}
	if qb.added["savepath"] == "/data/library/examples" {
		t.Fatal("save path must not be derived from the category")
	}
}

func TestRunNeverStartsIncompleteData(t *testing.T) {
	qb := &fakeQB{states: []Torrent{{State: "checkingUP", Progress: 0.2}, {State: "stoppedDL", Progress: 0.97}}}
	res, err := Run(context.Background(), qb, req(true))
	if err != nil {
		t.Fatal(err)
	}
	if qb.started || res.Started || !strings.Contains(res.Message, "NIE") {
		t.Fatalf("incomplete data must not start: %+v", res)
	}
}

func TestRunWithoutStartLeavesStopped(t *testing.T) {
	qb := &fakeQB{states: []Torrent{{State: "stoppedUP", Progress: 1}}}
	res, err := Run(context.Background(), qb, req(false))
	if err != nil || qb.started || res.Started {
		t.Fatalf("without --start nothing may start: %+v %v", res, err)
	}
}

func TestRunLeavesExistingTorrentUntouched(t *testing.T) {
	qb := &fakeQB{torrent: &Torrent{
		Name:     "Example-Zulea",
		State:    "uploading",
		Progress: 1,
		SavePath: "/other/library",
		Category: "kept",
	}}
	request := req(true)
	request.Category = "examples"
	res, err := Run(context.Background(), qb, request)
	if err != nil || !res.AlreadyExists || qb.added != nil || qb.rechecked || qb.started {
		t.Fatalf("existing torrent must be left alone: %+v %v", res, err)
	}
	if res.SeedSource != SeedSourceManual || res.SavePath != "/other/library" || res.Category != "kept" {
		t.Fatalf("existing path and category must be reported unchanged: %+v", res)
	}
}

func TestRunStopsOnNameMismatch(t *testing.T) {
	qb := &fakeQB{name: "Renamed-By-Tracker"}
	if _, err := Run(context.Background(), qb, req(true)); err == nil || qb.rechecked || qb.started {
		t.Fatalf("name mismatch must stop before recheck: %v", err)
	}
}

func TestStartVerified(t *testing.T) {
	qb := &fakeQB{torrent: &Torrent{
		Name:     "Example-Zulea",
		State:    "stoppedDL",
		Progress: 0.5,
	}}
	res, err := StartVerified(context.Background(), qb, strings.Repeat("a", 40))
	if err != nil || res.Started || qb.started {
		t.Fatalf("partial torrent must not start: %+v %v", res, err)
	}
	qb.torrent.Progress, qb.torrent.State = 1, "stoppedUP"
	if res, err = StartVerified(context.Background(), qb, strings.Repeat("a", 40)); err != nil || !res.Started {
		t.Fatalf("complete torrent should start: %+v %v", res, err)
	}
}
