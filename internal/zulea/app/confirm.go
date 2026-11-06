// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/autobrr/upbrr/internal/zulea/dupeaccept"
)

const confirmationMaxAge = 5 * time.Minute

// ConfirmationFile is the one-shot file the panel adapter writes. The CLI
// checks it instead of reading a terminal. It never skips an upload gate.
type ConfirmationFile struct {
	Command     string `json:"command"`
	Release     string `json:"release"`
	Fingerprint string `json:"fingerprint,omitempty"`
	SHA         string `json:"sha,omitempty"`
	State       string `json:"state,omitempty"`
	Phrase      string `json:"phrase"`
	Who         string `json:"who,omitempty"`
	IP          string `json:"ip,omitempty"`
	At          string `json:"at,omitempty"`
	Session     string `json:"session,omitempty"`
}

// Stamp returns the identity stored on a receipt. Channel is panel.
func (f ConfirmationFile) Stamp() ConfirmationStamp {
	return ConfirmationStamp{
		Channel: ChannelPanel,
		Who:     f.Who,
		IP:      f.IP,
		Session: f.Session,
	}
}

// ConfirmationExpect is what the command in hand must match.
type ConfirmationExpect struct {
	Command     string
	Release     string
	Fingerprint string
	SHA         string
	State       string
	Phrase      string
}

// SeedStartPhrase is what the operator types for seed --start-verified.
// The 12 hex characters are the start of the PTT infohash.
func SeedStartPhrase(infoHash string) (string, error) {
	hash := strings.ToLower(strings.TrimSpace(infoHash))
	if len(hash) != 40 || strings.Trim(hash, "0123456789abcdef") != "" {
		return "", errors.New("seed confirmation: infohash must be 40 lowercase hex characters")
	}
	return "SEEDUJE " + hash[:12], nil
}

// takePhrase returns the typed phrase and the stamp to store. A confirmation
// file is consumed only after the command has computed the phrase it expects.
func takePhrase(path string, expect ConfirmationExpect, prompt string, confirm func(string) (string, error)) (string, ConfirmationStamp, error) {
	if path != "" {
		file, err := ConsumeConfirmation(path, expect, time.Now())
		if err != nil {
			return "", ConfirmationStamp{}, err
		}
		return file.Phrase, file.Stamp(), nil
	}
	if confirm == nil {
		return "", ConfirmationStamp{}, errors.New("interactive confirmation is required")
	}
	typed, err := confirm(prompt)
	if err != nil {
		return "", ConfirmationStamp{}, err
	}
	return typed, ConfirmationStamp{Channel: ChannelTerminal}, nil
}

// ConsumeConfirmation checks path and renames it to .used-… before the
// command runs. A second use fails because the original path is gone.
func ConsumeConfirmation(path string, expect ConfirmationExpect, now time.Time) (ConfirmationFile, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return ConfirmationFile{}, fmt.Errorf("confirmation file: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ConfirmationFile{}, errors.New("confirmation file must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return ConfirmationFile{}, fmt.Errorf("confirmation file mode %04o is too open (0600 required)", info.Mode().Perm())
	}
	if err := confirmOwner(info); err != nil {
		return ConfirmationFile{}, err
	}
	age := now.Sub(info.ModTime())
	if age > confirmationMaxAge || info.ModTime().After(now.Add(time.Minute)) {
		return ConfirmationFile{}, errors.New("confirmation file is older than 5 minutes or not yet valid")
	}
	if strings.HasPrefix(filepath.Base(path), ".used-") {
		return ConfirmationFile{}, errors.New("confirmation file was already used")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ConfirmationFile{}, fmt.Errorf("confirmation file: %w", err)
	}
	if len(data) > 8192 {
		return ConfirmationFile{}, errors.New("confirmation file is too large")
	}
	var file ConfirmationFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return ConfirmationFile{}, fmt.Errorf("confirmation file: %w", err)
	}
	if err := file.matches(expect); err != nil {
		return ConfirmationFile{}, err
	}
	used := filepath.Join(filepath.Dir(path), ".used-"+filepath.Base(path)+"-"+now.UTC().Format("20060102T150405Z"))
	if err := os.Rename(path, used); err != nil {
		return ConfirmationFile{}, fmt.Errorf("confirmation file: %w", err)
	}
	return file, nil
}

func (f ConfirmationFile) matches(expect ConfirmationExpect) error {
	if f.Command == "" || f.Release == "" || strings.TrimSpace(f.Phrase) == "" {
		return errors.New("confirmation file needs command, release and phrase")
	}
	if f.Command != expect.Command || f.Release != expect.Release {
		return errors.New("confirmation file command or release does not match")
	}
	switch f.Command {
	case "upload":
		if len(f.Fingerprint) < 12 || f.Fingerprint != expect.Fingerprint || f.Phrase != expect.Phrase || f.Phrase != ConfirmationPhrase(f.Fingerprint) {
			return errors.New("confirmation file fingerprint or phrase does not match")
		}
	case "dupe-accept":
		accept, aerr := dupeaccept.AcceptPhrase(f.Release, f.SHA)
		revoke, rerr := dupeaccept.RevokePhrase(f.Release, f.SHA)
		bound := (aerr == nil && f.Phrase == accept) || (rerr == nil && f.Phrase == revoke)
		if !bound || f.SHA != expect.SHA || f.Phrase != expect.Phrase {
			return errors.New("confirmation file candidate sha or phrase does not match")
		}
	case "resolve":
		if f.State != expect.State || !strings.EqualFold(f.SHA, expect.SHA) || f.Phrase != expect.Phrase {
			return errors.New("confirmation file receipt sha or phrase does not match")
		}
		if len(f.SHA) < 12 || !strings.HasSuffix(f.Phrase, strings.ToLower(f.SHA[:12])) {
			return errors.New("confirmation file phrase is not bound to the receipt sha")
		}
	case "seed":
		phrase, err := SeedStartPhrase(f.SHA)
		if err != nil || !strings.EqualFold(f.SHA, expect.SHA) || f.Phrase != phrase || f.Phrase != expect.Phrase {
			return errors.New("confirmation file infohash or phrase does not match")
		}
	default:
		return errors.New("confirmation file command is not allowed")
	}
	return nil
}
