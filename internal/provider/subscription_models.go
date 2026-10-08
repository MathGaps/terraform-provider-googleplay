// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"google.golang.org/api/androidpublisher/v3"
)

type subscriptionModel struct {
	ID                         types.String                        `tfsdk:"id"`
	PackageName                types.String                        `tfsdk:"package_name"`
	ProductID                  types.String                        `tfsdk:"product_id"`
	RegionsVersion             types.String                        `tfsdk:"regions_version"`
	LatencyTolerance           types.String                        `tfsdk:"latency_tolerance"`
	Listings                   map[string]subscriptionListingModel `tfsdk:"listings"`
	BasePlans                  map[string]basePlanModel            `tfsdk:"base_plans"`
	TaxAndComplianceSettings   types.Object                        `tfsdk:"tax_and_compliance_settings"`
	RestrictedPaymentCountries types.Set                           `tfsdk:"restricted_payment_countries"`
}

type subscriptionListingModel struct {
	Title       types.String `tfsdk:"title"`
	Benefits    types.List   `tfsdk:"benefits"`
	Description types.String `tfsdk:"description"`
}

type subscriptionTaxModel struct {
	EeaWithdrawalRightType           types.String                `tfsdk:"eea_withdrawal_right_type"`
	IsTokenizedDigitalAsset          types.Bool                  `tfsdk:"is_tokenized_digital_asset"`
	ProductTaxCategoryCode           types.String                `tfsdk:"product_tax_category_code"`
	TaxRateInfoByRegionCode          map[string]regionalTaxModel `tfsdk:"tax_rate_info_by_region_code"`
	ProductAgeRatingTierByRegionCode types.Map                   `tfsdk:"product_age_rating_tier_by_region_code"`
}

type basePlanModel struct {
	State              types.String                           `tfsdk:"state"`
	AutoRenewing       *autoRenewingModel                     `tfsdk:"auto_renewing"`
	Prepaid            *prepaidModel                          `tfsdk:"prepaid"`
	Installments       *installmentsModel                     `tfsdk:"installments"`
	OfferTags          types.Set                              `tfsdk:"offer_tags"`
	RegionalConfigs    map[string]basePlanRegionalConfigModel `tfsdk:"regional_configs"`
	OtherRegionsConfig *otherRegionsConfigModel               `tfsdk:"other_regions_config"`
}

type autoRenewingModel struct {
	BillingPeriodDuration               types.String `tfsdk:"billing_period_duration"`
	GracePeriodDuration                 types.String `tfsdk:"grace_period_duration"`
	AccountHoldDuration                 types.String `tfsdk:"account_hold_duration"`
	ResubscribeState                    types.String `tfsdk:"resubscribe_state"`
	ProrationMode                       types.String `tfsdk:"proration_mode"`
	LegacyCompatible                    types.Bool   `tfsdk:"legacy_compatible"`
	LegacyCompatibleSubscriptionOfferID types.String `tfsdk:"legacy_compatible_subscription_offer_id"`
}

type prepaidModel struct {
	BillingPeriodDuration types.String `tfsdk:"billing_period_duration"`
	TimeExtension         types.String `tfsdk:"time_extension"`
}

type installmentsModel struct {
	BillingPeriodDuration  types.String `tfsdk:"billing_period_duration"`
	CommittedPaymentsCount types.Int64  `tfsdk:"committed_payments_count"`
	RenewalType            types.String `tfsdk:"renewal_type"`
	GracePeriodDuration    types.String `tfsdk:"grace_period_duration"`
	AccountHoldDuration    types.String `tfsdk:"account_hold_duration"`
	ResubscribeState       types.String `tfsdk:"resubscribe_state"`
	ProrationMode          types.String `tfsdk:"proration_mode"`
}

type basePlanRegionalConfigModel struct {
	Price                     *moneyModel `tfsdk:"price"`
	NewSubscriberAvailability types.Bool  `tfsdk:"new_subscriber_availability"`
}

type otherRegionsConfigModel struct {
	UsdPrice                  *moneyModel `tfsdk:"usd_price"`
	EurPrice                  *moneyModel `tfsdk:"eur_price"`
	NewSubscriberAvailability types.Bool  `tfsdk:"new_subscriber_availability"`
}

