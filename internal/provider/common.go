// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/MathGaps/terraform-provider-googleplay/internal/play"
)

var (
	developerIDPattern = regexp.MustCompile(`^[0-9]+$`)
	// An Android application id: at least two dot-separated segments, each
	// starting with a letter.
	packageNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)
	// "Product IDs must be composed of lower-case letters (a-z), numbers (0-9),
	// underscores (_) and dots (.). It must start with a lower-case letter or
	// number, and be between 1 and 40 (inclusive) characters in length."
	productIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.]{0,39}$`)
	// A track identifier is one path segment of the API's URLs and half of an
	// import id.
	trackPattern = regexp.MustCompile(`^[^/\s]+$`)
	// Base plan, purchase option and offer tag ids: lower-case letters,
	// numbers and hyphens.
	rfc1034Pattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

func packageNameValidators() []validator.String {
	return []validator.String{
		stringvalidator.RegexMatches(packageNamePattern, "must be an Android package name such as com.example.app"),
	}
}

const packageNameDescription = "The package name (application id) of the app, for example `com.example.app`. " +
	"The app must already exist in Play Console: the API cannot create one."

// clientFromProviderData returns the client the provider configured. It returns
// nil without a diagnostic before the provider is configured, when the
// framework calls Configure with no data.
func clientFromProviderData(providerData any, diags *diag.Diagnostics) *play.Client {
	if providerData == nil {
		return nil
	}

	client, ok := providerData.(*play.Client)
	if !ok {
		diags.AddError("Unexpected provider data",
			fmt.Sprintf("Expected *play.Client, got %T. This is a bug in the provider.", providerData))

		return nil
	}

	return client
}

// addAPIError reports a failed API call. The detail carries the response body
// Google sent, which is where the reason usually is.
func addAPIError(diags *diag.Diagnostics, summary string, err error) {
	if errors.Is(err, play.ErrNoDeveloperID) {
		diags.AddError("Missing developer account id",
			"This resource needs the Play Console developer account id. Set the provider's developer_id "+
				"attribute or the "+envDeveloperID+" environment variable to the number that follows "+
				"/developers/ in a Play Console URL.")

		return
	}

	diags.AddError(summary, play.ErrorDetail(err))
}

// splitImportID splits an import id of exactly n non-empty slash-separated
// parts.
func splitImportID(id string, names ...string) ([]string, error) {
	parts := strings.Split(id, "/")
	if len(parts) != len(names) || slices.Contains(parts, "") {
		return nil, fmt.Errorf("Expected an import id of the form %s, got %q.", strings.Join(names, "/"), id) //nolint:staticcheck // shown verbatim as a diagnostic
	}

	return parts, nil
}

// stringsFromSet returns the elements of a set of strings, sorted so that
// requests are deterministic. A null or unknown set gives nil.
func stringsFromSet(ctx context.Context, set types.Set, diags *diag.Diagnostics) []string {
	if set.IsNull() || set.IsUnknown() {
		return nil
	}

	var values []string
	diags.Append(set.ElementsAs(ctx, &values, false)...)
	slices.Sort(values)

	return values
}

// setFromStrings builds a set of strings. An empty result is null unless the
// prior value was a known empty set, so that a configuration that omits the
// attribute and one that writes [] both see no difference.
func setFromStrings(values []string, prior types.Set) types.Set {
	if len(values) == 0 {
		if !prior.IsNull() && !prior.IsUnknown() {
			return types.SetValueMust(types.StringType, nil)
		}

		return types.SetNull(types.StringType)
	}

	return stringSetValue(values)
}

// stringSetValue builds a known set of strings.
func stringSetValue(values []string) types.Set {
	elements := make([]attr.Value, len(values))
	for i, value := range values {
		elements[i] = types.StringValue(value)
	}

	return types.SetValueMust(types.StringType, elements)
}

// stringsFromList returns the elements of a list of strings in order.
func stringsFromList(ctx context.Context, list types.List, diags *diag.Diagnostics) []string {
	if list.IsNull() || list.IsUnknown() {
		return nil
	}

	var values []string
	diags.Append(list.ElementsAs(ctx, &values, false)...)

	return values
}

// listFromStrings is setFromStrings for an ordered list.
func listFromStrings(ctx context.Context, values []string, prior types.List, diags *diag.Diagnostics) types.List {
	if len(values) == 0 {
		if !prior.IsNull() && !prior.IsUnknown() {
			return types.ListValueMust(types.StringType, nil)
		}

		return types.ListNull(types.StringType)
	}

	list, d := types.ListValueFrom(ctx, types.StringType, values)
	diags.Append(d...)

	return list
}

// stringOrNull maps the API's empty string to null.
func stringOrNull(value string) types.String {
	if value == "" {
		return types.StringNull()
	}

	return types.StringValue(value)
}

// keepEmptyMap returns a non-nil empty map when the result is empty and the
// prior value was a known empty map, and the result unchanged otherwise. A nil
// map is stored as null and an empty one as {}.
func keepEmptyMap[V any](result, prior map[string]V) map[string]V {
	if len(result) == 0 {
		if prior != nil {
			return map[string]V{}
		}

		return nil
	}

	return result
}

// sortedKeys returns the keys of a map in order, for deterministic requests.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	return keys
}

// oneOfDescription lists enum values for a description.
func oneOfDescription(values ...string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = "`" + value + "`"
	}

	return strings.Join(quoted, ", ")
}
