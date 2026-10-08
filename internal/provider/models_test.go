// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/api/androidpublisher/v3"
)

func TestStateTransition(t *testing.T) {
	tests := []struct {
		current, desired     string
		activate, deactivate bool
		wantErr              bool
	}{
		{"DRAFT", "", false, false, false},
		{"DRAFT", "DRAFT", false, false, false},
		{"DRAFT", "ACTIVE", true, false, false},
		// Inactive means "was active": a draft is activated, then deactivated.
		{"DRAFT", "INACTIVE", true, true, false},
		{"ACTIVE", "ACTIVE", false, false, false},
		{"ACTIVE", "INACTIVE", false, true, false},
		{"ACTIVE", "DRAFT", false, false, true},
		{"INACTIVE", "ACTIVE", true, false, false},
		{"INACTIVE", "INACTIVE", false, false, false},
		{"INACTIVE", "DRAFT", false, false, true},
		{"INACTIVE_PUBLISHED", "INACTIVE", false, false, false},
		{"INACTIVE_PUBLISHED", "ACTIVE", true, false, false},
		{"ACTIVE", "PAUSED", false, false, true},
	}

	for _, tt := range tests {
		activate, deactivate, err := stateTransition(tt.current, tt.desired)
		if (err != nil) != tt.wantErr || activate != tt.activate || deactivate != tt.deactivate {
			t.Errorf("stateTransition(%q, %q) = %v, %v, %v; want %v, %v, error %v",
				tt.current, tt.desired, activate, deactivate, err, tt.activate, tt.deactivate, tt.wantErr)
		}
	}
}

func TestFlattenState(t *testing.T) {
	if got := flattenState("INACTIVE_PUBLISHED", types.StringValue("INACTIVE")); got.ValueString() != "INACTIVE" {
		t.Errorf("a configured INACTIVE was replaced by %q", got.ValueString())
	}
	if got := flattenState("INACTIVE_PUBLISHED", types.StringNull()); got.ValueString() != "INACTIVE_PUBLISHED" {
		t.Errorf("an unconfigured state was reported as %q", got.ValueString())
	}
	if got := flattenState("", types.StringNull()); !got.IsNull() {
		t.Errorf("an absent state was reported as %q", got.ValueString())
	}
}

func TestSplitImportID(t *testing.T) {
	parts, err := splitImportID("com.example.app/wear:qa", "package_name", "track")
	if err != nil || !slices.Equal(parts, []string{"com.example.app", "wear:qa"}) {
		t.Errorf("splitImportID = %v, %v", parts, err)
	}

	for _, bad := range []string{"", "com.example.app", "com.example.app/", "/qa", "a/b/c"} {
		if _, err := splitImportID(bad, "package_name", "track"); err == nil {
			t.Errorf("splitImportID(%q) succeeded", bad)
		}
	}
}

// An absent collection is null, unless the configuration wrote it as empty.
func TestEmptyCollectionsKeepTheirPriorShape(t *testing.T) {
	emptySet := types.SetValueMust(types.StringType, nil)

	if got := setFromStrings(nil, types.SetNull(types.StringType)); !got.IsNull() {
		t.Error("an absent set with a null prior must be null")
	}
	if got := setFromStrings(nil, emptySet); got.IsNull() || len(got.Elements()) != 0 {
		t.Error("an absent set with an empty prior must stay empty")
	}
	if got := setFromStrings([]string{"b", "a"}, types.SetNull(types.StringType)); len(got.Elements()) != 2 {
		t.Error("a set with values must hold them")
	}

	if got := keepEmptyMap(map[string]int{}, nil); got != nil {
		t.Error("an absent map with a null prior must be nil")
	}
	if got := keepEmptyMap(map[string]int{}, map[string]int{}); got == nil {
		t.Error("an absent map with an empty prior must stay empty")
	}
}

func TestEnumOrNull(t *testing.T) {
	for _, unset := range []string{"", "TAX_TIER_UNSPECIFIED", "PRODUCT_AGE_RATING_TIER_UNKNOWN"} {
		if !enumOrNull(unset).IsNull() {
			t.Errorf("enumOrNull(%q) is not null", unset)
		}
	}
	if enumOrNull("TAX_TIER_BOOKS_1").ValueString() != "TAX_TIER_BOOKS_1" {
		t.Error("a set enum value was dropped")
	}
}

