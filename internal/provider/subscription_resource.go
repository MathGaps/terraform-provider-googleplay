// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/objectvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/api/androidpublisher/v3"

	"github.com/MathGaps/terraform-provider-googleplay/internal/play"
)

var (
	_ resource.Resource                = &subscriptionResource{}
	_ resource.ResourceWithConfigure   = &subscriptionResource{}
	_ resource.ResourceWithImportState = &subscriptionResource{}
)

var (
	resubscribeStates = []string{"RESUBSCRIBE_STATE_ACTIVE", "RESUBSCRIBE_STATE_INACTIVE"}
	prorationModes    = []string{
		"SUBSCRIPTION_PRORATION_MODE_CHARGE_ON_NEXT_BILLING_DATE",
		"SUBSCRIPTION_PRORATION_MODE_CHARGE_FULL_PRICE_IMMEDIATELY",
	}
	timeExtensions = []string{"TIME_EXTENSION_ACTIVE", "TIME_EXTENSION_INACTIVE"}
	renewalTypes   = []string{"RENEWAL_TYPE_RENEWS_WITHOUT_COMMITMENT", "RENEWAL_TYPE_RENEWS_WITH_COMMITMENT"}
)

// NewSubscriptionResource returns the googleplay_subscription resource.
func NewSubscriptionResource() resource.Resource {
	return &subscriptionResource{}
}

type subscriptionResource struct {
	client *play.Client
}

func (r *subscriptionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_subscription"
}

func durationValidators() []validator.String {
	return []validator.String{
		stringvalidator.RegexMatches(isoDurationPattern, "must be an ISO 8601 duration such as P1M or P7D"),
	}
}

// serverDefaultString is an optional string the server fills in when the
// configuration leaves it out.
func serverDefaultString(description string, validators ...validator.String) schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: description,
		Optional:            true,
		Computed:            true,
		PlanModifiers:       []planmodifier.String{keepStateOfExistingParent{}},
		Validators:          validators,
	}
}

func defaultFalseBool(description string) schema.BoolAttribute {
	return schema.BoolAttribute{
		MarkdownDescription: description + " Defaults to `false`.",
		Optional:            true,
		Computed:            true,
		Default:             booldefault.StaticBool(false),
	}
}

func subscriptionTaxAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Tax and legal compliance settings. Left out, the settings Google Play assigns are " +
			"read back and kept.",
		Optional:      true,
		Computed:      true,
		PlanModifiers: []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
		Attributes: map[string]schema.Attribute{
			"eea_withdrawal_right_type": serverDefaultString(
				"The classification of the product for users in the European Economic Area, which decides the "+
					"withdrawal regime: "+oneOfDescription(withdrawalRightTypes...)+".",
				stringvalidator.OneOf(withdrawalRightTypes...),
			),
			"is_tokenized_digital_asset": defaultFalseBool("Whether the subscription is declared as a product representing a tokenized digital asset."),
			"product_tax_category_code": serverDefaultString(
				"The product tax category code, which decides the transaction tax rates applied.",
			),
			"tax_rate_info_by_region_code": schema.MapNestedAttribute{
				MarkdownDescription: "Tax rate details by region code.",
				Optional:            true,
				Validators:          []validator.Map{regionKeysValidator()},
				NestedObject:        schema.NestedAttributeObject{Attributes: regionalTaxAttributes()},
			},
			"product_age_rating_tier_by_region_code": productAgeRatingAttribute(),
		},
	}
}