// subscriptionTaxAttrTypes is the object type of tax_and_compliance_settings.
func subscriptionTaxAttrTypes() map[string]attr.Type {
	objectType, _ := subscriptionTaxAttribute().GetType().(basetypes.ObjectType)

	return objectType.AttrTypes
}

// --- expand: model to API ----------------------------------------------------

// expandSubscription converts the model into the API's Subscription. Unknown
// and null values are left out, and every list is built in a fixed order, so
// two models that mean the same thing expand to deeply equal values. The
// state of a base plan is not part of it: the API changes state through
// dedicated calls.
func expandSubscription(ctx context.Context, m subscriptionModel, diags *diag.Diagnostics) *androidpublisher.Subscription {
	sub := &androidpublisher.Subscription{
		PackageName:                m.PackageName.ValueString(),
		ProductId:                  m.ProductID.ValueString(),
		RestrictedPaymentCountries: expandRestrictedPaymentCountries(ctx, m.RestrictedPaymentCountries, diags),
		TaxAndComplianceSettings:   expandSubscriptionTax(ctx, m.TaxAndComplianceSettings, diags),
	}

	for _, language := range sortedKeys(m.Listings) {
		listing := m.Listings[language]
		sub.Listings = append(sub.Listings, &androidpublisher.SubscriptionListing{
			LanguageCode: language,
			Title:        listing.Title.ValueString(),
			Benefits:     stringsFromList(ctx, listing.Benefits, diags),
			Description:  listing.Description.ValueString(),
		})
	}

	for _, id := range sortedKeys(m.BasePlans) {
		sub.BasePlans = append(sub.BasePlans, expandBasePlan(ctx, id, m.BasePlans[id], path.Root("base_plans").AtMapKey(id), diags))
	}

	return sub
}

func expandSubscriptionTax(ctx context.Context, object types.Object, diags *diag.Diagnostics) *androidpublisher.SubscriptionTaxAndComplianceSettings {
	if object.IsNull() || object.IsUnknown() {
		return nil
	}

	var m subscriptionTaxModel
	diags.Append(object.As(ctx, &m, basetypes.ObjectAsOptions{})...)

	settings := &androidpublisher.SubscriptionTaxAndComplianceSettings{
		EeaWithdrawalRightType:        m.EeaWithdrawalRightType.ValueString(),
		IsTokenizedDigitalAsset:       m.IsTokenizedDigitalAsset.ValueBool(),
		ProductTaxCategoryCode:        m.ProductTaxCategoryCode.ValueString(),
		RegionalProductAgeRatingInfos: expandAgeRatings(ctx, m.ProductAgeRatingTierByRegionCode, diags),
	}

	for region, info := range m.TaxRateInfoByRegionCode {
		if settings.TaxRateInfoByRegionCode == nil {
			settings.TaxRateInfoByRegionCode = map[string]androidpublisher.RegionalTaxRateInfo{}
		}
		settings.TaxRateInfoByRegionCode[region] = androidpublisher.RegionalTaxRateInfo{
			EligibleForStreamingServiceTaxRate: info.EligibleForStreamingServiceTaxRate.ValueBool(),
			StreamingTaxType:                   info.StreamingTaxType.ValueString(),
			TaxTier:                            info.TaxTier.ValueString(),
		}
	}

	return settings
}

