// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/objectvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"google.golang.org/api/androidpublisher/v3"

	"github.com/MathGaps/terraform-provider-googleplay/internal/play"
)

var (
	_ resource.Resource                = &oneTimeProductResource{}
	_ resource.ResourceWithConfigure   = &oneTimeProductResource{}
	_ resource.ResourceWithImportState = &oneTimeProductResource{}
)

const availabilityAvailable = "AVAILABLE"

var (
	purchaseOptionAvailabilities = []string{
		availabilityAvailable,
		"NO_LONGER_AVAILABLE",
		"AVAILABLE_IF_RELEASED",
		"AVAILABLE_FOR_OFFERS_ONLY",
	}
	newRegionsAvailabilities = []string{availabilityAvailable, "NO_LONGER_AVAILABLE"}

	// oneTimeProductFields are the fields a patch can name in its update mask.
	oneTimeProductFields = []string{"listings", "purchaseOptions", "taxAndComplianceSettings", "restrictedPaymentCountries", "offerTags"}
)

// NewOneTimeProductResource returns the googleplay_one_time_product resource.
func NewOneTimeProductResource() resource.Resource {
	return &oneTimeProductResource{}
}

type oneTimeProductResource struct {
	client *play.Client
}

type oneTimeProductModel struct {
	ID                         types.String                          `tfsdk:"id"`
	PackageName                types.String                          `tfsdk:"package_name"`
	ProductID                  types.String                          `tfsdk:"product_id"`
	RegionsVersion             types.String                          `tfsdk:"regions_version"`
	LatencyTolerance           types.String                          `tfsdk:"latency_tolerance"`
	Listings                   map[string]oneTimeProductListingModel `tfsdk:"listings"`
	PurchaseOptions            map[string]purchaseOptionModel        `tfsdk:"purchase_options"`
	OfferTags                  types.Set                             `tfsdk:"offer_tags"`
	TaxAndComplianceSettings   types.Object                          `tfsdk:"tax_and_compliance_settings"`
	RestrictedPaymentCountries types.Set                             `tfsdk:"restricted_payment_countries"`
}

type oneTimeProductListingModel struct {
	Title       types.String `tfsdk:"title"`
	Description types.String `tfsdk:"description"`
}

type oneTimeProductTaxModel struct {
	IsTokenizedDigitalAsset          types.Bool                  `tfsdk:"is_tokenized_digital_asset"`
	ProductTaxCategoryCode           types.String                `tfsdk:"product_tax_category_code"`
	RegionalTaxConfigs               map[string]regionalTaxModel `tfsdk:"regional_tax_configs"`
	ProductAgeRatingTierByRegionCode types.Map                   `tfsdk:"product_age_rating_tier_by_region_code"`
}

type purchaseOptionModel struct {
	State               types.String                                 `tfsdk:"state"`
	Buy                 *buyOptionModel                              `tfsdk:"buy"`
	Rent                *rentOptionModel                             `tfsdk:"rent"`
	OfferTags           types.Set                                    `tfsdk:"offer_tags"`
	RegionalConfigs     map[string]purchaseOptionRegionalConfigModel `tfsdk:"regional_configs"`
	NewRegionsConfig    *newRegionsConfigModel                       `tfsdk:"new_regions_config"`
	WithdrawalRightType types.String                                 `tfsdk:"withdrawal_right_type"`
}

type buyOptionModel struct {
	LegacyCompatible     types.Bool `tfsdk:"legacy_compatible"`
	MultiQuantityEnabled types.Bool `tfsdk:"multi_quantity_enabled"`
}

type rentOptionModel struct {
	RentalPeriod     types.String `tfsdk:"rental_period"`
	ExpirationPeriod types.String `tfsdk:"expiration_period"`
}

type purchaseOptionRegionalConfigModel struct {
	Price        *moneyModel  `tfsdk:"price"`
	Availability types.String `tfsdk:"availability"`
}