func basePlanAttributes() map[string]schema.Attribute {
	typeNames := []string{"auto_renewing", "prepaid", "installments"}
	exactlyOneType := objectvalidator.ExactlyOneOf(
		path.MatchRelative().AtParent().AtName(typeNames[0]),
		path.MatchRelative().AtParent().AtName(typeNames[1]),
		path.MatchRelative().AtParent().AtName(typeNames[2]),
	)

	billingPeriod := schema.StringAttribute{
		MarkdownDescription: "The billing period as an ISO 8601 duration, for example `P1M` or `P1Y`. " +
			"The API does not allow it to change once the base plan exists.",
		Required:   true,
		Validators: durationValidators(),
	}
	gracePeriod := serverDefaultString(
		"The grace period as an ISO 8601 duration in days, between `P0D` and the lesser of `P30D` and the billing "+
			"period. Left out, the API picks a default from the billing period.",
		durationValidators()...,
	)
	accountHold := serverDefaultString(
		"The account hold period as an ISO 8601 duration in days, between `P0D` and `P60D`. Left out, the API uses "+
			"the recommended hold. The grace period and the account hold must add up to between 30 and 60 days.",
		durationValidators()...,
	)
	resubscribe := serverDefaultString(
		"Whether users can resubscribe to the base plan from Google Play: "+oneOfDescription(resubscribeStates...)+
			". The API defaults to `RESUBSCRIBE_STATE_ACTIVE`.",
		stringvalidator.OneOf(resubscribeStates...),
	)
	proration := serverDefaultString(
		"What happens when a user switches to this plan from another base plan: "+oneOfDescription(prorationModes...)+
			". The API defaults to charging on the next billing date.",
		stringvalidator.OneOf(prorationModes...),
	)

	return map[string]schema.Attribute{
		"state": productStateAttribute("base plan", "the `basePlans.activate` and `basePlans.deactivate` calls"),
		"auto_renewing": schema.SingleNestedAttribute{
			MarkdownDescription: "Makes this a base plan that renews automatically at the end of each billing " +
				"period. Exactly one of `auto_renewing`, `prepaid` and `installments` must be set.",
			Optional:   true,
			Validators: []validator.Object{exactlyOneType},
			Attributes: map[string]schema.Attribute{
				"billing_period_duration": billingPeriod,
				"grace_period_duration":   gracePeriod,
				"account_hold_duration":   accountHold,
				"resubscribe_state":       resubscribe,
				"proration_mode":          proration,
				"legacy_compatible": defaultFalseBool("Whether this is the base plan the deprecated " +
					"`querySkuDetailsAsync()` billing method returns. At most one renewing base plan of a subscription can be."),
				"legacy_compatible_subscription_offer_id": schema.StringAttribute{
					MarkdownDescription: "The id of the subscription offer that is legacy compatible, if any.",
					Optional:            true,
				},
			},
		},
		"prepaid": schema.SingleNestedAttribute{
			MarkdownDescription: "Makes this a base plan that does not renew: the user tops it up by hand.",
			Optional:            true,
			Attributes: map[string]schema.Attribute{
				"billing_period_duration": billingPeriod,
				"time_extension": serverDefaultString(
					"Whether users can extend the prepaid plan from Google Play: "+oneOfDescription(timeExtensions...)+
						". The API defaults to `TIME_EXTENSION_ACTIVE`.",
					stringvalidator.OneOf(timeExtensions...),
				),
			},
		},
		"installments": schema.SingleNestedAttribute{
			MarkdownDescription: "Makes this an installments base plan, where the user commits to a number of payments.",
			Optional:            true,
			Attributes: map[string]schema.Attribute{
				"billing_period_duration": billingPeriod,
				"committed_payments_count": schema.Int64Attribute{
					MarkdownDescription: "The number of payments the user commits to. The API does not allow it to change once the base plan exists.",
					Required:            true,
					Validators:          []validator.Int64{int64validator.AtLeast(1)},
				},
				"renewal_type": schema.StringAttribute{
					MarkdownDescription: "What happens at the end of the initial commitment: " + oneOfDescription(renewalTypes...) +
						". The API does not allow it to change once the base plan exists.",
					Required:   true,
					Validators: []validator.String{stringvalidator.OneOf(renewalTypes...)},
				},
				"grace_period_duration": gracePeriod,
				"account_hold_duration": accountHold,
				"resubscribe_state":     resubscribe,
				"proration_mode":        proration,
			},
		},
		"offer_tags": offerTagsAttribute("base plan"),
		"regional_configs": schema.MapNestedAttribute{
			MarkdownDescription: "The price and availability of the base plan by region code, for example `US`. " +
				"A region left out is one the base plan is not sold in.",
			Optional:   true,
			Validators: []validator.Map{regionKeysValidator()},
			NestedObject: schema.NestedAttributeObject{
				Attributes: map[string]schema.Attribute{
					"price": moneyAttribute("The price in the region, in the currency Google Play links to the region. "+
						"Required when the base plan is available to new subscribers there.", false),
					"new_subscriber_availability": defaultFalseBool("Whether new subscribers in the region can buy the base plan. " +
						"Existing subscribers keep their subscription either way."),
				},
			},
		},
		"other_regions_config": schema.SingleNestedAttribute{
			MarkdownDescription: "The prices to use in any region Google Play launches in later. Left out, the base " +
				"plan is not made available in new regions automatically.",
			Optional: true,
			Attributes: map[string]schema.Attribute{
				"usd_price":                   moneyAttribute("The price in USD for new regions.", true),
				"eur_price":                   moneyAttribute("The price in EUR for new regions.", true),
				"new_subscriber_availability": defaultFalseBool("Whether new subscribers in new regions can buy the base plan."),
			},
		},
	}
}

