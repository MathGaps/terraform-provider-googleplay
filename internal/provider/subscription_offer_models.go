// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/api/androidpublisher/v3"
)

type subscriptionOfferModel struct {
	ID                 types.String                        `tfsdk:"id"`
	PackageName        types.String                        `tfsdk:"package_name"`
	ProductID          types.String                        `tfsdk:"product_id"`
	BasePlanID         types.String                        `tfsdk:"base_plan_id"`
	OfferID            types.String                        `tfsdk:"offer_id"`
	RegionsVersion     types.String                        `tfsdk:"regions_version"`
	LatencyTolerance   types.String                        `tfsdk:"latency_tolerance"`
	State              types.String                        `tfsdk:"state"`
	Phases             []offerPhaseModel                   `tfsdk:"phases"`
	RegionalConfigs    map[string]offerRegionalConfigModel `tfsdk:"regional_configs"`
	OtherRegionsConfig *offerRegionalConfigModel           `tfsdk:"other_regions_config"`
	Targeting          *offerTargetingModel                `tfsdk:"targeting"`
	OfferTags          types.Set                           `tfsdk:"offer_tags"`
}

// offerRegionalConfigModel is the availability of an offer in a region, and
// in the regions Google Play launches in later.
type offerRegionalConfigModel struct {
	NewSubscriberAvailability types.Bool `tfsdk:"new_subscriber_availability"`
}

type offerPhaseModel struct {
	Duration           types.String                             `tfsdk:"duration"`
	RecurrenceCount    types.Int64                              `tfsdk:"recurrence_count"`
	RegionalConfigs    map[string]offerPhaseRegionalConfigModel `tfsdk:"regional_configs"`
	OtherRegionsConfig *offerPhaseOtherRegionsConfigModel       `tfsdk:"other_regions_config"`
}

// offerPhaseRegionalConfigModel is what a phase costs in one region: exactly
// one of the four is set.
type offerPhaseRegionalConfigModel struct {
	Price            *moneyModel   `tfsdk:"price"`
	AbsoluteDiscount *moneyModel   `tfsdk:"absolute_discount"`
	RelativeDiscount types.Float64 `tfsdk:"relative_discount"`
	Free             *emptyModel   `tfsdk:"free"`
}

type offerPhaseOtherRegionsConfigModel struct {
	Price            *otherRegionsPricesModel `tfsdk:"price"`
	AbsoluteDiscount *otherRegionsPricesModel `tfsdk:"absolute_discount"`
	RelativeDiscount types.Float64            `tfsdk:"relative_discount"`
	Free             *emptyModel              `tfsdk:"free"`
}

type otherRegionsPricesModel struct {
	UsdPrice *moneyModel `tfsdk:"usd_price"`
	EurPrice *moneyModel `tfsdk:"eur_price"`
}

type offerTargetingModel struct {
	AcquisitionRule *acquisitionRuleModel `tfsdk:"acquisition_rule"`
	UpgradeRule     *upgradeRuleModel     `tfsdk:"upgrade_rule"`
}

type acquisitionRuleModel struct {
	Scope *acquisitionScopeModel `tfsdk:"scope"`
}

type acquisitionScopeModel struct {
	ThisSubscription     *emptyModel `tfsdk:"this_subscription"`
	AnySubscriptionInApp *emptyModel `tfsdk:"any_subscription_in_app"`
}

type upgradeRuleModel struct {
	Scope                 *upgradeScopeModel `tfsdk:"scope"`
	BillingPeriodDuration types.String       `tfsdk:"billing_period_duration"`
	OncePerUser           types.Bool         `tfsdk:"once_per_user"`
}

type upgradeScopeModel struct {
	ThisSubscription          *emptyModel  `tfsdk:"this_subscription"`
	SpecificSubscriptionInApp types.String `tfsdk:"specific_subscription_in_app"`
}

// emptyModel is an API message with no fields, such as the marker that makes
// a phase free. It is written `{}` in a configuration.
type emptyModel struct{}

// --- expand: model to API ----------------------------------------------------

