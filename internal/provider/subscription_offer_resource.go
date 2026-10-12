// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/float64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/objectvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/api/androidpublisher/v3"

	"github.com/MathGaps/terraform-provider-googleplay/internal/play"
)

var (
	_ resource.Resource                   = &subscriptionOfferResource{}
	_ resource.ResourceWithConfigure      = &subscriptionOfferResource{}
	_ resource.ResourceWithImportState    = &subscriptionOfferResource{}
	_ resource.ResourceWithValidateConfig = &subscriptionOfferResource{}
)

// offerIDPattern is one segment of the import id. The API documents no other
// rule for an offer id than that it is unique within the base plan, so the
// rest is left to it.
var offerIDPattern = regexp.MustCompile(`^[^/\s]+$`)

// NewSubscriptionOfferResource returns the googleplay_subscription_offer
// resource.
func NewSubscriptionOfferResource() resource.Resource {
	return &subscriptionOfferResource{}
}

type subscriptionOfferResource struct {
	client *play.Client
}

func (r *subscriptionOfferResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_subscription_offer"
}

// emptyAttribute is an API message with no fields, written `{}`.
func emptyAttribute(description string) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: description,
		Optional:            true,
		Attributes:          map[string]schema.Attribute{},
	}
}

func relativeDiscountAttribute() schema.Float64Attribute {
	return schema.Float64Attribute{
		MarkdownDescription: "The fraction of the base plan price, prorated over the phase duration, that the user " +
			"pays, strictly between 0 and 1: `0.5` is half price. With a base plan of 12.00 a year, `0.5` for a " +
			"three-month phase is 1.50. The result is rounded to the nearest billable unit, and must not fall " +
			"below the minimum price of the region.",
		Optional: true,
		Validators: []validator.Float64{
			float64validator.Between(0, 1),
			float64validator.NoneOf(0, 1),
		},
	}
}

// phasePriceAttributes are the four ways to price a phase, exactly one of
// which must be set. price and absolute_discount are built by the caller,
// since a region takes one amount and new regions take two.
func phasePriceAttributes(price, absoluteDiscount schema.SingleNestedAttribute) map[string]schema.Attribute {
	price.Validators = []validator.Object{
		objectvalidator.ExactlyOneOf(
			path.MatchRelative().AtParent().AtName("price"),
			path.MatchRelative().AtParent().AtName("absolute_discount"),
			path.MatchRelative().AtParent().AtName("relative_discount"),
			path.MatchRelative().AtParent().AtName("free"),
		),
	}

	return map[string]schema.Attribute{
		"price":             price,
		"absolute_discount": absoluteDiscount,
		"relative_discount": relativeDiscountAttribute(),
		"free":              emptyAttribute("Makes the phase free: write `free = {}`. A free first phase is a free trial."),
	}
}

func otherRegionsPricesAttribute(description string) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: description,
		Optional:            true,
		Attributes: map[string]schema.Attribute{
			"usd_price": moneyAttribute("The amount in USD.", true),
			"eur_price": moneyAttribute("The amount in EUR.", true),
		},
	}
}

func offerPhaseAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"duration": schema.StringAttribute{
			MarkdownDescription: "The duration of one recurrence of the phase, as an ISO 8601 duration such as `P1W` or `P1M`.",
			Required:            true,
			Validators:          durationValidators(),
		},
		"recurrence_count": schema.Int64Attribute{
			MarkdownDescription: "How many times the phase repeats. Unless the phase is free, each recurrence charges its price.",
			Required:            true,
			Validators:          []validator.Int64{int64validator.AtLeast(1)},
		},
		"regional_configs": schema.MapNestedAttribute{
			MarkdownDescription: "What the phase costs by region code. It must have exactly one entry for each region " +
				"in the offer's `regional_configs`. Set exactly one of `price`, `absolute_discount`, " +
				"`relative_discount` and `free`.",
			Required:   true,
			Validators: []validator.Map{regionKeysValidator(), mapvalidator.SizeAtLeast(1)},
			NestedObject: schema.NestedAttributeObject{
				Attributes: phasePriceAttributes(
					moneyAttribute("The price the user pays for the phase, in the currency Google Play links to the "+
						"region. It must not be below the minimum price of the region.", false),
					moneyAttribute("The amount subtracted from the base plan price, prorated over the phase duration. "+
						"With a base plan of 12.00 a year, a discount of 1.00 on a three-month phase makes it 2.00.", false),
				),
			},
		},
		"other_regions_config": schema.SingleNestedAttribute{
			MarkdownDescription: "What the phase costs in any region Google Play launches in later. Set exactly one " +
				"of `price`, `absolute_discount`, `relative_discount` and `free`.",
			Optional: true,
			Attributes: phasePriceAttributes(
				otherRegionsPricesAttribute("The price the user pays for the phase in new regions."),
				otherRegionsPricesAttribute("The amount subtracted from the base plan price in new regions, prorated over the phase duration."),
			),
		},
	}
}