func (r *subscriptionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A subscription of an app, with its listings, its tax settings and its base plans " +
			"and their regional prices.\n\n" +
			"Base plans are part of this resource because that is how the API changes them: a base plan is " +
			"created and edited by updating the subscription, and has no create or update call of its own. " +
			"Only its state and its deletion have dedicated calls, and the provider makes them for you.\n\n" +
			"The resource is authoritative for the subscription's base plans: one that exists in Play Console " +
			"and not in the configuration is planned for removal.\n\n" +
			"~> **What Google Play does not let you undo.** A product id is reserved forever. A base plan can be " +
			"deleted only while it is a draft: removing an active or inactive one from the configuration is an " +
			"error, so retire it with `state = \"INACTIVE\"` and keep it declared. A subscription can be deleted " +
			"only if none of its base plans was ever activated: destroying one that cannot be deleted " +
			"deactivates its active base plans, removes it from state with a warning, and leaves it in Play Console.\n\n" +
			"Subscription offers (free trials, introductory prices) are not managed yet. Changing the price of " +
			"a region changes it for new subscribers; moving existing subscribers to it (`migratePrices`) is " +
			"not managed either.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "`{package_name}/{product_id}`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"package_name": schema.StringAttribute{
				MarkdownDescription: packageNameDescription + " Changing it creates a new subscription.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          packageNameValidators(),
			},
			"product_id": productIDAttribute("subscription"),
			"regions_version": regionsVersionAttribute(" The API does not report which version a subscription " +
				"was last written with, so the value is only what the configuration says."),
			"latency_tolerance": latencyToleranceAttribute(),
			"listings": schema.MapNestedAttribute{
				MarkdownDescription: "The store listing of the subscription by BCP-47 language code, for example " +
					"`en-US`. It must have an entry for the default language of the app.",
				Required: true,
				Validators: []validator.Map{
					mapvalidator.SizeAtLeast(1),
				},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"title": schema.StringAttribute{
							MarkdownDescription: "The title of the subscription in this language. Plain text.",
							Required:            true,
							Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
						},
						"benefits": schema.ListAttribute{
							MarkdownDescription: "Up to four benefits shown to the user, in order. Plain text.",
							ElementType:         types.StringType,
							Optional:            true,
							Validators:          []validator.List{listvalidator.SizeAtMost(4)},
						},
						"description": schema.StringAttribute{
							MarkdownDescription: "The description of the subscription in this language. Plain text, at most 200 characters.",
							Optional:            true,
							Validators:          []validator.String{stringvalidator.UTF8LengthBetween(1, 200)},
						},
					},
				},
			},
			"base_plans": schema.MapNestedAttribute{
				MarkdownDescription: "The base plans of the subscription by base plan id: lower-case letters, digits " +
					"and hyphens, at most 63 characters. A base plan sets the billing period and the prices " +
					"that apply when no offer does.",
				Optional: true,
				Validators: []validator.Map{
					mapvalidator.KeysAre(
						stringvalidator.RegexMatches(rfc1034Pattern, "must be lower-case letters, digits and hyphens"),
						stringvalidator.LengthAtMost(63),
					),
				},
				NestedObject: schema.NestedAttributeObject{Attributes: basePlanAttributes()},
			},
			"tax_and_compliance_settings":  subscriptionTaxAttribute(),
			"restricted_payment_countries": restrictedPaymentCountriesAttribute(),
		},
	}
}

