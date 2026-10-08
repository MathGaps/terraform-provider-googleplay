#!/usr/bin/env bash
# Copyright (c) MathGaps
# SPDX-License-Identifier: MPL-2.0

# Run `tofu validate` over every directory under examples/.
#
# `make generate` only formats the examples, which catches syntax but not an
# attribute the schema does not have or a value a validator rejects. Those only
# surface under `validate`, which needs the provider installed.
#
# The working tree is built here and installed into a throwaway filesystem
# mirror, so the examples validate against the schema in this checkout rather
# than the last release on the registry. dev_overrides is deliberately not used:
# it makes `init` refuse to run, and these directories need init.
#
# Set TF_BINARY=terraform to validate with Terraform instead of OpenTofu.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TF="${TF_BINARY:-tofu}"

if ! command -v "$TF" > /dev/null; then
  echo "$TF not found on PATH. Install OpenTofu, or set TF_BINARY to a terraform binary." >&2
  exit 1
fi

# `source = "mathgaps/googleplay"` resolves to the default registry of whichever
# CLI runs it, so the mirror holds the provider under both hosts.
PROVIDER_HOSTS=("registry.opentofu.org" "registry.terraform.io")
PROVIDER_PATH="mathgaps/googleplay"
# Must satisfy the version constraint the examples pin.
PROVIDER_VERSION="0.1.0"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "==> Building the provider into a local mirror"
go build -C "$REPO_ROOT" -o "$WORK/terraform-provider-googleplay" .
for host in "${PROVIDER_HOSTS[@]}"; do
  dir="$WORK/mirror/$host/$PROVIDER_PATH/$PROVIDER_VERSION/$(go env GOOS)_$(go env GOARCH)"
  mkdir -p "$dir"
  cp "$WORK/terraform-provider-googleplay" "$dir/terraform-provider-googleplay_v$PROVIDER_VERSION"
done

cat > "$WORK/tfrc" <<TFRC
provider_installation {
  filesystem_mirror {
    path    = "$WORK/mirror"
    include = ["registry.opentofu.org/mathgaps/*", "registry.terraform.io/mathgaps/*"]
  }
  direct {
    exclude = ["registry.opentofu.org/mathgaps/*", "registry.terraform.io/mathgaps/*"]
  }
}
TFRC
export TF_CLI_CONFIG_FILE="$WORK/tfrc"
export TF_IN_AUTOMATION=1

failed=()
while read -r dir; do
  name="${dir//\//_}"
  staged="$WORK/staged/$name"
  mkdir -p "$staged"
  cp "$REPO_ROOT/$dir"/*.tf "$staged/"

  # tfplugindocs examples carry no terraform block of their own, so the source
  # address has to be supplied for init to resolve the provider.
  if ! grep -qs required_providers "$staged"/*.tf; then
    cat > "$staged/zz_required_providers_shim.tf" <<TF
terraform {
  required_providers {
    googleplay = {
      source = "$PROVIDER_PATH"
    }
  }
}
TF
  fi

  echo "==> $dir"
  if (cd "$staged" && "$TF" init -backend=false -input=false -no-color > /dev/null && "$TF" validate -no-color); then
    :
  else
    failed+=("$dir")
  fi
done < <(cd "$REPO_ROOT" && find examples -type f -name '*.tf' -exec dirname {} \; | sort -u)

if [ ${#failed[@]} -ne 0 ]; then
  echo
  echo "$TF validate failed in ${#failed[@]} example director$([ ${#failed[@]} -eq 1 ] && echo y || echo ies):"
  printf '  %s\n' "${failed[@]}"
  exit 1
fi

echo
echo "All example directories validate."
