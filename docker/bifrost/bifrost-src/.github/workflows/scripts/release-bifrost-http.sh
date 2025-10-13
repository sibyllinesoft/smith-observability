#!/usr/bin/env bash
set -euo pipefail

# Release bifrost-http component
# Usage: ./release-bifrost-http.sh <version>

# Source Go utilities for exponential backoff
source "$(dirname "$0")/go-utils.sh"

# Validate input argument
if [ "${1:-}" = "" ]; then
  echo "Usage: $0 <version>" >&2
  exit 1
fi

VERSION="$1"
TAG_NAME="transports/v${VERSION}"

echo "🚀 Releasing bifrost-http v$VERSION..."

# Ensure tags are available (CI often does shallow clones)
git fetch --tags --force >/dev/null 2>&1 || true
LATEST_CORE_TAG=$(git tag -l "core/v*" | sort -V | tail -1)
LATEST_FRAMEWORK_TAG=$(git tag -l "framework/v*" | sort -V | tail -1)

if [ -z "$LATEST_CORE_TAG" ]; then
  CORE_VERSION="v$(tr -d '\n\r' < core/version)"
else
  CORE_VERSION=${LATEST_CORE_TAG#core/}
fi

if [ -z "$LATEST_FRAMEWORK_TAG" ]; then
  FRAMEWORK_VERSION="v$(tr -d '\n\r' < framework/version)"
else
  FRAMEWORK_VERSION=${LATEST_FRAMEWORK_TAG#framework/}
fi

echo "🔍 DEBUG: LATEST_CORE_TAG: $LATEST_CORE_TAG"
echo "🔍 DEBUG: CORE_VERSION: $CORE_VERSION"
echo "🔍 DEBUG: LATEST_FRAMEWORK_TAG: $LATEST_FRAMEWORK_TAG"
echo "🔍 DEBUG: FRAMEWORK_VERSION: $FRAMEWORK_VERSION"


# Get latest plugin versions
echo "🔌 Getting latest plugin release versions..."
declare -A PLUGIN_VERSIONS

# First, get versions for plugins that exist in the plugins/ directory
for plugin_dir in plugins/*/; do
  if [ -d "$plugin_dir" ]; then
    plugin_name=$(basename "$plugin_dir")

    # Check if VERSION parameter contains prerelease suffix
    if [[ "$VERSION" == *"-"* ]]; then
      # VERSION has prerelease, so include all versions but prefer stable
      ALL_TAGS=$(git tag -l "plugins/${plugin_name}/v*" | sort -V)
      STABLE_TAGS=$(echo "$ALL_TAGS" | grep -v '\-' || true)
      PRERELEASE_TAGS=$(echo "$ALL_TAGS" | grep '\-' || true)

      if [ -n "$STABLE_TAGS" ]; then
        # Get the highest stable version
        LATEST_PLUGIN_TAG=$(echo "$STABLE_TAGS" | tail -1)
        echo "latest plugin tag (stable preferred): $LATEST_PLUGIN_TAG"
      else
        # No stable versions, get highest prerelease
        LATEST_PLUGIN_TAG=$(echo "$PRERELEASE_TAGS" | tail -1)
        echo "latest plugin tag (prerelease only): $LATEST_PLUGIN_TAG"
      fi
    else
      # VERSION has no prerelease, so only consider stable releases
      LATEST_PLUGIN_TAG=$(git tag -l "plugins/${plugin_name}/v*" | grep -v '\-' | sort -V | tail -1 || true)
      echo "latest plugin tag (stable only): $LATEST_PLUGIN_TAG"
    fi

    if [ -z "$LATEST_PLUGIN_TAG" ]; then
      # No matching release found, use version from file
      PLUGIN_VERSION="v$(tr -d '\n\r' < "${plugin_dir}version")"
      echo "   📦 $plugin_name: $PLUGIN_VERSION (from version file - not yet released)"
    else
      PLUGIN_VERSION=${LATEST_PLUGIN_TAG#plugins/${plugin_name}/}
      echo "   📦 $plugin_name: $PLUGIN_VERSION (latest release)"
    fi

    PLUGIN_VERSIONS["$plugin_name"]="$PLUGIN_VERSION"
  fi
done

# Also check for any plugins already in transport go.mod that might not be in plugins/ directory
cd transports
echo "🔍 Checking for additional plugins in transport go.mod..."
# Parse go.mod plugin lines and add missing ones
while IFS= read -r plugin_line; do
  plugin_name=$(echo "$plugin_line" | awk -F'/' '{print $NF}' | awk '{print $1}')
  current_version=$(echo "$plugin_line" | awk '{print $NF}')

  # Only add if we don't already have this plugin
  if [[ -z "${PLUGIN_VERSIONS[$plugin_name]:-}" ]]; then
    echo "   📦 $plugin_name: $current_version (from transport go.mod)"
    PLUGIN_VERSIONS["$plugin_name"]="$current_version"
  fi
done < <(grep "github.com/maximhq/bifrost/plugins/" go.mod)
cd ..

echo "🔧 Using versions:"
echo "   Core: $CORE_VERSION"
echo "   Framework: $FRAMEWORK_VERSION"
echo "   Plugins:"
for plugin_name in "${!PLUGIN_VERSIONS[@]}"; do
  echo "     - $plugin_name: ${PLUGIN_VERSIONS[$plugin_name]}"
done

# Update transport dependencies to use latest plugin releases
echo "🔧 Using latest plugin release versions for transport..."
PLUGINS_USED=()

# Track which plugins are actually used by the transport
cd transports
for plugin_name in "${!PLUGIN_VERSIONS[@]}"; do
  plugin_version="${PLUGIN_VERSIONS[$plugin_name]}"

  # Check if transport depends on this plugin
  if grep -q "github.com/maximhq/bifrost/plugins/$plugin_name" go.mod; then
    echo "  📦 Using $plugin_name plugin $plugin_version"
    go_get_with_backoff "github.com/maximhq/bifrost/plugins/$plugin_name@$plugin_version"
    PLUGINS_USED+=("$plugin_name:$plugin_version")
  fi
done

# Also ensure core and framework are up to date

echo "  🔧 Updating core to $CORE_VERSION"
go_get_with_backoff "github.com/maximhq/bifrost/core@$CORE_VERSION"

echo "  📦 Updating framework to $FRAMEWORK_VERSION"
go_get_with_backoff "github.com/maximhq/bifrost/framework@$FRAMEWORK_VERSION"

go mod tidy

cd ..

# We need to build UI first before we can validate the transport build
echo "🎨 Building UI..."
make build-ui

# Validate transport build
echo "🔨 Validating transport build..."
cd transports
go test ./...
cd ..
echo "✅ Transport build validation successful"

# Commit and push changes if any
# First, stage any changes made to transports/
git add transports/
if ! git diff --cached --quiet; then
  git pull origin main
  git config user.name "github-actions[bot]"
  git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
  echo "🔧 Committing and pushing changes..."
  git commit -m "transports: update dependencies --skip-pipeline"
  git push -u origin HEAD
else
  echo "ℹ️ No staged changes to commit"
fi

# Install cross-compilation toolchains
echo "📦 Installing cross-compilation toolchains..."
bash ./.github/workflows/scripts/install-cross-compilers.sh

# Build Go executables
echo "🔨 Building executables..."
bash ./.github/workflows/scripts/build-executables.sh $VERSION

# Configure and upload to R2
echo "📤 Uploading binaries..."
bash ./.github/workflows/scripts/configure-r2.sh
bash ./.github/workflows/scripts/upload-to-r2.sh "$TAG_NAME"

# Capturing changelog
CHANGELOG_BODY=$(cat transports/changelog.md)
# Skip comments from changelog
CHANGELOG_BODY=$(echo "$CHANGELOG_BODY" | grep -v '^<!--' | grep -v '^-->')
# If changelog is empty, return error
if [ -z "$CHANGELOG_BODY" ]; then
  echo "❌ Changelog is empty"
  exit 1
fi
echo "📝 New changelog: $CHANGELOG_BODY"

# Finding previous tag
echo "🔍 Finding previous tag..."
PREV_TAG=$(git tag -l "transports/v*" | sort -V | tail -1)
if [[ "$PREV_TAG" == "$TAG_NAME" ]]; then
  PREV_TAG=$(git tag -l "transports/v*" | sort -V | tail -2 | head -1)
fi
echo "🔍 Previous tag: $PREV_TAG"

# Get message of the tag
echo "🔍 Getting previous tag message..."
PREV_CHANGELOG=$(git tag -l --format='%(contents)' "$PREV_TAG")
echo "📝 Previous changelog body: $PREV_CHANGELOG"

# Checking if tag message is the same as the changelog
if [[ "$PREV_CHANGELOG" == "$CHANGELOG_BODY" ]]; then
  echo "❌ Changelog is the same as the previous changelog"
  exit 1
fi

# Create and push tag
echo "🏷️ Creating tag: $TAG_NAME"
git tag "$TAG_NAME" -m "Release transports v$VERSION" -m "$CHANGELOG_BODY"
git push origin "$TAG_NAME"

# Create GitHub release
TITLE="Bifrost HTTP v$VERSION"

# Mark prereleases when version contains a hyphen
PRERELEASE_FLAG=""
if [[ "$VERSION" == *-* ]]; then
  PRERELEASE_FLAG="--prerelease"
fi

LATEST_FLAG=""
if [[ "$VERSION" != *-* ]]; then
  LATEST_FLAG="--latest"
fi

# Generate plugin version summary
PLUGIN_UPDATES=""
if [ ${#PLUGINS_USED[@]} -gt 0 ]; then
  PLUGIN_UPDATES="

### 🔌 Plugin Versions
This release includes the following plugin versions:
"
  for plugin_info in "${PLUGINS_USED[@]}"; do
    plugin_name="${plugin_info%%:*}"
    plugin_version="${plugin_info##*:}"
    PLUGIN_UPDATES="$PLUGIN_UPDATES- **$plugin_name**: \`$plugin_version\`
"
  done
else
  # Show all available plugin versions even if not directly used
  PLUGIN_UPDATES="

### 🔌 Available Plugin Versions
The following plugin versions are compatible with this release:
"
  for plugin_name in "${!PLUGIN_VERSIONS[@]}"; do
    plugin_version="${PLUGIN_VERSIONS[$plugin_name]}"
    PLUGIN_UPDATES="$PLUGIN_UPDATES- **$plugin_name**: \`$plugin_version\`
"
  done
fi

BODY="## Bifrost HTTP Transport Release v$VERSION

$CHANGELOG_BODY

### Installation

#### Docker
\`\`\`bash
docker run -p 8080:8080 maximhq/bifrost:v$VERSION
\`\`\`

#### Binary Download
\`\`\`bash
npx @maximhq/bifrost --transport-version v$VERSION
\`\`\`

### Docker Images
- **\`maximhq/bifrost:v$VERSION\`** - This specific version
- **\`maximhq/bifrost:latest\`** - Latest version (updated with this release)

---
_This release was automatically created with dependencies: core \`$CORE_VERSION\`, framework \`$FRAMEWORK_VERSION\`. All plugins have been validated and updated._"

if [ -z "${GH_TOKEN:-}" ] && [ -z "${GITHUB_TOKEN:-}" ]; then
  echo "Error: GH_TOKEN or GITHUB_TOKEN is not set. Please export one to authenticate the GitHub CLI."
  exit 1
fi

echo "🎉 Creating GitHub release for $TITLE..."
gh release create "$TAG_NAME" \
  --title "$TITLE" \
  --notes "$BODY" \
  ${PRERELEASE_FLAG} ${LATEST_FLAG}

echo "✅ Bifrost HTTP released successfully"

# Print summary
echo ""
echo "📋 Release Summary:"
echo "   🏷️  Tag: $TAG_NAME"
echo "   🔧 Core version: $CORE_VERSION"
echo "   🔧 Framework version: $FRAMEWORK_VERSION"
echo "   📦 Transport: Updated"
if [ ${#PLUGINS_USED[@]} -gt 0 ]; then
  echo "   🔌 Plugins used: ${PLUGINS_USED[*]}"
else
  echo "   🔌 Available plugins: $(printf "%s " "${!PLUGIN_VERSIONS[@]}")"
fi
echo "   🎉 GitHub release: Created"

echo "success=true" >> "$GITHUB_OUTPUT"