func money(currency, amount string) *moneyModel {
	return &moneyModel{CurrencyCode: types.StringValue(currency), Amount: NewDecimalValue(amount)}
}

func testSubscriptionModel() subscriptionModel {
	return subscriptionModel{
		PackageName:    types.StringValue("com.example.app"),
		ProductID:      types.StringValue("premium"),
		RegionsVersion: types.StringValue("2022/02"),
		Listings: map[string]subscriptionListingModel{
			"fr-FR": {Title: types.StringValue("Premium")},
			"en-US": {
				Title:       types.StringValue("Premium"),
				Benefits:    types.ListValueMust(types.StringType, []attr.Value{types.StringValue("No ads"), types.StringValue("Offline")}),
				Description: types.StringValue("Everything."),
			},
		},
		BasePlans: map[string]basePlanModel{
			"yearly": {
				State:   types.StringUnknown(),
				Prepaid: &prepaidModel{BillingPeriodDuration: types.StringValue("P1Y"), TimeExtension: types.StringUnknown()},
			},
			"monthly": {
				State: types.StringValue("ACTIVE"),
				AutoRenewing: &autoRenewingModel{
					BillingPeriodDuration: types.StringValue("P1M"),
					GracePeriodDuration:   types.StringUnknown(),
					LegacyCompatible:      types.BoolValue(false),
				},
				OfferTags: types.SetValueMust(types.StringType, []attr.Value{types.StringValue("b"), types.StringValue("a")}),
				RegionalConfigs: map[string]basePlanRegionalConfigModel{
					"US": {Price: money("USD", "4.50"), NewSubscriberAvailability: types.BoolValue(true)},
					"GB": {Price: money("GBP", "0.99"), NewSubscriberAvailability: types.BoolValue(false)},
				},
				OtherRegionsConfig: &otherRegionsConfigModel{
					UsdPrice:                  money("USD", "4.50"),
					EurPrice:                  money("EUR", "4.29"),
					NewSubscriberAvailability: types.BoolValue(true),
				},
			},
		},
		TaxAndComplianceSettings:   types.ObjectUnknown(subscriptionTaxAttrTypes()),
		RestrictedPaymentCountries: types.SetNull(types.StringType),
	}
}

func TestExpandSubscription(t *testing.T) {
	var diags diag.Diagnostics
	sub := expandSubscription(context.Background(), testSubscriptionModel(), &diags)
	if diags.HasError() {
		t.Fatalf("diagnostics: %v", diags)
	}

	// Lists are built in key order whatever order the maps iterate in.
	if sub.Listings[0].LanguageCode != "en-US" || sub.Listings[1].LanguageCode != "fr-FR" {
		t.Errorf("listings are not sorted by language: %q, %q", sub.Listings[0].LanguageCode, sub.Listings[1].LanguageCode)
	}
	if sub.BasePlans[0].BasePlanId != "monthly" || sub.BasePlans[1].BasePlanId != "yearly" {
		t.Errorf("base plans are not sorted by id")
	}

	monthly := sub.BasePlans[0]
	if monthly.State != "" {
		t.Errorf("state is output only and must not be sent, got %q", monthly.State)
	}
	if monthly.RegionalConfigs[0].RegionCode != "GB" || monthly.RegionalConfigs[1].RegionCode != "US" {
		t.Errorf("regional configs are not sorted by region")
	}
	if price := monthly.RegionalConfigs[1].Price; price.CurrencyCode != "USD" || price.Units != 4 || price.Nanos != 500_000_000 {
		t.Errorf("US price = %+v, want 4 units and 500000000 nanos", price)
	}
	if price := monthly.RegionalConfigs[0].Price; price.Units != 0 || price.Nanos != 990_000_000 {
		t.Errorf("GB price = %+v, want 0 units and 990000000 nanos", price)
	}
	if monthly.OfferTags[0].Tag != "a" || monthly.OfferTags[1].Tag != "b" {
		t.Errorf("offer tags are not sorted")
	}

	// Unknown values (server defaults not yet assigned) are left out.
	if monthly.AutoRenewingBasePlanType.GracePeriodDuration != "" {
		t.Errorf("an unknown grace period was sent as %q", monthly.AutoRenewingBasePlanType.GracePeriodDuration)
	}
	if sub.TaxAndComplianceSettings != nil || sub.RestrictedPaymentCountries != nil {
		t.Errorf("unknown and null settings must be left out: %+v, %+v", sub.TaxAndComplianceSettings, sub.RestrictedPaymentCountries)
	}
	if sub.BasePlans[1].AutoRenewingBasePlanType != nil || sub.BasePlans[1].PrepaidBasePlanType.BillingPeriodDuration != "P1Y" {
		t.Errorf("the prepaid base plan was expanded as %+v", sub.BasePlans[1])
	}
}

