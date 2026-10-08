// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

//go:build tools

// Package tools pins the version of the documentation generator. It is a
// separate module so that tfplugindocs and its dependencies stay out of the
// provider's own go.mod. scripts/generate-docs.sh runs it.
package tools

import (
	_ "github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs"
)
