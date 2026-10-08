// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/api/androidpublisher/v3"

	"github.com/MathGaps/terraform-provider-googleplay/internal/play"
)

// defaultRegionsVersion is the regions version sent with price-touching calls
// when the configuration names none. Google publishes the current value at
// https://support.google.com/googleplay/android-developer/answer/10532353; a
// request made with an older version still succeeds, which is the purpose of
// the parameter. It is the default of the regions_version attribute, shown in
// the documentation and overridable, rather than a hidden constant.
const defaultRegionsVersion = "2022/02"

const (
	stateDraft             = "DRAFT"
	stateActive            = "ACTIVE"
	stateInactive          = "INACTIVE"
	stateInactivePublished = "INACTIVE_PUBLISHED"

	latencySensitive = "PRODUCT_UPDATE_LATENCY_TOLERANCE_LATENCY_SENSITIVE"
	latencyTolerant  = "PRODUCT_UPDATE_LATENCY_TOLERANCE_LATENCY_TOLERANT"
)

var (
	regionCodePattern   = regexp.MustCompile(`^[A-Z]{2}$`)
	currencyCodePattern = regexp.MustCompile(`^[A-Z]{3}$`)
	isoDurationPattern  = regexp.MustCompile(`^P(\d+[YMWD])+$`)

	streamingTaxTypes = []string{
		"STREAMING_TAX_TYPE_TELCO_VIDEO_RENTAL",
		"STREAMING_TAX_TYPE_TELCO_VIDEO_SALES",
		"STREAMING_TAX_TYPE_TELCO_VIDEO_MULTI_CHANNEL",
		"STREAMING_TAX_TYPE_TELCO_AUDIO_RENTAL",
		"STREAMING_TAX_TYPE_TELCO_AUDIO_SALES",
		"STREAMING_TAX_TYPE_TELCO_AUDIO_MULTI_CHANNEL",
	}
	taxTiers = []string{
		"TAX_TIER_BOOKS_1",
		"TAX_TIER_NEWS_1",
		"TAX_TIER_NEWS_2",
		"TAX_TIER_MUSIC_OR_AUDIO_1",
		"TAX_TIER_LIVE_OR_BROADCAST_1",
	}
	productAgeRatingTiers = []string{
		"PRODUCT_AGE_RATING_TIER_EVERYONE",
		"PRODUCT_AGE_RATING_TIER_THIRTEEN_AND_ABOVE",
		"PRODUCT_AGE_RATING_TIER_SIXTEEN_AND_ABOVE",
		"PRODUCT_AGE_RATING_TIER_EIGHTEEN_AND_ABOVE",
	}
	withdrawalRightTypes = []string{
		"WITHDRAWAL_RIGHT_DIGITAL_CONTENT",
		"WITHDRAWAL_RIGHT_SERVICE",
	}
)

// --- shared schema -----------------------------------------------------------

func productIDAttribute(kind string) schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: "The product id of the " + kind + ", unique within the app: lower-case letters, " +
			"digits, underscores and dots, starting with a letter or digit, at most 40 characters. " +
			"**A product id is reserved forever**, even after the " + kind + " is deleted, so it cannot be reused. " +
			"Changing it creates a new " + kind + ".",
		Required:      true,
		PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
		Validators: []validator.String{
			stringvalidator.RegexMatches(productIDPattern, "must be 1 to 40 lower-case letters, digits, underscores and dots, starting with a letter or digit"),
		},
	}
}

func regionsVersionAttribute(computedNote string) schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: "The version of Google Play's list of available regions that the regional prices " +
			"in this configuration are written against. The API requires it on every call that creates or changes " +
			"the product, and accepts an older version than the current one. Defaults to `" + defaultRegionsVersion +
			"`. Google publishes the current version in " +
			"[this article](https://support.google.com/googleplay/android-developer/answer/10532353), and the " +
			"`googleplay_converted_region_prices` data source reports it as `region_version`." + computedNote,
		Optional: true,
		Computed: true,
		Default:  stringdefault.StaticString(defaultRegionsVersion),
		Validators: []validator.String{
			stringvalidator.RegexMatches(regexp.MustCompile(`^\d{4}/\d{2}$`), "must look like 2022/02"),
		},
	}
}

func latencyToleranceAttribute() schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: "How quickly a change must reach clients: `" + latencySensitive + "` (within minutes; " +
			"at most 7,200 updates per app per hour) or `" + latencyTolerant + "` (within 24 hours; a far higher " +
			"quota). It is sent with every call that accepts it. Left out, the API uses latency-sensitive. " +
			"It is a setting of the request, not of the product: changing it alone calls nothing.",
		Optional: true,
		Validators: []validator.String{
			stringvalidator.OneOf(latencySensitive, latencyTolerant),
		},
	}
}