type newRegionsConfigModel struct {
	UsdPrice     *moneyModel  `tfsdk:"usd_price"`
	EurPrice     *moneyModel  `tfsdk:"eur_price"`
	Availability types.String `tfsdk:"availability"`
}

func (r *oneTimeProductResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_one_time_product"
}

func oneTimeProductTaxAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Tax and legal compliance settings. Left out, the settings Google Play assigns are " +
			"read back and kept.",
		Optional:      true,
		Computed:      true,
		PlanModifiers: []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
		Attributes: map[string]schema.Attribute{
			"is_tokenized_digital_asset": defaultFalseBool("Whether the product is declared as a product representing a tokenized digital asset."),
			"product_tax_category_code": serverDefaultString(
				"The product tax category code, which decides the transaction tax rates applied.",
			),
			"regional_tax_configs": schema.MapNestedAttribute{
				MarkdownDescription: "Tax configuration by region code.",
				Optional:            true,
				Validators:          []validator.Map{regionKeysValidator()},
				NestedObject:        schema.NestedAttributeObject{Attributes: regionalTaxAttributes()},
			},
			"product_age_rating_tier_by_region_code": productAgeRatingAttribute(),
		},
	}
}

func oneTimeProductTaxAttrTypes() map[string]attr.Type {
	objectType, _ := oneTimeProductTaxAttribute().GetType().(basetypes.ObjectType)

	return objectType.AttrTypes
}

func purchaseOptionAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"state": productStateAttribute("purchase option", "the `purchaseOptions.batchUpdateStates` call"),
		"buy": schema.SingleNestedAttribute{
			MarkdownDescription: "Makes this a purchase option that is bought. Exactly one of `buy` and `rent` must " +
				"be set; write `buy = {}` for a plain purchase.",
			Optional: true,
			Validators: []validator.Object{
				objectvalidator.ExactlyOneOf(path.MatchRelative().AtParent().AtName("buy"), path.MatchRelative().AtParent().AtName("rent")),
			},
			Attributes: map[string]schema.Attribute{
				"legacy_compatible": defaultFalseBool("Whether the purchase option is available to billing flows that " +
					"predate the one-time products model. At most one buy option of a product can be."),
				"multi_quantity_enabled": defaultFalseBool("Whether a buyer can purchase more than one in a single checkout."),
			},
		},
		"rent": schema.SingleNestedAttribute{
			MarkdownDescription: "Makes this a purchase option that is rented.",
			Optional:            true,
			Attributes: map[string]schema.Attribute{
				"rental_period": schema.StringAttribute{
					MarkdownDescription: "How long the user has the entitlement, from the purchase, as an ISO 8601 duration such as `P30D`.",
					Required:            true,
				},
				"expiration_period": schema.StringAttribute{
					MarkdownDescription: "How long the user has, after starting to consume the entitlement, before it is revoked, as an ISO 8601 duration.",
					Optional:            true,
				},
			},
		},
		"offer_tags": offerTagsAttribute("purchase option"),
		"regional_configs": schema.MapNestedAttribute{
			MarkdownDescription: "The price and availability of the purchase option by region code, for example `US`.",
			Optional:            true,
			Validators:          []validator.Map{regionKeysValidator()},
			NestedObject: schema.NestedAttributeObject{
				Attributes: map[string]schema.Attribute{
					"price": moneyAttribute("The price in the region, in the currency Google Play links to the region.", false),
					"availability": schema.StringAttribute{
						MarkdownDescription: "The availability of the purchase option in the region: " +
							oneOfDescription(purchaseOptionAvailabilities...) + ". `NO_LONGER_AVAILABLE` is only " +
							"accepted for a region that was `AVAILABLE` before. Defaults to `AVAILABLE`.",
						Optional:   true,
						Computed:   true,
						Default:    stringdefault.StaticString(availabilityAvailable),
						Validators: []validator.String{stringvalidator.OneOf(purchaseOptionAvailabilities...)},
					},
				},
			},
		},
		"new_regions_config": schema.SingleNestedAttribute{
			MarkdownDescription: "The prices to use in any region Google Play launches in later. Left out, the " +
				"purchase option is not made available in new regions automatically.",
			Optional: true,
			Attributes: map[string]schema.Attribute{
				"usd_price": moneyAttribute("The price in USD for new regions.", true),
				"eur_price": moneyAttribute("The price in EUR for new regions.", true),
				"availability": schema.StringAttribute{
					MarkdownDescription: "Whether the prices are used for new regions: " +
						oneOfDescription(newRegionsAvailabilities...) + ". Defaults to `AVAILABLE`.",
					Optional:   true,
					Computed:   true,
					Default:    stringdefault.StaticString(availabilityAvailable),
					Validators: []validator.String{stringvalidator.OneOf(newRegionsAvailabilities...)},
				},
			},
		},
		"withdrawal_right_type": serverDefaultString(
			"The classification of the product for users in regions with a right of withdrawal: "+
				oneOfDescription(withdrawalRightTypes...)+". The API defaults to `WITHDRAWAL_RIGHT_DIGITAL_CONTENT`.",
			stringvalidator.OneOf(withdrawalRightTypes...),
		),
	}
}

