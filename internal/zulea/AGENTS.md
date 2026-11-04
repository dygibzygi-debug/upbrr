# Zulea uploader guidelines

Scope: `internal/zulea/`, `cmd/zulea-upbrr/`, `scripts/zulea/`. Root and `internal/AGENTS.md` rules apply.

## Invariants

- **Dry-run only (stage 1).** Plans use `trackers.PreparationIntentDryRun`. Do not add submit, upload or auto-confirm paths. Upload arrives only in a later, separately approved stage, and then always after a full preview and an explicit human confirmation bound to the plan fingerprint.
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
