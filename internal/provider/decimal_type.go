// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/attr/xattr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/MathGaps/terraform-provider-googleplay/internal/play"
)

var (
	_ basetypes.StringTypable                    = DecimalType{}
	_ basetypes.StringValuableWithSemanticEquals = DecimalValue{}
	_ xattr.ValidateableAttribute                = DecimalValue{}
)

// DecimalType is a string holding an exact decimal amount such as "4.99".
// Two values are semantically equal when they denote the same number, so a
// configuration may write "4.50" where the API reports "4.5".
type DecimalType struct {
	basetypes.StringType
}

// String implements attr.Type.
func (t DecimalType) String() string {
	return "provider.DecimalType"
}

// ValueType implements attr.Type.
func (t DecimalType) ValueType(_ context.Context) attr.Value {
	return DecimalValue{}
}

// Equal implements attr.Type.
func (t DecimalType) Equal(o attr.Type) bool {
	other, ok := o.(DecimalType)
	if !ok {
		return false
	}

	return t.StringType.Equal(other.StringType)
}

// ValueFromString implements basetypes.StringTypable.
func (t DecimalType) ValueFromString(_ context.Context, in basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return DecimalValue{StringValue: in}, nil
}

// ValueFromTerraform implements attr.Type.
func (t DecimalType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	attrValue, err := t.StringType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}

	stringValue, ok := attrValue.(basetypes.StringValue)
	if !ok {
		return nil, fmt.Errorf("unexpected value type of %T", attrValue)
	}

	return DecimalValue{StringValue: stringValue}, nil
}

// DecimalValue is a value of DecimalType.
type DecimalValue struct {
	basetypes.StringValue
}

// NewDecimalValue returns a known DecimalValue.
func NewDecimalValue(value string) DecimalValue {
	return DecimalValue{StringValue: basetypes.NewStringValue(value)}
}

// NewDecimalNull returns a null DecimalValue.
func NewDecimalNull() DecimalValue {
	return DecimalValue{StringValue: basetypes.NewStringNull()}
}

// Type implements attr.Value.
func (v DecimalValue) Type(_ context.Context) attr.Type {
	return DecimalType{}
}

// Equal implements attr.Value.
func (v DecimalValue) Equal(o attr.Value) bool {
	other, ok := o.(DecimalValue)
	if !ok {
		return false
	}

	return v.StringValue.Equal(other.StringValue)
}

// StringSemanticEquals reports whether both values denote the same number.
func (v DecimalValue) StringSemanticEquals(_ context.Context, newValuable basetypes.StringValuable) (bool, diag.Diagnostics) {
	newValue, ok := newValuable.(DecimalValue)
	if !ok {
		return false, diag.Diagnostics{diag.NewErrorDiagnostic("Semantic equality check error",
			fmt.Sprintf("Expected a DecimalValue, got %T. This is a bug in the provider.", newValuable))}
	}

	return play.DecimalsEqual(v.ValueString(), newValue.ValueString()), nil
}

// ValidateAttribute rejects a string that is not an exact decimal number.
func (v DecimalValue) ValidateAttribute(_ context.Context, req xattr.ValidateAttributeRequest, resp *xattr.ValidateAttributeResponse) {
	if v.IsNull() || v.IsUnknown() {
		return
	}

	if _, _, err := play.ParseDecimal(v.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid decimal amount",
			"The amount must be a decimal number written as a string, such as \"4.99\", with at most 9 fractional digits: "+err.Error())
	}
}

// keepEquivalentDecimal keeps the amount already in state when the configured
// one denotes the same number. Semantic equality covers apply and refresh, but
// not the first plan after an import, where state holds the API's rendering
// ("4.5") and the configuration may say "4.50".
type keepEquivalentDecimal struct{}

func (keepEquivalentDecimal) Description(_ context.Context) string {
	return "Keeps the amount in state when the configured amount is the same number written differently."
}

func (m keepEquivalentDecimal) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (keepEquivalentDecimal) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.StateValue.IsNull() || req.StateValue.IsUnknown() || req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	if play.DecimalsEqual(req.StateValue.ValueString(), req.ConfigValue.ValueString()) {
		resp.PlanValue = req.StateValue
	}
}
