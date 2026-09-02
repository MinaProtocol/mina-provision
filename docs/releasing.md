# Releasing

A release is a `vX.Y.Z` tag. Pushing it runs the `Release` workflow, which
builds the binaries, packages them, publishes a GitHub Release, and uploads
the packages to the apt repository.

```bash
git tag v0.1.0
git push origin v0.1.0
```

## What the workflow does

| Job | Output |
|---|---|
| `release` | static binaries for `amd64` and `arm64`, one `.deb` each, a GitHub Release with both |
| `publish-apt` | the same `.deb` files in `packages.o1test.net`, component `stable`, for five codenames |

The version is the tag with the leading `v` removed, and it is what the `.deb`
carries and what the verification step looks for.

The `Package` workflow publishes the container image to GHCR in parallel, from
the same tag.

## Enabling publication

`publish-apt` does not run until it is switched on. This is deliberate: without
the gate, a tag push would fail at the upload step on a repository that has no
credentials, rather than simply not publishing.

Two secrets and one variable are needed.

```bash
gh secret set AWS_ACCESS_KEY_ID     --repo MinaProtocol/mina-provision
gh secret set AWS_SECRET_ACCESS_KEY --repo MinaProtocol/mina-provision
gh variable set PUBLISH_APT --body true --repo MinaProtocol/mina-provision
```

The credentials need write access to the `packages.o1test.net` bucket in
`us-west-2`. The repository is unsigned, so no GPG key is involved.

## Choices worth knowing

**One codename at a time.** `deb-s3` rewrites the shared index files of the
bucket on every upload. Two jobs uploading at once can drop each other's
entries, so the matrix runs with `max-parallel: 1`.

**One package per call.** `deb-s3` handles a single upload predictably.
Batching several packages into one call does not report a per-package outcome,
so a partial failure is invisible.

**`--preserve-versions`, never a prune.** `deb-s3` ranks versions by Debian
ordering, which is not upload order: a longer version string can outrank a
newer build. A "keep the latest N" prune can therefore delete the newest
package. Old versions are kept.

**The repository is read back.** The exit status of `deb-s3` is not a reliable
verdict on its own, so the job lists the component afterwards and fails unless
the new version appears in it.

## Verifying by hand

```bash
deb-s3 list --bucket packages.o1test.net --s3-region us-west-2 \
  --codename noble --component stable | grep mina-provision
```

On a target host:

```bash
echo "deb [trusted=yes] http://packages.o1test.net $(lsb_release -cs) stable" \
  | sudo tee /etc/apt/sources.list.d/mina.list
sudo apt-get update
apt-cache policy mina-provision
```
