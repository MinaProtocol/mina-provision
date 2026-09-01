# mina-provision: fetches and places the published artifacts a Mina node needs.
#
# Two stages:
#   1. golang builds a static binary
#   2. debian-slim carries the binary plus psql, which the `archive` command
#      shells out to when it restores a dump
#
# The image is intentionally not based on mina-archive. This tool does not
# write to an archive database; applying blocks is mina-archive's own work.

ARG GO_IMAGE=golang:1.21.13-bookworm
ARG BASE_IMAGE=debian:bookworm-slim

FROM ${GO_IMAGE} AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/mina-provision .

FROM ${BASE_IMAGE}

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update --quiet \
    && apt-get install --no-install-recommends --quiet --yes \
        ca-certificates \
        postgresql-client \
        dumb-init \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/mina-provision /usr/local/bin/mina-provision

ENTRYPOINT ["/usr/bin/dumb-init", "/usr/local/bin/mina-provision"]
CMD ["--help"]