func TestExpandSubscriptionReportsABadAmount(t *testing.T) {
	model := testSubscriptionModel()
	plan := model.BasePlans["monthly"]
	plan.RegionalConfigs["US"] = basePlanRegionalConfigModel{Price: money("USD", "4,50")}

	var diags diag.Diagnostics
	expandSubscription(context.Background(), model, &diags)

	if !diags.HasError() {
		t.Fatal("a malformed amount was accepted")
	}
}

// Flattening what was expanded and expanding it again gives the same request,
// and the same amount written differently is not a change.
func TestSubscriptionRoundTripAndUpdateMask(t *testing.T) {
	ctx := context.Background()
	var diags diag.Diagnostics

	model := testSubscriptionModel()
	sent := expandSubscription(ctx, model, &diags)

	// What the server does with it: assigns states and defaults.
	stored := &androidpublisher.Subscription{}
	*stored = *sent
	stored.BasePlans = nil
	for _, plan := range sent.BasePlans {
		copied := *plan
		copied.State = "DRAFT"
		if copied.AutoRenewingBasePlanType != nil {
			renewing := *copied.AutoRenewingBasePlanType
			renewing.GracePeriodDuration = "P3D"
			copied.AutoRenewingBasePlanType = &renewing
		}
		stored.BasePlans = append(stored.BasePlans, &copied)
	}

	state := flattenSubscription(ctx, stored, model, &diags)
	if diags.HasError() {
		t.Fatalf("diagnostics: %v", diags)
	}

	if got := state.BasePlans["monthly"].State.ValueString(); got != "DRAFT" {
		t.Errorf("state = %q, want the API's DRAFT", got)
	}
	if got := state.BasePlans["monthly"].AutoRenewing.GracePeriodDuration.ValueString(); got != "P3D" {
		t.Errorf("grace period = %q, want the server default P3D", got)
	}
	if got := state.BasePlans["monthly"].RegionalConfigs["US"].Price.Amount.ValueString(); got != "4.5" {
		t.Errorf("amount = %q, want the canonical 4.5", got)
	}
	if !state.RestrictedPaymentCountries.IsNull() || !state.TaxAndComplianceSettings.IsNull() {
		t.Error("absent settings must flatten to null")
	}
	if !state.Listings["fr-FR"].Benefits.IsNull() || !state.Listings["fr-FR"].Description.IsNull() {
		t.Error("an absent benefits list and description must flatten to null")
	}

	// The plan for the next apply: same configuration, server values known.
	current := expandSubscription(ctx, state, &diags)

	plan := testSubscriptionModel()
	monthly := plan.BasePlans["monthly"]
	monthly.AutoRenewing.GracePeriodDuration = types.StringValue("P3D")
	plan.TaxAndComplianceSettings = state.TaxAndComplianceSettings
	if mask := subscriptionUpdateMask(current, expandSubscription(ctx, plan, &diags)); len(mask) != 0 {
		t.Errorf("an unchanged configuration (4.50 against 4.5) gave the mask %v", mask)
	}

	// One change per field.
	plan.Listings["en-US"] = subscriptionListingModel{Title: types.StringValue("Premium Plus")}
	if mask := subscriptionUpdateMask(current, expandSubscription(ctx, plan, &diags)); !reflect.DeepEqual(mask, []string{"listings"}) {
		t.Errorf("a title change gave the mask %v", mask)
	}

	monthly.RegionalConfigs["US"] = basePlanRegionalConfigModel{Price: money("USD", "4.51"), NewSubscriberAvailability: types.BoolValue(true)}
	plan.RestrictedPaymentCountries = types.SetValueMust(types.StringType, []attr.Value{types.StringValue("IN")})
	want := []string{"listings", "basePlans", "restrictedPaymentCountries"}
	if mask := subscriptionUpdateMask(current, expandSubscription(ctx, plan, &diags)); !reflect.DeepEqual(mask, want) {
		t.Errorf("mask = %v, want %v", mask, want)
	}

	// A state change is not a field of the subscription.
	onlyState := testSubscriptionModel()
	onlyState.BasePlans["monthly"].AutoRenewing.GracePeriodDuration = types.StringValue("P3D")
	changed := onlyState.BasePlans["monthly"]
	changed.State = types.StringValue("INACTIVE")
	onlyState.BasePlans["monthly"] = changed
	if mask := subscriptionUpdateMask(current, expandSubscription(ctx, onlyState, &diags)); len(mask) != 0 {
		t.Errorf("a state change gave the mask %v", mask)
	}

	if diags.HasError() {
		t.Fatalf("diagnostics: %v", diags)
	}
}

