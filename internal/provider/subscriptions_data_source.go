// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/api/androidpublisher/v3"

	"github.com/MathGaps/terraform-provider-googleplay/internal/play"
)

var (
	_ datasource.DataSource              = &subscriptionsDataSource{}
	_ datasource.DataSourceWithConfigure = &subscriptionsDataSource{}
)

// listPageSize is the largest page the monetization list calls return. The
// client documents it as "The maximum value is 1000; values above 1000 will be
// coerced to 1000", and the service may return fewer.
const listPageSize = 1000

// NewSubscriptionsDataSource returns the googleplay_subscriptions data source.
func NewSubscriptionsDataSource() datasource.DataSource {
	return &subscriptionsDataSource{}
}

type subscriptionsDataSource struct {
	client *play.Client
}

type subscriptionsDataSourceModel struct {
	PackageName   types.String                    `tfsdk:"package_name"`
	ProductIDs    types.Set                       `tfsdk:"product_ids"`
	Subscriptions []subscriptionsDataProductModel `tfsdk:"subscriptions"`
}

type subscriptionsDataProductModel struct {
	ProductID types.String                     `tfsdk:"product_id"`
	Titles    types.Map                        `tfsdk:"titles"`
	BasePlans []subscriptionsDataBasePlanModel `tfsdk:"base_plans"`
}

type subscriptionsDataBasePlanModel struct {
	BasePlanID            types.String                  `tfsdk:"base_plan_id"`
	State                 types.String                  `tfsdk:"state"`
	Type                  types.String                  `tfsdk:"type"`
	BillingPeriodDuration types.String                  `tfsdk:"billing_period_duration"`
	Offers                []subscriptionsDataOfferModel `tfsdk:"offers"`
}

type subscriptionsDataOfferModel struct {
	OfferID types.String `tfsdk:"offer_id"`
	State   types.String `tfsdk:"state"`
}

func (d *subscriptionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_subscriptions"
}

