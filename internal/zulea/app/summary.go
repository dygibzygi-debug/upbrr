// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/autobrr/upbrr/internal/zulea/notify"
	"github.com/autobrr/upbrr/internal/zulea/seed"
)

// Summary is a short, secret-free status digest for notifications.
type Summary struct {
	Text string
	// Attention is true when something needs a human (STOP, review, seed problems, waiting approval).
	Attention bool
}

// BuildSummary condenses the latest check and scan reports, receipts,
// moderation and (when client is non-nil) seeding state.
func (r *Runner) BuildSummary(ctx context.Context, client seed.Client) (Summary, error) {
	now := time.Now().UTC()
	var lines []string
	attention := false
	lines = append(lines, "Zulea uploader — "+now.Format("2006-01-02 15:04 UTC"))
	dir := filepath.Join(r.Config.WorkDir, "reports")
	var checkResults []CheckResult
	checkLoaded := false
	if path, err := latestReport(dir, "check-", ".json"); err == nil {
		var c CheckSummary
		if readJSON(path, &c) == nil {
			checkLoaded = true
			checkResults = c.Results
			lines = append(lines, fmt.Sprintf("Łańcuch: OK=%d STOP=%d (pominięte %d)", c.OK, c.Stop, c.Skipped))
		}
	}
	prev, hasPrev, err := r.readNightlyState()
	if err != nil {
		return Summary{}, err
	}
	if checkLoaded {
		delta, chainAttention, nextOK := nightlyChainDelta(prev, hasPrev, checkResults)
		lines = append(lines, delta...)
		attention = attention || chainAttention
		prev.OK = nextOK
	}
	if path, err := latestReport(dir, "scan-", ".json"); err == nil {
		var s ScanSummary
		if readJSON(path, &s) == nil {
			keys := make([]string, 0, len(s.Counts))
			for k := range s.Counts {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			parts := make([]string, 0, len(keys))
			for _, k := range keys {
				parts = append(parts, fmt.Sprintf("%s=%d", k, s.Counts[k]))
			}
			lines = append(lines, "PTT: "+strings.Join(parts, " "))
		}
	}
	receipts, err := r.Receipts()
	if err != nil {
		return Summary{}, err
	}
	states := map[string]int{}
	for _, rc := range receipts {
		states[rc.State]++
		if why := nightlyReceiptAlert(rc, now); why != "" {
			attention = true
			lines = append(lines, fmt.Sprintf("Pokwitowanie %s: %s", rc.Release, why))
		}
	}
	lines = append(lines, fmt.Sprintf("Uploady: accepted=%d rejected=%d uncertain=%d attempted=%d",
		states[ReceiptAccepted], states[ReceiptRejected], states[ReceiptUncertain], states[ReceiptAttempted]))
	if blocked, berr := r.queueBlockers(); berr != nil {
		attention = true
		lines = append(lines, "Pokwitowania: nie można odczytać listy")
	} else if len(blocked) > 0 {
		lines = append(lines, fmt.Sprintf("Kolejka wstrzymana: %d pokwitowań wymaga uwagi (receipts, ptt-status, resolve)", len(blocked)))
	}
	if entries, _, qerr := r.Queue(); qerr == nil {
		lines = append(lines, fmt.Sprintf("Kolejka: %d wydań gotowych", len(entries)))
	}
	if client != nil {
		results, serr := r.SeedStatus(ctx, client)
		if serr != nil {
			lines = append(lines, "Seed: błąd seed-status")
			attention = true
		} else {
			bad, seedErr := 0, 0
			for _, s := range results {
				if s.Seed != SeedSeeding {
					bad++
				}
				if s.Seed == SeedError || s.Error != "" {
					seedErr++
				}
			}
			lines = append(lines, fmt.Sprintf("Seed: %d/%d seeduje", len(results)-bad, len(results)))
			if seedErr > 0 {
				attention = true
				lines = append(lines, fmt.Sprintf("Seed: błąd przy %d wydaniach", seedErr))
			}
		}
	}
	if err := r.writeNightlyState(prev); err != nil {
		return Summary{}, err
	}
	if attention {
		lines = append(lines, "Wymaga uwagi — szczegóły w raportach work/reports.")
	}
	return Summary{Text: strings.Join(lines, "\n"), Attention: attention}, nil
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return nil
}

// Notify sends text to the configured channels.
func (r *Runner) Notify(ctx context.Context, text string) (map[string]error, error) {
	n := r.Config.Notifications
	if n == nil {
		return nil, errors.New("notifications are not configured")
	}
	var channels []notify.Channel
	if n.DiscordWebhookFile != "" {
		url, err := readKeyFile(n.DiscordWebhookFile)
		if err != nil {
			return nil, fmt.Errorf("discord webhook: %w", err)
		}
		channels = append(channels, notify.Discord{WebhookURL: url})
	}
	if n.TelegramTokenFile != "" {
		token, err := readKeyFile(n.TelegramTokenFile)
		if err != nil {
			return nil, fmt.Errorf("telegram token: %w", err)
		}
		channels = append(channels, notify.Telegram{Token: token, ChatID: n.TelegramChatID})
	}
	return notify.SendAll(ctx, channels, text), nil
}