func TestFlattenSubscriptionTax(t *testing.T) {
	ctx := context.Background()
	var diags diag.Diagnostics

	object := flattenSubscriptionTax(ctx, &androidpublisher.SubscriptionTaxAndComplianceSettings{
		EeaWithdrawalRightType: "WITHDRAWAL_RIGHT_SERVICE",
		TaxRateInfoByRegionCode: map[string]androidpublisher.RegionalTaxRateInfo{
			"US": {EligibleForStreamingServiceTaxRate: true, StreamingTaxType: "STREAMING_TAX_TYPE_TELCO_VIDEO_RENTAL"},
		},
		RegionalProductAgeRatingInfos: []*androidpublisher.RegionalProductAgeRatingInfo{
			{RegionCode: "US", ProductAgeRatingTier: "PRODUCT_AGE_RATING_TIER_EVERYONE"},
		},
	}, types.ObjectNull(subscriptionTaxAttrTypes()), &diags)
	if diags.HasError() {
		t.Fatalf("diagnostics: %v", diags)
	}

	back := expandSubscriptionTax(ctx, object, &diags)
	if diags.HasError() {
		t.Fatalf("diagnostics: %v", diags)
	}

	if back.EeaWithdrawalRightType != "WITHDRAWAL_RIGHT_SERVICE" ||
		!back.TaxRateInfoByRegionCode["US"].EligibleForStreamingServiceTaxRate ||
		back.TaxRateInfoByRegionCode["US"].StreamingTaxType != "STREAMING_TAX_TYPE_TELCO_VIDEO_RENTAL" ||
		len(back.RegionalProductAgeRatingInfos) != 1 ||
		back.RegionalProductAgeRatingInfos[0].ProductAgeRatingTier != "PRODUCT_AGE_RATING_TIER_EVERYONE" {
		t.Errorf("tax settings did not survive the round trip: %+v", back)
	}

	if !flattenSubscriptionTax(ctx, nil, types.ObjectNull(subscriptionTaxAttrTypes()), &diags).IsNull() {
		t.Error("absent tax settings must flatten to null")
	}
}

