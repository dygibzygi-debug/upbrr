# syntax=docker/dockerfile:1
# Copyright (c) 2026, Zulea contributors.
# SPDX-License-Identifier: GPL-2.0-or-later
#
# zulea-upbrr: CLI only, no listening port, runs as nobody:users (99:100) so it
# can read the Torrent Maker's 0600 nobody:users torrents without changing them.
# Build from the repository root:
#   docker build -f scripts/zulea/docker/Dockerfile -t zulea-upbrr:local .
# Mount the library and TM inputs read only; see scripts/zulea/docker/README.md.

ARG GO_VERSION=1.27.3

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/zulea-upbrr ./cmd/zulea-upbrr

FROM alpine:3.24
# mediainfo: decision D-6 (MediaInfo CLI text, same as manual uploads).
RUN apk add --no-cache ca-certificates mediainfo tzdata \
    && mediainfo --Version
COPY --from=build /out/zulea-upbrr /usr/local/bin/zulea-upbrr
# Unraid's nobody:users. No HOME writes are needed; every artifact goes to work_dir.
USER 99:100
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/zulea-upbrr"]
CMD ["--help"]