func offerTargetingAttribute() schema.SingleNestedAttribute {
	thisSubscription := emptyAttribute("The subscription this offer belongs to: write `this_subscription = {}`.")

	return schema.SingleNestedAttribute{
		MarkdownDescription: "Who Google Play offers this to. Set exactly one of `acquisition_rule` and " +
			"`upgrade_rule`. Left out, Google Play applies no rule: the offer is developer determined, and the " +
			"app decides whom to show it to.",
		Optional: true,
		Attributes: map[string]schema.Attribute{
			"acquisition_rule": schema.SingleNestedAttribute{
				MarkdownDescription: "Offers it to new customers: users who have never subscribed within the scope.",
				Optional:            true,
				Validators: []validator.Object{
					objectvalidator.ExactlyOneOf(
						path.MatchRelative().AtParent().AtName("acquisition_rule"),
						path.MatchRelative().AtParent().AtName("upgrade_rule"),
					),
				},
				Attributes: map[string]schema.Attribute{
					"scope": schema.SingleNestedAttribute{
						MarkdownDescription: "The subscriptions the user must never have had. Set exactly one of " +
							"`this_subscription` and `any_subscription_in_app`.",
						Required: true,
						Attributes: map[string]schema.Attribute{
							"this_subscription": func() schema.SingleNestedAttribute {
								a := thisSubscription
								a.Validators = []validator.Object{objectvalidator.ExactlyOneOf(
									path.MatchRelative().AtParent().AtName("this_subscription"),
									path.MatchRelative().AtParent().AtName("any_subscription_in_app"),
								)}

								return a
							}(),
							"any_subscription_in_app": emptyAttribute("Any subscription of the app: write `any_subscription_in_app = {}`."),
						},
					},
				},
			},
			"upgrade_rule": schema.SingleNestedAttribute{
				MarkdownDescription: "Offers it to users who currently hold a subscription, to upgrade from it.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"scope": schema.SingleNestedAttribute{
						MarkdownDescription: "The subscription the user must currently hold. Set exactly one of " +
							"`this_subscription` and `specific_subscription_in_app`.",
						Required: true,
						Attributes: map[string]schema.Attribute{
							"this_subscription": thisSubscription,
							"specific_subscription_in_app": schema.StringAttribute{
								MarkdownDescription: "The product id of another subscription of the same app.",
								Optional:            true,
								Validators: []validator.String{
									stringvalidator.ExactlyOneOf(
										path.MatchRelative().AtParent().AtName("this_subscription"),
										path.MatchRelative().AtParent().AtName("specific_subscription_in_app"),
									),
									stringvalidator.RegexMatches(productIDPattern, "must be a product id"),
								},
							},
						},
					},
					"billing_period_duration": schema.StringAttribute{
						MarkdownDescription: "The billing period, as an ISO 8601 duration, the user must currently be " +
							"subscribed to. Left out, any billing period matches.",
						Optional:   true,
						Validators: durationValidators(),
					},
					"once_per_user": defaultFalseBool("Whether a user who has ever had this offer can never have it again."),
				},
			},
		},
	}
}

