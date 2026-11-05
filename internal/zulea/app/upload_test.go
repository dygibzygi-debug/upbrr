// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/autobrr/upbrr/internal/pathing"
	"github.com/autobrr/upbrr/internal/zulea/tm"
	"github.com/autobrr/upbrr/internal/zulea/zuleatest"
)

// fakeUploadPTT emulates the Unit3D upload API and registered-torrent download.
type fakeUploadPTT struct {
	t          *testing.T
	env        *zuleatest.Env
	mu         sync.Mutex
	uploads    int
	fields     map[string]string
	torrent    []byte
	nfo        []byte
	respondErr bool
	filterRows string
	// approved makes /api/torrents/77 visible; pending lists 77 on /api/torrents/pending.
	approved    bool
	pending     bool
	reportedSum string
}

func (f *fakeUploadPTT) handler(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+pttKey {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/torrents/filter":
		_, _ = w.Write([]byte(`{"data":[` + f.filterRows + `],"links":{"next":null},"meta":{"current_page":1,"last_page":1}}`))
	case r.Method == http.MethodPost && r.URL.Path == "/api/torrents/upload":
		f.mu.Lock()
		f.uploads++
		f.mu.Unlock()
		if f.respondErr {
			_, _ = w.Write([]byte(`{"success":false,"message":"rejected by fake"}`))
			return
		}
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			http.Error(w, "bad content type", http.StatusBadRequest)
			return
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		f.fields = map[string]string{}
		for {
			part, err := reader.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				http.Error(w, "bad multipart", http.StatusBadRequest)
				return
			}
			data, _ := io.ReadAll(part)
			switch part.FormName() {
			case "torrent":
				f.torrent = data
			case "nfo":
				f.nfo = data
			default:
				f.fields[part.FormName()] = string(data)
			}
		}
		_, _ = w.Write([]byte(`{"success":true,"message":"Torrent uploaded","data":"77"}`))
	case r.Method == http.MethodGet && r.URL.Path == "/api/torrents/77":
		if !f.approved {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"type":"torrent","id":77,"attributes":{"name":"x","info_hash":"` + f.reportedSum + `"}}}`))
	case r.Method == http.MethodGet && r.URL.Path == "/api/torrents/pending":
		if !f.pending {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":77,"tmdb_id":100001,"name":"x"}]}`))
	case r.Method == http.MethodGet && r.URL.Path == "/torrent/download/77":
		// The tracker returns the uploaded info plus its own source and announce.
		info := trackerInfo(f.t, f.torrent)
		announce := string(zuleatest.Encode("https://polishtorrent.top/announce/" + zuleatest.PasskeySentinel))
		_, _ = w.Write(append(append([]byte("d8:announce"+announce+"4:info"), info...), 'e'))
	default:
		http.Error(w, "unexpected", http.StatusNotFound)
	}
}

// trackerInfo returns the uploaded info dictionary with source=PTT appended
// ("source" sorts after "private", the last TM key), as Unit3D does on accept.
func trackerInfo(t *testing.T, uploaded []byte) []byte {
	t.Helper()
	const marker = "4:info"
	idx := bytes.Index(uploaded, []byte(marker))
	if idx < 0 || len(uploaded) < idx+len(marker)+2 {
		t.Errorf("uploaded torrent has no info")
		return []byte("de")
	}
	info := uploaded[idx+len(marker) : len(uploaded)-1]
	out := append([]byte(nil), info[:len(info)-1]...)
	out = append(out, "6:source3:PTT"...)
	return append(out, 'e')
}

func uploadSetup(t *testing.T) (*zuleatest.Env, *fakeUploadPTT, *Runner) {
	t.Helper()
	env, cfg, guard := setup(t)
	fake := &fakeUploadPTT{t: t, env: env}
	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	guard.ptt = target
	cfg = completeCanaryConfig(cfg)
	runner := &Runner{Config: cfg, MediaInfo: fakeMediaInfo{dir: filepath.Join(cfg.WorkDir, "mi")}}
	return env, fake, runner
}

func dryRunFingerprint(t *testing.T, runner *Runner) string {
	t.Helper()
	outcomes, err := runner.DryRun(context.Background(), []string{zuleatest.Release})
	if err != nil || outcomes[0].Plan == nil || !outcomes[0].Canary.Ready {
		t.Fatalf("dry-run not ready: %+v err=%v", outcomes, err)
	}
	return outcomes[0].Plan.Fingerprint
}

func typed(answer string) func(string) (string, error) {
	return func(string) (string, error) { return answer, nil }
}