func expandBasePlan(ctx context.Context, id string, m basePlanModel, at path.Path, diags *diag.Diagnostics) *androidpublisher.BasePlan {
	plan := &androidpublisher.BasePlan{
		BasePlanId: id,
		OfferTags:  expandOfferTags(ctx, m.OfferTags, diags),
	}

	if t := m.AutoRenewing; t != nil {
		plan.AutoRenewingBasePlanType = &androidpublisher.AutoRenewingBasePlanType{
			BillingPeriodDuration:               t.BillingPeriodDuration.ValueString(),
			GracePeriodDuration:                 t.GracePeriodDuration.ValueString(),
			AccountHoldDuration:                 t.AccountHoldDuration.ValueString(),
			ResubscribeState:                    t.ResubscribeState.ValueString(),
			ProrationMode:                       t.ProrationMode.ValueString(),
			LegacyCompatible:                    t.LegacyCompatible.ValueBool(),
			LegacyCompatibleSubscriptionOfferId: t.LegacyCompatibleSubscriptionOfferID.ValueString(),
		}
	}
	if t := m.Prepaid; t != nil {
		plan.PrepaidBasePlanType = &androidpublisher.PrepaidBasePlanType{
			BillingPeriodDuration: t.BillingPeriodDuration.ValueString(),
			TimeExtension:         t.TimeExtension.ValueString(),
		}
	}
	if t := m.Installments; t != nil {
		plan.InstallmentsBasePlanType = &androidpublisher.InstallmentsBasePlanType{
			BillingPeriodDuration:  t.BillingPeriodDuration.ValueString(),
			CommittedPaymentsCount: t.CommittedPaymentsCount.ValueInt64(),
			RenewalType:            t.RenewalType.ValueString(),
			GracePeriodDuration:    t.GracePeriodDuration.ValueString(),
			AccountHoldDuration:    t.AccountHoldDuration.ValueString(),
			ResubscribeState:       t.ResubscribeState.ValueString(),
			ProrationMode:          t.ProrationMode.ValueString(),
		}
	}

	for _, region := range sortedKeys(m.RegionalConfigs) {
		config := m.RegionalConfigs[region]
		plan.RegionalConfigs = append(plan.RegionalConfigs, &androidpublisher.RegionalBasePlanConfig{
			RegionCode:                region,
			Price:                     expandMoney(config.Price, at.AtName("regional_configs").AtMapKey(region).AtName("price"), diags),
			NewSubscriberAvailability: config.NewSubscriberAvailability.ValueBool(),
		})
	}

	if other := m.OtherRegionsConfig; other != nil {
		otherPath := at.AtName("other_regions_config")
		plan.OtherRegionsConfig = &androidpublisher.OtherRegionsBasePlanConfig{
			UsdPrice:                  expandMoney(other.UsdPrice, otherPath.AtName("usd_price"), diags),
			EurPrice:                  expandMoney(other.EurPrice, otherPath.AtName("eur_price"), diags),
			NewSubscriberAvailability: other.NewSubscriberAvailability.ValueBool(),
		}
	}

	return plan
}

// --- flatten: API to model ---------------------------------------------------

// flattenSubscription converts the API's Subscription into the model. prior is
// the plan or the previous state: it supplies the attributes the API does not
// return (regions_version, latency_tolerance) and decides between null and
// empty for a collection the API reports as absent.
func flattenSubscription(ctx context.Context, sub *androidpublisher.Subscription, prior subscriptionModel, diags *diag.Diagnostics) subscriptionModel {
	model := subscriptionModel{
		ID:                         types.StringValue(sub.PackageName + "/" + sub.ProductId),
		PackageName:                types.StringValue(sub.PackageName),
		ProductID:                  types.StringValue(sub.ProductId),
		RegionsVersion:             prior.RegionsVersion,
		LatencyTolerance:           prior.LatencyTolerance,
		Listings:                   map[string]subscriptionListingModel{},
		TaxAndComplianceSettings:   flattenSubscriptionTax(ctx, sub.TaxAndComplianceSettings, prior.TaxAndComplianceSettings, diags),
		RestrictedPaymentCountries: flattenRestrictedPaymentCountries(sub.RestrictedPaymentCountries, prior.RestrictedPaymentCountries),
	}

	if model.RegionsVersion.IsNull() || model.RegionsVersion.IsUnknown() {
		model.RegionsVersion = types.StringValue(defaultRegionsVersion)
	}

	for _, listing := range sub.Listings {
		model.Listings[listing.LanguageCode] = subscriptionListingModel{
			Title:       types.StringValue(listing.Title),
			Benefits:    listFromStrings(ctx, listing.Benefits, prior.Listings[listing.LanguageCode].Benefits, diags),
			Description: stringOrNull(listing.Description),
		}
	}

	basePlans := map[string]basePlanModel{}
	for _, plan := range sub.BasePlans {
		basePlans[plan.BasePlanId] = flattenBasePlan(plan, prior.BasePlans[plan.BasePlanId])
	}
	model.BasePlans = keepEmptyMap(basePlans, prior.BasePlans)

	return model
}