func (r *subscriptionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

// basePlanStates maps base plan id to its state in an API subscription.
func basePlanStates(sub *androidpublisher.Subscription) map[string]string {
	states := map[string]string{}
	for _, plan := range sub.BasePlans {
		states[plan.BasePlanId] = plan.State
	}

	return states
}

// reconcileBasePlanStates activates and deactivates base plans until each one
// with a configured state is in it. current is the state of each plan as the
// API last reported it.
func (r *subscriptionResource) reconcileBasePlanStates(ctx context.Context, plan subscriptionModel, current map[string]string) error {
	packageName, productID := plan.PackageName.ValueString(), plan.ProductID.ValueString()
	latency := plan.LatencyTolerance.ValueString()

	for _, id := range sortedKeys(plan.BasePlans) {
		activate, deactivate, err := stateTransition(current[id], plan.BasePlans[id].State.ValueString())
		if err != nil {
			return fmt.Errorf("base plan %q: %w", id, err)
		}

		if activate {
			_, err := r.client.Service.Monetization.Subscriptions.BasePlans.Activate(packageName, productID, id,
				&androidpublisher.ActivateBasePlanRequest{
					PackageName:      packageName,
					ProductId:        productID,
					BasePlanId:       id,
					LatencyTolerance: latency,
				}).Context(ctx).Do()
			if err != nil {
				return fmt.Errorf("activating base plan %q: %w", id, err)
			}
		}
		if deactivate {
			_, err := r.client.Service.Monetization.Subscriptions.BasePlans.Deactivate(packageName, productID, id,
				&androidpublisher.DeactivateBasePlanRequest{
					PackageName:      packageName,
					ProductId:        productID,
					BasePlanId:       id,
					LatencyTolerance: latency,
				}).Context(ctx).Do()
			if err != nil {
				return fmt.Errorf("deactivating base plan %q: %w", id, err)
			}
		}
	}

	return nil
}

// readInto gets the subscription and writes it into the model.
func (r *subscriptionResource) readInto(ctx context.Context, prior subscriptionModel, diags *diag.Diagnostics) (subscriptionModel, error) {
	sub, err := r.client.Service.Monetization.Subscriptions.
		Get(prior.PackageName.ValueString(), prior.ProductID.ValueString()).Context(ctx).Do()
	if err != nil {
		return subscriptionModel{}, err
	}

	// The package and product come from the request path, whatever the body says.
	sub.PackageName, sub.ProductId = prior.PackageName.ValueString(), prior.ProductID.ValueString()

	return flattenSubscription(ctx, sub, prior, diags), nil
}

func (r *subscriptionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan subscriptionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	desired := expandSubscription(ctx, plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// subscriptions.create takes no latency tolerance.
	created, err := r.client.Service.Monetization.Subscriptions.Create(desired.PackageName, desired).
		ProductId(desired.ProductId).
		RegionsVersionVersion(plan.RegionsVersion.ValueString()).
		Context(ctx).Do()
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create the subscription", err)

		return
	}

	// From here the subscription exists, so state is written even when a later
	// step fails: the next apply then finishes the job instead of colliding
	// with a product id that is already taken.
	stateErr := r.reconcileBasePlanStates(ctx, plan, basePlanStates(created))

	state, err := r.readInto(ctx, plan, &resp.Diagnostics)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read the subscription after creating it", err)

		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)

	if stateErr != nil {
		addAPIError(&resp.Diagnostics, "Unable to set the state of a base plan", stateErr)
	}
}

