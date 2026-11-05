// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// reasonClass groups STOP reasons for the summary table.
var reasonClasses = []struct {
	label   string
	pattern *regexp.Regexp
}{
	{"MKV niezgodny z zamrożeniem (ino/size/mtime)", regexp.MustCompile(`ino/size/mtime`)},
	{"dysk fizyczny (W-2)", regexp.MustCompile(`physical_N5_disk|array path|Unraid location`)},
	{"brak lub niezgodny dowód TM (manifest/torrent)", regexp.MustCompile(`manifest|TM torrent|torrent rejected|TM runs`)},
	{"NFO", regexp.MustCompile(`NFO`)},
	{"opis", regexp.MustCompile(`description`)},
	{"katalog wydania", regexp.MustCompile(`release directory|partial state|regular file`)},
	{"zamrożenie (status/rekordy)", regexp.MustCompile(`freeze records|status `)},
	{"nazwa", regexp.MustCompile(`release name|tracker title`)},
}

func classify(reason string) string {
	for _, c := range reasonClasses {
		if c.pattern.MatchString(reason) {
			return c.label
		}
	}
	return "inne"
}

func mdEscape(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ").Replace(s)
}

// CheckMarkdown renders the readiness report of a check.
func CheckMarkdown(s CheckSummary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Gotowość biblioteki — łańcuch dowodów (bez sieci)\n\n")
	fmt.Fprintf(&b, "Zamrożenie SHA-256: `%s`\n\n", s.FreezeSHA256)
	fmt.Fprintf(&b, "| OK | STOP | pominięte (bez statusu OK) |\n|---|---|---|\n| %d | %d | %d |\n\n", s.OK, s.Stop, s.Skipped)
	classes := map[string]int{}
	warned := 0
	for _, r := range s.Results {
		if r.Status != "OK" {
			classes[classify(r.Reason)]++
		}
		if len(r.Warnings) > 0 {
			warned++
		}
	}
	if len(classes) > 0 {
		b.WriteString("## Powody STOP\n\n| Powód | Liczba |\n|---|---|\n")
		keys := make([]string, 0, len(classes))
		for k := range classes {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			return classes[keys[i]] > classes[keys[j]] || classes[keys[i]] == classes[keys[j]] && keys[i] < keys[j]
		})
		for _, k := range keys {
			fmt.Fprintf(&b, "| %s | %d |\n", k, classes[k])
		}
		b.WriteString("\n### Wydania ze STOP\n\n| Wydanie | Powód |\n|---|---|\n")
		for _, r := range s.Results {
			if r.Status != "OK" {
				fmt.Fprintf(&b, "| `%s` | %s |\n", r.Release, mdEscape(r.Reason))
			}
		}
		b.WriteString("\n")
	}
	if warned > 0 {
		fmt.Fprintf(&b, "## Ostrzeżenia do sprawdzenia (%d wydań)\n\n| Wydanie | Tytuł PTT | Ostrzeżenia |\n|---|---|---|\n", warned)
		for _, r := range s.Results {
			if len(r.Warnings) > 0 {
				fmt.Fprintf(&b, "| `%s` | %s | %s |\n", r.Release, mdEscape(r.Title), mdEscape(strings.Join(r.Warnings, "; ")))
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("## Wydania gotowe (łańcuch OK)\n\n| Wydanie | Run TM | Screeny | Tytuł PTT |\n|---|---|---|---|\n")
	for _, r := range s.Results {
		if r.Status == "OK" {
			fmt.Fprintf(&b, "| `%s` | %s | %d | %s |\n", r.Release, dash(r.RunID), r.Images, mdEscape(r.Title))
		}
	}
	return b.String()
}

// ScanMarkdown renders the PTT presence report.
func ScanMarkdown(s ScanSummary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Obecność na PTT (tylko odczyt)\n\nStart: %s · zamrożenie `%s`\n\n| Status | Liczba |\n|---|---|\n", s.StartedAt, s.FreezeSHA256)
	keys := make([]string, 0, len(s.Counts))
	for k := range s.Counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "| %s | %d |\n", k, s.Counts[k])
	}
	sections := []struct{ title, status string }{
		{"Wolne na PTT (CLEAR) — kandydaci do uploadu", "CLEAR"},
		{"Już na PTT (DUPLICATE)", "DUPLICATE"},
		{"Do ręcznej oceny (REVIEW)", "REVIEW"},
		{"Nie sprawdzono (FAILED/NOT_RUN)", "FAILED"},
	}
	for _, sec := range sections {
		fmt.Fprintf(&b, "\n## %s\n\n| Wydanie | Szczegóły |\n|---|---|\n", sec.title)
		for _, r := range s.Results {
			match := r.Dupe == sec.status || sec.status == "FAILED" && r.Dupe == "NOT_RUN"
			if r.Chain != "OK" || !match {
				continue
			}
			var detail strings.Builder
			detail.WriteString(r.Message)
			for _, c := range r.Candidates {
				fmt.Fprintf(&detail, "; [%s] %s %s", c.Relation, c.Name, c.Link)
			}
			fmt.Fprintf(&b, "| `%s` | %s |\n", r.Release, mdEscape(detail.String()))
		}
	}
	b.WriteString("\n## Łańcuch STOP (nie sprawdzano PTT)\n\n| Wydanie | Powód |\n|---|---|\n")
	for _, r := range s.Results {
		if r.Chain != "OK" {
			fmt.Fprintf(&b, "| `%s` | %s |\n", r.Release, mdEscape(r.ChainReason))
		}
	}
	return b.String()
}
