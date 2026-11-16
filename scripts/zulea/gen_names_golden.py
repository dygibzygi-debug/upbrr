#!/usr/bin/env python3
# Copyright (c) 2026, Zulea contributors.
# SPDX-License-Identifier: GPL-2.0-or-later
"""Run the reference Release Panel names.py over a case file and print golden JSON.

Usage:
  python3 -I scripts/zulea/gen_names_golden.py <path/to/names.py> <cases.json> > golden.json

cases.json: {"records": {"<key>": {TMDB_ID,...} | null}, "cases": [{"release": str, "record": "<key>"|null}]}
The reference names.py is not part of this repository.
"""
import importlib.util
import json
import sys


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__, file=sys.stderr)
        return 2
    spec = importlib.util.spec_from_file_location("zulea_reference_names", sys.argv[1])
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    with open(sys.argv[2], encoding="utf-8") as fh:
        data = json.load(fh)
    out = []
    for case in data["cases"]:
        key = case.get("record")
        rec = data["records"][key] if key else None
        res = mod.build(case["release"], rec)
        out.append({
            "release": case["release"],
            "record": key,
            "cinemamovies": res["cinemamovies"],
            "ptt": res["ptt"],
            "warnings": res["warnings"],
            "error": res["error"],
        })
    json.dump({"rule_version": "0.2.2", "results": out}, sys.stdout, ensure_ascii=False, indent=1)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