func productStateAttribute(kind, activation string) schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: "The state of the " + kind + ": `DRAFT`, `ACTIVE` or `INACTIVE`. A new " + kind +
			" is a draft until activated. Set `ACTIVE` to make it available and `INACTIVE` to retire it; the " +
			"provider reaches the state with " + activation + ". A " + kind + " that has left `DRAFT` cannot return " +
			"to it. Left out, the state is whatever the API reports and is never changed.",
		Optional:      true,
		Computed:      true,
		PlanModifiers: []planmodifier.String{keepStateOfExistingParent{}},
		Validators: []validator.String{
			stringvalidator.OneOf(stateDraft, stateActive, stateInactive),
		},
	}
}

func regionKeysValidator() validator.Map {
	return mapvalidator.KeysAre(stringvalidator.RegexMatches(regionCodePattern, "must be a two-letter region code such as US"))
}

func offerTagsAttribute(owner string) schema.SetAttribute {
	return schema.SetAttribute{
		MarkdownDescription: "Up to 20 custom tags for this " + owner + ", returned to the app by the billing " +
			"library. Each is lower-case letters, digits and hyphens, at most 20 characters.",
		ElementType: types.StringType,
		Optional:    true,
		Validators: []validator.Set{
			setvalidator.SizeAtMost(20),
			setvalidator.ValueStringsAre(
				stringvalidator.RegexMatches(rfc1034Pattern, "must be lower-case letters, digits and hyphens"),
				stringvalidator.LengthAtMost(20),
			),
		},
	}
}

func restrictedPaymentCountriesAttribute() schema.SetAttribute {
	return schema.SetAttribute{
		MarkdownDescription: "Region codes of the countries where the purchase is restricted to payment methods " +
			"registered in the same country. Left out, there is no restriction.",
		ElementType: types.StringType,
		Optional:    true,
		Validators: []validator.Set{
			setvalidator.ValueStringsAre(stringvalidator.RegexMatches(regionCodePattern, "must be a two-letter region code such as US")),
		},
	}
}

// moneyAttribute is a price: a currency and an exact decimal amount.
func moneyAttribute(description string, required bool) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: description,
		Required:            required,
		Optional:            !required,
		Attributes: map[string]schema.Attribute{
			"currency_code": schema.StringAttribute{
				MarkdownDescription: "The three-letter ISO 4217 currency code, for example `USD`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(currencyCodePattern, "must be a three-letter ISO 4217 currency code such as USD"),
				},
			},
			"amount": schema.StringAttribute{
				MarkdownDescription: "The amount as a decimal string, for example `\"4.99\"`. It is converted to the " +
					"API's units and nanos exactly, never through a floating-point number, and `\"4.50\"` and " +
					"`\"4.5\"` are the same amount. At most 9 fractional digits.",
				CustomType:    DecimalType{},
				Required:      true,
				PlanModifiers: []planmodifier.String{keepEquivalentDecimal{}},
			},
		},
	}
}

// regionalTaxAttributes are the per-region tax settings both product kinds
// share.
func regionalTaxAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"eligible_for_streaming_service_tax_rate": schema.BoolAttribute{
			MarkdownDescription: "Whether the product is a streaming product, for US state and local sales tax. " +
				"Only supported in the United States. Defaults to `false`.",
			Optional: true,
			Computed: true,
			Default:  booldefault.StaticBool(false),
		},
		"streaming_tax_type": schema.StringAttribute{
			MarkdownDescription: "The tax category for communications or amusement taxes in the United States: " +
				oneOfDescription(streamingTaxTypes...) + ".",
			Optional:   true,
			Validators: []validator.String{stringvalidator.OneOf(streamingTaxTypes...)},
		},
		"tax_tier": schema.StringAttribute{
			MarkdownDescription: "The tax tier for a reduced tax rate: " + oneOfDescription(taxTiers...) + ".",
			Optional:            true,
			Validators:          []validator.String{stringvalidator.OneOf(taxTiers...)},
		},
	}
}

func productAgeRatingAttribute() schema.MapAttribute {
	return schema.MapAttribute{
		MarkdownDescription: "The age rating tier of the product by region code. The API currently supports only " +
			"the region `US`. Each value is one of: " + oneOfDescription(productAgeRatingTiers...) + ".",
		ElementType: types.StringType,
		Optional:    true,
		Validators: []validator.Map{
			regionKeysValidator(),
			mapvalidator.ValueStringsAre(stringvalidator.OneOf(productAgeRatingTiers...)),
		},
	}
}

// --- shared models -----------------------------------------------------------

type moneyModel struct {
	CurrencyCode types.String `tfsdk:"currency_code"`
	Amount       DecimalValue `tfsdk:"amount"`
}

