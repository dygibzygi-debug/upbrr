# zulea-upbrr container (deployment template for W-3)

A template for the operator who deploys (Codex). It has no web port and runs
as `nobody:users` (99:100), the owner of the Torrent Maker torrents (mode
`0600`).

## Build

```bash
docker build -f scripts/zulea/docker/Dockerfile -t zulea-upbrr:local .
```

## Mounts

Every input is read only (`:ro`). Each one is mounted through exactly one
host path.

| Container path | Host source | Mode |
|---|---|---|
| `/library` | the release library (choose option A or B below) | `ro` |
| `/inputs/freeze` | the freeze directory | `ro` |
| `/inputs/titles` | the directory holding `tmdb-titles.tsv` | `ro` |
| `/inputs/tm-runs` | TM batch `runs/` (`<RUN_ID>/manifest-*.tsv`) | `ro` |
| `/inputs/tm-output` | TM `output/` (`<RUN_ID>/*.torrent`) | `ro` |
| `/work` | appdata work directory (reports, MediaInfo, receipts, registered torrents) | `rw` |
| `/secrets` | appdata secrets: config `zulea.json`, `tmdb.key`, `ptt.key`, all `0600` and owned by 99:100 | `ro` |

### Library path

- **Option A (recommended):** mount the array-disk path the TM wrapper uses
  (`/mnt/<disk>/<share>`) and set `"disk_check_mode": "path"`.
  `library_root` must then be under `/mnt/<disk>/` inside the container
  too. Mount it at the same path, e.g.
  `-v /mnt/disk1/<share>:/mnt/disk1/<share>:ro`.
- **Option B:** mount the user-share path and set
  `"disk_check_mode": "unraid_xattr"`.

Before choosing, run `verify-identity` for one release with each candidate
mount (read only):

```bash
docker run --rm --network none \
  -v /mnt/disk1/<share>:/mnt/disk1/<share>:ro \
  -v <freeze-dir>:/inputs/freeze:ro -v <secrets>:/secrets:ro -v <work>:/work \
  zulea-upbrr:local verify-identity --config /secrets/zulea.json --release <REL>
```

`TOŻSAMOŚĆ_ZGODNA=TAK` is required. The uploader never writes to the
library, so the classic mixed-access shfs failure, which comes from writing
through `/mnt/user` and `/mnt/diskX`, is not triggered. Still, keep one path
per mount.

### Permissions

The TM wrapper keeps the freeze at `0400 root:root` in a `0700 root:root`
directory, while its torrents are `0600 nobody:users`. No single non-root
user can read both. Pick one of these and record the choice:

1. **Recommended:** a dedicated freeze copy for the uploader, e.g.
   `appdata/zulea-upbrr/freeze/`, mode `0400`, owned by 99:100. The uploader
   verifies its SHA-256 against `freeze_sha256` anyway, so a copy cannot
   silently diverge.
2. Run the container as root with every input mounted read only. Root then
   reads both, and writes go only to `/work`. This weakens the "least
   privilege" posture.

The same applies to `tmdb-titles.tsv` and the TM `runs/` manifests (`0600`):
the container user must be able to read them.

## Commands

```bash
# whole-library chain check, no network
docker run --rm --network none ... zulea-upbrr:local check --config /secrets/zulea.json
# dry-run (TMDB by id and PTT duplicate search only)
docker run --rm ... zulea-upbrr:local plan --config /secrets/zulea.json --release <REL>
# canary upload: interactive terminal required (-it), run only by the owner
docker run --rm -it ... zulea-upbrr:local upload --config /secrets/zulea.json --release <REL> --fingerprint <FP>
```

The container never adds anything to qBittorrent. After an accepted upload,
the registered torrent is in `/work`, and adding it with a full recheck stays
the existing manual step.