func (r *subscriptionOfferResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	idAttribute := func(description string, validators ...validator.String) schema.StringAttribute {
		return schema.StringAttribute{
			MarkdownDescription: description + " Changing it creates a new offer.",
			Required:            true,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			Validators:          validators,
		}
	}

	state := productStateAttribute("offer", "the `offers.activate` and `offers.deactivate` calls")
	state.MarkdownDescription += " The state of the base plan comes first: an active offer is not available " +
		"while its base plan is not active."
	state.PlanModifiers = []planmodifier.String{stringplanmodifier.UseStateForUnknown()}

	resp.Schema = schema.Schema{
		MarkdownDescription: "A subscription offer: a free trial, an introductory price or a discount on one " +
			"base plan of a subscription, for the users its targeting picks.\n\n" +
			"An offer has its own create and update calls, so unlike a base plan it is a resource of its own. " +
			"Only auto-renewing base plans can have offers. An offer runs through one or two phases, in order, " +
			"before the user pays the base plan price: a free trial, a single payment, or a number of " +
			"discounted recurring payments.\n\n" +
			"~> **What Google Play does not let you undo.** An offer can be deleted only while it is a draft. " +
			"Destroying one that has been activated deactivates it, removes it from state with a warning, and " +
			"leaves it in Play Console. Play Console's help says an offer id cannot be reused, even after the " +
			"offer is deleted.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "`{package_name}/{product_id}/{base_plan_id}/{offer_id}`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"package_name": idAttribute(packageNameDescription, packageNameValidators()...),
			"product_id": idAttribute("The product id of the subscription the offer belongs to.",
				stringvalidator.RegexMatches(productIDPattern, "must be a product id")),
			"base_plan_id": idAttribute("The id of the base plan the offer belongs to. It must be an auto-renewing base plan.",
				stringvalidator.RegexMatches(rfc1034Pattern, "must be lower-case letters, digits and hyphens"),
				stringvalidator.LengthAtMost(63)),
			"offer_id": idAttribute("The id of the offer, unique within the base plan. Play Console's help says an "+
				"offer id cannot be reused, even after the offer is deleted.",
				stringvalidator.RegexMatches(offerIDPattern, "must not contain slashes or spaces")),
			"regions_version": regionsVersionAttribute(" The API does not report which version an offer was last " +
				"written with, so the value is only what the configuration says."),
			"latency_tolerance": latencyToleranceAttribute(),
			"state":             state,
			"phases": schema.ListNestedAttribute{
				MarkdownDescription: "The one or two phases of the offer, in the order the user goes through them. " +
					"After the last one the user pays the base plan price.",
				Required:     true,
				Validators:   []validator.List{listvalidator.SizeBetween(1, 2)},
				NestedObject: schema.NestedAttributeObject{Attributes: offerPhaseAttributes()},
			},
			"regional_configs": schema.MapNestedAttribute{
				MarkdownDescription: "The regions the offer is set up in, by region code, for example `US`. Every " +
					"phase must price each of them.",
				Required:   true,
				Validators: []validator.Map{regionKeysValidator(), mapvalidator.SizeAtLeast(1)},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"new_subscriber_availability": defaultFalseBool("Whether new subscribers in the region can get the offer. " +
							"Existing subscribers keep it either way."),
					},
				},
			},
			"other_regions_config": schema.SingleNestedAttribute{
				MarkdownDescription: "Whether the offer is available in regions Google Play launches in later. A phase " +
					"prices them with its own `other_regions_config`.",
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"new_subscriber_availability": defaultFalseBool("Whether new subscribers in new regions can get the offer."),
				},
			},
			"targeting":  offerTargetingAttribute(),
			"offer_tags": offerTagsAttribute("offer"),
		},
	}
}

func (r *subscriptionOfferResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

// ValidateConfig checks the rule the schema cannot express: each phase prices
// exactly the regions of the offer.
func (r *subscriptionOfferResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var regions types.Map
	var phases types.List
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("regional_configs"), &regions)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("phases"), &phases)...)
	if resp.Diagnostics.HasError() || regions.IsNull() || regions.IsUnknown() || phases.IsNull() || phases.IsUnknown() {
		return
	}

	want := sortedKeys(regions.Elements())
	for i := range phases.Elements() {
		at := path.Root("phases").AtListIndex(i).AtName("regional_configs")

		var phaseRegions types.Map
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, at, &phaseRegions)...)
		if phaseRegions.IsNull() || phaseRegions.IsUnknown() {
			continue
		}

		if got := sortedKeys(phaseRegions.Elements()); !slices.Equal(got, want) {
			resp.Diagnostics.AddAttributeError(at, "Phase does not price the offer's regions",
				fmt.Sprintf("Each phase must have exactly one regional config for each region of the offer. The offer "+
					"is set up in %s and this phase prices %s.", strings.Join(want, ", "), strings.Join(got, ", ")))
		}
	}
}

// --- CRUD --------------------------------------------------------------------