// expandSubscriptionOffer converts the model into the API's SubscriptionOffer,
// in a fixed order and without the state, as expandSubscription does.
func expandSubscriptionOffer(ctx context.Context, m subscriptionOfferModel, diags *diag.Diagnostics) *androidpublisher.SubscriptionOffer {
	offer := &androidpublisher.SubscriptionOffer{
		PackageName: m.PackageName.ValueString(),
		ProductId:   m.ProductID.ValueString(),
		BasePlanId:  m.BasePlanID.ValueString(),
		OfferId:     m.OfferID.ValueString(),
		OfferTags:   expandOfferTags(ctx, m.OfferTags, diags),
	}

	for _, region := range sortedKeys(m.RegionalConfigs) {
		offer.RegionalConfigs = append(offer.RegionalConfigs, &androidpublisher.RegionalSubscriptionOfferConfig{
			RegionCode:                region,
			NewSubscriberAvailability: m.RegionalConfigs[region].NewSubscriberAvailability.ValueBool(),
		})
	}
	if other := m.OtherRegionsConfig; other != nil {
		offer.OtherRegionsConfig = &androidpublisher.OtherRegionsSubscriptionOfferConfig{
			OtherRegionsNewSubscriberAvailability: other.NewSubscriberAvailability.ValueBool(),
		}
	}

	for i, phase := range m.Phases {
		at := path.Root("phases").AtListIndex(i)
		expanded := &androidpublisher.SubscriptionOfferPhase{
			Duration:        phase.Duration.ValueString(),
			RecurrenceCount: phase.RecurrenceCount.ValueInt64(),
		}
		for _, region := range sortedKeys(phase.RegionalConfigs) {
			config := phase.RegionalConfigs[region]
			configPath := at.AtName("regional_configs").AtMapKey(region)
			expanded.RegionalConfigs = append(expanded.RegionalConfigs, &androidpublisher.RegionalSubscriptionOfferPhaseConfig{
				RegionCode:       region,
				Price:            expandMoney(config.Price, configPath.AtName("price"), diags),
				AbsoluteDiscount: expandMoney(config.AbsoluteDiscount, configPath.AtName("absolute_discount"), diags),
				RelativeDiscount: config.RelativeDiscount.ValueFloat64(),
				Free:             expandEmpty[androidpublisher.RegionalSubscriptionOfferPhaseFreePriceOverride](config.Free),
			})
		}
		if other := phase.OtherRegionsConfig; other != nil {
			otherPath := at.AtName("other_regions_config")
			expanded.OtherRegionsConfig = &androidpublisher.OtherRegionsSubscriptionOfferPhaseConfig{
				OtherRegionsPrices: expandOtherRegionsPrices(other.Price, otherPath.AtName("price"), diags),
				AbsoluteDiscounts:  expandOtherRegionsPrices(other.AbsoluteDiscount, otherPath.AtName("absolute_discount"), diags),
				RelativeDiscount:   other.RelativeDiscount.ValueFloat64(),
				Free:               expandEmpty[androidpublisher.OtherRegionsSubscriptionOfferPhaseFreePriceOverride](other.Free),
			}
		}
		offer.Phases = append(offer.Phases, expanded)
	}

	if t := m.Targeting; t != nil && (t.AcquisitionRule != nil || t.UpgradeRule != nil) {
		offer.Targeting = &androidpublisher.SubscriptionOfferTargeting{}
		if rule := t.AcquisitionRule; rule != nil && rule.Scope != nil {
			offer.Targeting.AcquisitionRule = &androidpublisher.AcquisitionTargetingRule{
				Scope: &androidpublisher.TargetingRuleScope{
					ThisSubscription:     expandEmpty[androidpublisher.TargetingRuleScopeThisSubscription](rule.Scope.ThisSubscription),
					AnySubscriptionInApp: expandEmpty[androidpublisher.TargetingRuleScopeAnySubscriptionInApp](rule.Scope.AnySubscriptionInApp),
				},
			}
		}
		if rule := t.UpgradeRule; rule != nil && rule.Scope != nil {
			offer.Targeting.UpgradeRule = &androidpublisher.UpgradeTargetingRule{
				BillingPeriodDuration: rule.BillingPeriodDuration.ValueString(),
				OncePerUser:           rule.OncePerUser.ValueBool(),
				Scope: &androidpublisher.TargetingRuleScope{
					ThisSubscription:          expandEmpty[androidpublisher.TargetingRuleScopeThisSubscription](rule.Scope.ThisSubscription),
					SpecificSubscriptionInApp: rule.Scope.SpecificSubscriptionInApp.ValueString(),
				},
			}
		}
	}

	return offer
}

// expandEmpty converts a marker: a set one gives an empty message, which the
// client sends as {}, and an unset one gives nil, which it leaves out.
func expandEmpty[T any](m *emptyModel) *T {
	if m == nil {
		return nil
	}

	return new(T)
}

func expandOtherRegionsPrices(m *otherRegionsPricesModel, at path.Path, diags *diag.Diagnostics) *androidpublisher.OtherRegionsSubscriptionOfferPhasePrices {
	if m == nil {
		return nil
	}

	return &androidpublisher.OtherRegionsSubscriptionOfferPhasePrices{
		UsdPrice: expandMoney(m.UsdPrice, at.AtName("usd_price"), diags),
		EurPrice: expandMoney(m.EurPrice, at.AtName("eur_price"), diags),
	}
}

// --- flatten: API to model ---------------------------------------------------

