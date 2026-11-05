// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package ptttax

import (
	"strings"
	"testing"
)

const selects = `<select name="category_id" class="form__select"><option value="">--</option><option value="1">Filmy</option><option value="2">Seriale</option></select>
<select name="type_id"><option value="1">Full Disc</option><option value="2">Remux</option><option value="3">Encode</option></select>
<select name="resolution_id"><option value="1">4320p</option><option value="2">2160p</option><option value="3">1080p</option>
<option value="4">1080i</option><option value="5">720p</option><option value="6">576p</option><option value="7">576i</option>
<option value="8">480p</option><option value="9">480i</option><option value="10">Other</option></select>`

func TestParseSelectsAndBuild(t *testing.T) {
	lists, err := Parse(selects)
	if err != nil {
		t.Fatal(err)
	}
	tax, notes, err := Build(lists)
	if err != nil {
		t.Fatal(err)
	}
	if tax.Category["MOVIE"] != "1" || tax.Type["REMUX"] != "2" || tax.Resolution["2160p"] != "2" || tax.Resolution["1080i"] != "4" || len(notes) != 0 {
		t.Fatalf("taxonomy %+v notes %q", tax, notes)
	}
}

func TestParseHeadingsAndReportMissing(t *testing.T) {
	text := "Kategoria\n<option value=\"7\">Film</option>\nRodzaj\n<option value=\"12\">REMUX</option>\nResolution\n<option value=\"3\">1080p</option>\n"
	lists, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	tax, notes, err := Build(lists)
	if err != nil || tax.Category["MOVIE"] != "7" || tax.Type["REMUX"] != "12" || len(tax.Resolution) != 1 || len(notes) != 7 {
		t.Fatalf("tax %+v notes %d err %v", tax, len(notes), err)
	}
}

func TestBuildRejectsAmbiguity(t *testing.T) {
	cases := map[string]string{
		"two films":   strings.Replace(selects, `value="2">Seriale`, `value="9">Film`, 1),
		"no remux":    strings.Replace(selects, ">Remux<", ">Remuks<", 1),
		"non numeric": strings.Replace(selects, `value="2">Remux`, `value="x">Remux`, 1),
	}
	for name, text := range cases {
		lists, err := Parse(text)
		if err != nil {
			t.Fatalf("%s parse: %v", name, err)
		}
		if _, _, err := Build(lists); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := Parse("<option value=\"1\">Filmy</option>"); err == nil {
		t.Error("options without headings must be rejected")
	}
}
