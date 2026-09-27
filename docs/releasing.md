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
| `release` | static binaries for `amd64` and `arm64`, one `.deb` each, `SHA256SUMS`, a provenance attestation, a GitHub Release with all of them |
| `publish-apt` | the same packages in `stable.apt.packages.minaprotocol.com`, component `stable` |

The version is the tag with the leading `v` removed. It is what the `.deb`
carries and what the verification step looks for.

The `Package` workflow publishes the container image to GHCR in parallel, from
the same tag. Its `version-match` job then waits for the `amd64` `.deb` in the
GitHub Release, and fails unless the image and the package report the same
`--version`. The `docker build` CI job does the same check on every pull
request, against a package built in the same run.

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

The credentials are kept in the `apt-publish` environment, never at repository
level. A repository-level secret is readable by a workflow on any branch, so
anyone with push access could read it. An environment secret is given only to a
job that names the environment, and only after the environment's protection
rules pass.

Create the environment once (repository admin):

- **Settings → Environments → New environment**, name `apt-publish`.
- **Required reviewers:** the release owners.
- **Deployment branches and tags:** "Selected branches and tags", add the tag
  pattern `v*`.

Then set the secrets in it:

```bash
E="--env apt-publish --repo MinaProtocol/mina-provision"

# the repository is signed, so the packages must be signed too
gh secret set DEBIAN_SIGN_KEY_ID      $E
gh secret set DEBIAN_SIGN_PRIVATE_KEY $E   # armoured signing subkey, see below
gh secret set DEBIAN_SIGN_PASSPHRASE  $E   # the passphrase of that subkey

# AWS: preferred, a role assumed through GitHub OIDC (no stored AWS secret)
gh variable set AWS_ROLE_ARN --body arn:aws:iam::<account>:role/<role> --repo MinaProtocol/mina-provision
# or, only until the role exists, static keys
gh secret set AWS_ACCESS_KEY_ID       $E
gh secret set AWS_SECRET_ACCESS_KEY   $E

gh variable set PUBLISH_APT --body true --repo MinaProtocol/mina-provision
```

When `AWS_ROLE_ARN` is set, the static keys are ignored; delete them from the
environment and from IAM after the first release that uses the role.

`DEBIAN_SIGN_PRIVATE_KEY` is a signing-only subkey of the key that signs the
repository, exported with a passphrase. The primary key stays offline:

```bash
gpg --quick-add-key <primary-fpr> ed25519 sign 1y   # or rsa4096, to match the primary
gpg --armor --export-secret-subkeys <subkey-id>!    # the "!" exports this subkey only
```

apt verifies a subkey signature against the primary public key, so clients need
no change.

### The AWS role

The trust policy accepts only this repository's release environment:

```json
"Condition": {
  "StringEquals": {
    "token.actions.githubusercontent.com:aud": "sts.amazonaws.com",
    "token.actions.githubusercontent.com:sub": "repo:MinaProtocol/mina-provision:environment:apt-publish"
  }
}
```

The role (or, until then, the static keys) needs:

| Permission | Resource | For |
|---|---|---|
| `s3:ListBucket` | the bucket | reading the distribution |
| `s3:GetObject` | `dists/*`, `pool/*` | reading the index and checking for an existing package |
| `s3:PutObject` | `dists/*`, `pool/*/m/mi/mina-provision*` | the package, the index and the signed `Release` |
| `s3:DeleteObject` | `dists/*/lockfile*`, `dists/*/*/binary-/lockfile` | releasing and clearing the upload lock |
| `cloudfront:ListDistributions` | `*` | finding the distribution |
| `cloudfront:CreateInvalidation` | the one distribution | making the new index visible |

It must not have `s3:DeleteObject` on the whole bucket: other Mina packages are
served from it.

The bucket is named after the repository, `stable.apt.packages.minaprotocol.com`,
in `us-west-2`.

### Repository settings

These are part of the release's security, not only of this workflow
(repository admin):

- A **tag ruleset** on `refs/tags/v*`: only release owners may create, and
  nobody may update or delete.
- **Branch protection** on `main`: pull request, at least one approval, passing
  CI.
- **Actions → Workflow permissions:** read repository contents; do not let
  Actions approve pull requests. Each workflow asks for more only where it
  needs it.
- **Actions → Require actions to be pinned to a full-length commit SHA.**
- The same protection on `main` of `MinaProtocol/deb-s3`, whose code runs in the
  release job.

## Choices worth knowing

**Signing is not optional.** The job fails if `DEBIAN_SIGN_KEY_ID` or
`DEBIAN_SIGN_PRIVATE_KEY` is missing. An unsigned package in a signed
repository breaks `apt update` for every client of that distribution.

**Tooling and actions are pinned.** `deb-s3` is built from a fixed commit
(`DEB_S3_COMMIT` in `release.yml`) and every action from a full commit SHA.
Code that runs next to the release credentials changes only through a reviewed
change to this repository. Dependabot proposes the action updates; a `deb-s3`
update is a manual change of the commit after a review of its diff.

**Secrets reach only the steps that use them.** No secret is set at job level.
The AWS credentials are configured after `deb-s3` is installed, and the signing
key ID is given only to the import and upload steps.

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

**Checksums and provenance.** `SHA256SUMS` lists the binaries and the
packages. `actions/attest-build-provenance` signs a statement, through GitHub
OIDC, that each file in `dist/` was built by this workflow at the tagged
commit. Only the `release` job has the `id-token: write` and
`attestations: write` permissions this needs.

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
sudo install -d -m 0755 /etc/apt/keyrings
sudo wget -qO /etc/apt/keyrings/minaprotocol.gpg \
  https://stable.apt.packages.minaprotocol.com/repo-signing-key.gpg
gpg --show-keys /etc/apt/keyrings/minaprotocol.gpg
echo "deb [signed-by=/etc/apt/keyrings/minaprotocol.gpg] https://stable.apt.packages.minaprotocol.com $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
  | sudo tee /etc/apt/sources.list.d/mina.list
sudo apt-get update
apt-cache policy mina-provision
```

The signing key is the one that signs every Mina repository, fingerprint
`386E 9DAC 3787 26A4 8ED5  CE56 ADB3 0D9A CE02 F414`. `gpg --show-keys` must
show this fingerprint. The key is published at `/repo-signing-key.gpg` on each
repository host; `key.asc` does not exist.

`signed-by` makes apt trust the key for this one source only. A key in
`/etc/apt/trusted.gpg.d/` is trusted for every configured source.

The release assets, in a directory that has `SHA256SUMS` and the downloaded
files:

```bash
gh release download v0.1.0 -R MinaProtocol/mina-provision
sha256sum -c SHA256SUMS
gh attestation verify mina-provision-linux-amd64 -R MinaProtocol/mina-provision
```

`sha256sum -c --ignore-missing SHA256SUMS` checks only the files that were
downloaded. `gh attestation verify` accepts any of the files in the release,
`SHA256SUMS` included.
