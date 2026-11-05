# Zulea uploader (stage 1: PTT dry-run)

`zulea-upbrr` connects the Zulea release pipeline to upbrr. It reuses the
ready-made pipeline outputs (frozen name, NFO, description with screenshots,
Torrent Maker torrent) and the upbrr PTT tracker definition. Nothing is
recomputed and nothing in the library is written.

**Dry-run by default.** A real upload exists only as the gated canary
command (see "Upload"). It never touches a torrent client.

## Chain of evidence

Every link must hold, otherwise the release is a `STOP` and the next release
continues:

1. **Freeze** (`internal/zulea/freeze`):
   - SHA-256 of the freeze file equals the configured value;
   - exact 9-column header;
   - exactly one record for the release, with status `OK`;
   - MKV `stat` identity (inode, size, mtime seconds) equals the freeze;
   - `physical_N5_disk` equals the configured `physical_disk`, and the file
     really lives there (W-2, as in the TM wrapper). See "Disk check".
2. **Release directory** (`internal/zulea/input`):
   - a real directory, not a symlink;
   - exactly one `.mkv` and one `.nfo`;
   - no `*.partial*`, `*.journal` or `*.lock` entries.
3. **NFO**:
   - non-empty;
   - exactly one `RELEASE.........:` line, equal to the directory name;
   - `stat` and SHA-256 equal the Torrent Maker manifest row.
4. **Torrent Maker manifest** (`internal/zulea/tm`):
   - `TRYB=APPLY`;
   - exactly one row for the release, with `WYNIK=OK`;
   - the profile status is `APPLY_OK` or `ALREADY_OK`;
   - MKV identity, `TMDB_ID` and `SOURCE_ID` equal the freeze.
5. **TM torrent**:
   - strict canonical bencode;
   - `info` keys are exactly `files, name, piece length, pieces, private`;
   - `private=1` and `name` = release;
   - files are exactly the MKV and the NFO, with on-disk sizes;
   - the piece count matches;
   - there is no `source`;
   - the file SHA-256 and the infohash equal the manifest.
6. **Title** (`internal/zulea/names`): a byte-for-byte port of the Release
   Panel rule `names.py` v0.2.2, using the TMDB titles file.
7. **TMDB** (`internal/zulea/tmdb`):
   - one `GET /movie/{TMDB_ID}`, by the frozen id only (decision D-2);
   - no name search and no other provider;
   - the returned `id` must equal the freeze.
8. **PTT payload** (`internal/zulea/pttplan`). The upbrr PTT definition
   (`unit3d.Definition.Prepare`, `PreparationIntentDryRun`) builds the
   preview, and the plan stops unless all of these hold:
   - `name` is the `names.py` title;
   - `description` is the ready description (upbrr trims surrounding
     whitespace only);
   - the torrent is the upload copy and carries the TM `info` bytes verbatim
     (same infohash), with no announce, passkey or source;
   - the NFO is the library NFO and its SHA-256 equals the manifest;
   - `tmdb` is the frozen id;
   - MediaInfo is present and BDInfo is empty.

9. **Duplicates** (`internal/zulea/pttdupe`): the unchanged upbrr duplicate
   service searches PTT (`/api/torrents/filter` by TMDB id, read only) and
   evaluates every candidate:

   | Status | Meaning |
   |---|---|
   | `CLEAR` | complete search, no duplicate, nothing to review |
   | `DUPLICATE` | a candidate occupies the same slot; the release is a STOP |
   | `REVIEW` | an incomplete search, a tracker error or candidates needing judgement; blocks the canary |
   | `NOT_RUN` | no API key; blocks the canary |
   | `FAILED` | the search failed; blocks the canary |

The adapter never opens `<REL>.screens.json`. Screenshot links come only
from the description.

## Architecture: what comes from upbrr

| Piece | Source |
|---|---|
| PTT endpoint, payload fields, category/type/resolution IDs, banned groups, final-description handling | upbrr `internal/trackers/impl/unit3d` + `sites/ptt` (unchanged) |
| MediaInfo text | upbrr `internal/metadata/mediainfo` (go-mediainfo), written below `work_dir` |
| Path and secret helpers | upbrr `internal/pathing`, `internal/redaction` |
| Freeze, manifest, torrent, names, adapter, narrow TMDB | this package (new code) |

No upbrr core file is modified. The TM torrent reaches the PTT definition as
the prepared upload artifact. Because of that, upbrr's torrent building
(mkbrr hashing, hardlink or copy staging) and personalization (`source`)
never run.

A new tracker profile (for example CinemaMovies later) is a new
`input.Profile` value: description suffix, torrent suffix, TM profile column
and title choice. The adapter needs no change.

## Configuration

The config is JSON, mode `0600`, and unknown fields are rejected. It holds
**no secret values**: keys live in separate `0600` files.

