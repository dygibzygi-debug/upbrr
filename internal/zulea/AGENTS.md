# Zulea uploader guidelines

Scope: `internal/zulea/`, `cmd/zulea-upbrr/`, `scripts/zulea/`. Root and `internal/AGENTS.md` rules apply.

## Invariants

- **Upload only through the gated canary path.** `zulea-upbrr upload` (`app.Runner.Upload`) is the only submit path. Keep every gate:
  - an interactive terminal;
  - no prior receipt, unless it is a proven `not_sent`;
  - a fresh dry-run that is READY;
  - an open canary gate;
  - a fingerprint equal to the reviewed one;
  - the typed phrase `WYSYLAM <12 hex>`;
  - an upload-intent plan that reproduces the same fingerprint.

  Never add flags, environment variables, batch modes or defaults that skip a gate. Never retry automatically after `attempted`, `uncertain`, `rejected` or `accepted`. Uploads never inject into a torrent client.
- **A duplicate passes only through `dupe-accept`.** `DUPLICATE` and `REVIEW` open the canary gate and `queue` only when the fresh search is complete, lists at least one candidate, and `work_dir/dupe-accept/<REL>.ptt.json` holds an active acceptance of that same candidate-set SHA. `FAILED`, `NOT_RUN`, an incomplete search, an empty candidate list, a `-Zulea` candidate (any letter case) and `exact_duplicate` are hard refusals. The command is interactive, offline (last `scan-ptt` or `plan` report), requires `--reason` (≤500 bytes; the text is the operator's note and is not checked against tracker rules) and the phrase `AKCEPTUJE <REL-skrót> <12 hex>`. `--revoke` appends history; it does not erase it. Never add `--force` or a PTT duplicate rule in the upbrr core. The tool does not judge tracker-rule compliance. The plan fingerprint includes the acceptance SHA, and the receipt stores the path and SHA. Do not log the decision text.
- **Seeding is a separate, explicit command.** `zulea-upbrr seed` requires an `accepted` receipt with `DANE_ZGODNE=TAK` and matching names. It adds only the registered torrent downloaded from PTT, always stopped, with `autoTMM=false` and the configured save path (the path the qBittorrent container sees, never derived from the category). The lookup infohash is that registered file's infohash. PTT rewrites `info` (`source` and entropy), so this is not the TM infohash; using the TM hash would miss a torrent that is already seeding and add a second copy. An existing torrent is recognized, left unchanged (path and category included) and recorded as `seed_source: manual`. It starts seeding only at progress 100% when the operator asked (`--start` / `--start-verified`). It never deletes, moves or rewrites torrents.
- **Read-only tracker operations stay read only.** `scan-ptt`, `queue`, `verify-uploaded` and `ptt-status` only search or download; they never upload. `seed-status` only reads qBittorrent.
- **The queue waits for a settled outcome.** Any receipt other than a proven `not_sent`, an `APPROVED` `accepted` or an acknowledged `rejected` closes `queue` and the canary gate. Only `resolve` (interactive, typed phrase bound to the receipt bytes, mandatory note) settles a receipt. Never add a bypass, never ask anyone to edit receipt JSON, and keep receipt writes atomic.
- **Automation never uploads, seeds or accepts duplicates.** `nightly.sh`, `summary` and notifications report only. They must not run `dupe-accept`. The nightly warning is only a change from the previous run (new STOP among previously OK releases, a release leaving OK, an unreadable or unresolved receipt, moderation waiting longer than 24h, a seed-status error). Standing STOPs stay in the summary without a warning. The previous OK set is `work_dir/nightly/state.json`, mode 0600, replaced atomically. Nightly `scan-ptt` is off unless `nightly.scan_ptt` is true; it waits at least 3s and stops on the first API error. Notification secrets live in `0600` files; messages must not mention people.
- **Upstream sync never pushes.** `upstream-sync.sh` works on a new local branch. A failing `TestUpbrrPTTContract` means upbrr changed the PTT payload: review it, never just update the expected list.
- **`sync-inputs` never overwrites evidence.** A different freeze or completed manifest under the same name is an error; manifests of running TM batches are skipped.
- **The library is read only.** Never write, rename, chmod or create files under `library_root`. Generated artifacts (upload torrent copy, MediaInfo, reports) go below `work_dir`, which must not overlap the library.
- **Never open `<REL>.screens.json`.** It holds ImgBB delete URLs. Screenshot links come only from the ready description.
- **The TM torrent is sent unchanged.** The upload copy keeps the TM `info` bytes verbatim (same infohash) and drops every top-level key. There is no fallback: a missing or non-conforming TM torrent is a STOP, never a reason to let upbrr build or personalize a torrent.
- **TMDB is used narrowly.** Only `GET /movie/{TMDB_ID}` with the frozen id; the returned `id` must match. No search, no other provider.
- **Titles follow `names.py` exactly.** Changes need a regenerated golden file (`scripts/zulea/gen_names_golden.py`) from the reference implementation.
- **No secrets** in config, reports, logs or tests. Keys live in `0600` files referenced by path. Tests use sentinel values and assert they never appear.
- **No core changes to upbrr trackers.** Consume the registry definition. A change outside `internal/zulea` needs an explicit reason in the stage report.
- **Use synthetic data in fixtures.** Real release names, server paths and the Zulea package stay out of the repository.

## Checks

```bash
go test -race ./internal/zulea/... ./cmd/zulea-upbrr/
make architecturepolicy pathpolicy literalpolicy logpolicy
golangci-lint run ./internal/zulea/... ./cmd/zulea-upbrr/...   # version pinned in .github/workflows/golangci-lint.yml
```
