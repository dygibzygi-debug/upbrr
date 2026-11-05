// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/autobrr/upbrr/internal/zulea/pttfetch"
)

// nightlyState is the previous nightly run, stored under work_dir so the next
// run can warn only when an OK release changes. Standing STOPs stay in the
// summary without a warning.
type nightlyState struct {
	OK []string `json:"ok"`
}

func (r *Runner) nightlyStatePath() string {
	return filepath.Join(r.Config.WorkDir, "nightly", "state.json")
}

func (r *Runner) readNightlyState() (nightlyState, bool, error) {
	data, err := os.ReadFile(r.nightlyStatePath())
	if errors.Is(err, os.ErrNotExist) {
		return nightlyState{}, false, nil
	}
	if err != nil {
		return nightlyState{}, false, fmt.Errorf("nightly state: %w", err)
	}
	var state nightlyState
	if err := json.Unmarshal(data, &state); err != nil {
		return nightlyState{}, false, fmt.Errorf("nightly state unreadable: %w", err)
	}
	return state, true, nil
}

func (r *Runner) writeNightlyState(state nightlyState) error {
	state.OK = append([]string(nil), state.OK...)
	slices.Sort(state.OK)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("nightly state: %w", err)
	}
	if err := writeAtomicPrivate(r.nightlyStatePath(), append(data, '\n')); err != nil {
		return fmt.Errorf("nightly state: %w", err)
	}
	return nil
}

// nightlyChainDelta compares this check with the previous OK set.
// A missing previous state is a baseline: standing STOPs are recorded and
// do not raise a warning.
func nightlyChainDelta(prev nightlyState, hasPrev bool, results []CheckResult) (lines []string, attention bool, next []string) {
	var stops []string
	okSet := map[string]struct{}{}
	for _, result := range results {
		switch result.Status {
		case "OK":
			next = append(next, result.Release)
			okSet[result.Release] = struct{}{}
		case "STOP":
			stops = append(stops, result.Release)
		}
	}
	stopSet := map[string]struct{}{}
	for _, rel := range stops {
		stopSet[rel] = struct{}{}
	}
	if !hasPrev {
		if len(stops) > 0 {
			lines = append(lines, fmt.Sprintf("STOP zastane (pierwszy zapis stanu, bez ostrzeżenia): %d", len(stops)))
		}
		return lines, false, next
	}
	var newStops, gone []string
	for _, rel := range prev.OK {
		if _, ok := okSet[rel]; ok {
			continue
		}
		if _, stopped := stopSet[rel]; stopped {
			newStops = append(newStops, rel)
			continue
		}
		gone = append(gone, rel)
	}
	slices.Sort(newStops)
	slices.Sort(gone)
	if len(newStops) > 0 {
		attention = true
		lines = append(lines, "Nowy STOP (wcześniej OK): "+strings.Join(newStops, ", "))
	}
	if len(gone) > 0 {
		attention = true
		lines = append(lines, "Zniknęło z OK: "+strings.Join(gone, ", "))
	}
	if !attention && len(stops) > 0 {
		lines = append(lines, fmt.Sprintf("STOP bez zmian względem poprzedniego przebiegu: %d", len(stops)))
	}
	return lines, attention, next
}

// nightlyReceiptAlert returns a warning reason for a receipt that newly needs
// a person: unreadable, unresolved, or accepted and still not approved after
// 24 hours. A fresh accepted upload waiting for moderation is not a warning.
func nightlyReceiptAlert(rc Receipt, now time.Time) string {
	switch rc.State {
	case ReceiptNotSent:
		return ""
	case ReceiptUnreadable:
		return "nieczytelne"
	case ReceiptAccepted:
		if rc.Moderation == pttfetch.StateApproved {
			return ""
		}
		finished, err := time.Parse(time.RFC3339, rc.FinishedAt)
		if err != nil {
			return ""
		}
		if now.Sub(finished) > 24*time.Hour {
			return "moderacja dłużej niż 24h"
		}
		return ""
	case ReceiptRejected:
		if rc.acknowledgedRejection() {
			return ""
		}
		return "nierozstrzygnięte"
	case ReceiptAttempted, ReceiptUncertain:
		return "nierozstrzygnięte"
	default:
		if rc.State == "" {
			return "nieczytelne"
		}
		return "nierozstrzygnięte"
	}
}