func flattenSubscriptionTax(ctx context.Context, settings *androidpublisher.SubscriptionTaxAndComplianceSettings, prior types.Object, diags *diag.Diagnostics) types.Object {
	if settings == nil {
		return types.ObjectNull(subscriptionTaxAttrTypes())
	}

	var priorModel subscriptionTaxModel
	if !prior.IsNull() && !prior.IsUnknown() {
		diags.Append(prior.As(ctx, &priorModel, basetypes.ObjectAsOptions{})...)
	}

	model := subscriptionTaxModel{
		EeaWithdrawalRightType:           enumOrNull(settings.EeaWithdrawalRightType),
		IsTokenizedDigitalAsset:          types.BoolValue(settings.IsTokenizedDigitalAsset),
		ProductTaxCategoryCode:           stringOrNull(settings.ProductTaxCategoryCode),
		ProductAgeRatingTierByRegionCode: flattenAgeRatings(settings.RegionalProductAgeRatingInfos, priorModel.ProductAgeRatingTierByRegionCode),
	}

	rates := map[string]regionalTaxModel{}
	for region, info := range settings.TaxRateInfoByRegionCode {
		rates[region] = regionalTaxModel{
			EligibleForStreamingServiceTaxRate: types.BoolValue(info.EligibleForStreamingServiceTaxRate),
			StreamingTaxType:                   enumOrNull(info.StreamingTaxType),
			TaxTier:                            enumOrNull(info.TaxTier),
		}
	}
	model.TaxRateInfoByRegionCode = keepEmptyMap(rates, priorModel.TaxRateInfoByRegionCode)

	object, d := types.ObjectValueFrom(ctx, subscriptionTaxAttrTypes(), model)
	diags.Append(d...)

	return object
}

func flattenBasePlan(plan *androidpublisher.BasePlan, prior basePlanModel) basePlanModel {
	model := basePlanModel{
		State:     flattenState(plan.State, prior.State),
		OfferTags: flattenOfferTags(plan.OfferTags, prior.OfferTags),
	}

	if t := plan.AutoRenewingBasePlanType; t != nil {
		model.AutoRenewing = &autoRenewingModel{
			BillingPeriodDuration:               types.StringValue(t.BillingPeriodDuration),
			GracePeriodDuration:                 stringOrNull(t.GracePeriodDuration),
			AccountHoldDuration:                 stringOrNull(t.AccountHoldDuration),
			ResubscribeState:                    enumOrNull(t.ResubscribeState),
			ProrationMode:                       enumOrNull(t.ProrationMode),
			LegacyCompatible:                    types.BoolValue(t.LegacyCompatible),
			LegacyCompatibleSubscriptionOfferID: stringOrNull(t.LegacyCompatibleSubscriptionOfferId),
		}
	}
	if t := plan.PrepaidBasePlanType; t != nil {
		model.Prepaid = &prepaidModel{
			BillingPeriodDuration: types.StringValue(t.BillingPeriodDuration),
			TimeExtension:         enumOrNull(t.TimeExtension),
		}
	}
	if t := plan.InstallmentsBasePlanType; t != nil {
		model.Installments = &installmentsModel{
			BillingPeriodDuration:  types.StringValue(t.BillingPeriodDuration),
			CommittedPaymentsCount: types.Int64Value(t.CommittedPaymentsCount),
			RenewalType:            types.StringValue(t.RenewalType),
			GracePeriodDuration:    stringOrNull(t.GracePeriodDuration),
			AccountHoldDuration:    stringOrNull(t.AccountHoldDuration),
			ResubscribeState:       enumOrNull(t.ResubscribeState),
			ProrationMode:          enumOrNull(t.ProrationMode),
		}
	}

	regional := map[string]basePlanRegionalConfigModel{}
	for _, config := range plan.RegionalConfigs {
		regional[config.RegionCode] = basePlanRegionalConfigModel{
			Price:                     flattenMoney(config.Price),
			NewSubscriberAvailability: types.BoolValue(config.NewSubscriberAvailability),
		}
	}
	model.RegionalConfigs = keepEmptyMap(regional, prior.RegionalConfigs)

	if other := plan.OtherRegionsConfig; other != nil {
		model.OtherRegionsConfig = &otherRegionsConfigModel{
			UsdPrice:                  flattenMoney(other.UsdPrice),
			EurPrice:                  flattenMoney(other.EurPrice),
			NewSubscriberAvailability: types.BoolValue(other.NewSubscriberAvailability),
		}
	}

	return model
}
