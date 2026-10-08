// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// keepStateOfExistingParent is UseStateForUnknown for an optional, computed
// string nested inside a map element or an optional object.
//
// The framework's two modifiers each get one case wrong there. Take an
// attribute the server may leave empty, read back as null:
//
//   - UseStateForUnknown copies the state whenever the resource exists. For an
//     element being added to the map the state of the attribute is null, so it
//     plans null for a value the server is about to assign, and the apply fails
//     with an inconsistent result.
//   - UseNonNullStateForUnknown never copies a null, so an attribute that is
//     legitimately null stays "known after apply" and shows up as a change in
//     every plan.
//
// This modifier tells the two situations apart by looking at the parent: when
// the object holding the attribute exists in state its value is kept, null
// included, and when it does not the value stays unknown.
type keepStateOfExistingParent struct{}

func (keepStateOfExistingParent) Description(_ context.Context) string {
	return "Keeps the value in state, null included, once the enclosing object exists."
}

func (m keepStateOfExistingParent) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (keepStateOfExistingParent) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	// Nothing to keep while the resource is being created, when the value is
	// already decided, or when the configuration is yet to be known.
	if req.State.Raw.IsNull() || !req.PlanValue.IsUnknown() || req.ConfigValue.IsUnknown() {
		return
	}

	var parent types.Object
	if diags := req.State.GetAttribute(ctx, req.Path.ParentPath(), &parent); diags.HasError() {
		// The path does not resolve in state: the parent is new.
		return
	}
	if parent.IsNull() || parent.IsUnknown() {
		return
	}

	resp.PlanValue = req.StateValue
}

// keepStateIfEqualFold keeps the value in state when the configured one
// differs from it only in case. Email addresses are matched case-insensitively
// by Play Console, and without this a user imported as "Ada@Example.com" and
// configured as "ada@example.com" would be planned for replacement, which
// means removing them from the developer account. List it before
// RequiresReplace.
type keepStateIfEqualFold struct{}

func (keepStateIfEqualFold) Description(_ context.Context) string {
	return "Keeps the value in state when the configured value differs only in case."
}

func (m keepStateIfEqualFold) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (keepStateIfEqualFold) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.StateValue.IsNull() || req.StateValue.IsUnknown() || req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	if strings.EqualFold(req.StateValue.ValueString(), req.ConfigValue.ValueString()) {
		resp.PlanValue = req.StateValue
	}
}
