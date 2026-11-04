// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestUploadCommandIsRefused(t *testing.T) {
	for _, cmd := range []string{"upload", "submit", "send"} {
		var out, errOut bytes.Buffer
		if code := run(context.Background(), []string{cmd}, &out, &errOut); code != exitUsage {
			t.Fatalf("%s: exit %d", cmd, code)
		}
		if !strings.Contains(errOut.String(), "tylko dry-run") {
			t.Fatalf("%s: %q", cmd, errOut.String())
		}
	}
}

func TestNamesCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"names", "Example.Film.2019.MULTi.1080p.REMUX.BluRay.AVC.DTS-HD.MA.5.1-Zulea"}, &out, &errOut)
	if code != exitOK || !strings.Contains(out.String(), "PTT: Example Film 2019 MULTi 1080p BluRay REMUX AVC DTS-HD MA 5.1-Zulea") {
		t.Fatalf("exit %d out %q err %q", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := run(context.Background(), []string{"names", "Bad-GRP"}, &out, &errOut); code != exitStop {
		t.Fatalf("invalid name exit %d", code)
	}
}

func TestPlanRequiresConfig(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"plan", "--release", "X"}, &out, &errOut); code != exitUsage {
		t.Fatalf("exit %d", code)
	}
}
