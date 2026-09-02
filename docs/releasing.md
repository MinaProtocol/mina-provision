# Releasing

A release is a `vX.Y.Z` tag. Pushing it runs the `Release` workflow, which
builds the binaries, packages them, publishes a GitHub Release, and uploads the
packages to the signed apt repository.

```bash
git tag v0.1.0
git push origin v0.1.0
```

## What the workflow does

| Job | Output |
|---|---|
| `release` | static binaries for `amd64` and `arm64`, one `.deb` each, a GitHub Release with both |
| `publish-apt` | the same packages in `stable.apt.packages.minaprotocol.com`, component `stable` |

The version is the tag with the leading `v` removed. It is what the `.deb`
carries and what the verification step looks for.

The `Package` workflow publishes the container image to GHCR in parallel, from
the same tag.

## What is published where

| Codename | amd64 | arm64 |
|---|---|---|
| bullseye | yes | no |
| focal | yes | no |
| jammy | yes | no |
| bookworm | yes | yes |
| noble | yes | yes |

`amd64` goes to every supported distribution. `arm64` goes only to `bookworm`
and `noble`, which are the two that already declare that architecture.
Uploading `arm64` elsewhere would add an architecture to a shared, signed,
production distribution as a side effect of releasing this tool, and would
rewrite and re-sign a `Release` file that other packages depend on.

## Enabling publication

`publish-apt` does not run until it is switched on. This is deliberate: without
the gate, a tag push would fail at the upload step on a repository that has no
credentials, rather than simply not publishing.

```bash
# S3 write access to the bucket, plus CloudFront invalidation
gh secret set AWS_ACCESS_KEY_ID          --repo MinaProtocol/mina-provision
gh secret set AWS_SECRET_ACCESS_KEY      --repo MinaProtocol/mina-provision

# the repository is signed, so the packages must be signed too
gh secret set DEBIAN_SIGN_KEY_ID         --repo MinaProtocol/mina-provision
gh secret set DEBIAN_SIGN_PRIVATE_KEY    --repo MinaProtocol/mina-provision  # armoured private key
gh secret set DEBIAN_SIGN_PASSPHRASE     --repo MinaProtocol/mina-provision  # omit if the key has none

gh variable set PUBLISH_APT --body true  --repo MinaProtocol/mina-provision
```

`DEBIAN_SIGN_PRIVATE_KEY` is the armoured export of the key that signs the
repository:

```bash
gpg --armor --export-secret-keys <key-id>
```

The AWS credentials need:

| Permission | For |
|---|---|
| `s3:GetObject`, `s3:PutObject`, `s3:DeleteObject`, `s3:ListBucket` on the bucket | uploading and rewriting the distribution index |
| `cloudfront:ListDistributions`, `cloudfront:CreateInvalidation` | making the new index visible |

The bucket is named after the repository, `stable.apt.packages.minaprotocol.com`,
in `us-west-2`.

## Choices worth knowing

**Signing is not optional.** The job fails if `DEBIAN_SIGN_KEY_ID` or
`DEBIAN_SIGN_PRIVATE_KEY` is missing. An unsigned package in a signed
repository breaks `apt update` for every client of that distribution.

**The gpg agent is primed before deb-s3 runs.** `deb-s3` invokes `gpg` itself,
with no terminal attached, so the agent is configured for loopback pinentry and
given one signature of the workflow's own making. The signature `deb-s3` then
asks for does not need a passphrase it has no way to supply.

**One upload at a time** (`max-parallel: 1`). `deb-s3` rewrites the shared index
files of a distribution on every upload and takes a lock in the bucket to do
it. Concurrent jobs contend for that lock and can drop each other's entries.

**A stale lock is cleared, a live one is not.** A process that dies between
taking the lock and releasing it blocks every later upload.
`.github/scripts/clear-s3-lock.sh` removes a lock older than five minutes and
refuses to touch a younger one, because removing a live lock would let two
writers rewrite the same index.

**`--fail-if-exists`.** A release version must not already be present.
Overwriting a published package would change what an operator already
installed.

**`--preserve-versions`, never a prune.** `deb-s3` ranks versions by Debian
ordering, which is not upload order: a longer version string can outrank a
newer build, so a "keep the latest N" prune can delete the newest package. Old
versions are kept.

**The distribution is read back.** The exit status of `deb-s3` is not a reliable
verdict on its own, so the job lists the distribution afterwards and fails
unless the new version appears in it.

**The CDN cache is invalidated.** The signed repositories are served through
CloudFront. Without an invalidation, apt clients keep reading the previous
index and `Release` signature for as long as the edge caches hold them, and the
release is invisible.

## Verifying by hand

```bash
deb-s3 list --bucket stable.apt.packages.minaprotocol.com --s3-region us-west-2 \
  --codename noble --component stable --arch amd64 | grep mina-provision
```

On a target host:

```bash
sudo wget -q https://stable.apt.packages.minaprotocol.com/repo-signing-key.gpg \
  -O /etc/apt/trusted.gpg.d/minaprotocol.gpg
echo "deb https://stable.apt.packages.minaprotocol.com $(lsb_release -cs) stable" \
  | sudo tee /etc/apt/sources.list.d/mina.list
sudo apt-get update
apt-cache policy mina-provision
```

The signing key is the one that signs every Mina repository, key id
`386E9DAC378726A48ED5CE56ADB30D9ACE02F414`. It is published at
`/repo-signing-key.gpg` on each repository host; `key.asc` does not exist.