func (d *subscriptionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Every subscription of an app, with its base plans and the ids of their offers. Use " +
			"it to find the product, base plan and offer ids a configuration refers to, and to write import " +
			"blocks for what already exists: a subscription imports as `{package_name}/{product_id}`.\n\n" +
			"A read lists the subscriptions, a page of up to 1,000 per call, and then every offer of the app " +
			"in a single list across all its subscriptions, again up to 1,000 per call. It reads no offer " +
			"one by one.",
		Attributes: map[string]schema.Attribute{
			"package_name": schema.StringAttribute{
				MarkdownDescription: packageNameDescription,
				Required:            true,
				Validators:          packageNameValidators(),
			},
			"product_ids": schema.SetAttribute{
				MarkdownDescription: "The product ids of every subscription of the app.",
				ElementType:         types.StringType,
				Computed:            true,
			},
			"subscriptions": schema.ListNestedAttribute{
				MarkdownDescription: "Every subscription of the app, ordered by product id.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"product_id": schema.StringAttribute{
							MarkdownDescription: "The product id of the subscription.",
							Computed:            true,
						},
						"titles": schema.MapAttribute{
							MarkdownDescription: "The title of the subscription by BCP-47 language code.",
							ElementType:         types.StringType,
							Computed:            true,
						},
						"base_plans": schema.ListNestedAttribute{
							MarkdownDescription: "The base plans of the subscription, ordered by base plan id.",
							Computed:            true,
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"base_plan_id": schema.StringAttribute{
										MarkdownDescription: "The id of the base plan.",
										Computed:            true,
									},
									"state": schema.StringAttribute{
										MarkdownDescription: "The state of the base plan: `DRAFT`, `ACTIVE`, `INACTIVE` or `INACTIVE_PUBLISHED`.",
										Computed:            true,
									},
									"type": schema.StringAttribute{
										MarkdownDescription: "The kind of base plan, named as in `googleplay_subscription`: " +
											"`auto_renewing`, `prepaid` or `installments`. Only an auto-renewing base plan can have offers.",
										Computed: true,
									},
									"billing_period_duration": schema.StringAttribute{
										MarkdownDescription: "The billing period as an ISO 8601 duration, for example `P1M`.",
										Computed:            true,
									},
									"offers": schema.ListNestedAttribute{
										MarkdownDescription: "The offers of the base plan, ordered by offer id. An offer imports " +
											"as `{package_name}/{product_id}/{base_plan_id}/{offer_id}`.",
										Computed: true,
										NestedObject: schema.NestedAttributeObject{
											Attributes: map[string]schema.Attribute{
												"offer_id": schema.StringAttribute{
													MarkdownDescription: "The id of the offer.",
													Computed:            true,
												},
												"state": schema.StringAttribute{
													MarkdownDescription: "The state of the offer: `DRAFT`, `ACTIVE` or `INACTIVE`.",
													Computed:            true,
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func (d *subscriptionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

// listOfferStates returns the id and state of every offer of the app, keyed by
// product id and base plan id. "-" for both reads "all offers under ... an
// app" in one list.
func (d *subscriptionsDataSource) listOfferStates(ctx context.Context, packageName string) (map[[2]string][]subscriptionsDataOfferModel, error) {
	offers := map[[2]string][]subscriptionsDataOfferModel{}
	err := d.client.Service.Monetization.Subscriptions.BasePlans.Offers.List(packageName, "-", "-").
		PageSize(listPageSize).
		Pages(ctx, func(page *androidpublisher.ListSubscriptionOffersResponse) error {
			for _, offer := range page.SubscriptionOffers {
				key := [2]string{offer.ProductId, offer.BasePlanId}
				offers[key] = append(offers[key], subscriptionsDataOfferModel{
					OfferID: types.StringValue(offer.OfferId),
					State:   stringOrNull(offer.State),
				})
			}

			return nil
		})
	if err != nil {
		return nil, err
	}

	for _, list := range offers {
		slices.SortFunc(list, func(a, b subscriptionsDataOfferModel) int {
			return strings.Compare(a.OfferID.ValueString(), b.OfferID.ValueString())
		})
	}

	return offers, nil
}

func (d *subscriptionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data subscriptionsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	packageName := data.PackageName.ValueString()

	// showArchived is not sent: the client documents it as "Deprecated:
	// subscription archiving is not supported".
	var subscriptions []*androidpublisher.Subscription
	err := d.client.Service.Monetization.Subscriptions.List(packageName).
		PageSize(listPageSize).
		Pages(ctx, func(page *androidpublisher.ListSubscriptionsResponse) error {
			subscriptions = append(subscriptions, page.Subscriptions...)

			return nil
		})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to list the subscriptions of the app", err)

		return
	}

	offers, err := d.listOfferStates(ctx, packageName)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to list the subscription offers of the app", err)

		return
	}

	slices.SortFunc(subscriptions, func(a, b *androidpublisher.Subscription) int {
		return strings.Compare(a.ProductId, b.ProductId)
	})

	productIDs := make([]string, 0, len(subscriptions))
	data.Subscriptions = make([]subscriptionsDataProductModel, 0, len(subscriptions))
	for _, sub := range subscriptions {
		productIDs = append(productIDs, sub.ProductId)

		titles := map[string]string{}
		for _, listing := range sub.Listings {
			titles[listing.LanguageCode] = listing.Title
		}
		titleMap, diags := types.MapValueFrom(ctx, types.StringType, titles)
		resp.Diagnostics.Append(diags...)

		plans := slices.Clone(sub.BasePlans)
		slices.SortFunc(plans, func(a, b *androidpublisher.BasePlan) int {
			return strings.Compare(a.BasePlanId, b.BasePlanId)
		})

		item := subscriptionsDataProductModel{
			ProductID: types.StringValue(sub.ProductId),
			Titles:    titleMap,
			BasePlans: make([]subscriptionsDataBasePlanModel, 0, len(plans)),
		}
		for _, plan := range plans {
			kind, period := basePlanKind(plan)
			planOffers := offers[[2]string{sub.ProductId, plan.BasePlanId}]
			if planOffers == nil {
				planOffers = []subscriptionsDataOfferModel{}
			}

			item.BasePlans = append(item.BasePlans, subscriptionsDataBasePlanModel{
				BasePlanID:            types.StringValue(plan.BasePlanId),
				State:                 stringOrNull(plan.State),
				Type:                  stringOrNull(kind),
				BillingPeriodDuration: stringOrNull(period),
				Offers:                planOffers,
			})
		}
		data.Subscriptions = append(data.Subscriptions, item)
	}
	data.ProductIDs = stringSetValue(productIDs)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// basePlanKind names the type of a base plan as the subscription resource's
// attributes do, and returns its billing period.
func basePlanKind(plan *androidpublisher.BasePlan) (kind, billingPeriod string) {
	switch {
	case plan.AutoRenewingBasePlanType != nil:
		return "auto_renewing", plan.AutoRenewingBasePlanType.BillingPeriodDuration
	case plan.PrepaidBasePlanType != nil:
		return "prepaid", plan.PrepaidBasePlanType.BillingPeriodDuration
	case plan.InstallmentsBasePlanType != nil:
		return "installments", plan.InstallmentsBasePlanType.BillingPeriodDuration
	}

	return "", ""
}
