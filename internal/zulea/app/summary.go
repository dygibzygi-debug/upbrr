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
	var lines []string
	attention := false
	lines = append(lines, "Zulea uploader — "+time.Now().UTC().Format("2006-01-02 15:04 UTC"))
	dir := filepath.Join(r.Config.WorkDir, "reports")
	if path, err := latestReport(dir, "check-", ".json"); err == nil {
		var c CheckSummary
		if readJSON(path, &c) == nil {
			lines = append(lines, fmt.Sprintf("Łańcuch: OK=%d STOP=%d (pominięte %d)", c.OK, c.Stop, c.Skipped))
			attention = attention || c.Stop > 0
		}
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
			attention = attention || s.Counts["REVIEW"] > 0 || s.Counts["FAILED"] > 0
		}
	}
	receipts, err := r.Receipts()
	if err != nil {
		return Summary{}, err
	}
	states := map[string]int{}
	for _, rc := range receipts {
		states[rc.State]++
	}
	lines = append(lines, fmt.Sprintf("Uploady: accepted=%d rejected=%d uncertain=%d attempted=%d",
		states[ReceiptAccepted], states[ReceiptRejected], states[ReceiptUncertain], states[ReceiptAttempted]))
	attention = attention || states[ReceiptUncertain] > 0 || states[ReceiptAttempted] > 0 || states[ReceiptRejected] > 0
	if waiting, err := r.awaitingApproval(); err == nil && len(waiting) > 0 {
		lines = append(lines, fmt.Sprintf("Czeka na zatwierdzenie PTT: %d (kolejka wstrzymana)", len(waiting)))
		attention = true
	}
	if entries, _, err := r.Queue(); err == nil {
		lines = append(lines, fmt.Sprintf("Kolejka: %d wydań gotowych", len(entries)))
	}
	if client != nil {
		results, err := r.SeedStatus(ctx, client)
		if err != nil {
			lines = append(lines, "Seed: nie udało się sprawdzić qBittorrenta")
			attention = true
		} else {
			bad := 0
			for _, s := range results {
				if s.Seed != SeedSeeding {
					bad++
				}
			}
			lines = append(lines, fmt.Sprintf("Seed: %d/%d seeduje", len(results)-bad, len(results)))
			attention = attention || bad > 0
		}
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