func (r *oneTimeProductResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A one-time product of an app (a product bought or rented once, as opposed to a " +
			"subscription), with its listings, its tax settings and its purchase options and their regional " +
			"prices. It uses the `monetization.onetimeproducts` API, not the deprecated `inappproducts`.\n\n" +
			"Purchase options are part of this resource because the API creates and edits them by updating the " +
			"product. The resource is authoritative for them: one that exists in Play Console and not in the " +
			"configuration is planned for deletion.\n\n" +
			"~> **A product id is reserved forever**, even after the product is deleted.\n\n" +
			"Offers on a purchase option (pre-orders, discounts) are not managed yet. Deleting a purchase " +
			"option that has offers fails until the offers are removed in Play Console.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "`{package_name}/{product_id}`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"package_name": schema.StringAttribute{
				MarkdownDescription: packageNameDescription + " Changing it creates a new product.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          packageNameValidators(),
			},
			"product_id": productIDAttribute("one-time product"),
			"regions_version": regionsVersionAttribute(" The value in state is what the configuration says, " +
				"not the version the API reports the product was last written with."),
			"latency_tolerance": latencyToleranceAttribute(),
			"listings": schema.MapNestedAttribute{
				MarkdownDescription: "The store listing of the product by BCP-47 language code, for example `en-US`.",
				Required:            true,
				Validators:          []validator.Map{mapvalidator.SizeAtLeast(1)},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"title": schema.StringAttribute{
							MarkdownDescription: "The title of the product in this language. At most 55 characters.",
							Required:            true,
							Validators:          []validator.String{stringvalidator.UTF8LengthBetween(1, 55)},
						},
						"description": schema.StringAttribute{
							MarkdownDescription: "The description of the product in this language. At most 200 characters.",
							Required:            true,
							Validators:          []validator.String{stringvalidator.UTF8LengthBetween(1, 200)},
						},
					},
				},
			},
			"purchase_options": schema.MapNestedAttribute{
				MarkdownDescription: "The purchase options of the product by purchase option id: lower-case letters, " +
					"digits and hyphens, at most 63 characters.",
				Required: true,
				Validators: []validator.Map{
					mapvalidator.SizeAtLeast(1),
					mapvalidator.KeysAre(
						stringvalidator.RegexMatches(rfc1034Pattern, "must be lower-case letters, digits and hyphens"),
						stringvalidator.LengthAtMost(63),
					),
				},
				NestedObject: schema.NestedAttributeObject{Attributes: purchaseOptionAttributes()},
			},
			"offer_tags":                   offerTagsAttribute("product"),
			"tax_and_compliance_settings":  oneTimeProductTaxAttribute(),
			"restricted_payment_countries": restrictedPaymentCountriesAttribute(),
		},
	}
}

