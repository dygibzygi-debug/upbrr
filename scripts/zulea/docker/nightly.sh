#!/bin/bash
# Copyright (c) 2026, Zulea contributors.
# SPDX-License-Identifier: GPL-2.0-or-later
#
# Nightly status run for Unraid User Scripts (run as root on the host).
# It never uploads, seeds or accepts duplicates. It refreshes inputs, checks
# the chain, reads PTT and qBittorrent state, writes the history CSV and
# sends one summary.
#
# A warning is raised only when the summary reports a change: a release that
# was OK is now STOP or has disappeared, an unreadable or unresolved receipt,
# an upload waiting for moderation for more than 24h, or a seed-status error.
# Standing STOPs stay in the summary at normal level. The previous OK set is
# work/nightly/state.json (0600, replaced atomically).
#
# scan-ptt runs only when zulea.json has nightly.scan_ptt=true (default off),
# once, with at least 3s between releases. An API error stops that scan.
#
# Network, unchanged for every other command: --network-none stays on check
# and history. seed-status (and summary --with-seed) keep the default network
# because they read the qBittorrent WebUI. scan-ptt and ptt-status keep the
# default network because they read PTT, as before. The wrapper itself is not
# modified.
# Fill the variables below.
set -uo pipefail

ZULEA="/path/to/zulea-upbrr.sh"          # docker run wrapper
SYNC="/path/to/sync-inputs.sh"           # input copy script (root)
CONFIG="/secrets/zulea.json"             # path inside the container
SEED_CHECK="yes"                         # qBittorrent configured: yes/no
EXTERNAL_NOTIFY="no"                     # Discord/Telegram configured: yes/no
UNRAID_NOTIFY="/usr/local/emhttp/webGui/scripts/notify"

problems=()
# Exit 3 is a STOP or a disabled switch, not a warning by itself. The summary
# command decides whether the night needs attention.
record() { # name, command...
  local name=$1; shift
  "$@"
  local rc=$?
  case $rc in
    0|3) ;;
    *) problems+=("$name: błąd (kod $rc)") ;;
  esac
  return 0
}

record "sync-inputs" "$SYNC"
record "check" "$ZULEA" --network-none check --config "$CONFIG"
"$ZULEA" --network-none nightly-scan-enabled --config "$CONFIG"
scan_rc=$?
case $scan_rc in
  0) record "scan-ptt" "$ZULEA" scan-ptt --config "$CONFIG" --delay 3s ;;
  3) echo "nocny scan-ptt: wyłączony" ;;
  *) problems+=("nightly-scan-enabled: błąd (kod $scan_rc)") ;;
esac
record "ptt-status" "$ZULEA" ptt-status --config "$CONFIG"
if [ "$SEED_CHECK" = yes ]; then
  # No --network-none: this command reads the qBittorrent WebUI.
  record "seed-status" "$ZULEA" seed-status --config "$CONFIG"
fi
record "history" "$ZULEA" --network-none history --config "$CONFIG"

summary_args=(summary --config "$CONFIG")
[ "$SEED_CHECK" = yes ] && summary_args+=(--with-seed)
[ "$EXTERNAL_NOTIFY" = yes ] && summary_args+=(--notify)
# summary --with-seed reads qBittorrent, so it keeps the default network.
summary=$("$ZULEA" "${summary_args[@]}")
rc=$?

level=normal
if [ "$rc" -eq 3 ] || [ ${#problems[@]} -gt 0 ]; then
  level=warning
elif [ "$rc" -ne 0 ]; then
  level=warning
  problems+=("summary: błąd (kod $rc)")
fi
details="$summary"
if [ ${#problems[@]} -gt 0 ]; then
  details="$details
Kroki: $(IFS='; '; echo "${problems[*]}")"
fi
echo "$details"
if [ -x "$UNRAID_NOTIFY" ]; then
  "$UNRAID_NOTIFY" -e "Zulea uploader" -s "Zulea: raport nocny" -d "$details" -i "$level"
fi
