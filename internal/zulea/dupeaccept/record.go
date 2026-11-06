// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package dupeaccept records a human decision to let one duplicate search
// result through the Zulea canary gate. It does not search a tracker and it
// does not upload.
package dupeaccept

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/autobrr/upbrr/internal/zulea/names"
	"github.com/autobrr/upbrr/internal/zulea/pttdupe"
	"github.com/autobrr/upbrr/pkg/api"
)

const (
	// ActionAccept records that the human let this candidate set through.
	ActionAccept = "accept"
	// ActionRevoke records that the human withdrew an earlier acceptance.
	ActionRevoke = "revoke"
	// MaxReasonBytes is the maximum stored length of --reason.
	MaxReasonBytes = 500
)

var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Decision is one appended human decision. A new decision is never a
// replacement of the earlier ones.
type Decision struct {
	Action       string              `json:"action"`
	At           string              `json:"at"`
	Reason       string              `json:"reason"`
	CandidateSHA string              `json:"candidate_sha"`
	Candidates   []pttdupe.Candidate `json:"candidates"`
	Status       string              `json:"status"`
	SourceReport string              `json:"source_report,omitempty"`
	Version      string              `json:"version"`
	Channel      string              `json:"channel,omitempty"`
	Who          string              `json:"who,omitempty"`
	IP           string              `json:"ip,omitempty"`
	Session      string              `json:"session,omitempty"`
}

// Record is work/dupe-accept/<REL>.ptt.json. The top-level fields mirror the
// latest decision; History is the source of truth.
type Record struct {
	Release      string              `json:"release"`
	Version      string              `json:"version"`
	Action       string              `json:"action"`
	At           string              `json:"at"`
	Reason       string              `json:"reason"`
	CandidateSHA string              `json:"candidate_sha"`
	Status       string              `json:"status"`
	Candidates   []pttdupe.Candidate `json:"candidates"`
	SourceReport string              `json:"source_report,omitempty"`
	Channel      string              `json:"channel,omitempty"`
	Who          string              `json:"who,omitempty"`
	IP           string              `json:"ip,omitempty"`
	Session      string              `json:"session,omitempty"`
	History      []Decision          `json:"history"`
}

// Active returns the latest decision when it is an acceptance.
func (r *Record) Active() *Decision {
	if r == nil || len(r.History) == 0 {
		return nil
	}
	last := r.History[len(r.History)-1]
	if last.Action != ActionAccept {
		return nil
	}
	return &last
}

// Append adds a decision and mirrors it on the record. History is kept.
func (r *Record) Append(d Decision) {
	d.Candidates = cloneCandidates(d.Candidates)
	r.History = append(r.History, d)
	r.Action = d.Action
	r.At = d.At
	r.Reason = d.Reason
	r.CandidateSHA = d.CandidateSHA
	r.Status = d.Status
	r.Candidates = cloneCandidates(d.Candidates)
	r.SourceReport = d.SourceReport
	r.Version = d.Version
	r.Channel = d.Channel
	r.Who = d.Who
	r.IP = d.IP
	r.Session = d.Session
}

// Path is the acceptance file for one release.
func Path(workDir, release string) string {
	return filepath.Join(workDir, "dupe-accept", release+".ptt.json")
}

// ReleaseShort is the 12 hex characters the human types for this release.
// It is the leading bytes of SHA-256 over the exact release name.
func ReleaseShort(release string) string {
	sum := sha256.Sum256([]byte(release))
	return hex.EncodeToString(sum[:])[:12]
}

// AcceptPhrase is `AKCEPTUJE <REL-skrót> <12 hex of the candidate-set SHA>`.
func AcceptPhrase(release, candidateSHA string) (string, error) {
	if !shaPattern.MatchString(candidateSHA) {
		return "", errors.New("dupe-accept: candidate sha must be 64 lowercase hex characters")
	}
	return "AKCEPTUJE " + ReleaseShort(release) + " " + candidateSHA[:12], nil
}

// RevokePhrase is `WYCOFUJE <REL-skrót> <12 hex>` bound to the acceptance
// being withdrawn.
func RevokePhrase(release, candidateSHA string) (string, error) {
	if !shaPattern.MatchString(candidateSHA) {
		return "", errors.New("dupe-accept: candidate sha must be 64 lowercase hex characters")
	}
	return "WYCOFUJE " + ReleaseShort(release) + " " + candidateSHA[:12], nil
}

// AcceptableStatus reports whether a search result may be accepted.
// FAILED and NOT_RUN are a hard refusal. Anything other than DUPLICATE or
// REVIEW is refused as well.
func AcceptableStatus(status string) error {
	switch status {
	case pttdupe.StatusDuplicate, pttdupe.StatusReview:
		return nil
	case pttdupe.StatusFailed, pttdupe.StatusNotRun:
		return fmt.Errorf("twarda odmowa: status %s", status)
	default:
		return fmt.Errorf("akceptacja dotyczy tylko DUPLICATE lub REVIEW (jest %s)", empty(status))
	}
}