func TestUploadCanaryEndToEnd(t *testing.T) {
	env, fake, runner := uploadSetup(t)
	fp := dryRunFingerprint(t, runner)
	var preview bytes.Buffer
	result, err := runner.Upload(context.Background(), UploadRequest{
		Release:     zuleatest.Release,
		Fingerprint: fp,
		Preview:     &preview,
		Confirm:     typed(ConfirmationPhrase(fp)),
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if fake.uploads != 1 || result.Receipt.State != ReceiptAccepted || result.Receipt.TorrentID != "77" {
		t.Fatalf("result %+v uploads=%d", result, fake.uploads)
	}
	// The sent payload is the reviewed one: title, description, flags, TM info bytes, NFO bytes.
	if fake.fields["name"] != "Przykładowy film / Example Film 2019 MULTi 1080p BluRay REMUX AVC TrueHD Atmos 7.1-Zulea" ||
		fake.fields["description"] != strings.TrimSpace(zuleatest.Description) ||
		fake.fields["mod_queue_opt_in"] != "1" || fake.fields["tmdb"] != zuleatest.TMDBID {
		t.Fatalf("fields %+v", fake.fields)
	}
	if hash, err := tm.InfoHashOf(fake.torrent); err != nil || hash != env.InfoHash {
		t.Fatalf("sent torrent infohash %s err=%v", hash, err)
	}
	if bytes.Contains(fake.torrent, []byte("announce")) || !bytes.Equal(fake.nfo, env.NFOBytes) {
		t.Fatal("sent torrent must have no announce and the NFO must be byte-identical")
	}
	if result.Comparison == nil || !result.Comparison.OK() || !result.Comparison.InfoHashChanged {
		t.Fatalf("registered torrent comparison %+v (receipt message %q)", result.Comparison, result.Receipt.Message)
	}
	reg := result.Receipt.RegisteredTorrent
	if st, err := os.Stat(reg); err != nil || !pathing.IsWithinRoot(runner.Config.WorkDir, reg) || st.Mode().Perm() != 0o600 {
		t.Fatalf("registered torrent %s must be private inside work_dir: %v", reg, err)
	}
	if strings.Contains(result.Comparison.Report(), zuleatest.PasskeySentinel) {
		t.Fatal("comparison report must not expose the passkey")
	}
	if !strings.Contains(preview.String(), "gotowość do canary: TAK") {
		t.Fatalf("preview:\n%s", preview.String())
	}
	data, err := os.ReadFile(result.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	var receipt Receipt
	if err := json.Unmarshal(data, &receipt); err != nil || receipt.State != ReceiptAccepted || receipt.DataMatch == nil || !*receipt.DataMatch {
		t.Fatalf("receipt %s", data)
	}
	if strings.Contains(string(data), pttKey) || strings.Contains(string(data), "torrent/download") {
		t.Fatal("receipt must not contain the key or the private download URL")
	}
	// A second upload of the same release is refused without any request.
	_, err = runner.Upload(context.Background(), UploadRequest{
		Release:     zuleatest.Release,
		Fingerprint: fp,
		Preview:     io.Discard,
		Confirm:     typed(ConfirmationPhrase(fp)),
	})
	if !errors.Is(err, ErrUploadRefused) || fake.uploads != 1 {
		t.Fatalf("repeat upload must be refused: %v uploads=%d", err, fake.uploads)
	}
}

func TestUploadRefusals(t *testing.T) {
	cases := map[string]func(cfg *Config, fp string) UploadRequest{
		"wrong phrase": func(_ *Config, fp string) UploadRequest {
			return UploadRequest{
				Release:     zuleatest.Release,
				Fingerprint: fp,
				Preview:     io.Discard,
				Confirm:     typed("TAK"),
			}
		},
		"stale fingerprint": func(_ *Config, _ string) UploadRequest {
			other := strings.Repeat("0", 64)
			return UploadRequest{
				Release:     zuleatest.Release,
				Fingerprint: other,
				Preview:     io.Discard,
				Confirm:     typed(ConfirmationPhrase(other)),
			}
		},
		"short fingerprint": func(_ *Config, fp string) UploadRequest {
			return UploadRequest{
				Release:     zuleatest.Release,
				Fingerprint: fp[:16],
				Preview:     io.Discard,
				Confirm:     typed("x"),
			}
		},
		"no confirm": func(_ *Config, fp string) UploadRequest {
			return UploadRequest{
				Release:     zuleatest.Release,
				Fingerprint: fp,
				Preview:     io.Discard,
			}
		},
		"gate closed": func(cfg *Config, fp string) UploadRequest {
			cfg.PTT.ModQueue = false
			return UploadRequest{
				Release:     zuleatest.Release,
				Fingerprint: fp,
				Preview:     io.Discard,
				Confirm:     typed(ConfirmationPhrase(fp)),
			}
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			_, fake, runner := uploadSetup(t)
			fp := dryRunFingerprint(t, runner)
			req := build(&runner.Config, fp)
			_, err := runner.Upload(context.Background(), req)
			if !errors.Is(err, ErrUploadRefused) || fake.uploads != 0 {
				t.Fatalf("expected refusal without upload, got %v uploads=%d", err, fake.uploads)
			}
			if _, statErr := os.Stat(runner.receiptPath(zuleatest.Release)); statErr == nil {
				t.Fatal("a refused upload must not leave a receipt")
			}
		})
	}
}

func TestUploadRejectedIsRecordedAndNotRetried(t *testing.T) {
	_, fake, runner := uploadSetup(t)
	fp := dryRunFingerprint(t, runner)
	fake.respondErr = true
	req := UploadRequest{
		Release:     zuleatest.Release,
		Fingerprint: fp,
		Preview:     io.Discard,
		Confirm:     typed(ConfirmationPhrase(fp)),
	}
	result, err := runner.Upload(context.Background(), req)
	if err == nil || result.Receipt.State != ReceiptRejected || fake.uploads != 1 {
		t.Fatalf("rejected upload: %+v err=%v", result.Receipt, err)
	}
	if _, err := runner.Upload(context.Background(), req); !errors.Is(err, ErrUploadRefused) || fake.uploads != 1 {
		t.Fatalf("rejected upload must not be retried automatically: %v", err)
	}
}