func TestOneTimeProductRoundTripAndUpdateMask(t *testing.T) {
	ctx := context.Background()
	var diags diag.Diagnostics

	model := oneTimeProductModel{
		PackageName: types.StringValue("com.example.app"),
		ProductID:   types.StringValue("remove_ads"),
		Listings: map[string]oneTimeProductListingModel{
			"en-US": {Title: types.StringValue("Remove ads"), Description: types.StringValue("No more ads.")},
		},
		PurchaseOptions: map[string]purchaseOptionModel{
			"rental": {
				Rent: &rentOptionModel{RentalPeriod: types.StringValue("P30D")},
				RegionalConfigs: map[string]purchaseOptionRegionalConfigModel{
					"US": {Price: money("USD", "1.99"), Availability: types.StringValue("AVAILABLE")},
				},
				WithdrawalRightType: types.StringUnknown(),
			},
			"buy": {
				State: types.StringValue("ACTIVE"),
				Buy:   &buyOptionModel{LegacyCompatible: types.BoolValue(true), MultiQuantityEnabled: types.BoolValue(false)},
				RegionalConfigs: map[string]purchaseOptionRegionalConfigModel{
					"US": {Price: money("USD", "9.90"), Availability: types.StringValue("AVAILABLE")},
				},
				NewRegionsConfig: &newRegionsConfigModel{
					UsdPrice: money("USD", "9.90"), EurPrice: money("EUR", "9.49"), Availability: types.StringValue("AVAILABLE"),
				},
				WithdrawalRightType: types.StringUnknown(),
			},
		},
		OfferTags:                  types.SetValueMust(types.StringType, []attr.Value{types.StringValue("evergreen")}),
		TaxAndComplianceSettings:   types.ObjectNull(oneTimeProductTaxAttrTypes()),
		RestrictedPaymentCountries: types.SetNull(types.StringType),
	}

	sent := expandOneTimeProduct(ctx, model, &diags)
	if diags.HasError() {
		t.Fatalf("diagnostics: %v", diags)
	}

	if sent.PurchaseOptions[0].PurchaseOptionId != "buy" || sent.PurchaseOptions[1].PurchaseOptionId != "rental" {
		t.Errorf("purchase options are not sorted by id")
	}
	buy := sent.PurchaseOptions[0]
	if buy.State != "" || !buy.BuyOption.LegacyCompatible || buy.RentOption != nil || buy.TaxAndComplianceSettings != nil {
		t.Errorf("the buy option was expanded as %+v", buy)
	}
	if price := buy.RegionalPricingAndAvailabilityConfigs[0].Price; price.Units != 9 || price.Nanos != 900_000_000 {
		t.Errorf("US price = %+v, want 9 units and 900000000 nanos", price)
	}
	if price := buy.NewRegionsConfig.EurPrice; price.CurrencyCode != "EUR" || price.Units != 9 || price.Nanos != 490_000_000 {
		t.Errorf("EUR price = %+v", price)
	}

	state := flattenOneTimeProduct(ctx, sent, model, &diags)
	if got := state.PurchaseOptions["buy"].RegionalConfigs["US"].Price.Amount.ValueString(); got != "9.9" {
		t.Errorf("amount = %q, want the canonical 9.9", got)
	}
	if state.PurchaseOptions["rental"].Rent.RentalPeriod.ValueString() != "P30D" || state.PurchaseOptions["rental"].Buy != nil {
		t.Errorf("the rental option was flattened as %+v", state.PurchaseOptions["rental"])
	}

	current := expandOneTimeProduct(ctx, state, &diags)
	if mask := oneTimeProductUpdateMask(current, sent); len(mask) != 0 {
		t.Errorf("an unchanged product (9.90 against 9.9) gave the mask %v", mask)
	}

	model.OfferTags = types.SetNull(types.StringType)
	delete(model.PurchaseOptions, "rental")
	want := []string{"purchaseOptions", "offerTags"}
	if mask := oneTimeProductUpdateMask(current, expandOneTimeProduct(ctx, model, &diags)); !reflect.DeepEqual(mask, want) {
		t.Errorf("mask = %v, want %v", mask, want)
	}

	if diags.HasError() {
		t.Fatalf("diagnostics: %v", diags)
	}
}

func TestDecimalValueSemanticEquality(t *testing.T) {
	ctx := context.Background()

	equal, diags := NewDecimalValue("4.50").StringSemanticEquals(ctx, NewDecimalValue("4.5"))
	if diags.HasError() || !equal {
		t.Errorf("4.50 and 4.5 must be semantically equal (%v)", diags)
	}

	equal, _ = NewDecimalValue("4.50").StringSemanticEquals(ctx, NewDecimalValue("4.51"))
	if equal {
		t.Error("4.50 and 4.51 must differ")
	}

	if NewDecimalValue("4.50").Equal(NewDecimalValue("4.5")) {
		t.Error("Equal is exact: semantic equality is a separate question")
	}
	if !NewDecimalNull().IsNull() {
		t.Error("NewDecimalNull is not null")
	}
}