func (r *oneTimeProductResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

// --- expand: model to API ----------------------------------------------------

// expandOneTimeProduct converts the model into the API's OneTimeProduct, in a
// fixed order and without the state of a purchase option, as
// expandSubscription does.
func expandOneTimeProduct(ctx context.Context, m oneTimeProductModel, diags *diag.Diagnostics) *androidpublisher.OneTimeProduct {
	product := &androidpublisher.OneTimeProduct{
		PackageName:                m.PackageName.ValueString(),
		ProductId:                  m.ProductID.ValueString(),
		OfferTags:                  expandOfferTags(ctx, m.OfferTags, diags),
		RestrictedPaymentCountries: expandRestrictedPaymentCountries(ctx, m.RestrictedPaymentCountries, diags),
		TaxAndComplianceSettings:   expandOneTimeProductTax(ctx, m.TaxAndComplianceSettings, diags),
	}

	for _, language := range sortedKeys(m.Listings) {
		listing := m.Listings[language]
		product.Listings = append(product.Listings, &androidpublisher.OneTimeProductListing{
			LanguageCode: language,
			Title:        listing.Title.ValueString(),
			Description:  listing.Description.ValueString(),
		})
	}

	for _, id := range sortedKeys(m.PurchaseOptions) {
		at := path.Root("purchase_options").AtMapKey(id)
		option := m.PurchaseOptions[id]

		expanded := &androidpublisher.OneTimeProductPurchaseOption{
			PurchaseOptionId: id,
			OfferTags:        expandOfferTags(ctx, option.OfferTags, diags),
		}
		if option.Buy != nil {
			expanded.BuyOption = &androidpublisher.OneTimeProductBuyPurchaseOption{
				LegacyCompatible:     option.Buy.LegacyCompatible.ValueBool(),
				MultiQuantityEnabled: option.Buy.MultiQuantityEnabled.ValueBool(),
			}
		}
		if option.Rent != nil {
			expanded.RentOption = &androidpublisher.OneTimeProductRentPurchaseOption{
				RentalPeriod:     option.Rent.RentalPeriod.ValueString(),
				ExpirationPeriod: option.Rent.ExpirationPeriod.ValueString(),
			}
		}
		if rightType := option.WithdrawalRightType.ValueString(); rightType != "" {
			expanded.TaxAndComplianceSettings = &androidpublisher.PurchaseOptionTaxAndComplianceSettings{WithdrawalRightType: rightType}
		}
		for _, region := range sortedKeys(option.RegionalConfigs) {
			config := option.RegionalConfigs[region]
			expanded.RegionalPricingAndAvailabilityConfigs = append(expanded.RegionalPricingAndAvailabilityConfigs,
				&androidpublisher.OneTimeProductPurchaseOptionRegionalPricingAndAvailabilityConfig{
					RegionCode:   region,
					Price:        expandMoney(config.Price, at.AtName("regional_configs").AtMapKey(region).AtName("price"), diags),
					Availability: config.Availability.ValueString(),
				})
		}
		if other := option.NewRegionsConfig; other != nil {
			otherPath := at.AtName("new_regions_config")
			expanded.NewRegionsConfig = &androidpublisher.OneTimeProductPurchaseOptionNewRegionsConfig{
				UsdPrice:     expandMoney(other.UsdPrice, otherPath.AtName("usd_price"), diags),
				EurPrice:     expandMoney(other.EurPrice, otherPath.AtName("eur_price"), diags),
				Availability: other.Availability.ValueString(),
			}
		}

		product.PurchaseOptions = append(product.PurchaseOptions, expanded)
	}

	return product
}

func expandOneTimeProductTax(ctx context.Context, object types.Object, diags *diag.Diagnostics) *androidpublisher.OneTimeProductTaxAndComplianceSettings {
	if object.IsNull() || object.IsUnknown() {
		return nil
	}

	var m oneTimeProductTaxModel
	diags.Append(object.As(ctx, &m, basetypes.ObjectAsOptions{})...)

	settings := &androidpublisher.OneTimeProductTaxAndComplianceSettings{
		IsTokenizedDigitalAsset:       m.IsTokenizedDigitalAsset.ValueBool(),
		ProductTaxCategoryCode:        m.ProductTaxCategoryCode.ValueString(),
		RegionalProductAgeRatingInfos: expandAgeRatings(ctx, m.ProductAgeRatingTierByRegionCode, diags),
	}
	for _, region := range sortedKeys(m.RegionalTaxConfigs) {
		config := m.RegionalTaxConfigs[region]
		settings.RegionalTaxConfigs = append(settings.RegionalTaxConfigs, &androidpublisher.RegionalTaxConfig{
			RegionCode:                         region,
			EligibleForStreamingServiceTaxRate: config.EligibleForStreamingServiceTaxRate.ValueBool(),
			StreamingTaxType:                   config.StreamingTaxType.ValueString(),
			TaxTier:                            config.TaxTier.ValueString(),
		})
	}

	return settings
}

// --- flatten: API to model ---------------------------------------------------

// flattenOneTimeProduct converts the API's OneTimeProduct into the model, with
// prior playing the part it does in flattenSubscription.
func flattenOneTimeProduct(ctx context.Context, product *androidpublisher.OneTimeProduct, prior oneTimeProductModel, diags *diag.Diagnostics) oneTimeProductModel {
	model := oneTimeProductModel{
		ID:                         types.StringValue(product.PackageName + "/" + product.ProductId),
		PackageName:                types.StringValue(product.PackageName),
		ProductID:                  types.StringValue(product.ProductId),
		RegionsVersion:             prior.RegionsVersion,
		LatencyTolerance:           prior.LatencyTolerance,
		Listings:                   map[string]oneTimeProductListingModel{},
		PurchaseOptions:            map[string]purchaseOptionModel{},
		OfferTags:                  flattenOfferTags(product.OfferTags, prior.OfferTags),
		TaxAndComplianceSettings:   flattenOneTimeProductTax(ctx, product.TaxAndComplianceSettings, prior.TaxAndComplianceSettings, diags),
		RestrictedPaymentCountries: flattenRestrictedPaymentCountries(product.RestrictedPaymentCountries, prior.RestrictedPaymentCountries),
	}

	if model.RegionsVersion.IsNull() || model.RegionsVersion.IsUnknown() {
		model.RegionsVersion = types.StringValue(defaultRegionsVersion)
	}

	for _, listing := range product.Listings {
		model.Listings[listing.LanguageCode] = oneTimeProductListingModel{
			Title:       types.StringValue(listing.Title),
			Description: types.StringValue(listing.Description),
		}
	}

	for _, option := range product.PurchaseOptions {
		priorOption := prior.PurchaseOptions[option.PurchaseOptionId]

		flattened := purchaseOptionModel{
			State:               flattenState(option.State, priorOption.State),
			OfferTags:           flattenOfferTags(option.OfferTags, priorOption.OfferTags),
			WithdrawalRightType: types.StringNull(),
		}
		if option.BuyOption != nil {
			flattened.Buy = &buyOptionModel{
				LegacyCompatible:     types.BoolValue(option.BuyOption.LegacyCompatible),
				MultiQuantityEnabled: types.BoolValue(option.BuyOption.MultiQuantityEnabled),
			}
		}
		if option.RentOption != nil {
			flattened.Rent = &rentOptionModel{
				RentalPeriod:     types.StringValue(option.RentOption.RentalPeriod),
				ExpirationPeriod: stringOrNull(option.RentOption.ExpirationPeriod),
			}
		}
		if option.TaxAndComplianceSettings != nil {
			flattened.WithdrawalRightType = enumOrNull(option.TaxAndComplianceSettings.WithdrawalRightType)
		}

		regional := map[string]purchaseOptionRegionalConfigModel{}
		for _, config := range option.RegionalPricingAndAvailabilityConfigs {
			regional[config.RegionCode] = purchaseOptionRegionalConfigModel{
				Price:        flattenMoney(config.Price),
				Availability: types.StringValue(config.Availability),
			}
		}
		flattened.RegionalConfigs = keepEmptyMap(regional, priorOption.RegionalConfigs)

		if other := option.NewRegionsConfig; other != nil {
			flattened.NewRegionsConfig = &newRegionsConfigModel{
				UsdPrice:     flattenMoney(other.UsdPrice),
				EurPrice:     flattenMoney(other.EurPrice),
				Availability: types.StringValue(other.Availability),
			}
		}

		model.PurchaseOptions[option.PurchaseOptionId] = flattened
	}

	return model
}

func flattenOneTimeProductTax(ctx context.Context, settings *androidpublisher.OneTimeProductTaxAndComplianceSettings, prior types.Object, diags *diag.Diagnostics) types.Object {
	if settings == nil {
		return types.ObjectNull(oneTimeProductTaxAttrTypes())
	}

	var priorModel oneTimeProductTaxModel
	if !prior.IsNull() && !prior.IsUnknown() {
		diags.Append(prior.As(ctx, &priorModel, basetypes.ObjectAsOptions{})...)
	}

	model := oneTimeProductTaxModel{
		IsTokenizedDigitalAsset:          types.BoolValue(settings.IsTokenizedDigitalAsset),
		ProductTaxCategoryCode:           stringOrNull(settings.ProductTaxCategoryCode),
		ProductAgeRatingTierByRegionCode: flattenAgeRatings(settings.RegionalProductAgeRatingInfos, priorModel.ProductAgeRatingTierByRegionCode),
	}

	configs := map[string]regionalTaxModel{}
	for _, config := range settings.RegionalTaxConfigs {
		configs[config.RegionCode] = regionalTaxModel{
			EligibleForStreamingServiceTaxRate: types.BoolValue(config.EligibleForStreamingServiceTaxRate),
			StreamingTaxType:                   enumOrNull(config.StreamingTaxType),
			TaxTier:                            enumOrNull(config.TaxTier),
		}
	}
	model.RegionalTaxConfigs = keepEmptyMap(configs, priorModel.RegionalTaxConfigs)

	object, d := types.ObjectValueFrom(ctx, oneTimeProductTaxAttrTypes(), model)
	diags.Append(d...)

	return object
}

// --- CRUD --------------------------------------------------------------------

func purchaseOptionStates(product *androidpublisher.OneTimeProduct) map[string]string {
	states := map[string]string{}
	for _, option := range product.PurchaseOptions {
		states[option.PurchaseOptionId] = option.State
	}

	return states
}

// patch sends the product with the given update mask.
func (r *oneTimeProductResource) patch(ctx context.Context, plan oneTimeProductModel, product *androidpublisher.OneTimeProduct, mask []string, allowMissing bool) (*androidpublisher.OneTimeProduct, error) {
	call := r.client.Service.Monetization.Onetimeproducts.Patch(product.PackageName, product.ProductId, product).
		UpdateMask(strings.Join(mask, ",")).
		RegionsVersionVersion(plan.RegionsVersion.ValueString())
	if allowMissing {
		call = call.AllowMissing(true)
	}
	if latency := plan.LatencyTolerance.ValueString(); latency != "" {
		call = call.LatencyTolerance(latency)
	}

	return call.Context(ctx).Do()
}

// reconcilePurchaseOptionStates brings every purchase option with a configured
// state into it: one batch of activations, then one of deactivations, since a
// draft that should end up inactive needs both.
func (r *oneTimeProductResource) reconcilePurchaseOptionStates(ctx context.Context, plan oneTimeProductModel, current map[string]string) error {
	packageName, productID := plan.PackageName.ValueString(), plan.ProductID.ValueString()
	latency := plan.LatencyTolerance.ValueString()

	var activations, deactivations []*androidpublisher.UpdatePurchaseOptionStateRequest
	for _, id := range sortedKeys(plan.PurchaseOptions) {
		activate, deactivate, err := stateTransition(current[id], plan.PurchaseOptions[id].State.ValueString())
		if err != nil {
			return fmt.Errorf("purchase option %q: %w", id, err)
		}

		if activate {
			activations = append(activations, &androidpublisher.UpdatePurchaseOptionStateRequest{
				ActivatePurchaseOptionRequest: &androidpublisher.ActivatePurchaseOptionRequest{
					PackageName:      packageName,
					ProductId:        productID,
					PurchaseOptionId: id,
					LatencyTolerance: latency,
				},
			})
		}
		if deactivate {
			deactivations = append(deactivations, &androidpublisher.UpdatePurchaseOptionStateRequest{
				DeactivatePurchaseOptionRequest: &androidpublisher.DeactivatePurchaseOptionRequest{
					PackageName:      packageName,
					ProductId:        productID,
					PurchaseOptionId: id,
					LatencyTolerance: latency,
				},
			})
		}
	}

	for _, batch := range [][]*androidpublisher.UpdatePurchaseOptionStateRequest{activations, deactivations} {
		if len(batch) == 0 {
			continue
		}
		_, err := r.client.Service.Monetization.Onetimeproducts.PurchaseOptions.BatchUpdateStates(packageName, productID,
			&androidpublisher.BatchUpdatePurchaseOptionStatesRequest{Requests: batch}).Context(ctx).Do()
		if err != nil {
			return err
		}
	}

	return nil
}

func (r *oneTimeProductResource) readInto(ctx context.Context, prior oneTimeProductModel, diags *diag.Diagnostics) (oneTimeProductModel, error) {
	product, err := r.client.Service.Monetization.Onetimeproducts.
		Get(prior.PackageName.ValueString(), prior.ProductID.ValueString()).Context(ctx).Do()
	if err != nil {
		return oneTimeProductModel{}, err
	}

	product.PackageName, product.ProductId = prior.PackageName.ValueString(), prior.ProductID.ValueString()

	return flattenOneTimeProduct(ctx, product, prior, diags), nil
}

func (r *oneTimeProductResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan oneTimeProductModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	desired := expandOneTimeProduct(ctx, plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// The API has no create call for a one-time product: a patch with
	// allowMissing creates it, and would as readily overwrite one that exists.
	// Look first, so that creating never silently adopts a product.
	_, err := r.client.Service.Monetization.Onetimeproducts.Get(desired.PackageName, desired.ProductId).Context(ctx).Do()
	switch {
	case err == nil:
		resp.Diagnostics.AddError("One-time product already exists",
			"The app "+desired.PackageName+" already has a one-time product with the id "+desired.ProductId+
				". Import it to manage it: "+desired.PackageName+"/"+desired.ProductId)

		return
	case !play.IsNotFound(err):
		addAPIError(&resp.Diagnostics, "Unable to check whether the one-time product exists", err)

		return
	}

	created, err := r.patch(ctx, plan, desired, oneTimeProductFields, true)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create the one-time product", err)

		return
	}

	// As for a subscription, state is written even when a later step fails.
	stateErr := r.reconcilePurchaseOptionStates(ctx, plan, purchaseOptionStates(created))

	state, err := r.readInto(ctx, plan, &resp.Diagnostics)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read the one-time product after creating it", err)

		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)

	if stateErr != nil {
		addAPIError(&resp.Diagnostics, "Unable to set the state of a purchase option", stateErr)
	}
}

