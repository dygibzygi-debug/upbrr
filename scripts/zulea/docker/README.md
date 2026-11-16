# zulea-upbrr container (deployment template for W-3)

A template for the operator who deploys (Codex). It has no web port and runs
as `nobody:users` (99:100), the owner of the Torrent Maker torrents (mode
`0600`).

## Build

Base images are pinned by digest. The MediaInfo package version is a
required build argument, because MediaInfo text is part of the reviewed upload
(D-6). Read the version Alpine currently ships, then build with it:

```bash
docker run --rm alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a \
  sh -c 'apk update -q && apk policy mediainfo'
docker build -f scripts/zulea/docker/Dockerfile --build-arg MEDIAINFO_VERSION=<version> -t zulea-upbrr:local .
docker run --rm --entrypoint cat zulea-upbrr:local /usr/local/share/zulea-upbrr/BUILD-INFO
```

Record `BUILD-INFO` in the deployment report. A later rebuild with the same
argument either produces the same MediaInfo or fails; it never changes it
silently.

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
# human duplicate acceptance: interactive terminal, no tracker traffic, no --force
docker run --rm -it --network none ... zulea-upbrr:local dupe-accept --config /secrets/zulea.json --release <REL> --reason "..."
# seed: needs a path to the qBittorrent WebUI; adds the PTT torrent stopped
docker run --rm ... zulea-upbrr:local seed --config /secrets/zulea.json --release <REL>
# check-config with a qbittorrent section also reads that WebUI
docker run --rm ... zulea-upbrr:local check-config --config /secrets/zulea.json
```

`dupe-accept` reads the newest `scan-ptt` or `plan` report under `/work`.
Nightly automation must not run it. The operator runbook in
`scripts/zulea/deploy/v5/` changes only the image tag on the wrapper already
deployed on the host. It does not replace that wrapper.

`check`, `history`, `dupe-accept` and `verify-identity` stay on
`--network none`. `seed`, `seed-status` and `check-config` (when
`qbittorrent` is configured) need the default network so they can reach the
qBittorrent WebUI. `plan`, `upload`, `scan-ptt` and `ptt-status` keep the
network they already use for TMDB and PTT. The host wrapper's hardening
(`--read-only`, `--cap-drop ALL`, `no-new-privileges`, read-only mounts) is
unchanged; this template does not replace that wrapper.

`seed` sends the registered torrent from `/work` to qBittorrent through the
WebAPI. It does not copy that file anywhere else. The torrent is added
stopped, with automatic management off and the configured save path. It is
started only after a recheck reaches 100% and the operator asked for it. A
torrent already present under the PTT infohash is reported and left as it is.
