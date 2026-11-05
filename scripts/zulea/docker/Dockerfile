# syntax=docker/dockerfile:1
# Copyright (c) 2026, Zulea contributors.
# SPDX-License-Identifier: GPL-2.0-or-later
#
# zulea-upbrr: CLI only, no listening port, runs as nobody:users (99:100) so it
# can read the Torrent Maker's 0600 nobody:users torrents without changing them.
# Build from the repository root:
#   docker build -f scripts/zulea/docker/Dockerfile \
#     --build-arg MEDIAINFO_VERSION=<apk version> -t zulea-upbrr:local .
# Build facts: docker run --rm --entrypoint cat zulea-upbrr:local /usr/local/share/zulea-upbrr/BUILD-INFO
# Mount the library and TM inputs read only; see scripts/zulea/docker/README.md.

# Base images are pinned by digest (resolved from Docker Hub on 2026-11-05), so a
# rebuild cannot silently change the toolchain or the runtime.
ARG GO_IMAGE=golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb
ARG RUNTIME_IMAGE=alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a

FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/zulea-upbrr ./cmd/zulea-upbrr

FROM ${RUNTIME_IMAGE}
ARG GO_IMAGE
ARG RUNTIME_IMAGE
# mediainfo: decision D-6 (MediaInfo CLI text, same as manual uploads). The
# exact Alpine package version is required (for example 25.04-r0), because the
# MediaInfo text is part of the reviewed upload. Read the available version with
#   docker run --rm <RUNTIME_IMAGE> sh -c 'apk update -q && apk policy mediainfo'
# If Alpine no longer ships that version, the build fails instead of changing it.
ARG MEDIAINFO_VERSION
RUN test -n "${MEDIAINFO_VERSION}" || { echo "MEDIAINFO_VERSION build argument is required" >&2; exit 1; } \
    && apk add --no-cache ca-certificates tzdata "mediainfo=${MEDIAINFO_VERSION}" \
    && mkdir -p /usr/local/share/zulea-upbrr \
    && { echo "go_image=${GO_IMAGE}"; \
         echo "runtime_image=${RUNTIME_IMAGE}"; \
         echo "mediainfo_package=$(apk info -v mediainfo 2>/dev/null | head -n1)"; \
         echo "mediainfo_version=$(mediainfo --Version | tail -n1)"; } \
       > /usr/local/share/zulea-upbrr/BUILD-INFO \
    && cat /usr/local/share/zulea-upbrr/BUILD-INFO
COPY --from=build /out/zulea-upbrr /usr/local/bin/zulea-upbrr
# Unraid's nobody:users. No HOME writes are needed; every artifact goes to work_dir.
USER 99:100
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/zulea-upbrr"]
CMD ["--help"]
