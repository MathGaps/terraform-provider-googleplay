// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

// Package main serves the Google Play provider for OpenTofu and Terraform,
// built on the Terraform Plugin Framework.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/MathGaps/terraform-provider-googleplay/internal/provider"
)

// version is set by GoReleaser through -ldflags. It stays "dev" for local
// builds.
var version = "dev"

func main() {
	var debug bool

	flag.BoolVar(&debug, "debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	opts := providerserver.ServeOpts{
		// The OpenTofu Registry address this provider is published under.
		Address: provider.Address,
		Debug:   debug,
	}

	if err := providerserver.Serve(context.Background(), provider.New(version), opts); err != nil {
		log.Fatal(err.Error())
	}
}
