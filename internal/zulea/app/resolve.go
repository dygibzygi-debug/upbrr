// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/autobrr/upbrr/internal/zulea/pttfetch"
)

// ReceiptUnreadable marks a receipt file that exists but cannot be decoded.
const ReceiptUnreadable = "unreadable"

// ErrQueueBlocked stops the queue and the canary gate until every earlier
// attempt has a known, settled outcome.
var ErrQueueBlocked = errors.New("queue paused: an earlier upload needs attention")

// Resolution records one human decision about a receipt.
type Resolution struct {
	From  string `json:"from"`
	To    string `json:"to"`
	PTTID string `json:"ptt_id,omitempty"`
	Note  string `json:"note"`
	At    string `json:"at"`
}

func (rc Receipt) acknowledgedRejection() bool {
	n := len(rc.Resolutions)
	return rc.State == ReceiptRejected && n > 0 && rc.Resolutions[n-1].To == ReceiptRejected
}

// blockReason explains why a receipt blocks the next upload, or "" when it does not.
// Only a proven not_sent, an APPROVED accepted upload and a rejection the human
// acknowledged with resolve are settled.
func blockReason(rc Receipt) string {
	switch rc.State {
	case ReceiptNotSent:
		return ""
	case ReceiptAccepted:
		if rc.Moderation == pttfetch.StateApproved {
			return ""
		}
		moderation := rc.Moderation
		if moderation == "" {
			moderation = "niesprawdzona"
		}
		return "przyjęty, moderacja " + moderation + " — uruchom ptt-status; odrzucony lub usunięty: resolve --state rejected"
	case ReceiptRejected:
		if rc.acknowledgedRejection() {
			return ""
		}
		return "odrzucony — wymaga decyzji: resolve --state rejected"
	case ReceiptAttempted, ReceiptUncertain:
		return "wynik nieznany (" + rc.State + ") — sprawdź PTT ręcznie, potem resolve --state not_sent|accepted|rejected"
	default:
		return "pokwitowanie nieczytelne lub w nieznanym stanie (" + rc.State + ") — sprawdź PTT ręcznie, potem resolve"
	}
}

// queueBlockers lists every receipt that blocks the queue and the canary gate.
func (r *Runner) queueBlockers() ([]string, error) {
	receipts, err := r.Receipts()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, rc := range receipts {
		if reason := blockReason(rc); reason != "" {
			out = append(out, rc.Release+": "+reason)
		}
	}
	return out, nil
}

// ResolveRequest is one human decision about an unsettled receipt.
type ResolveRequest struct {
	Release string
	// State is the outcome the human verified on PTT: not_sent, accepted or rejected.
	State string
	// PTTID is required for accepted: the torrent id seen on PTT.
	PTTID string
	// Note says how the outcome was verified. It is stored in the receipt.
	Note    string
	Preview io.Writer
	Confirm func(prompt string) (string, error)
}

var pttIDPattern = regexp.MustCompile(`^[0-9]{1,12}$`)

const maxResolveNote = 500

// ResolvePhrase is what the human types to settle a receipt. The token binds
// the decision to the exact receipt bytes that were shown.
func ResolvePhrase(state string, receiptBytes []byte) string {
	sum := sha256.Sum256(receiptBytes)
	return "ROZSTRZYGAM " + state + " " + hex.EncodeToString(sum[:])[:12]
}

func allowedResolution(from, to string, rc Receipt) bool {
	switch from {
	case ReceiptAttempted, ReceiptUncertain, ReceiptUnreadable:
		return to == ReceiptNotSent || to == ReceiptAccepted || to == ReceiptRejected
	case ReceiptRejected:
		return to == ReceiptRejected && !rc.acknowledgedRejection()
	case ReceiptAccepted:
		return to == ReceiptRejected && rc.Moderation != pttfetch.StateApproved
	default:
		return false
	}
}

// Resolve settles one receipt after the human checked PTT. It never contacts a
// tracker and never uploads. It requires the typed phrase bound to the shown
// receipt and keeps the full decision history in the receipt.
func (r *Runner) Resolve(req ResolveRequest) (Receipt, error) {
	if req.Confirm == nil || req.Preview == nil {
		return Receipt{}, refuse("interactive confirmation is required")
	}
	if !validName(req.Release) {
		return Receipt{}, refuse("invalid release name")
	}
	note := strings.TrimSpace(req.Note)
	if note == "" || len(note) > maxResolveNote {
		return Receipt{}, refuse("--note is required (how the outcome was verified, at most %d bytes)", maxResolveNote)
	}
	switch req.State {
	case ReceiptNotSent, ReceiptRejected:
		if req.PTTID != "" {
			return Receipt{}, refuse("--ptt-id is only valid with --state accepted")
		}
	case ReceiptAccepted:
		if !pttIDPattern.MatchString(req.PTTID) {
			return Receipt{}, refuse("--state accepted requires the numeric --ptt-id seen on PTT")
		}
	default:
		return Receipt{}, refuse("--state must be not_sent, accepted or rejected")
	}
	path := r.receiptPath(req.Release)
	data, err := os.ReadFile(path)
	if err != nil {
		return Receipt{}, refuse("no readable receipt for %s: %v", req.Release, err)
	}
	var current Receipt
	var from string
	if json.Unmarshal(data, &current) != nil || current.State == "" {
		from = ReceiptUnreadable
		current = Receipt{Release: req.Release, Tracker: "PTT"}
	} else {
		from = current.State
	}
	if !allowedResolution(from, req.State, current) {
		return Receipt{}, refuse("receipt state %s (moderation %q) cannot be resolved as %s", from, current.Moderation, req.State)
	}
	_, _ = fmt.Fprintf(req.Preview, "Pokwitowanie %s:\n%s\n", path, strings.TrimSpace(string(data)))
	phrase := ResolvePhrase(req.State, data)
	typed, err := req.Confirm(fmt.Sprintf("Zmienić stan %s z %s na %s? Wpisz dokładnie: %s\n> ", req.Release, from, req.State, phrase))
	if err != nil {
		return Receipt{}, refuse("confirmation failed: %v", err)
	}
	if strings.TrimSpace(typed) != phrase {
		return Receipt{}, refuse("confirmation phrase did not match; nothing was changed")
	}
	if from == ReceiptUnreadable {
		// Keep the unreadable bytes as evidence; Receipts() ignores this name.
		evidence := path + ".unreadable-" + time.Now().UTC().Format("20060102T150405Z")
		//nolint:gosec // Receipt path is work_dir/receipts/<validated release name>.
		if err := os.WriteFile(evidence, data, 0o600); err != nil {
			return Receipt{}, fmt.Errorf("zulea: keep unreadable receipt: %w", err)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	next := current
	next.State = req.State
	next.Resolutions = append(append([]Resolution(nil), current.Resolutions...), Resolution{
		From:  from,
		To:    req.State,
		PTTID: req.PTTID,
		Note:  note,
		At:    now,
	})
	if req.State == ReceiptAccepted {
		next.TorrentID = req.PTTID
		next.Moderation = ""
		next.ModerationCheckedAt = ""
	}
	// The file must still hold exactly the bytes the human confirmed.
	again, err := os.ReadFile(path)
	if err != nil || string(again) != string(data) {
		return Receipt{}, refuse("the receipt changed during confirmation; nothing was changed")
	}
	if err := writeReceipt(path, next, false); err != nil {
		return Receipt{}, fmt.Errorf("zulea: resolve: %w", err)
	}
	return next, nil
}
