#!/bin/bash
set -e

# Get the latest tag
LAST_TAG=$(git describe --tags --abbrev=0 2>/dev/null || echo "")

# Get commits since the last tag, or all commits if no tag exists
if [ -z "$LAST_TAG" ]; then
  COMMITS=$(git log --pretty=format:"%s")
else
  COMMITS=$(git log "$LAST_TAG"..HEAD --pretty=format:"%s")
fi

# Check if there are any new commits
if [ -z "$COMMITS" ]; then
  echo "No new commits since ${LAST_TAG:-beginning of history}. Exiting."
  exit 0
fi

# If no tag, start at 0.0.0 for first release
if [ -z "$LAST_TAG" ]; then
  MAJOR=0
  MINOR=0
  PATCH=0
else
  IFS='.' read -r MAJOR MINOR PATCH <<<"${LAST_TAG#v}"
  MAJOR=$((10#$MAJOR))
  MINOR=$((10#$MINOR))
  PATCH=$((10#$PATCH))
fi

# Check for breaking changes (type! or type(scope)!: ...)
if echo "$COMMITS" | grep -Eq "^(feat|fix|chore|docs|style|refactor|perf|test|build|ci)(\(.+\))?!: |^(feat|fix|chore|docs|style|refactor|perf|test|build|ci)!:"; then
  MAJOR=$((MAJOR + 1))
  MINOR=0
  PATCH=0
elif echo "$COMMITS" | grep -q "^feat"; then
  MINOR=$((MINOR + 1))
  PATCH=0
elif echo "$COMMITS" | grep -q "^fix"; then
  PATCH=$((PATCH + 1))
fi

NEXT_VERSION="v$MAJOR.$MINOR.$PATCH"
CURRENT_DATE=$(date +%Y-%m-%d)

# Generate categorized changelog for this release
NEW_CHANGELOG=$(echo "$COMMITS" | \
  grep -E "^(feat|fix|chore|docs|style|refactor|perf|test|build|ci)(\(.+\))?: |^(feat|fix|chore|docs|style|refactor|perf|test|build|ci)(\(.+\))?!: |^(feat|fix|chore|docs|style|refactor|perf|test|build|ci)!:" | \
  awk -v ver="$NEXT_VERSION" -v date="$CURRENT_DATE" '
    BEGIN { print "## " ver " - " date "\n" }
    /^feat/ { feats = feats $0 "\n" }
    /^fix/ { fixes = fixes $0 "\n" }
    /^chore/ { chores = chores $0 "\n" }
    /^docs/ { docs = docs $0 "\n" }
    /^style/ { styles = styles $0 "\n" }
    /^refactor/ { refactors = refactors $0 "\n" }
    /^perf/ { perfs = perfs $0 "\n" }
    /^test/ { tests = tests $0 "\n" }
    /^build/ { builds = builds $0 "\n" }
    /^ci/ { cis = cis $0 "\n" }
    END {
      if (feats) print "### Features\n" feats
      if (fixes) print "### Fixes\n" fixes
      if (chores) print "### Chores\n" chores
      if (docs) print "### Documentation\n" docs
      if (styles) print "### Styles\n" styles
      if (refactors) print "### Refactoring\n" refactors
      if (perfs) print "### Performance\n" perfs
      if (tests) print "### Tests\n" tests
      if (builds) print "### Build\n" builds
      if (cis) print "### CI\n" cis
      print "---\n"
    }
  ')

# Confirmation before pushing changelog commit and tag
echo "About to push the changelog commit and tag '$NEXT_VERSION'."
read -p "Continue? [y/N]: " CONFIRM
if [[ ! "$CONFIRM" =~ ^[Yy]$ ]]; then
  echo "Aborted by user."
  exit 1
fi

# If there's no CHANGELOG.md then create one with a placeholder
CHANGELOG="CHANGELOG.md"
PLACEHOLDER="<!-- changelog-placeholder -->"
if [ ! -f "$CHANGELOG" ]; then
  echo -e "# Changelog\n\n$PLACEHOLDER\n" > "$CHANGELOG"
fi

# Insert new changelog entry after the placeholder
awk -v entry="$NEW_CHANGELOG" -v ph="$PLACEHOLDER" '
  BEGIN { done=0 }
  {
    print
    if (!done && index($0, ph)) {
      print entry
      done=1
    }
  }
' "$CHANGELOG" > "$CHANGELOG.tmp" && mv "$CHANGELOG.tmp" "$CHANGELOG"

# Create a commit with the updated changelog and push it
git add "$CHANGELOG"
git commit -m "docs(changelog): $NEXT_VERSION"
git push

# Create a new tag with the next version and changelog as description
git tag -a "$NEXT_VERSION" -m "$NEW_CHANGELOG"
# 6. Push the new tag
git push origin "$NEXT_VERSION"
