#!/usr/bin/env bash
# Copyright (c) MathGaps
# SPDX-License-Identifier: MPL-2.0

# Format examples/ and regenerate docs/ with tfplugindocs.
#
# tfplugindocs normally builds the provider and asks a `terraform` binary for
# its schema, downloading Terraform when there is none on PATH. This project is
# an OpenTofu provider, so the schema is taken with `tofu` here and handed to
# tfplugindocs with --providers-schema, which then calls no CLI at all.
# Set TF_BINARY=terraform to use Terraform instead.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TF="${TF_BINARY:-tofu}"

if ! command -v "$TF" > /dev/null; then
  echo "$TF not found on PATH. Install OpenTofu, or set TF_BINARY to a terraform binary." >&2
  exit 1
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "==> Formatting examples"
"$TF" fmt -recursive "$REPO_ROOT/examples/"

# tfplugindocs looks the schema up under registry.terraform.io/hashicorp/<name>
# (or the bare name), so the freshly built provider is installed into a
# throwaway mirror under that address. The address is only a lookup key: it
# appears nowhere in the generated pages.
SCHEMA_ADDR="registry.terraform.io/hashicorp/googleplay"
MIRROR="$WORK/mirror/$SCHEMA_ADDR/0.0.1/$(go env GOOS)_$(go env GOARCH)"
mkdir -p "$MIRROR" "$WORK/schema"

echo "==> Building the provider"
(cd "$REPO_ROOT" && go build -o "$MIRROR/terraform-provider-googleplay_v0.0.1" .)

cat > "$WORK/tfrc" <<TFRC
provider_installation {
  filesystem_mirror {
    path    = "$WORK/mirror"
    include = ["$SCHEMA_ADDR"]
  }
}
TFRC

cat > "$WORK/schema/main.tf" <<TF
terraform {
  required_providers {
    googleplay = {
      source = "$SCHEMA_ADDR"
    }
  }
}
TF

echo "==> Reading the provider schema with $TF"
(
  cd "$WORK/schema"
  export TF_CLI_CONFIG_FILE="$WORK/tfrc" TF_IN_AUTOMATION=1
  "$TF" init -backend=false -input=false -no-color > /dev/null
  "$TF" providers schema -json > "$WORK/schema.json"
)

echo "==> Generating docs"
(
  cd "$REPO_ROOT/tools"
  go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs generate \
    --provider-dir .. \
    --provider-name googleplay \
    --rendered-provider-name "Google Play" \
    --providers-schema "$WORK/schema.json"
)