```json
{
  "library_root": "/srv/library",
  "freeze_path": "/srv/freeze/zulea-frozen-names-YYYYMMDD-HHMMSS.tsv",
  "freeze_sha256": "<64 lowercase hex>",
  "titles_path": "/srv/release-panel/tmdb-titles.tsv",
  "tm_runs_dir": "/srv/tm/runs",
  "tm_output_base": "/srv/tm/output",
  "work_dir": "/srv/zulea-upbrr/work",
  "tmdb_key_file": "/srv/zulea-upbrr/secrets/tmdb.key",
  "physical_disk": "disk1",
  "disk_check_mode": "path",
  "mediainfo": {"source": "cli", "binary": "/usr/bin/mediainfo"},
  "trackers": ["PTT"],
  "ptt": {
    "api_key_file": "/srv/zulea-upbrr/secrets/ptt.key",
    "anonymous": false,
    "personal_release": null,
    "mod_queue": true,
    "taxonomy": {
      "category": {"MOVIE": "<id>"},
      "type": {"REMUX": "<id>"},
      "resolution": {"2160p": "<id>", "1080p": "<id>", "1080i": "<id>", "720p": "<id>", "576i": "<id>", "480i": "<id>"}
    }
  }
}
```

- TM source: either `tm_runs_dir` + `tm_output_base` (all batch runs: the
  newest run whose manifest proves the release wins, and runs that disagree
  on the infohash are a STOP), or `manifest_path` + `tm_output_dir` (one run).
- `trackers` empty means nothing runs; stage 1 accepts only `PTT`.
- `work_dir` and `library_root` must not overlap. Mount the library read-only.
- Omit `taxonomy` until the owner confirms the PTT form IDs. The dry-run then
  reports `ID PTT potwierdzone: false` and uses upbrr's Unit3D defaults.
  Once set, any mismatch is a STOP.
- `api_key_file` is optional for dry-run. The key is never transmitted in
  dry-run.

### Disk check (W-2)

`disk_check_mode` is required. There are two modes:

- **`path`**: the MKV must be read from `/mnt/<physical_disk>/...`. This is the
  same array-disk path the TM wrapper uses (`SOURCE_ROOT=/mnt/<disk>/...`).
- **`unraid_xattr`**: reads Unraid's `system.LOCATION` attribute of a
  `/mnt/user/...` file. Where the attribute is unavailable, the check fails
  closed (STOP).

Deployment note: the freeze records `MKV_INO` as seen by the TM wrapper,
which reads the array-disk path. Through the user-share FUSE layer the
reported inode can differ. Verify the identity check on one release before
choosing the mount (`zulea-upbrr plan` gives a STOP with the
"ino/size/mtime" reason if it differs). The uploader only reads. Mount the
library read only through exactly one path.

### MediaInfo (D-6)

`mediainfo.source` is required:

- **`cli`** runs the MediaInfo binary (only reads the MKV). It writes the
  text to `work_dir/mediainfo/`, mode `0600`, with `Complete name` reduced
  to the file name.
- **`file`** reads a ready `<dir>/<REL>.mediainfo.txt` (outside the
  library). It must already name only the MKV file.
- **`upbrr`** uses go-mediainfo. The canary gate rejects it.

### Canary gate

Every report includes `canary.ready` and the list of blockers. The gate is
open only when all of these hold:

- the PTT IDs are confirmed (W-1);
- an API key is present;
- the duplicate check is `CLEAR`;
- `personal_release` is decided (not `null`) and `mod_queue` is `true` (D-5);
- MediaInfo is `cli` or `file` (D-6);
- every earlier `accepted` upload is `APPROVED` on PTT (`ptt-status`).

The gate never uploads. A real upload stays a separate, human-confirmed step.

## Usage

```bash
zulea-upbrr check-config --config /srv/zulea-upbrr/zulea.json
zulea-upbrr plan --config /srv/zulea-upbrr/zulea.json --release <REL>
zulea-upbrr plan --config /srv/zulea-upbrr/zulea.json --list releases.txt
zulea-upbrr names --titles tmdb-titles.tsv --tmdb-id <ID> <REL>
zulea-upbrr check --config /srv/zulea-upbrr/zulea.json            # whole chain, every OK release, no network
zulea-upbrr verify-identity --config /srv/zulea-upbrr/zulea.json --release <REL>
zulea-upbrr receipts --config /srv/zulea-upbrr/zulea.json
zulea-upbrr scan-ptt --config F [--delay 3s]          # which OK releases are already on PTT (read only) + Markdown report
zulea-upbrr queue --config F                           # next CLEAR releases without a receipt (from the latest scan)
zulea-upbrr verify-uploaded --config F --release REL   # compare a past manual upload's PTT torrent with TM
zulea-upbrr seed --config F --release REL --wait 30m [--start]   # qBittorrent: stopped, full recheck, start only at 100%
zulea-upbrr sync-inputs --dest D --freeze F --freeze-sha S --runs R --titles T --owner 99:100   # uploader-readable copies
zulea-upbrr ptt-taxonomy --options pasted.html [--apply zulea.json]   # PTT form IDs -> config (W-1)
zulea-upbrr ptt-status --config F [--release REL]     # moderation state of accepted uploads (read only)
zulea-upbrr seed-status --config F                     # qBittorrent state of accepted uploads (read only)
zulea-upbrr history --config F [--out file.csv]        # receipts as CSV (no network)
zulea-upbrr summary --config F [--with-seed] [--notify]   # short status; optional Discord/Telegram
```

