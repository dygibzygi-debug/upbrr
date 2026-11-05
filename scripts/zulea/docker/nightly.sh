#!/bin/bash
# Copyright (c) 2026, Zulea contributors.
# SPDX-License-Identifier: GPL-2.0-or-later
#
# Nightly status run for Unraid User Scripts (run as root on the host).
# It never uploads: it refreshes inputs, checks the chain, reads PTT and
# qBittorrent state, writes the history CSV and sends one summary.
# Fill the variables below. Exit code 3 from a step means "needs attention",
# not failure, so the run continues.
set -uo pipefail

ZULEA="/path/to/zulea-upbrr.sh"          # docker run wrapper
SYNC="/path/to/sync-inputs.sh"           # input copy script (root)
CONFIG="/secrets/zulea.json"             # path inside the container
SCAN_WEEKDAY="7"                         # full PTT scan day (1=Mon .. 7=Sun); empty = never
SEED_CHECK="yes"                         # qBittorrent configured: yes/no
EXTERNAL_NOTIFY="no"                     # Discord/Telegram configured: yes/no
UNRAID_NOTIFY="/usr/local/emhttp/webGui/scripts/notify"

problems=()
step() { # name, command...
  local name=$1; shift
  "$@"
  local rc=$?
  case $rc in
    0) ;;
    3) problems+=("$name: wymaga uwagi") ;;
    *) problems+=("$name: błąd (kod $rc)") ;;
  esac
  return 0
}

step "sync-inputs" "$SYNC"
step "check" "$ZULEA" --network-none check --config "$CONFIG"
if [ -n "$SCAN_WEEKDAY" ] && [ "$(date +%u)" = "$SCAN_WEEKDAY" ]; then
  step "scan-ptt" "$ZULEA" scan-ptt --config "$CONFIG" --delay 3s
fi
step "ptt-status" "$ZULEA" ptt-status --config "$CONFIG"
if [ "$SEED_CHECK" = yes ]; then
  step "seed-status" "$ZULEA" seed-status --config "$CONFIG"
fi
step "history" "$ZULEA" --network-none history --config "$CONFIG"

summary_args=(summary --config "$CONFIG")
[ "$SEED_CHECK" = yes ] && summary_args+=(--with-seed)
[ "$EXTERNAL_NOTIFY" = yes ] && summary_args+=(--notify)
summary=$("$ZULEA" "${summary_args[@]}")
rc=$?

level=normal
if [ $rc -ne 0 ] || [ ${#problems[@]} -gt 0 ]; then
  level=warning
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