func (r *subscriptionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state subscriptionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState, err := r.readInto(ctx, state, &resp.Diagnostics)
	if play.IsNotFound(err) {
		resp.State.RemoveResource(ctx)

		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read the subscription", err)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// subscriptionUpdateMask lists the fields of the subscription that differ.
func subscriptionUpdateMask(current, desired *androidpublisher.Subscription) []string {
	var mask []string
	if !reflect.DeepEqual(current.Listings, desired.Listings) {
		mask = append(mask, "listings")
	}
	if !reflect.DeepEqual(current.BasePlans, desired.BasePlans) {
		mask = append(mask, "basePlans")
	}
	if !reflect.DeepEqual(current.TaxAndComplianceSettings, desired.TaxAndComplianceSettings) {
		mask = append(mask, "taxAndComplianceSettings")
	}
	if !reflect.DeepEqual(current.RestrictedPaymentCountries, desired.RestrictedPaymentCountries) {
		mask = append(mask, "restrictedPaymentCountries")
	}

	return mask
}

func (r *subscriptionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state subscriptionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	desired := expandSubscription(ctx, plan, &resp.Diagnostics)
	current := expandSubscription(ctx, state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	packageName, productID := desired.PackageName, desired.ProductId

	// Refuse what the API cannot do before changing anything.
	states := map[string]string{}
	var removed []string
	for _, id := range sortedKeys(state.BasePlans) {
		priorState := state.BasePlans[id].State.ValueString()
		states[id] = priorState

		planned, kept := plan.BasePlans[id]
		if !kept {
			if priorState != stateDraft {
				resp.Diagnostics.AddAttributeError(path.Root("base_plans"), "Base plan cannot be deleted",
					fmt.Sprintf("Base plan %q is %s. The API can delete a base plan only while it is a draft. "+
						"Keep it in the configuration with state = \"INACTIVE\" to retire it.", id, priorState))
			}
			removed = append(removed, id)

			continue
		}
		if _, _, err := stateTransition(priorState, planned.State.ValueString()); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("base_plans").AtMapKey(id).AtName("state"),
				"Invalid base plan state change", fmt.Sprintf("Base plan %q: %s.", id, err))
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}

	for _, id := range removed {
		err := r.client.Service.Monetization.Subscriptions.BasePlans.Delete(packageName, productID, id).Context(ctx).Do()
		if err != nil && !play.IsNotFound(err) {
			addAPIError(&resp.Diagnostics, fmt.Sprintf("Unable to delete base plan %q", id), err)

			return
		}
	}

	if mask := subscriptionUpdateMask(current, desired); len(mask) > 0 {
		call := r.client.Service.Monetization.Subscriptions.Patch(packageName, productID, desired).
			UpdateMask(strings.Join(mask, ",")).
			RegionsVersionVersion(plan.RegionsVersion.ValueString())
		if latency := plan.LatencyTolerance.ValueString(); latency != "" {
			call = call.LatencyTolerance(latency)
		}

		patched, err := call.Context(ctx).Do()
		if err != nil {
			addAPIError(&resp.Diagnostics, "Unable to update the subscription", err)

			return
		}
		// A base plan added by the patch starts as a draft.
		for id, apiState := range basePlanStates(patched) {
			states[id] = apiState
		}
	}

	stateErr := r.reconcileBasePlanStates(ctx, plan, states)

	newState, err := r.readInto(ctx, plan, &resp.Diagnostics)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read the subscription after updating it", err)

		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)

	if stateErr != nil {
		addAPIError(&resp.Diagnostics, "Unable to set the state of a base plan", stateErr)
	}
}

func (r *subscriptionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state subscriptionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	packageName, productID := state.PackageName.ValueString(), state.ProductID.ValueString()

	deleteErr := r.client.Service.Monetization.Subscriptions.Delete(packageName, productID).Context(ctx).Do()
	if deleteErr == nil || play.IsNotFound(deleteErr) {
		return
	}

	// "A subscription can only be deleted if it has never had a base plan
	// published." A refusal on those grounds is not something a retry fixes,
	// so retire the subscription instead. Any other failure is an error.
	if code := play.StatusCode(deleteErr); code != http.StatusBadRequest && code != http.StatusConflict && code != http.StatusPreconditionFailed {
		addAPIError(&resp.Diagnostics, "Unable to delete the subscription", deleteErr)

		return
	}

	sub, err := r.client.Service.Monetization.Subscriptions.Get(packageName, productID).Context(ctx).Do()
	if play.IsNotFound(err) {
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to delete the subscription", deleteErr)

		return
	}

	published := false
	for _, plan := range sub.BasePlans {
		if plan.State != stateDraft {
			published = true
		}
	}
	if !published {
		// Nothing was ever published, so the refusal has another cause.
		addAPIError(&resp.Diagnostics, "Unable to delete the subscription", deleteErr)

		return
	}

	for _, plan := range sub.BasePlans {
		if plan.State != stateActive {
			continue
		}
		_, err := r.client.Service.Monetization.Subscriptions.BasePlans.Deactivate(packageName, productID, plan.BasePlanId,
			&androidpublisher.DeactivateBasePlanRequest{
				PackageName:      packageName,
				ProductId:        productID,
				BasePlanId:       plan.BasePlanId,
				LatencyTolerance: state.LatencyTolerance.ValueString(),
			}).Context(ctx).Do()
		if err != nil {
			addAPIError(&resp.Diagnostics, fmt.Sprintf("Unable to deactivate base plan %q", plan.BasePlanId), err)

			return
		}
	}

	resp.Diagnostics.AddWarning("The subscription was not deleted",
		"Google Play deletes a subscription only if none of its base plans was ever activated, and "+productID+
			" of "+packageName+" has had one. Its active base plans have been deactivated, so it is no longer "+
			"sold, and it has been removed from state. It remains in Play Console and its product id stays reserved.\n\n"+
			"The API said: "+play.ErrorDetail(deleteErr))
}

func (r *subscriptionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := splitImportID(req.ID, "package_name", "product_id")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import id", err.Error())

		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("package_name"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("product_id"), parts[1])...)
	// The API does not report the regions version. Start from the default so
	// that a configuration which leaves it out plans no change.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("regions_version"), defaultRegionsVersion)...)
}