## Operations after the canary

- **Moderation.** `ptt-status` asks PTT whether each accepted upload is
  visible. The standard Unit3D API only lists approved torrents, so the
  states are `APPROVED`, `PENDING` (only when the site offers
  `/api/torrents/pending`) and `NOT_VISIBLE` (waiting, rejected or deleted;
  the API cannot tell these apart). The result is stored in the receipt.
  While any accepted upload is not `APPROVED`, `queue` returns nothing and the
  canary gate stays closed. For approved torrents it also compares the PTT
  infohash with the registered torrent.
- **Seeding state.** `seed-status` reads qBittorrent only: `SEEDING`,
  `STOPPED_100`, `CHECKING`, `INCOMPLETE`, `ERROR`, `ABSENT`, `UNKNOWN`.
- **History.** `history` writes all receipts as CSV (default
  `work_dir/history/zulea-upbrr-history.csv`, written atomically).
- **Notifications.** Optional `notifications` config section with
  `discord_webhook_file`, or `telegram_token_file` plus `telegram_chat_id`.
  Secrets are read from `0600` files. Messages never mention people
  (`allowed_mentions` is empty), and errors never contain the URL or token.
- **Nightly run.** `scripts/zulea/docker/nightly.sh` is a template for the
  Unraid User Scripts plugin: sync-inputs, check, weekly scan-ptt, ptt-status,
  seed-status, history and summary. Exit code 3 means "needs attention" and
  does not stop the run. It never uploads or seeds.

## Upstream updates

`scripts/zulea/upstream-sync.sh` fetches upstream upbrr and merges it into a
new local branch `zulea/upstream-sync-<time>`. It prints the diff for the
areas the uploader depends on, then builds and runs the tests. It never
pushes. `internal/zulea/pttplan/contract_test.go` pins the PTT endpoint, the
form field set, default IDs and attached files that upbrr produces. If
upstream changes them, the test fails, and the change needs a manual review
before any deployment.

`check` and `scan-ptt` also write a Markdown readiness report next to the
JSON report. A container template lives in `scripts/zulea/docker/`.

## Upload (canary, human-confirmed)

```bash
zulea-upbrr upload --config /srv/zulea-upbrr/zulea.json --release <REL> --fingerprint <64 hex from the reviewed dry-run>
```

Gates, in order (any failure: nothing is sent and no receipt is written):

1. An interactive terminal is required. Piped or scripted confirmation is
   refused.
2. There is no earlier receipt in `work_dir/receipts/<REL>.ptt.json`, unless
   that attempt is proven `not_sent`.
3. A fresh dry-run (same code path) is READY and its canary gate is open.
4. Its fingerprint equals `--fingerprint`.
5. The human types `WYSYLAM <first 12 fingerprint characters>`.
6. The upload-intent plan from upbrr reproduces the same fingerprint.

The receipt is written **before** submission. After submission its state is
one of the following, and no state other than `not_sent` is ever retried
automatically:

| State | Meaning |
|---|---|
| `accepted` | the tracker accepted the upload |
| `rejected` | the tracker explicitly rejected it |
| `not_sent` | proven not sent |
| `uncertain` | the outcome is unknown; check PTT manually |

On acceptance, upbrr downloads the registered torrent from PTT into
`work_dir`. `zulea-upbrr` then compares it with the TM torrent, as
`compare-torrent` does. Adding it to qBittorrent (full recheck, seed at 100%)
stays the existing manual step.

`zulea-upbrr compare-torrent TM.torrent DOWNLOADED.torrent` is a Go port of
`porownaj-torrent-z-trackera.py`. Its verdict lines (`DANE_ZGODNE`,
`NAZWY_ZGODNE`, `INFOHASH_ZMIENIONY_PRZEZ_TRACKER`, `PRIVATE`) are verified
identical to the reference script on four scenarios. Exit code 0 only when
`DANE_ZGODNE=TAK` and `PRIVATE=1`.

Exit codes:

| Code | Meaning |
|---|---|
| 0 | all releases READY |
| 3 | at least one STOP |
| 2 | usage or config error |
| 1 | runtime error |

The full plan for each release is written to
`work_dir/reports/<REL>.ptt.<fingerprint16>.json` (mode `0600`). It includes
the description, MediaInfo and file digests. The `fingerprint` is SHA-256
over the canonical payload and file digests: the exact content a later,
human-confirmed upload must reproduce.

## Tests

All tests are offline:

```bash
go test -race ./internal/zulea/... ./cmd/zulea-upbrr/
```

- TMDB is a loopback `httptest` server. The end-to-end test fails on any
  other network request.
- The golden name file comes from the reference `names.py` on synthetic
  names, via `scripts/zulea/gen_names_golden.py`.
- A private sample can be compared locally through
  `ZULEA_NAMES_PRIVATE_CASES` and `ZULEA_NAMES_PRIVATE_GOLDEN`. It never
  enters the repository.