// flattenSubscriptionOffer converts the API's SubscriptionOffer into the
// model, with prior playing the part it does in flattenSubscription.
func flattenSubscriptionOffer(offer *androidpublisher.SubscriptionOffer, prior subscriptionOfferModel) subscriptionOfferModel {
	model := subscriptionOfferModel{
		ID:               types.StringValue(offer.PackageName + "/" + offer.ProductId + "/" + offer.BasePlanId + "/" + offer.OfferId),
		PackageName:      types.StringValue(offer.PackageName),
		ProductID:        types.StringValue(offer.ProductId),
		BasePlanID:       types.StringValue(offer.BasePlanId),
		OfferID:          types.StringValue(offer.OfferId),
		RegionsVersion:   prior.RegionsVersion,
		LatencyTolerance: prior.LatencyTolerance,
		State:            flattenState(offer.State, prior.State),
		RegionalConfigs:  map[string]offerRegionalConfigModel{},
		OfferTags:        flattenOfferTags(offer.OfferTags, prior.OfferTags),
	}

	if model.RegionsVersion.IsNull() || model.RegionsVersion.IsUnknown() {
		model.RegionsVersion = types.StringValue(defaultRegionsVersion)
	}

	for _, config := range offer.RegionalConfigs {
		model.RegionalConfigs[config.RegionCode] = offerRegionalConfigModel{
			NewSubscriberAvailability: types.BoolValue(config.NewSubscriberAvailability),
		}
	}
	if other := offer.OtherRegionsConfig; other != nil {
		model.OtherRegionsConfig = &offerRegionalConfigModel{
			NewSubscriberAvailability: types.BoolValue(other.OtherRegionsNewSubscriberAvailability),
		}
	}

	for _, phase := range offer.Phases {
		flattened := offerPhaseModel{
			Duration:        types.StringValue(phase.Duration),
			RecurrenceCount: types.Int64Value(phase.RecurrenceCount),
			RegionalConfigs: map[string]offerPhaseRegionalConfigModel{},
		}
		for _, config := range phase.RegionalConfigs {
			flattened.RegionalConfigs[config.RegionCode] = offerPhaseRegionalConfigModel{
				Price:            flattenMoney(config.Price),
				AbsoluteDiscount: flattenMoney(config.AbsoluteDiscount),
				RelativeDiscount: float64OrNull(config.RelativeDiscount),
				Free:             flattenEmpty(config.Free),
			}
		}
		if other := phase.OtherRegionsConfig; other != nil {
			flattened.OtherRegionsConfig = &offerPhaseOtherRegionsConfigModel{
				Price:            flattenOtherRegionsPrices(other.OtherRegionsPrices),
				AbsoluteDiscount: flattenOtherRegionsPrices(other.AbsoluteDiscounts),
				RelativeDiscount: float64OrNull(other.RelativeDiscount),
				Free:             flattenEmpty(other.Free),
			}
		}
		model.Phases = append(model.Phases, flattened)
	}

	if t := offer.Targeting; t != nil && (t.AcquisitionRule != nil || t.UpgradeRule != nil) {
		model.Targeting = &offerTargetingModel{}
		if rule := t.AcquisitionRule; rule != nil {
			model.Targeting.AcquisitionRule = &acquisitionRuleModel{}
			if scope := rule.Scope; scope != nil {
				model.Targeting.AcquisitionRule.Scope = &acquisitionScopeModel{
					ThisSubscription:     flattenEmpty(scope.ThisSubscription),
					AnySubscriptionInApp: flattenEmpty(scope.AnySubscriptionInApp),
				}
			}
		}
		if rule := t.UpgradeRule; rule != nil {
			model.Targeting.UpgradeRule = &upgradeRuleModel{
				BillingPeriodDuration: stringOrNull(rule.BillingPeriodDuration),
				OncePerUser:           types.BoolValue(rule.OncePerUser),
			}
			if scope := rule.Scope; scope != nil {
				model.Targeting.UpgradeRule.Scope = &upgradeScopeModel{
					ThisSubscription:          flattenEmpty(scope.ThisSubscription),
					SpecificSubscriptionInApp: stringOrNull(scope.SpecificSubscriptionInApp),
				}
			}
		}
	}

	return model
}

func flattenEmpty[T any](message *T) *emptyModel {
	if message == nil {
		return nil
	}

	return &emptyModel{}
}

func flattenOtherRegionsPrices(prices *androidpublisher.OtherRegionsSubscriptionOfferPhasePrices) *otherRegionsPricesModel {
	if prices == nil {
		return nil
	}

	return &otherRegionsPricesModel{
		UsdPrice: flattenMoney(prices.UsdPrice),
		EurPrice: flattenMoney(prices.EurPrice),
	}
}

// float64OrNull maps the API's zero, which it never sends, to null.
func float64OrNull(value float64) types.Float64 {
	if value == 0 {
		return types.Float64Null()
	}

	return types.Float64Value(value)
}
