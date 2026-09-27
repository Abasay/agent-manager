#!/usr/bin/env bash
# Update Helm chart versions and image tags
# Usage: update-helm-charts.sh <target-version> <release-tag>

set -euo pipefail

TARGET_VERSION="${1:-}"

if [ -z "$TARGET_VERSION" ]; then
  echo "Error: Missing required arguments"
  echo "Usage: update-helm-charts.sh <target-version>"
  exit 1
fi

# Which versioned documentation this release points at. The rule lives in
# resolve-docs-version.sh so the promotion workflow can pre-flight it; empty
# means no docs version was cut and the chart keeps /docs/latest.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DOCS_VERSION="$(bash "$SCRIPT_DIR/resolve-docs-version.sh" "$TARGET_VERSION")"

# A release must not ship a console pinned to documentation that was never
# published, so fail early rather than emitting links that 404. The check reads
# the manifest published by wso2/docs-agent-platform; candidates and nightlies
# pass straight through, since DOCS_VERSION is empty for them.
bash "$SCRIPT_DIR/assert-docs-version-published.sh" "$DOCS_VERSION" "$TARGET_VERSION"

if [ -n "$DOCS_VERSION" ]; then
  echo "Documentation version for this release: $DOCS_VERSION"
else
  echo "No documentation version for $TARGET_VERSION; the console will follow /docs/latest"
fi

# Find all Chart.yaml files and replace 0.0.0-dev
find ./deployments/helm-charts -name "Chart.yaml" -type f | while read -r chart_file; do
  # Replace version: 0.0.0-dev with vTARGET_VERSION (using | as delimiter to avoid conflicts with / in version)
  sed -i.bak "s|version: 0\.0\.0-dev|version: v$TARGET_VERSION|g" "$chart_file"
  # Replace appVersion: "0.0.0-dev" with vTARGET_VERSION (all versions must be vx.x.x format)
  sed -i.bak "s|appVersion: \"0\.0\.0-dev\"|appVersion: \"v$TARGET_VERSION\"|g" "$chart_file"
  # Remove backup files
  rm -f "${chart_file}.bak"
done

# Find all values.yaml files and replace 0.0.0-dev in image tags
find ./deployments/helm-charts -name "values.yaml" -type f | while read -r values_file; do
  # Replace tag: "0.0.0-dev" with vTARGET_VERSION (all image tags must be vx.x.x format)
  sed -i.bak "s|tag: \"0\.0\.0-dev\"|tag: \"v$TARGET_VERSION\"|g" "$values_file"
  # Replace ampVersion: "0.0.0-dev" with vTARGET_VERSION — pins the console's
  # deployment-script git ref (amp/vX.Y.Z) to this release
  sed -i.bak "s|ampVersion: \"0\.0\.0-dev\"|ampVersion: \"v$TARGET_VERSION\"|g" "$values_file"
  # Pin the console's in-product doc links to this release's documentation.
  # Left untouched when there is no docs version, so the chart keeps "latest".
  if [ -n "$DOCS_VERSION" ]; then
    sed -i.bak "s|docsVersion: \"latest\"|docsVersion: \"$DOCS_VERSION\"|g" "$values_file"
  fi
  # Remove backup files
  rm -f "${values_file}.bak"
done

echo "✅ Updated all Helm chart versions"
