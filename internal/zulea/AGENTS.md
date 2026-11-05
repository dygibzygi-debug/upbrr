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
- **Seeding is a separate, explicit command.** `zulea-upbrr seed` requires an `accepted` receipt with `DANE_ZGODNE=TAK` and matching names. It adds only the registered torrent downloaded from PTT, always stopped with a full recheck, and never touches an existing torrent. It starts seeding only at progress 100% when the operator asked (`--start` / `--start-verified`). It never deletes, moves or rewrites torrents.
- **Read-only tracker operations stay read only.** `scan-ptt`, `queue`, `verify-uploaded` and `ptt-status` only search or download; they never upload. `seed-status` only reads qBittorrent.
- **The queue waits for moderation.** While an accepted upload is not `APPROVED`, `queue` is empty and the canary gate is closed. Do not add a bypass.
- **Automation never uploads or seeds.** `nightly.sh`, `summary` and notifications report only. Notification secrets live in `0600` files; messages must not mention people.
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
