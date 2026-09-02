#!/usr/bin/env bash
#
# Invalidates the CloudFront cache for one distribution of an apt repository.
#
# The signed repositories are served through CloudFront. After an upload the
# edge caches still hold the previous Packages index and Release signature, so
# apt clients do not see the new package until those entries expire. The
# invalidation is what makes a release visible.
#
# Usage: invalidate-cdn.sh <bucket-or-cname> <codename>

set -euo pipefail

BUCKET="${1:?bucket or cname required}"
CODENAME="${2:?codename required}"

PATHS="/dists/${CODENAME}/*"

echo "resolving ${BUCKET}"
cf_domain="$(dig +short CNAME "$BUCKET" | sed 's/\.$//')"
if [ -z "$cf_domain" ]; then
  echo "could not resolve ${BUCKET} to a CloudFront domain" >&2
  exit 1
fi
echo "CloudFront domain: ${cf_domain}"

dist_id="$(aws cloudfront list-distributions \
  --query "DistributionList.Items[?DomainName=='${cf_domain}'].Id" \
  --output text)"
if [ -z "$dist_id" ] || [ "$dist_id" = "None" ]; then
  echo "no CloudFront distribution serves ${cf_domain}" >&2
  exit 1
fi
echo "distribution: ${dist_id}"

aws cloudfront create-invalidation \
  --distribution-id "$dist_id" \
  --paths "$PATHS"
