# mina-provision: fetches and places the published artifacts a Mina node needs.
#
# Two stages:
#   1. golang builds a static binary
#   2. debian-slim carries the binary plus psql, which the `archive` command
#      shells out to when it restores a dump
#
# The image is intentionally not based on mina-archive. This tool does not
# write to an archive database; applying blocks is mina-archive's own work.

# Both base images are pinned by the digest of their multi-arch index, so a
# rebuild of the same commit uses the same bases for amd64 and arm64. The tag
# stays beside the digest because Dependabot reads it to find a newer image.
# Dependabot reads only FROM lines, not ARG defaults, so the references are
# written here and not in build arguments.
#
# Keep the Go version the same as the toolchain line in go.mod.
FROM golang:1.27.1-bookworm@sha256:69a7b9788769bec032d238959b61854e9ae87f57be9029ec04e9885fabf99195 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/mina-provision .

FROM debian:bookworm-slim@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update --quiet \
    && apt-get install --no-install-recommends --quiet --yes \
        ca-certificates \
        postgresql-client \
        dumb-init \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/mina-provision /usr/local/bin/mina-provision

# The tool needs no privileges. It runs as a fixed, unprivileged uid, so that
# `docker run --user "$(id -u):$(id -g)"` is the only change needed to make the
# output files belong to the host user.
#
# /work is the working directory, because `archive --work-dir` and the
# default output paths of the other commands are relative to it. It is
# world-writable with the sticky bit, like /tmp: the image then also works
# with an arbitrary --user, whose uid has no entry in /etc/passwd.
RUN groupadd --system --gid 10001 provision \
    && useradd --system --uid 10001 --gid 10001 --create-home provision \
    && install -d -m 1777 -o 10001 -g 10001 /work
WORKDIR /work
USER 10001

ENTRYPOINT ["/usr/bin/dumb-init", "/usr/local/bin/mina-provision"]
CMD ["--help"]