func (r *oneTimeProductResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state oneTimeProductModel
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
		addAPIError(&resp.Diagnostics, "Unable to read the one-time product", err)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// oneTimeProductUpdateMask lists the fields of the product that differ.
func oneTimeProductUpdateMask(current, desired *androidpublisher.OneTimeProduct) []string {
	var mask []string
	if !reflect.DeepEqual(current.Listings, desired.Listings) {
		mask = append(mask, "listings")
	}
	if !reflect.DeepEqual(current.PurchaseOptions, desired.PurchaseOptions) {
		mask = append(mask, "purchaseOptions")
	}
	if !reflect.DeepEqual(current.TaxAndComplianceSettings, desired.TaxAndComplianceSettings) {
		mask = append(mask, "taxAndComplianceSettings")
	}
	if !reflect.DeepEqual(current.RestrictedPaymentCountries, desired.RestrictedPaymentCountries) {
		mask = append(mask, "restrictedPaymentCountries")
	}
	if !reflect.DeepEqual(current.OfferTags, desired.OfferTags) {
		mask = append(mask, "offerTags")
	}

	return mask
}

func (r *oneTimeProductResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state oneTimeProductModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	desired := expandOneTimeProduct(ctx, plan, &resp.Diagnostics)
	current := expandOneTimeProduct(ctx, state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	packageName, productID := desired.PackageName, desired.ProductId

	states := map[string]string{}
	var removed []string
	for _, id := range sortedKeys(state.PurchaseOptions) {
		priorState := state.PurchaseOptions[id].State.ValueString()
		states[id] = priorState

		planned, kept := plan.PurchaseOptions[id]
		if !kept {
			removed = append(removed, id)

			continue
		}
		if _, _, err := stateTransition(priorState, planned.State.ValueString()); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("purchase_options").AtMapKey(id).AtName("state"),
				"Invalid purchase option state change", fmt.Sprintf("Purchase option %q: %s.", id, err))
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}

	// "All requests must delete purchase options from different one-time
	// products", so each deletion is a batch of one.
	for _, id := range removed {
		err := r.client.Service.Monetization.Onetimeproducts.PurchaseOptions.BatchDelete(packageName, productID,
			&androidpublisher.BatchDeletePurchaseOptionsRequest{
				Requests: []*androidpublisher.DeletePurchaseOptionRequest{{
					PackageName:      packageName,
					ProductId:        productID,
					PurchaseOptionId: id,
					LatencyTolerance: plan.LatencyTolerance.ValueString(),
				}},
			}).Context(ctx).Do()
		if err != nil && !play.IsNotFound(err) {
			addAPIError(&resp.Diagnostics, fmt.Sprintf("Unable to delete purchase option %q", id), err)

			return
		}
	}

	if mask := oneTimeProductUpdateMask(current, desired); len(mask) > 0 {
		patched, err := r.patch(ctx, plan, desired, mask, false)
		if err != nil {
			addAPIError(&resp.Diagnostics, "Unable to update the one-time product", err)

			return
		}
		for id, apiState := range purchaseOptionStates(patched) {
			states[id] = apiState
		}
	}

	stateErr := r.reconcilePurchaseOptionStates(ctx, plan, states)

	newState, err := r.readInto(ctx, plan, &resp.Diagnostics)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read the one-time product after updating it", err)

		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)

	if stateErr != nil {
		addAPIError(&resp.Diagnostics, "Unable to set the state of a purchase option", stateErr)
	}
}

func (r *oneTimeProductResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state oneTimeProductModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	call := r.client.Service.Monetization.Onetimeproducts.Delete(state.PackageName.ValueString(), state.ProductID.ValueString())
	if latency := state.LatencyTolerance.ValueString(); latency != "" {
		call = call.LatencyTolerance(latency)
	}

	if err := call.Context(ctx).Do(); err != nil && !play.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete the one-time product", err)
	}
}

func (r *oneTimeProductResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := splitImportID(req.ID, "package_name", "product_id")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import id", err.Error())

		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("package_name"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("product_id"), parts[1])...)
	// See the subscription resource: start from the default regions version.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("regions_version"), defaultRegionsVersion)...)
}
