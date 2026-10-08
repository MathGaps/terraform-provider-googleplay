#!/usr/bin/env bash
# Copyright (c) MathGaps
# SPDX-License-Identifier: MPL-2.0

# Fail when a Go source file or shell script is missing the licence header.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

missing=()
while IFS= read -r file; do
  if ! head -n 5 "$file" | grep -q 'SPDX-License-Identifier: MPL-2.0'; then
    missing+=("$file")
  fi
done < <(git ls-files --cached --others --exclude-standard -- '*.go' '*.sh' | grep -v '^examples/')

if [ ${#missing[@]} -ne 0 ]; then
  echo "Missing the licence header (Copyright (c) MathGaps / SPDX-License-Identifier: MPL-2.0):" >&2
  printf '  %s\n' "${missing[@]}" >&2
  exit 1
fi
