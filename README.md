# mina-provision

Fetches, verifies and places the published artifacts a Mina node needs before
it can start: archive database dumps, precomputed blocks and runtime
configuration files.

```bash
mina-provision archive --network mainnet --pg-uri postgres://...   # a database, from a published dump
mina-provision blocks  --network mainnet --range 50000-51000       # precomputed blocks, onto disk
mina-provision config  --network mainnet --out /var/lib/coda       # the config file the daemon auto-loads
```

## Installing

```bash
# Debian and Ubuntu, from the signed apt repository
sudo wget -q https://stable.apt.packages.minaprotocol.com/repo-signing-key.gpg \
  -O /etc/apt/trusted.gpg.d/minaprotocol.gpg
echo "deb https://stable.apt.packages.minaprotocol.com $(lsb_release -cs) stable" \
  | sudo tee /etc/apt/sources.list.d/mina.list
sudo apt-get update && sudo apt-get install mina-provision

# or a single .deb from a release
sudo dpkg -i mina-provision_<version>_amd64.deb

# or a container
docker run --rm -v "$PWD/out:/out" ghcr.io/minaprotocol/mina-provision \
  config --network mainnet --out /out
```

`postgresql-client` is a recommendation, not a dependency. Only `archive`
shells out to `psql`.

## Commands

### `archive`

Fetches an archive dump, extracts it, applies the recommended PostgreSQL
tuning, and loads the SQL.

| Flag | Meaning |
|---|---|
| `--pg-uri` | target database; required unless `--skip-pg` |
| `--date` | dump date, `YYYY-MM-DD`; defaults to today, UTC |
| `--hour` | dump hour, `HHMM`; dumps are produced hourly |
| `--work-dir` | where the download and the extracted SQL are written |
| `--skip-pg` | download and extract only |

`ALTER SYSTEM` writes to `postgresql.auto.conf`, so PostgreSQL must be
restarted for the tuning to take effect.

### `blocks`

Fetches precomputed block files for a height range.

| Range | Meaning |
|---|---|
| `--range 50000` | the blocks at height 50000 |
| `--range 50000-51000` | inclusive on both ends |
| `--range 50000-` | open-ended, up to the chain tip |

A block name embeds the network, the height and the state hash, so a height on
its own narrows the name only to a prefix, which the provider is then asked to
list. One height can yield several blocks when the chain had competing blocks
at that height.

An open-ended range stops after 1000 consecutive heights with no block. A
single run fetches at most 50000 blocks.

### `config`

Fetches the runtime configuration for a network.

| Flag | Meaning |
|---|---|
| `--out` | directory to write into |
| `--version` | exact package version; default is the highest |
| `--repository`, `--component`, `--codename`, `--package` | override the provider's repository settings |
| `--ref` | git ref, for a provider that serves a source tree |

The default provider serves this as a Debian package rather than as a plain
file, because the daemon auto-loads `/var/lib/coda/config_<hash>.json`, and
that hash is derived from the commit at build time. It is not the hash in the
package version:

```
package version   3.4.0-bd0fe9e         (7 characters)
file inside it    config_bd0fe9e9.json  (9 characters)
```

The same JSON exists in the mina source tree as
`genesis_ledgers/<network>.json`. `--provider github --ref <tag>` fetches it
from there, but it arrives under the source-tree name, which the daemon does
not auto-load.

With no `--version`, the highest version is chosen by Debian ordering — the
package `apt` itself would install. Debian ordering is not upload order, so a
build with a longer version string can outrank a newer one. Pin with
`--version` when a specific build is meant.

## Providers

Every endpoint, bucket and naming rule is configuration, not code. The Mina
Foundation is the default publisher of these artifacts, not the only possible
one: a mirror, an internal artifact store or a directory on disk is selected
with `--provider`, with no change to the program.

```bash
mina-provision blocks --provider acme-mirror --range 50000-50100
```

The built-in defaults are written in the same schema an operator writes, are
embedded in the binary, and are read by the same parser. A custom provider can
therefore express everything the defaults express.

[`docs/providers.md`](docs/providers.md) is the full guide: worked examples per
backend, what merging does, and how to add a provider to the built-in
defaults.

### Where the configuration is read from

In order; the first file found is used.

1. `--provider-config <path>`
2. `MINA_PROVISION_CONFIG`
3. `./mina-provision.yaml`
4. `$XDG_CONFIG_HOME/mina-provision/config.yaml`
5. `/etc/mina-provision/config.yaml`