// HardRefuse rejects a candidate set that contains our own group or an
// exact duplicate. No acceptance can override that.
func HardRefuse(candidates []pttdupe.Candidate) error {
	var problems []string
	for _, c := range candidates {
		label := c.ID
		if label == "" {
			label = c.Name
		}
		if containsOurGroup(c.Name) {
			problems = append(problems, "kandydat "+label+" ("+c.Name+") jest grupy -"+names.Group)
		}
		if strings.EqualFold(c.Relation, string(api.DupeRelationExactDuplicate)) {
			problems = append(problems, "kandydat "+label+" ("+c.Name+") ma relację exact_duplicate")
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("twarda odmowa: %s", strings.Join(problems, "; "))
}

// SearchRefused is a hard refusal when the search did not show every candidate.
// An incomplete search or an empty candidate list must not be accepted, and a
// stored acceptance of such a result must not open the gate.
func SearchRefused(searchComplete bool, candidates []pttdupe.Candidate) error {
	if !searchComplete {
		return errors.New("twarda odmowa: wyszukiwanie niekompletne")
	}
	if len(candidates) == 0 {
		return errors.New("twarda odmowa: pusta lista kandydatów")
	}
	return nil
}

func containsOurGroup(name string) bool {
	return strings.Contains(strings.ToLower(name), "-"+strings.ToLower(names.Group))
}

// hashCandidate is the canonical candidate document. Field order is the
// hash input: id, name, type and size first, then every other shown field,
// so a change of any of them changes the SHA.
type hashCandidate struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	SizeBytes  int64    `json:"size_bytes"`
	Resolution string   `json:"resolution"`
	Relation   string   `json:"relation"`
	Reasons    []string `json:"reasons"`
	Link       string   `json:"link"`
}

// SetSHA is SHA-256 of the candidate set sorted by id, name, type and size.
func SetSHA(candidates []pttdupe.Candidate) (string, error) {
	items := make([]hashCandidate, len(candidates))
	for i, c := range candidates {
		reasons := append([]string(nil), c.Reasons...)
		if reasons == nil {
			reasons = []string{}
		}
		items[i] = hashCandidate{
			ID:         c.ID,
			Name:       c.Name,
			Type:       c.Type,
			SizeBytes:  c.SizeBytes,
			Resolution: c.Resolution,
			Relation:   c.Relation,
			Reasons:    reasons,
			Link:       c.Link,
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].ID != items[j].ID {
			return items[i].ID < items[j].ID
		}
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		if items[i].Type != items[j].Type {
			return items[i].Type < items[j].Type
		}
		if items[i].SizeBytes != items[j].SizeBytes {
			return items[i].SizeBytes < items[j].SizeBytes
		}
		if items[i].Resolution != items[j].Resolution {
			return items[i].Resolution < items[j].Resolution
		}
		if items[i].Relation != items[j].Relation {
			return items[i].Relation < items[j].Relation
		}
		if reasonsKey(items[i].Reasons) != reasonsKey(items[j].Reasons) {
			return reasonsKey(items[i].Reasons) < reasonsKey(items[j].Reasons)
		}
		return items[i].Link < items[j].Link
	})
	data, err := json.Marshal(items)
	if err != nil {
		return "", fmt.Errorf("dupe-accept: candidate sha: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Load reads a record. A missing file is fs.ErrNotExist.
func Load(path string) (Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Record{}, fmt.Errorf("dupe-accept: %w", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var rec Record
	if err := dec.Decode(&rec); err != nil {
		return Record{}, fmt.Errorf("dupe-accept: parse: %w", err)
	}
	if dec.More() {
		return Record{}, errors.New("dupe-accept: trailing data")
	}
	if err := rec.validate(); err != nil {
		return Record{}, err
	}
	return rec, nil
}

func (r *Record) validate() error {
	if strings.TrimSpace(r.Release) == "" || len(r.History) == 0 {
		return errors.New("dupe-accept: record has no release or history")
	}
	for i, d := range r.History {
		if d.Action != ActionAccept && d.Action != ActionRevoke {
			return fmt.Errorf("dupe-accept: history[%d] action %q", i, d.Action)
		}
		if !shaPattern.MatchString(d.CandidateSHA) {
			return fmt.Errorf("dupe-accept: history[%d] candidate sha", i)
		}
		got, err := SetSHA(d.Candidates)
		if err != nil {
			return err
		}
		if got != d.CandidateSHA {
			return fmt.Errorf("dupe-accept: history[%d] candidate sha does not match the stored candidates", i)
		}
	}
	last := r.History[len(r.History)-1]
	if r.Action != last.Action || r.CandidateSHA != last.CandidateSHA || r.Reason != last.Reason ||
		r.At != last.At || r.Version != last.Version || r.Status != last.Status || r.SourceReport != last.SourceReport ||
		r.Channel != last.Channel || r.Who != last.Who || r.IP != last.IP || r.Session != last.Session {
		return errors.New("dupe-accept: record header does not match the latest decision")
	}
	got, err := SetSHA(r.Candidates)
	if err != nil {
		return err
	}
	if got != r.CandidateSHA {
		return errors.New("dupe-accept: record candidates do not match candidate_sha")
	}
	return nil
}

// Save replaces the record atomically (synced temporary file, mode 0600, rename).
func Save(path string, rec Record) error {
	if err := rec.validate(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("dupe-accept: directory: %w", err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("dupe-accept: encode: %w", err)
	}
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("dupe-accept: temp: %w", err)
	}
	tmp := file.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("dupe-accept: chmod: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return fmt.Errorf("dupe-accept: write: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("dupe-accept: sync: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("dupe-accept: close: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("dupe-accept: rename: %w", err)
	}
	syncDir(dir)
	return nil
}

func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

func cloneCandidates(in []pttdupe.Candidate) []pttdupe.Candidate {
	out := make([]pttdupe.Candidate, len(in))
	for i, c := range in {
		out[i] = c
		if c.Reasons == nil {
			out[i].Reasons = []string{}
			continue
		}
		out[i].Reasons = append([]string(nil), c.Reasons...)
	}
	return out
}

func reasonsKey(reasons []string) string { return strings.Join(reasons, "\x00") }

func empty(status string) string {
	if strings.TrimSpace(status) == "" {
		return "(brak)"
	}
	return status
}