// reconcileState activates or deactivates the offer until it is in the
// configured state.
func (r *subscriptionOfferResource) reconcileState(ctx context.Context, plan subscriptionOfferModel, current string) error {
	activate, deactivate, err := stateTransition(current, plan.State.ValueString())
	if err != nil {
		return err
	}

	packageName, productID := plan.PackageName.ValueString(), plan.ProductID.ValueString()
	basePlanID, offerID := plan.BasePlanID.ValueString(), plan.OfferID.ValueString()
	latency := plan.LatencyTolerance.ValueString()

	if activate {
		_, err := r.client.Service.Monetization.Subscriptions.BasePlans.Offers.Activate(packageName, productID, basePlanID, offerID,
			&androidpublisher.ActivateSubscriptionOfferRequest{
				PackageName:      packageName,
				ProductId:        productID,
				BasePlanId:       basePlanID,
				OfferId:          offerID,
				LatencyTolerance: latency,
			}).Context(ctx).Do()
		if err != nil {
			return fmt.Errorf("activating the offer: %w", err)
		}
	}
	if deactivate {
		_, err := r.client.Service.Monetization.Subscriptions.BasePlans.Offers.Deactivate(packageName, productID, basePlanID, offerID,
			&androidpublisher.DeactivateSubscriptionOfferRequest{
				PackageName:      packageName,
				ProductId:        productID,
				BasePlanId:       basePlanID,
				OfferId:          offerID,
				LatencyTolerance: latency,
			}).Context(ctx).Do()
		if err != nil {
			return fmt.Errorf("deactivating the offer: %w", err)
		}
	}

	return nil
}

// readInto gets the offer and writes it into the model.
func (r *subscriptionOfferResource) readInto(ctx context.Context, prior subscriptionOfferModel) (subscriptionOfferModel, error) {
	offer, err := r.client.Service.Monetization.Subscriptions.BasePlans.Offers.Get(prior.PackageName.ValueString(),
		prior.ProductID.ValueString(), prior.BasePlanID.ValueString(), prior.OfferID.ValueString()).Context(ctx).Do()
	if err != nil {
		return subscriptionOfferModel{}, err
	}

	// The ids come from the request path, whatever the body says.
	offer.PackageName, offer.ProductId = prior.PackageName.ValueString(), prior.ProductID.ValueString()
	offer.BasePlanId, offer.OfferId = prior.BasePlanID.ValueString(), prior.OfferID.ValueString()

	return flattenSubscriptionOffer(offer, prior), nil
}

func (r *subscriptionOfferResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan subscriptionOfferModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	desired := expandSubscriptionOffer(ctx, plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// offers.create takes no latency tolerance.
	created, err := r.client.Service.Monetization.Subscriptions.BasePlans.Offers.
		Create(desired.PackageName, desired.ProductId, desired.BasePlanId, desired).
		OfferId(desired.OfferId).
		RegionsVersionVersion(plan.RegionsVersion.ValueString()).
		Context(ctx).Do()
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create the subscription offer", err)

		return
	}

	// As for a subscription, state is written even when a later step fails.
	stateErr := r.reconcileState(ctx, plan, created.State)

	state, err := r.readInto(ctx, plan)
	if err != nil {
		// The offer id is taken for good, so the offer goes into state from the
		// create response; the next refresh reads the rest.
		created.PackageName, created.ProductId = desired.PackageName, desired.ProductId
		created.BasePlanId, created.OfferId = desired.BasePlanId, desired.OfferId
		state = flattenSubscriptionOffer(created, plan)
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		addCreatedWarning(&resp.Diagnostics, "Unable to read the subscription offer after creating it", err)

		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)

	if stateErr != nil {
		addCreatedWarning(&resp.Diagnostics, "Unable to set the state of the subscription offer", stateErr)
	}
}