The file is **merged over** the built-in defaults, per provider, per network,
per artifact. Adding a mirror therefore does not mean restating the default
entries, and those entries keep receiving updates. An artifact that is
mentioned is replaced whole, so a partial entry cannot inherit half of the
endpoint it replaces.

The configuration is read from disk only, and is never fetched. It states which
hosts may supply artifacts, so downloading it would remove the property that
makes it worth having.

### Writing a provider

```yaml
version: 1
default_provider: acme

providers:
  acme:
    description: internal mirror
    networks:
      mainnet:
        archive_dump:
          backend: http
          base_url: https://artifacts.acme.internal/mina
          name: "dumps/mainnet-{date}.sql.tar.gz"
          checksum: sidecar          # expects <file>.sha256 beside it
        precomputed_blocks:
          backend: file
          path: /srv/mina/blocks
          name: "mainnet-{height}-{state_hash}.json"
          index: https://artifacts.acme.internal/mina/blocks.txt
```

Backends:

| Backend | Reads from | Can list names |
|---|---|---|
| `gcs` | a public Google Cloud Storage bucket | yes |
| `http` | any web server | only with an `index:` |
| `file` | a local or mounted directory | yes |
| `apt` | a Debian repository; `config` only | not applicable |

A web server cannot be enumerated, so `blocks` needs an `index:` URL — one
object name per line — from an `http` provider. Without it the command reports
that discovery is impossible rather than reporting no blocks, which would read
as "the block is not published".

Name templates carry these fields, and a template using any other field is
rejected when the configuration is read:

| Artifact | Fields |
|---|---|
| `archive_dump` | `{date}`, `{hour}`  |
| `precomputed_blocks` | `{height}`, `{state_hash}` |
| `config` | `{ref}` |

### Verification

`checksum:` states how a download is proved to be what the publisher intended.

| Mode | Meaning |
|---|---|
| `index` | the digest comes from the Debian repository index; `apt` only |
| `sidecar` | the digest comes from `<file>.sha256` beside the object |
| `none` | only the transfer itself is checked |

A configured check that cannot be performed is a failure, not a silent pass: a
missing sidecar fails the download.

## Scope

This tool fetches, verifies and places files. It does not write blocks into an
archive database, and it does not repair gaps.

Inserting a precomputed block is not a file copy: the block is decoded, its
hashes are derived, and about twenty related tables are written. That logic
belongs to the archive writer, and a second implementation of it in another
language would diverge silently.

| Concern | Owner |
|---|---|
| Where artifacts are, and whether they are genuine | `mina-provision` |
| What is missing from a database, and writing it | `mina-archive` |
| Verifying a block's proof | [`mina-verify`](https://github.com/MinaProtocol/mina-verify) |

Dumps are produced hourly, so a restored database is behind the chain tip.
Fetch the difference with `mina-provision blocks` and apply it with
`mina-archive`, which takes a local directory of block files as input.

## Environment variables

| Variable | Flag |
|---|---|
| `MINA_NETWORK` | `--network` |
| `MINA_PROVIDER` | `--provider` |
| `MINA_PROVISION_CONFIG` | `--provider-config` |

A flag on the command line always wins over the environment, and an empty
variable is treated as unset.

## Building

```bash
go build ./...
go vet ./...
go test ./...                  # no network access
go test -tags integration ./...  # needs PostgreSQL and live publishers
```

The default test suite never touches the network. A daily workflow runs the
live checks against the default provider, so a bucket that is renamed or a
naming rule that changes is noticed in this repository rather than by an
operator.

## Releasing

Pushing a `vX.Y.Z` tag builds a static binary for `amd64` and `arm64`, packages
each as a `.deb`, attaches them to a GitHub Release, and publishes the signed
packages to the `stable` component of `stable.apt.packages.minaprotocol.com`.
`amd64` goes to bullseye, focal, jammy, bookworm and noble; `arm64` goes to
bookworm and noble, the two distributions that declare it.
Publication is described in [`docs/releasing.md`](docs/releasing.md).

## Planned

- `ledger` — genesis and epoch ledger tarballs. A ledger tarball's identity is
  its sha3 value, equal to `s3_data_hash`, while its file name carries a blake2
  hash.
- `checkpoint` — replayer checkpoints.
- `peers` — seed and peer lists.
- Signature verification, beyond the published checksum.
- An `s3` backend.
