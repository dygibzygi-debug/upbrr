// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/autobrr/upbrr/internal/zulea/pttdupe"
	"github.com/autobrr/upbrr/internal/zulea/pttplan"
	"github.com/autobrr/upbrr/internal/zulea/seed"
	"github.com/autobrr/upbrr/internal/zulea/tm"
	"github.com/autobrr/upbrr/internal/zulea/tmdb"
	"github.com/autobrr/upbrr/internal/zulea/zuleatest"
)

const sameSlotRow = `{"id":7,"attributes":{"name":"Example Film 2019 MULTi 1080p BluRay REMUX AVC TrueHD Atmos 7.1-GRP",` +
	`"tmdb_id":100001,"category":"Movie","type":"Remux","resolution":"1080p","size":40}}`

func TestScanPTTAndQueue(t *testing.T) {
	_, fake, runner := uploadSetup(t)
	summary, err := runner.ScanPTT(context.Background(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Results) != 1 || summary.Results[0].Dupe != "CLEAR" || summary.Counts["CLEAR"] != 1 {
		t.Fatalf("scan %+v", summary)
	}
	md, err := os.ReadFile(strings.TrimSuffix(summary.ReportPath, ".json") + ".md")
	if err != nil || !strings.Contains(string(md), "Wolne na PTT") || !strings.Contains(string(md), zuleatest.Release) {
		t.Fatalf("markdown: %v\n%s", err, md)
	}
	queue, _, err := runner.Queue()
	if err != nil || len(queue) != 1 || queue[0].Release != zuleatest.Release {
		t.Fatalf("queue %+v %v", queue, err)
	}
	if fake.uploads != 0 {
		t.Fatal("scan and queue must never upload")
	}
	// After an upload receipt the release leaves the queue.
	fp := dryRunFingerprint(t, runner)
	if _, err := runner.Upload(context.Background(), UploadRequest{
		Release:     zuleatest.Release,
		Fingerprint: fp,
		Preview:     io.Discard,
		Confirm:     typed(ConfirmationPhrase(fp)),
	}); err != nil {
		t.Fatal(err)
	}
	// Until PTT approves the upload, the queue is paused.
	if _, _, err = runner.Queue(); !errors.Is(err, ErrQueueBlocked) {
		t.Fatalf("queue must pause until approval: %v", err)
	}
	statuses, err := runner.PTTStatus(context.Background(), nil)
	if err != nil || len(statuses) != 1 || statuses[0].State != "NOT_VISIBLE" {
		t.Fatalf("status %+v %v", statuses, err)
	}
	fake.pending = true
	if statuses, err = runner.PTTStatus(context.Background(), nil); err != nil || statuses[0].State != "PENDING" {
		t.Fatalf("pending status %+v %v", statuses, err)
	}
	if _, _, err = runner.Queue(); !errors.Is(err, ErrQueueBlocked) {
		t.Fatalf("PENDING must still pause the queue: %v", err)
	}
	receipt, err := runner.readReceipt(zuleatest.Release)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := tm.ReadFile(receipt.RegisteredTorrent)
	if err != nil {
		t.Fatal(err)
	}
	desc, err := tm.Describe(registered)
	if err != nil {
		t.Fatal(err)
	}
	fake.approved, fake.reportedSum = true, desc.InfoHash
	statuses, err = runner.PTTStatus(context.Background(), nil)
	if err != nil || statuses[0].State != "APPROVED" || statuses[0].InfoHashMatch == nil || !*statuses[0].InfoHashMatch {
		t.Fatalf("approved status %+v %v", statuses, err)
	}
	if queue, _, err = runner.Queue(); err != nil || len(queue) != 0 {
		t.Fatalf("after approval the uploaded release must leave a running queue: %+v %v", queue, err)
	}
	fake.reportedSum = strings.Repeat("0", 40)
	if statuses, err = runner.PTTStatus(context.Background(), nil); err != nil || *statuses[0].InfoHashMatch {
		t.Fatalf("a different PTT infohash must be reported: %+v %v", statuses, err)
	}
}

func TestCanaryGateBlocksWhileAwaitingApproval(t *testing.T) {
	_, _, runner := uploadSetup(t)
	fp := dryRunFingerprint(t, runner)
	if _, err := runner.Upload(context.Background(), UploadRequest{
		Release:     zuleatest.Release,
		Fingerprint: fp,
		Preview:     io.Discard,
		Confirm:     typed(ConfirmationPhrase(fp)),
	}); err != nil {
		t.Fatal(err)
	}
	gate := runner.canaryGate(zuleatest.Release, &pttplan.Plan{TaxonomyConfirmed: true}, &pttdupe.Result{Status: "CLEAR"}, "k")
	if gate.Ready || !strings.Contains(strings.Join(gate.Blockers, "|"), "wymaga uwagi") {
		t.Fatalf("gate must block while awaiting approval: %+v", gate)
	}
}

type errMovies struct{}

func (errMovies) Movie(context.Context, string) (tmdb.Movie, error) {
	return tmdb.Movie{}, errors.New("tmdb down")
}

func TestScanPTTStopsOnAPIError(t *testing.T) {
	_, _, runner := uploadSetup(t)
	runner.Movies = errMovies{}
	summary, err := runner.ScanPTT(context.Background(), []string{zuleatest.Release, zuleatest.Release}, 0)
	if err == nil || len(summary.Results) != 1 || summary.Results[0].Dupe != pttdupe.StatusFailed {
		t.Fatalf("scan must stop on the first API error: %+v %v", summary.Results, err)
	}
	if !strings.Contains(err.Error(), "bez ponawiania") {
		t.Fatalf("error %v", err)
	}
}

func TestScanPTTRejectsShortDelay(t *testing.T) {
	_, err := (&Runner{}).ScanPTT(context.Background(), nil, time.Second)
	if err == nil || !strings.Contains(err.Error(), "3s") {
		t.Fatal(err)
	}
}

func TestScanPTTReportsDuplicate(t *testing.T) {
	_, fake, runner := uploadSetup(t)
	fake.filterRows = sameSlotRow
	summary, err := runner.ScanPTT(context.Background(), []string{zuleatest.Release, "Missing.2000.PL.1080p.REMUX.BluRay.AVC.DD.2.0-Zulea"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Results[0].Dupe != "DUPLICATE" || summary.Results[1].Chain != "STOP" || summary.Counts["CHAIN_STOP"] != 1 {
		t.Fatalf("scan %+v", summary)
	}
	if queue, _, err := runner.Queue(); err != nil || len(queue) != 0 {
		t.Fatalf("duplicate must not be queued: %+v %v", queue, err)
	}
}

func TestVerifyUploadedFindsExactTitle(t *testing.T) {
	env, fake, runner := uploadSetup(t)
	tmTorrent, err := os.ReadFile(env.TorrentPath)
	if err != nil {
		t.Fatal(err)
	}
	fake.torrent = tmTorrent
	fake.filterRows = `{"id":77,"attributes":{"name":"Przykładowy film / Example Film 2019 MULTi 1080p BluRay REMUX AVC TrueHD Atmos 7.1-Zulea",` +
		`"tmdb_id":100001,"category":"Movie","type":"Remux","resolution":"1080p","size":40}}`
	res, err := runner.VerifyUploaded(context.Background(), zuleatest.Release, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.PTTID != "77" || !res.DataMatch || !res.NamesMatch || !res.PrivateOK || !res.Comparison.InfoHashChanged {
		t.Fatalf("verify %+v", res)
	}
	if strings.Contains(res.Comparison.Report(), zuleatest.PasskeySentinel) {
		t.Fatal("report must not leak the passkey")
	}
	fake.filterRows = sameSlotRow
	if _, err := runner.VerifyUploaded(context.Background(), zuleatest.Release, ""); err == nil {
		t.Fatal("without an exact title match an explicit --ptt-id is required")
	}
	if _, err := runner.VerifyUploaded(context.Background(), zuleatest.Release, "x7"); err == nil {
		t.Fatal("invalid id must be rejected")
	}
}

type recordingQB struct {
	added    string
	options  map[string]string
	started  bool
	present  *seed.Torrent
	byHash   map[string]*seed.Torrent
	lookups  []string
	failAdd  bool
	rechecks int
}

func (q *recordingQB) Login(context.Context) error { return nil }
func (q *recordingQB) Find(_ context.Context, hash string) (*seed.Torrent, error) {
	q.lookups = append(q.lookups, hash)
	if q.byHash != nil {
		return q.byHash[hash], nil
	}
	return q.present, nil
}
func (q *recordingQB) Add(_ context.Context, path string, opts map[string]string) error {
	q.added, q.options = path, opts
	if q.failAdd {
		return errors.New("add refused in test")
	}
	q.present = &seed.Torrent{
		Name:     zuleatest.Release,
		State:    "stoppedUP",
		Progress: 1,
	}
	return nil
}
func (q *recordingQB) Recheck(context.Context, string) error { q.rechecks++; return nil }
func (q *recordingQB) Start(context.Context, string) error   { q.started = true; return nil }

func TestSeedAfterAcceptedUpload(t *testing.T) {
	_, _, runner := uploadSetup(t)
	runner.Config.QBittorrent = &QBitConfig{
		Host:     "http://qbittorrent.invalid",
		SavePath: "/data/library",
		Category: "zulea",
	}
	qb := &recordingQB{}
	if _, err := runner.Seed(context.Background(), SeedRequest{Release: zuleatest.Release}, qb); err == nil {
		t.Fatal("seed without an accepted receipt must be refused")
	}
	fp := dryRunFingerprint(t, runner)
	result, err := runner.Upload(context.Background(), UploadRequest{
		Release:     zuleatest.Release,
		Fingerprint: fp,
		Preview:     io.Discard,
		Confirm:     typed(ConfirmationPhrase(fp)),
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := runner.Seed(context.Background(), SeedRequest{
		Release: zuleatest.Release,
		Wait:    1e9,
		Poll:    1e6,
		Start:   true,
	}, qb)
	if err != nil {
		t.Fatal(err)
	}
	if qb.added != result.Receipt.RegisteredTorrent || qb.options["savepath"] != "/data/library" || qb.options["stopped"] != "true" {
		t.Fatalf("added %q options %+v", qb.added, qb.options)
	}
	if !res.Added || !res.Started || !qb.started || res.Hash != result.Comparison.Downloaded.InfoHash || res.SeedSource != seed.SeedSourceCLI {
		t.Fatalf("seed %+v", res)
	}
	if res.Hash == result.Receipt.InfoHash {
		t.Fatal("seed must not use the TM infohash")
	}
	receipt, err := runner.readReceipt(zuleatest.Release)
	if err != nil || receipt.SeedSource != seed.SeedSourceCLI {
		t.Fatalf("receipt seed source %+v %v", receipt, err)
	}
}

func TestSeedMatchesPTTInfoHashNotTM(t *testing.T) {
	_, _, runner := uploadSetup(t)
	fp := dryRunFingerprint(t, runner)
	result, err := runner.Upload(context.Background(), UploadRequest{
		Release:     zuleatest.Release,
		Fingerprint: fp,
		Preview:     io.Discard,
		Confirm:     typed(ConfirmationPhrase(fp)),
	})
	if err != nil {
		t.Fatal(err)
	}
	pttHash := result.Comparison.Downloaded.InfoHash
	tmHash := result.Receipt.InfoHash
	if pttHash == "" || pttHash == tmHash || !result.Comparison.InfoHashChanged {
		t.Fatalf("PTT infohash %s must differ from TM %s", pttHash, tmHash)
	}
	if !slices.Contains(result.Comparison.Downloaded.InfoKeys, "source") || !slices.Contains(result.Comparison.Downloaded.InfoKeys, "entropy") {
		t.Fatalf("downloaded info keys %v", result.Comparison.Downloaded.InfoKeys)
	}
	runner.Config.QBittorrent = &QBitConfig{
		Host:     "http://qbittorrent.invalid",
		SavePath: "/data/library",
		Category: "examples",
	}
	present := &seed.Torrent{
		Hash:     pttHash,
		Name:     zuleatest.Release,
		State:    "uploading",
		Progress: 1,
		SavePath: "/data/library",
		Category: "kept",
	}
	qb := &recordingQB{byHash: map[string]*seed.Torrent{pttHash: present}}
	res, err := runner.Seed(context.Background(), SeedRequest{Release: zuleatest.Release, Start: true}, qb)
	if err != nil || res.Added || !res.AlreadyExists || res.SeedSource != seed.SeedSourceManual || qb.added != "" || qb.rechecks != 0 || qb.started {
		t.Fatalf("existing PTT torrent must not be added again: %+v %v lookups=%v", res, err, qb.lookups)
	}
	if len(qb.lookups) != 1 || qb.lookups[0] != pttHash {
		t.Fatalf("lookup must be the PTT infohash, not the TM one: %v tm=%s", qb.lookups, tmHash)
	}
	if res.SavePath != "/data/library" || res.Category != "kept" {
		t.Fatalf("path and category must stay as qBittorrent has them: %+v", res)
	}
	receipt, err := runner.readReceipt(zuleatest.Release)
	if err != nil || receipt.SeedSource != seed.SeedSourceManual || receipt.InfoHash != tmHash {
		t.Fatalf("receipt %+v %v", receipt, err)
	}
	data, err := os.ReadFile(runner.receiptPath(zuleatest.Release))
	if err != nil || !strings.Contains(string(data), `"seed_source": "manual"`) {
		t.Fatalf("receipt json %s %v", data, err)
	}
	// A qBittorrent entry under the TM hash is a different torrent.
	qb = &recordingQB{byHash: map[string]*seed.Torrent{tmHash: present}, failAdd: true}
	if _, err = runner.Seed(context.Background(), SeedRequest{Release: zuleatest.Release}, qb); err == nil || qb.added == "" {
		t.Fatal("TM infohash must not count as the torrent already in qBittorrent")
	}
	if len(qb.lookups) == 0 || qb.lookups[0] != pttHash || slices.Contains(qb.lookups, tmHash) {
		t.Fatalf("lookups %v ptt=%s tm=%s", qb.lookups, pttHash, tmHash)
	}
}

func TestSyncInputs(t *testing.T) {
	env := zuleatest.New(t, zuleatest.Options{})
	dest := filepath.Join(t.TempDir(), "uploader-inputs")
	runsDir := filepath.Dir(filepath.Dir(env.ManifestPath))
	running := filepath.Join(runsDir, "run-2")
	if err := os.MkdirAll(running, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(running, "manifest-20261105-000000.tsv"), []byte("# TRYB=APPLY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(env.FreezePath)
	req := SyncRequest{
		FreezeSrc:    env.FreezePath,
		FreezeSHA256: env.FreezeSHA256,
		RunsSrc:      runsDir,
		TitlesSrc:    env.TitlesPath,
		Dest:         dest,
	}
	res, err := SyncInputs(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Copied) != 3 || len(res.Incomplete) != 1 {
		t.Fatalf("sync %+v", res)
	}
	copied, err := os.ReadFile(filepath.Join(dest, "freeze", filepath.Base(env.FreezePath)))
	sum := sha256.Sum256(copied)
	if err != nil || hex.EncodeToString(sum[:]) != env.FreezeSHA256 {
		t.Fatal("freeze copy must be byte-identical")
	}
	if st, _ := os.Stat(filepath.Join(dest, "freeze", filepath.Base(env.FreezePath))); st.Mode().Perm() != 0o400 {
		t.Fatalf("copy mode %v", st.Mode().Perm())
	}
	if after, _ := os.ReadFile(env.FreezePath); string(after) != string(before) {
		t.Fatal("source must be untouched")
	}
	if res, err = SyncInputs(req); err != nil || len(res.Unchanged) != 3 || len(res.Copied) != 0 {
		t.Fatalf("second sync must be idempotent: %+v %v", res, err)
	}
	bad := req
	bad.FreezeSHA256 = strings.Repeat("0", 64)
	if _, err := SyncInputs(bad); err == nil {
		t.Fatal("wrong freeze SHA must fail")
	}
	// A different freeze under the same name is never overwritten.
	if err := os.Chmod(env.FreezePath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.FreezePath, append(before, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := req
	changed.FreezeSHA256 = digest(append(before, '\n'))
	if _, err := SyncInputs(changed); err == nil || !strings.Contains(err.Error(), "never overwritten") {
		t.Fatalf("different evidence must not overwrite: %v", err)
	}
}

func writeConfig(t *testing.T, path string, cfg Config) {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestApplyTaxonomy(t *testing.T) {
	_, cfg, _ := setup(t)
	path := filepath.Join(t.TempDir(), "zulea.json")
	writeConfig(t, path, cfg)
	tax := pttplan.Taxonomy{
		Category:   map[string]string{"MOVIE": "1"},
		Type:       map[string]string{"REMUX": "2"},
		Resolution: map[string]string{"1080p": "3"},
	}
	if err := ApplyTaxonomy(path, tax); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil || loaded.PTT.Taxonomy == nil || loaded.PTT.Taxonomy.Type["REMUX"] != "2" || loaded.LibraryRoot != cfg.LibraryRoot {
		t.Fatalf("applied %+v %v", loaded.PTT.Taxonomy, err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("config mode %v", st.Mode().Perm())
	}
}

func TestCheckMarkdownClassifiesStops(t *testing.T) {
	md := CheckMarkdown(CheckSummary{
		FreezeSHA256: "abc",
		OK:           1,
		Stop:         2,
		Results: []CheckResult{
			{
				Release:  "A-Zulea",
				Status:   "OK",
				Title:    "A",
				Warnings: []string{"brak tytułu z TMDB"},
			},
			{
				Release: "B-Zulea",
				Status:  "STOP",
				Reason:  "MKV differs from freeze (ino/size/mtime 1 vs 2)",
			},
			{
				Release: "C-Zulea",
				Status:  "STOP",
				Reason:  "tm: manifest does not prove release",
			},
		},
	})
	for _, want := range []string{"MKV niezgodny z zamrożeniem", "brak lub niezgodny dowód TM", "Ostrzeżenia do sprawdzenia (1 wydań)", "`A-Zulea`"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
}