func (r *subscriptionOfferResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state subscriptionOfferModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState, err := r.readInto(ctx, state)
	if play.IsNotFound(err) {
		resp.State.RemoveResource(ctx)

		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read the subscription offer", err)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// subscriptionOfferUpdateMask lists the fields of the offer that differ.
func subscriptionOfferUpdateMask(current, desired *androidpublisher.SubscriptionOffer) []string {
	var mask []string
	if !reflect.DeepEqual(current.Phases, desired.Phases) {
		mask = append(mask, "phases")
	}
	if !reflect.DeepEqual(current.RegionalConfigs, desired.RegionalConfigs) {
		mask = append(mask, "regionalConfigs")
	}
	if !reflect.DeepEqual(current.OtherRegionsConfig, desired.OtherRegionsConfig) {
		mask = append(mask, "otherRegionsConfig")
	}
	if !reflect.DeepEqual(current.Targeting, desired.Targeting) {
		mask = append(mask, "targeting")
	}
	if !reflect.DeepEqual(current.OfferTags, desired.OfferTags) {
		mask = append(mask, "offerTags")
	}

	return mask
}

func (r *subscriptionOfferResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state subscriptionOfferModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	desired := expandSubscriptionOffer(ctx, plan, &resp.Diagnostics)
	current := expandSubscriptionOffer(ctx, state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// Refuse what the API cannot do before changing anything.
	currentState := state.State.ValueString()
	if _, _, err := stateTransition(currentState, plan.State.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("state"), "Invalid offer state change", "The offer: "+err.Error()+".")

		return
	}

	if mask := subscriptionOfferUpdateMask(current, desired); len(mask) > 0 {
		call := r.client.Service.Monetization.Subscriptions.BasePlans.Offers.
			Patch(desired.PackageName, desired.ProductId, desired.BasePlanId, desired.OfferId, desired).
			UpdateMask(strings.Join(mask, ",")).
			RegionsVersionVersion(plan.RegionsVersion.ValueString())
		if latency := plan.LatencyTolerance.ValueString(); latency != "" {
			call = call.LatencyTolerance(latency)
		}

		patched, err := call.Context(ctx).Do()
		if err != nil {
			addAPIError(&resp.Diagnostics, "Unable to update the subscription offer", err)

			return
		}
		currentState = patched.State
	}

	stateErr := r.reconcileState(ctx, plan, currentState)

	newState, err := r.readInto(ctx, plan)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read the subscription offer after updating it", err)

		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)

	if stateErr != nil {
		addAPIError(&resp.Diagnostics, "Unable to set the state of the subscription offer", stateErr)
	}
}

func (r *subscriptionOfferResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state subscriptionOfferModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	packageName, productID := state.PackageName.ValueString(), state.ProductID.ValueString()
	basePlanID, offerID := state.BasePlanID.ValueString(), state.OfferID.ValueString()

	deleteErr := r.client.Service.Monetization.Subscriptions.BasePlans.Offers.
		Delete(packageName, productID, basePlanID, offerID).Context(ctx).Do()
	if deleteErr == nil || play.IsNotFound(deleteErr) {
		return
	}

	// "Can only be done for draft offers." A refusal on those grounds is not
	// something a retry fixes, so retire the offer instead, as a subscription
	// that cannot be deleted is retired. Any other failure is an error.
	if code := play.StatusCode(deleteErr); code != http.StatusBadRequest && code != http.StatusConflict && code != http.StatusPreconditionFailed {
		addAPIError(&resp.Diagnostics, "Unable to delete the subscription offer", deleteErr)

		return
	}

	offer, err := r.client.Service.Monetization.Subscriptions.BasePlans.Offers.
		Get(packageName, productID, basePlanID, offerID).Context(ctx).Do()
	if play.IsNotFound(err) {
		return
	}
	if err != nil || offer.State == stateDraft {
		// A draft can be deleted, so the refusal has another cause.
		addAPIError(&resp.Diagnostics, "Unable to delete the subscription offer", deleteErr)

		return
	}

	if offer.State == stateActive {
		_, err := r.client.Service.Monetization.Subscriptions.BasePlans.Offers.Deactivate(packageName, productID, basePlanID, offerID,
			&androidpublisher.DeactivateSubscriptionOfferRequest{
				PackageName:      packageName,
				ProductId:        productID,
				BasePlanId:       basePlanID,
				OfferId:          offerID,
				LatencyTolerance: state.LatencyTolerance.ValueString(),
			}).Context(ctx).Do()
		if err != nil {
			addAPIError(&resp.Diagnostics, "Unable to deactivate the subscription offer", err)

			return
		}
	}

	resp.Diagnostics.AddWarning("The subscription offer was not deleted",
		"Google Play deletes an offer only while it is a draft, and "+offerID+" on base plan "+basePlanID+" of "+
			productID+" has been activated. It has been deactivated, so new subscribers can no longer get it, and "+
			"it has been removed from state. It remains in Play Console, and existing subscribers keep it.\n\n"+
			"The API said: "+play.ErrorDetail(deleteErr))
}

func (r *subscriptionOfferResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := splitImportID(req.ID, "package_name", "product_id", "base_plan_id", "offer_id")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import id", err.Error())

		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("package_name"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("product_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("base_plan_id"), parts[2])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("offer_id"), parts[3])...)
	// See the subscription resource: start from the default regions version.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("regions_version"), defaultRegionsVersion)...)
}
