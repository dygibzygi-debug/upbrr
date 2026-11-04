// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package pttplan

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

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