// expandMoney converts a price. A nil model gives nil.
func expandMoney(m *moneyModel, at path.Path, diags *diag.Diagnostics) *androidpublisher.Money {
	if m == nil {
		return nil
	}

	money, err := play.NewMoney(m.CurrencyCode.ValueString(), m.Amount.ValueString())
	if err != nil {
		diags.AddAttributeError(at.AtName("amount"), "Invalid decimal amount", err.Error())

		return nil
	}

	return money
}

func flattenMoney(m *androidpublisher.Money) *moneyModel {
	if m == nil {
		return nil
	}

	return &moneyModel{
		CurrencyCode: types.StringValue(m.CurrencyCode),
		Amount:       NewDecimalValue(play.MoneyAmount(m)),
	}
}

type regionalTaxModel struct {
	EligibleForStreamingServiceTaxRate types.Bool   `tfsdk:"eligible_for_streaming_service_tax_rate"`
	StreamingTaxType                   types.String `tfsdk:"streaming_tax_type"`
	TaxTier                            types.String `tfsdk:"tax_tier"`
}

// enumOrNull maps an empty or *_UNSPECIFIED enum value to null.
func enumOrNull(value string) types.String {
	if value == "" || strings.HasSuffix(value, "_UNSPECIFIED") || strings.HasSuffix(value, "_UNKNOWN") {
		return types.StringNull()
	}

	return types.StringValue(value)
}

func expandOfferTags(ctx context.Context, set types.Set, diags *diag.Diagnostics) []*androidpublisher.OfferTag {
	var tags []*androidpublisher.OfferTag
	for _, tag := range stringsFromSet(ctx, set, diags) {
		tags = append(tags, &androidpublisher.OfferTag{Tag: tag})
	}

	return tags
}

func flattenOfferTags(tags []*androidpublisher.OfferTag, prior types.Set) types.Set {
	values := make([]string, 0, len(tags))
	for _, tag := range tags {
		values = append(values, tag.Tag)
	}

	return setFromStrings(values, prior)
}

func expandRestrictedPaymentCountries(ctx context.Context, set types.Set, diags *diag.Diagnostics) *androidpublisher.RestrictedPaymentCountries {
	codes := stringsFromSet(ctx, set, diags)
	if len(codes) == 0 {
		return nil
	}

	return &androidpublisher.RestrictedPaymentCountries{RegionCodes: codes}
}

func flattenRestrictedPaymentCountries(countries *androidpublisher.RestrictedPaymentCountries, prior types.Set) types.Set {
	if countries == nil {
		return setFromStrings(nil, prior)
	}

	return setFromStrings(countries.RegionCodes, prior)
}

func expandAgeRatings(ctx context.Context, tiers types.Map, diags *diag.Diagnostics) []*androidpublisher.RegionalProductAgeRatingInfo {
	if tiers.IsNull() || tiers.IsUnknown() {
		return nil
	}

	values := map[string]string{}
	diags.Append(tiers.ElementsAs(ctx, &values, false)...)

	var infos []*androidpublisher.RegionalProductAgeRatingInfo
	for _, region := range sortedKeys(values) {
		infos = append(infos, &androidpublisher.RegionalProductAgeRatingInfo{
			RegionCode:           region,
			ProductAgeRatingTier: values[region],
		})
	}

	return infos
}

func flattenAgeRatings(infos []*androidpublisher.RegionalProductAgeRatingInfo, prior types.Map) types.Map {
	if len(infos) == 0 {
		if !prior.IsNull() && !prior.IsUnknown() {
			return types.MapValueMust(types.StringType, nil)
		}

		return types.MapNull(types.StringType)
	}

	elements := map[string]attr.Value{}
	for _, info := range infos {
		elements[info.RegionCode] = types.StringValue(info.ProductAgeRatingTier)
	}

	return types.MapValueMust(types.StringType, elements)
}

// --- state transitions -------------------------------------------------------

// stateTransition says which calls take a base plan or purchase option from
// its current state to the desired one. An empty desired state asks for
// nothing.
func stateTransition(current, desired string) (activate, deactivate bool, err error) {
	if current == stateInactivePublished {
		current = stateInactive
	}

	switch {
	case desired == "" || desired == current:
		return false, false, nil
	case desired == stateActive:
		return true, false, nil
	case desired == stateInactive && current == stateActive:
		return false, true, nil
	case desired == stateInactive:
		// Inactive means "was active": a draft has to be activated first.
		return true, true, nil
	case desired == stateDraft:
		return false, false, fmt.Errorf("it is %s and cannot return to DRAFT", current)
	default:
		return false, false, fmt.Errorf("unsupported state %q", desired)
	}
}

// flattenState reports the API's state, except that INACTIVE_PUBLISHED, which
// only a product Google migrated can be in, satisfies a configured INACTIVE.
func flattenState(apiState string, prior types.String) types.String {
	if apiState == stateInactivePublished && prior.ValueString() == stateInactive {
		return prior
	}

	return stringOrNull(apiState)
}
