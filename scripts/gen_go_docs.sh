#!/usr/bin/env bash
# Generate markdown Go package reference docs (one file per package) under
# docs/code/ using the stdlib `go doc` toolchain — no external generators.
set -euo pipefail
cd "$(dirname "$0")/.."

outdir=docs/code
mkdir -p "$outdir"

# Clear previously generated package files (keep handwritten ones, if any).
find "$outdir" -name '*.generated.md' -delete

count=0
for d in $(find internal cmd -mindepth 1 -maxdepth 1 -type d | sort); do
  doc=$(go doc -all "./$d" 2>/dev/null || true)
  [ -z "$doc" ] && continue
  name=$(basename "$d")
  group=$(dirname "$d")            # internal | cmd
  slug="code/${group}/${name}"
  title="${group}/${name}"
  file="$outdir/${group}-${name}.generated.md"
  {
    echo '---'
    echo "slug: ${slug}"
    echo "title: Package ${title}"
    echo "description: Auto-generated go doc reference for the ${title} package."
    echo '---'
    echo
    echo "# Package \`${title}\`"
    echo
    echo "_Generated with \`go doc -all ./${d}\`. Regenerate with \`scripts/gen_go_docs.sh\`._"
    echo
    echo '```text'
    echo "$doc"
    echo '```'
  } > "$file"
  count=$((count + 1))
done

echo "wrote $count package docs under $outdir"
