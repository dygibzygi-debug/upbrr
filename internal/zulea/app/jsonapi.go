// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/autobrr/upbrr/internal/zulea/pttplan"
	"github.com/autobrr/upbrr/internal/zulea/seed"
)

// SchemaVersion is the stable --json document version.
const SchemaVersion = 1

var (
	jsonAnnounceRe = regexp.MustCompile(`(?i)https?://[^"\\\s]*announce[^"\\\s]*`)
	jsonDeleteRe   = regexp.MustCompile(`(?i)https?://[^"\\\s]*/delete[^"\\\s]*`)
)

// HistoryView is the --json body of history.
type HistoryView struct {
	Count    int       `json:"count"`
	Receipts []Receipt `json:"receipts"`
}

// QueueView is the --json body of queue.
type QueueView struct {
	Entries []QueueEntry `json:"entries"`
	Scan    string       `json:"scan,omitempty"`
	Blocked string       `json:"blocked,omitempty"`
}

// JSONDocument is the only stdout shape when a command is run with --json.
type JSONDocument struct {
	SchemaVersion int    `json:"schema_version"`
	Command       string `json:"command"`
	OK            bool   `json:"ok"`
	ExitCode      int    `json:"exit_code"`
	Error         string `json:"error,omitempty"`
	Result        any    `json:"result,omitempty"`
}

// WritePublicJSON writes one document. mask hides known secrets. Paths of
// torrents that carry a passkey are omitted before encoding. Announce URLs
// and delete_url values are masked in the encoded text.
func WritePublicJSON(w io.Writer, command string, exitCode int, result any, err error, mask func(string) string) error {
	doc := JSONDocument{
		SchemaVersion: SchemaVersion,
		Command:       command,
		OK:            err == nil,
		ExitCode:      exitCode,
		Result:        publicValue(result),
	}
	if err != nil {
		msg := err.Error()
		if mask != nil {
			msg = mask(msg)
		}
		doc.Error = scrubJSONText(msg, nil)
		doc.OK = false
	}
	data, merr := json.MarshalIndent(doc, "", "  ")
	if merr != nil {
		return fmt.Errorf("json: %w", merr)
	}
	text := string(data)
	if mask != nil {
		text = mask(text)
	}
	text = scrubJSONText(text, nil)
	if _, werr := io.WriteString(w, text+"\n"); werr != nil {
		return fmt.Errorf("json: %w", werr)
	}
	return nil
}

func scrubJSONText(text string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "<MASKED>")
		}
	}
	text = jsonAnnounceRe.ReplaceAllString(text, "<MASKED>")
	text = jsonDeleteRe.ReplaceAllString(text, "<MASKED>")
	return text
}

func publicValue(result any) any {
	switch v := result.(type) {
	case nil:
		return nil
	case Outcome:
		return publicOutcome(v)
	case []Outcome:
		out := make([]Outcome, len(v))
		for i := range v {
			out[i] = publicOutcome(v[i])
		}
		return out
	case Receipt:
		return publicReceipt(v)
	case []Receipt:
		out := make([]Receipt, len(v))
		for i := range v {
			out[i] = publicReceipt(v[i])
		}
		return out
	case HistoryView:
		for i := range v.Receipts {
			v.Receipts[i] = publicReceipt(v.Receipts[i])
		}
		return v
	case seed.Result:
		return v
	default:
		return v
	}
}

func publicOutcome(o Outcome) Outcome {
	if o.Plan != nil {
		plan := *o.Plan
		plan.Files = publicFiles(o.Plan.Files)
		o.Plan = &plan
	}
	if o.Acceptance != nil {
		acc := *o.Acceptance
		acc.Path = filepath.Base(acc.Path)
		o.Acceptance = &acc
	}
	o.ReportPath = filepath.Base(o.ReportPath)
	return o
}

func publicFiles(files []pttplan.File) []pttplan.File {
	out := make([]pttplan.File, len(files))
	for i, file := range files {
		file.Path = filepath.Base(file.Path)
		out[i] = file
	}
	return out
}

func publicReceipt(rc Receipt) Receipt {
	rc.RegisteredTorrent = ""
	return rc
}
