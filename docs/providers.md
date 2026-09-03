# Adding a provider

A provider is a publisher of Mina artifacts. The Mina Foundation is the
default one; it is not the only one. This page covers both ways of adding
another.

| You want | Do this | Section |
|---|---|---|
| Read from your own mirror, on your own hosts | Write a local configuration file | [A local provider](#a-local-provider) |
| Make a publisher available to everyone using this tool | Add it to the built-in defaults, in a pull request | [A built-in provider](#a-built-in-provider) |

Prefer the local file. A provider belongs in the built-in defaults only when it
publishes artifacts that are useful to operators generally — a public mirror,
or a new network the Foundation publishes. An internal store belongs in a file
on the hosts that use it.

## The model

Three levels, in this order:

```
provider  ->  network  ->  artifact
```

An artifact says where one kind of file lives, how it is named, and how it is
verified. Three kinds exist:

| Kind | Fetched by | Name template fields |
|---|---|---|
| `archive_dump` | `mina-provision archive` | `{date}`, `{hour}` |
| `precomputed_blocks` | `mina-provision blocks` | `{height}`, `{state_hash}` |
| `config` | `mina-provision config` | `{ref}` |

A provider does not have to publish all three. `mina-provision blocks
--provider x` fails with a clear message if `x` publishes no blocks for that
network, and the other commands keep working.

### The network key is a label, not a derivation

Nothing is derived from the network name. The key groups a set of artifacts;
the file name comes only from the `name` template. This matters because
**archive dumps are named after the node that produced them, which is often not
the network**. Two objects from the Foundation's own bucket:

```
mainnet-archive-dump-2026-09-03_0000.sql.tar.gz          the node name matches the network
mina-mesa-rc-1-archive-dump-2026-08-28_0700.sql.tar.gz   it does not
```

So a network key may legitimately hold a template that mentions a node, and two
providers may use different keys for the same chain. Write the name the
publisher actually produces, and never infer one from the other.

## A local provider

Write the file, point the tool at it, and nothing else changes:

```bash
mina-provision blocks --provider-config ./mina-provision.yaml \
  --provider acme --network mainnet --range 300000
```

The file is found without the flag in any of these places, in order:

1. `MINA_PROVISION_CONFIG`
2. `./mina-provision.yaml`
3. `$XDG_CONFIG_HOME/mina-provision/config.yaml`
4. `/etc/mina-provision/config.yaml`

### It merges, it does not replace

The file is merged over the built-in defaults, per provider, per network, per
artifact. Three consequences worth knowing before writing one:

- Adding a provider does not mean restating the default entries. They stay,
  and they keep receiving updates when the tool is upgraded.
- Overriding one artifact leaves the provider's other artifacts alone. This
  file makes `blocks` read a local directory while `archive` and `config`
  still read the Foundation's endpoints:

  ```yaml
  version: 1
  providers:
    o1labs:
      networks:
        mainnet:
          precomputed_blocks:
            backend: file
            path: /srv/mina/blocks
            name: "mainnet-{height}-{state_hash}.json"
  ```

- An artifact that is mentioned is replaced **whole**. A partial artifact does
  not inherit the half it did not restate, so an entry that names a `path` but
  omits `name` is rejected rather than silently keeping the old name rule.

To make your provider the one used when `--provider` is not given, set
`default_provider`.

## Backends

| Backend | Reads from | Required fields | Can list names |
|---|---|---|---|
| `gcs` | a public Google Cloud Storage bucket | `bucket` | yes |
| `http` | any web server | `base_url` | only with `index` |
| `file` | a local or mounted directory | `path` | yes |
| `apt` | a Debian repository, `config` only | `repository`, `package` | not applicable |

### `file` — a staged directory

The one that makes an air-gapped host work. Artifacts are staged once, and
every later run reads them without leaving the machine.

```yaml
version: 1
default_provider: staged
providers:
  staged:
    description: artifacts staged on this host
    networks:
      mainnet:
        precomputed_blocks:
          backend: file
          path: /srv/mina/blocks
          name: "mainnet-{height}-{state_hash}.json"
          checksum: sidecar
```

A name is not allowed to reach outside `path`, so an object name containing
`../` is refused rather than followed.

### `http` — a mirror, and why it needs an index

```yaml
version: 1
providers:
  acme:
    networks:
      mainnet:
        archive_dump:
          backend: http
          base_url: https://artifacts.acme.internal/mina
          name: "dumps/mainnet-{date}_{hour}.sql.tar.gz"
          checksum: sidecar
        precomputed_blocks:
          backend: http
          base_url: https://artifacts.acme.internal/mina
          name: "blocks/mainnet-{height}-{state_hash}.json"
          index: https://artifacts.acme.internal/mina/blocks.txt
```

`archive` needs no index: a date and an hour name the file completely.

`blocks` does need one. A block's name contains its state hash, which is not
known from the height, so a height narrows the name only to a prefix that then
has to be listed. A bucket can be listed; a web server cannot. `index` is a URL
serving one object name per line, and `blocks` keeps the lines that start with
the prefix. Without it the command reports that discovery is impossible, rather
than reporting no blocks — which would read as "that block is not published".

### `apt` — a Debian repository

Only for `config`, and only because of what the daemon loads. The daemon
auto-loads `/var/lib/coda/config_<hash>.json`, where the hash is derived from
the commit at build time and is not the hash in the package version. The
package is the only published artifact carrying both the right content and the
right file name.

```yaml
config:
  backend: apt
  repository: https://packages.o1test.net
  codename: noble
  component: stable
  package: mina-mainnet-config
  checksum: index
```

Any of these can be overridden per run with `--repository`, `--codename`,
`--component`, `--package` and `--version`.

## Verification

`checksum:` states how a download is proved to be what the publisher intended.
A configured check that cannot be performed **fails** the download; it never
passes quietly.

| Mode | Meaning | Available with |
|---|---|---|
| `index` | the digest comes from the repository index | `apt` only |
| `sidecar` | the digest comes from `<file>.sha256` beside the object | `gcs`, `http`, `file` |
| `none` | only the transfer itself is checked | all |

A sidecar is a file next to the object holding its hex SHA256. Both forms
`sha256sum` produces are accepted:

```
1c7313c8da63efbf4c0ddb113da03f9ca6bfd5d59669fb535abe485f41879ea2
1c7313c8da63efbf4c0ddb113da03f9ca6bfd5d59669fb535abe485f41879ea2  mainnet-300000-3NK.json
```

Publish sidecars if you can. `checksum: none` means an operator is trusting the
host and the transport, and nothing else. It is the honest setting for a
publisher that states no digest — and writing the word down makes that a
visible decision rather than a silent default.

## Name templates

The naming rule is data, not code. This is what lets a second publisher use its
own scheme: pluggable buckets alone would not be enough, because publishers
name their files differently.

- A placeholder not allowed for that artifact kind is rejected when the file is
  read, naming the provider, network, artifact and the fields that kind does
  have.
- A placeholder with no value at fetch time is an error. Leaving it in would
  request a file whose name contains a brace and report a confusing 404.
- An unbalanced or empty brace is rejected.

## Checking your file

Validation happens when the configuration is loaded, before any request, so any
command surfaces a mistake immediately:

```bash
$ mina-provision archive --provider-config ./bad.yaml --skip-pg
Error: provider o1labs, network mainnet, archive_dump: name template uses {height}, which archive_dump does not have (it has: date, hour)
```

Common messages and what they mean:

| Message | Cause |
|---|---|
| `field ... not found in type provider.Artifact` | a misspelt key. Unknown keys are refused, so a typo cannot silently leave the default endpoint in use |
| `unknown provider "x"; configured providers: ...` | `--provider` does not match any entry, after merging |
| `provider "x" publishes no <kind> for network "y"` | the provider is fine, that artifact is not defined |
| `backend gcs needs a bucket` | a required field for that backend is missing |
| `checksum: index needs an index to read` | `index` is only meaningful for `apt` |
| `provider serves ... over http with no index` | `blocks` from an `http` provider without `index` |

## A built-in provider

The built-in defaults live in `internal/provider/default.yaml`. They are not a
special case in the code: that file is embedded with `go:embed` and parsed by
the same reader a local file goes through, so anything a local provider can
express, a built-in one can too.

To add one:

1. Add the entry to `internal/provider/default.yaml`. Comment anything an
   operator could not infer — why a bucket is what it is, why a network is
   present, why a `checksum` is set the way it is.
2. Keep `default_provider` as it is. Adding a publisher must not change which
   one is used by default.
3. Add the network to `TestBuiltInDefaultsAreValid` in
   `internal/provider/provider_test.go` if the provider serves a network the
   defaults did not cover.
4. If the entry is for an existing network, add its expected file name to
   `TestDefaultProviderProducesTheKnownNames` in `cmd/archive_test.go`. That
   test exists so a change to a naming rule cannot pass unnoticed.
5. Run the offline suite, then the live one:

   ```bash
   go test ./...
   go test -tags integration ./internal/source/...
   ```

   The live test lists each default artifact and fails if nothing is there. It
   is what catches a bucket that has been renamed.

What a reviewer will look for:

- The endpoint is public, or the pull request says who can reach it.
- `checksum` is `none` only when the publisher genuinely states no digest.
- An `http` entry that serves blocks has an `index`.
- The name template matches what the publisher really produces, with a real
  example in the pull request description.

## Planned

There is no command yet that only validates a configuration and prints the
resolved endpoints. Validation is reached through a real command, which then
goes on to do its work. A `mina-provision providers` verb that lists and
validates without fetching is worth adding.
