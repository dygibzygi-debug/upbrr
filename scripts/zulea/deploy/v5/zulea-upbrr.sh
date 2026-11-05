#!/bin/bash
# Copyright (c) 2026, Zulea contributors.
# SPDX-License-Identifier: GPL-2.0-or-later
#
# Host wrapper for zulea-upbrr 0.6.0 (pakiet v5).
# Paths live in zulea-upbrr.env next to this script (operator-local, not part
# of SHA256SUMS). Copy zulea-upbrr.env.example and fill it before replacing
# the previous wrapper. --network-none must be the first argument.
# This wrapper adds no flags of its own. It never uploads by itself.
set -euo pipefail

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
env_file=${ZULEA_WRAPPER_ENV:-"$here/zulea-upbrr.env"}
if [[ -f $env_file ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$env_file"
  set +a
fi

: "${ZULEA_IMAGE:?set ZULEA_IMAGE in zulea-upbrr.env; keep zulea-upbrr:5b87372 as the rollback image}"
: "${ZULEA_LIBRARY:?set ZULEA_LIBRARY}"
: "${ZULEA_FREEZE:?set ZULEA_FREEZE}"
: "${ZULEA_TITLES:?set ZULEA_TITLES}"
: "${ZULEA_TM_RUNS:?set ZULEA_TM_RUNS}"
: "${ZULEA_TM_OUTPUT:?set ZULEA_TM_OUTPUT}"
: "${ZULEA_WORK:?set ZULEA_WORK}"
: "${ZULEA_SECRETS:?set ZULEA_SECRETS}"

docker_net=()
if [[ ${1:-} == --network-none ]]; then
  docker_net=(--network none)
  shift
fi

tty_args=()
if [[ -t 0 && -t 1 ]]; then
  tty_args=(-it)
fi

library_mount=${ZULEA_LIBRARY_MOUNT:-$ZULEA_LIBRARY}

exec docker run --rm "${tty_args[@]}" "${docker_net[@]}" \
  --user 99:100 \
  -v "${ZULEA_LIBRARY}:${library_mount}:ro" \
  -v "${ZULEA_FREEZE}:/inputs/freeze:ro" \
  -v "${ZULEA_TITLES}:/inputs/titles:ro" \
  -v "${ZULEA_TM_RUNS}:/inputs/tm-runs:ro" \
  -v "${ZULEA_TM_OUTPUT}:/inputs/tm-output:ro" \
  -v "${ZULEA_WORK}:/work" \
  -v "${ZULEA_SECRETS}:/secrets:ro" \
  "$ZULEA_IMAGE" "$@"
