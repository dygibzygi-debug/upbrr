// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package pttplan

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/autobrr/upbrr/internal/metadata/mediainfo"
	"github.com/autobrr/upbrr/pkg/api"
)

// UpbrrMediaInfo renders MediaInfo with upbrr's own analyzer (go-mediainfo).
// The MKV is only read; reports are written below TempRoot, which must be
// outside the library. "Complete name" is reduced to the file name.
type UpbrrMediaInfo struct {
	TempRoot string
	Logger   api.Logger
}

// MediaInfoText implements MediaInfoSource.
func (u UpbrrMediaInfo) MediaInfoText(ctx context.Context, mkvPath string, _ string) (string, error) {
	if u.TempRoot == "" {
		return "", errors.New("pttplan: MediaInfo temp root is required")
	}
	result, err := mediainfo.NewService(u.Logger, nil).Export(ctx, mediainfo.Request{
		SourcePath: mkvPath,
		VideoPath:  mkvPath,
		TempRoot:   filepath.Clean(u.TempRoot),
		Release:    api.ReleaseInfo{},
	})
	if err != nil {
		return "", fmt.Errorf("pttplan: MediaInfo export: %w", err)
	}
	if result.TextPath == "" {
		return "", errors.New("pttplan: MediaInfo export produced no text report")
	}
	return result.TextPath, nil
}

// MaxMediaInfoBytes bounds a MediaInfo text report.
const MaxMediaInfoBytes = 4 << 20

// CLIMediaInfo runs the MediaInfo command-line tool (decision D-6: the same
// text as manual uploads). The MKV is only read; the report is written to
// OutputDir with mode 0600, never into the library. "Complete name" is
// reduced to the file name, like upbrr's own MediaInfo cleanup.
type CLIMediaInfo struct {
	// Binary is the mediainfo executable (absolute path recommended).
	Binary    string
	OutputDir string
	// Run executes the binary; nil uses os/exec (tests substitute it).
	Run func(ctx context.Context, binary string, args ...string) ([]byte, error)
}

// MediaInfoText implements MediaInfoSource.
func (c CLIMediaInfo) MediaInfoText(ctx context.Context, mkvPath string, release string) (string, error) {
	if strings.TrimSpace(c.Binary) == "" || strings.TrimSpace(c.OutputDir) == "" {
		return "", errors.New("pttplan: mediainfo binary and output directory are required")
	}
	run := c.Run
	if run == nil {
		run = runCommand
	}
	out, err := run(ctx, c.Binary, "--Output=Text", mkvPath)
	if err != nil {
		return "", fmt.Errorf("pttplan: mediainfo CLI failed: %w", err)
	}
	if len(out) > MaxMediaInfoBytes {
		return "", errors.New("pttplan: mediainfo output too large")
	}
	text, err := normalizeMediaInfo(string(out), mkvPath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(c.OutputDir, 0o700); err != nil {
		return "", fmt.Errorf("pttplan: mediainfo output directory: %w", err)
	}
	path := filepath.Join(c.OutputDir, release+".mediainfo.txt")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", fmt.Errorf("pttplan: write mediainfo: %w", err)
	}
	return path, nil
}

func runCommand(ctx context.Context, binary string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = []string{"LC_ALL=C.UTF-8", "LANG=C.UTF-8"}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("run %s: %w", filepath.Base(binary), err)
	}
	return out, nil
}

// FileMediaInfo reads a ready MediaInfo text report <Dir>/<REL>.mediainfo.txt
// (prepared outside the library, e.g. by the Remux Panel). The report must
// describe exactly this MKV: its "Complete name" is the MKV file name.
type FileMediaInfo struct {
	Dir string
}

// MediaInfoText implements MediaInfoSource.
func (f FileMediaInfo) MediaInfoText(_ context.Context, mkvPath string, release string) (string, error) {
	if strings.TrimSpace(f.Dir) == "" {
		return "", errors.New("pttplan: mediainfo directory is required")
	}
	path := filepath.Join(f.Dir, release+".mediainfo.txt")
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("pttplan: mediainfo file: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > MaxMediaInfoBytes {
		return "", errors.New("pttplan: mediainfo file must be a regular file within the size limit")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("pttplan: read mediainfo file: %w", err)
	}
	text, err := normalizeMediaInfo(string(data), mkvPath)
	if err != nil {
		return "", err
	}
	if text != string(data) {
		return "", errors.New("pttplan: mediainfo file must already be normalized (LF line endings, bare MKV file name as Complete name)")
	}
	return path, nil
}

// normalizeMediaInfo validates a MediaInfo text report for mkvPath and reduces
// "Complete name" to the file name. It fails when the report names another file.
func normalizeMediaInfo(text string, mkvPath string) (string, error) {
	if !utf8.ValidString(text) {
		return "", errors.New("pttplan: mediainfo text is not UTF-8")
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	base := filepath.Base(mkvPath)
	lines := strings.Split(text, "\n")
	general, names := false, 0
	for i, line := range lines {
		if strings.TrimSpace(line) == "General" {
			general = true
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(field) != "Complete name" {
			continue
		}
		names++
		if filepath.Base(strings.TrimSpace(value)) != base {
			return "", fmt.Errorf("pttplan: mediainfo describes %q, not the release MKV", filepath.Base(strings.TrimSpace(value)))
		}
		lines[i] = field + ": " + base
	}
	if !general || names != 1 {
		return "", errors.New("pttplan: mediainfo text must have a General section with exactly one Complete name")
	}
	return strings.Join(lines, "\n"), nil
}
