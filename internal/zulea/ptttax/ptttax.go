// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package ptttax turns the pasted <option> lists of the PTT upload form
// ("Kategoria", "Rodzaj", "Resolution") into the confirmed taxonomy table
// (decision D-4b), so no ID is typed by hand. Matching is exact on the visible
// label; anything ambiguous or missing is an error, never a guess.
package ptttax

import (
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/autobrr/upbrr/internal/zulea/pttplan"
)

// Option is one <option> of a form list.
type Option struct {
	Value string
	Label string
}

var (
	selectPattern = regexp.MustCompile(`(?is)<select[^>]*\bname\s*=\s*["']([^"']+)["'][^>]*>(.*?)</select>`)
	optionPattern = regexp.MustCompile(`(?is)<option[^>]*\bvalue\s*=\s*["']([^"']*)["'][^>]*>(.*?)</option>`)
	tagPattern    = regexp.MustCompile(`(?s)<[^>]*>`)
	idPattern     = regexp.MustCompile(`^[0-9]{1,6}$`)
)

// Resolutions are the resolution labels a Zulea release can carry.
var Resolutions = []string{"2160p", "1080p", "1080i", "720p", "576p", "576i", "480p", "480i"}

// Lists holds the three parsed lists.
type Lists struct {
	Category   []Option
	Type       []Option
	Resolution []Option
}

// Parse reads pasted HTML. It accepts whole <select name="category_id|type_id|resolution_id">
// elements or bare option lines grouped under headings containing
// "Kategoria"/"Category", "Rodzaj"/"Type" and "Resolution"/"Rozdzielczość".
func Parse(text string) (Lists, error) {
	var lists Lists
	selects := selectPattern.FindAllStringSubmatch(text, -1)
	if len(selects) > 0 {
		for _, sel := range selects {
			opts := options(sel[2])
			switch strings.ToLower(strings.TrimSpace(sel[1])) {
			case "category_id":
				lists.Category = opts
			case "type_id":
				lists.Type = opts
			case "resolution_id":
				lists.Resolution = opts
			}
		}
	} else {
		var current *[]Option
		for _, line := range strings.Split(text, "\n") {
			lower := strings.ToLower(line)
			if !strings.Contains(lower, "<option") {
				switch {
				case strings.Contains(lower, "kategori") || strings.Contains(lower, "category"):
					current = &lists.Category
				case strings.Contains(lower, "rodzaj") || strings.Contains(lower, "type"):
					current = &lists.Type
				case strings.Contains(lower, "resolution") || strings.Contains(lower, "rozdzielczo"):
					current = &lists.Resolution
				}
				continue
			}
			if current == nil {
				return Lists{}, errors.New("ptttax: option lines must follow a heading (Kategoria, Rodzaj, Resolution) or be inside <select name=...>")
			}
			*current = append(*current, options(line)...)
		}
	}
	if len(lists.Category) == 0 || len(lists.Type) == 0 || len(lists.Resolution) == 0 {
		return Lists{}, fmt.Errorf("ptttax: need all three lists (found category=%d type=%d resolution=%d options)",
			len(lists.Category), len(lists.Type), len(lists.Resolution))
	}
	return lists, nil
}

func options(fragment string) []Option {
	matches := optionPattern.FindAllStringSubmatch(fragment, -1)
	out := make([]Option, 0, len(matches))
	for _, m := range matches {
		label := strings.Join(strings.Fields(html.UnescapeString(tagPattern.ReplaceAllString(m[2], " "))), " ")
		out = append(out, Option{Value: strings.TrimSpace(m[1]), Label: label})
	}
	return out
}

func pick(opts []Option, list string, labels ...string) (string, error) {
	var found []Option
	for _, o := range opts {
		for _, l := range labels {
			if strings.EqualFold(o.Label, l) {
				found = append(found, o)
				break
			}
		}
	}
	switch {
	case len(found) == 0:
		return "", fmt.Errorf("ptttax: %s list has no option labelled %q", list, labels)
	case len(found) > 1:
		return "", fmt.Errorf("ptttax: %s list has %d options matching %q; resolve by hand", list, len(found), labels)
	case !idPattern.MatchString(found[0].Value):
		return "", fmt.Errorf("ptttax: %s option %q has a non-numeric value %q", list, found[0].Label, found[0].Value)
	}
	return found[0].Value, nil
}

// Build maps the lists to the taxonomy. Category MOVIE matches "Film", "Filmy",
// "Movie" or "Movies"; type REMUX matches "Remux"; resolutions match their
// exact label. Resolutions absent from the form are reported, not invented.
func Build(lists Lists) (pttplan.Taxonomy, []string, error) {
	category, err := pick(lists.Category, "Kategoria", "Film", "Filmy", "Movie", "Movies")
	if err != nil {
		return pttplan.Taxonomy{}, nil, err
	}
	remux, err := pick(lists.Type, "Rodzaj", "Remux")
	if err != nil {
		return pttplan.Taxonomy{}, nil, err
	}
	tax := pttplan.Taxonomy{
		Category:   map[string]string{"MOVIE": category},
		Type:       map[string]string{"REMUX": remux},
		Resolution: map[string]string{},
	}
	var notes []string
	for _, res := range Resolutions {
		value, err := pick(lists.Resolution, "Resolution", res)
		if err != nil {
			notes = append(notes, fmt.Sprintf("rozdzielczość %s: %v", res, err))
			continue
		}
		tax.Resolution[res] = value
	}
	if len(tax.Resolution) == 0 {
		return pttplan.Taxonomy{}, notes, errors.New("ptttax: no Zulea resolution found in the Resolution list")
	}
	return tax, notes, nil
}
