# mina-provision

Fetches, verifies and places the published artifacts a Mina node needs before
it can start: archive database dumps, precomputed blocks and runtime
configuration files.

It replaces the long `curl`, `gsutil`, `tar` and `psql` sequences that
operators currently copy out of the documentation pages and out of the Rosetta
`docker-compose` stack.

```bash
mina-provision archive --network mainnet --pg-uri postgres://...   # a database, from a published dump
mina-provision blocks  --network mainnet --range 50000-51000       # precomputed blocks, onto disk
mina-provision config  --network mainnet --out /var/lib/coda       # the config file the daemon auto-loads
```

Each subcommand names the thing being provisioned. `--network` can also be set
once with `MINA_NETWORK`; a flag on the command line always wins over the
environment.

## What it does not do

It does not write blocks into an archive database, and it does not repair gaps.

Inserting a precomputed block is not a file copy: the block is decoded, its
hashes are derived, and about twenty related tables are written. That logic is
the archive writer's, and a second implementation of it in another language
would be a source of silent divergence. So this tool stops at the file system:

| Concern | Owner |
|---|---|
| Where the artifacts are, and are they genuine | `mina-provision` |
| What is missing from the database, and writing it | `mina-archive` |
| Verifying a block's proof | [`mina-verify`](https://github.com/MinaProtocol/mina-verify) |

A dump is produced hourly, so a freshly restored database is behind the chain
tip. Fetch the difference with `mina-provision blocks` and apply it with
`mina-archive`, which also takes a local directory of block files as its input.

## Why the configuration comes from a Debian package

`mina-provision config` reads the `mina-<network>-config` package by default,
not the `mina` source tree, because the daemon auto-loads
`/var/lib/coda/config_<hash>.json` where the hash is derived from the commit at
build time. That hash is not the one in the package version:

```
package version   3.4.0-bd0fe9e     (7 characters)
file inside it    config_bd0fe9e9.json  (9 characters)
```

The identical JSON also exists as `genesis_ledgers/<network>.json` in the
source tree, so GitHub gives the right content under a name the daemon will not
load. Only the package carries both, and the repository index publishes a
SHA256 for it, which this command always checks. `--source github --ref <tag>`
remains available for an operator who wants what a specific commit carries.

Two details worth knowing:

- The default channel is `o1test`. The configuration package is not present in
  every signed channel yet; the error message lists the components a channel
  actually publishes.
- With no `--version`, the highest version is chosen by Debian ordering, which
  is the package `apt` itself would install. Debian ordering is not upload
  order, so a build with a longer version string can outrank a newer one. Pin
  with `--version` when a specific build is meant.

## Installing

```bash
# Debian package, from a release
sudo dpkg -i mina-provision_<version>_amd64.deb

# container
docker run --rm -v "$PWD/out:/out" ghcr.io/minaprotocol/mina-provision \
  config --network mainnet --out /out
```

`postgresql-client` is a recommendation, not a dependency: only `archive`
shells out to `psql`.

## Building

```bash
go build ./...
go vet ./...
go test ./...          # no network access
```

The default test suite never touches the network. Tests behind the
`integration` build tag need PostgreSQL and live buckets:

```bash
go test -tags integration ./...
```

A daily workflow exercises the live path against the real repository, so a
published artifact that moves is noticed there rather than in a pull request.

## Still to come

- `ledger` — genesis and epoch ledger tarballs. The identity of a ledger
  tarball is its **sha3** value, equal to `s3_data_hash`, while its file name
  carries a blake2 hash. That mismatch is exactly the kind of rule this tool
  exists to encode.
- `checkpoint` — replayer checkpoints.
- `peers` — seed and peer lists.
- Signature verification, beyond the checksum the index publishes.
- Publishing the package to the APT repositories from the release workflow.

## History

Extracted from [MinaProtocol/mina#18845](https://github.com/MinaProtocol/mina/pull/18845),
where it was `mina-bootstrap` under `src/app/bootstrap`. It has no build-time
or run-time dependency on the mina tree, so it lives on its own and releases on
its own schedule.
