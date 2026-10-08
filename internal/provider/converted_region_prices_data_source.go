// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/api/androidpublisher/v3"

	"github.com/MathGaps/terraform-provider-googleplay/internal/play"
)

var (
	_ datasource.DataSource              = &convertedRegionPricesDataSource{}
	_ datasource.DataSourceWithConfigure = &convertedRegionPricesDataSource{}
)

// NewConvertedRegionPricesDataSource returns the
// googleplay_converted_region_prices data source.
func NewConvertedRegionPricesDataSource() datasource.DataSource {
	return &convertedRegionPricesDataSource{}
}

type convertedRegionPricesDataSource struct {
	client *play.Client
}

type convertedRegionPricesModel struct {
	PackageName                types.String                         `tfsdk:"package_name"`
	Price                      *moneyModel                          `tfsdk:"price"`
	ProductTaxCategoryCode     types.String                         `tfsdk:"product_tax_category_code"`
	ConvertedRegionPrices      map[string]convertedRegionPriceModel `tfsdk:"converted_region_prices"`
	ConvertedOtherRegionsPrice *convertedOtherRegionsPriceModel     `tfsdk:"converted_other_regions_price"`
	RegionVersion              types.String                         `tfsdk:"region_version"`
}

type convertedRegionPriceModel struct {
	RegionCode types.String `tfsdk:"region_code"`
	Price      *moneyModel  `tfsdk:"price"`
	TaxAmount  *moneyModel  `tfsdk:"tax_amount"`
}

type convertedOtherRegionsPriceModel struct {
	UsdPrice *moneyModel `tfsdk:"usd_price"`
	EurPrice *moneyModel `tfsdk:"eur_price"`
}

func (d *convertedRegionPricesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_converted_region_prices"
}

// computedMoneyAttribute is a price the API returns.
func computedMoneyAttribute(description string) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: description,
		Computed:            true,
		Attributes: map[string]schema.Attribute{
			"currency_code": schema.StringAttribute{
				MarkdownDescription: "The three-letter ISO 4217 currency code.",
				Computed:            true,
			},
			"amount": schema.StringAttribute{
				MarkdownDescription: "The amount as an exact decimal string, for example `\"4.99\"`.",
				CustomType:          DecimalType{},
				Computed:            true,
			},
		},
	}
}

func (d *convertedRegionPricesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Converts one price into the price of every region Google Play sells in, using " +
			"today's exchange rates and each country's pricing patterns (`monetization.convertRegionPrices`). " +
			"It is the API behind Play Console's \"set prices\" dialog.\n\n" +
			"~> **The result changes over time.** Exchange rates move, so wiring this data source straight into " +
			"the `regional_configs` of a product makes its prices drift from one plan to the next. Use it to " +
			"derive prices once and write them down, or accept the drift knowingly.",
		Attributes: map[string]schema.Attribute{
			"package_name": schema.StringAttribute{
				MarkdownDescription: packageNameDescription,
				Required:            true,
				Validators:          packageNameValidators(),
			},
			"price": schema.SingleNestedAttribute{
				MarkdownDescription: "The price to convert, exclusive of tax.",
				Required:            true,
				Attributes: map[string]schema.Attribute{
					"currency_code": schema.StringAttribute{
						MarkdownDescription: "The three-letter ISO 4217 currency code, for example `USD`.",
						Required:            true,
						Validators: []validator.String{
							stringvalidator.RegexMatches(currencyCodePattern, "must be a three-letter ISO 4217 currency code such as USD"),
						},
					},
					"amount": schema.StringAttribute{
						MarkdownDescription: "The amount as a decimal string, for example `\"4.99\"`. It is converted " +
							"to the API's units and nanos exactly, never through a floating-point number.",
						CustomType: DecimalType{},
						Required:   true,
					},
				},
			},
			"product_tax_category_code": schema.StringAttribute{
				MarkdownDescription: "The product tax category code whose tax rates go into the calculation. " +
					"Left out, the rates of the default category are used.",
				Optional: true,
			},
			"converted_region_prices": schema.MapNestedAttribute{
				MarkdownDescription: "The converted price of each region, by region code.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"region_code": schema.StringAttribute{
							MarkdownDescription: "The region code, the same as the map key.",
							Computed:            true,
						},
						"price":      computedMoneyAttribute("The converted price, inclusive of tax."),
						"tax_amount": computedMoneyAttribute("The tax included in the converted price."),
					},
				},
			},
			"converted_other_regions_price": schema.SingleNestedAttribute{
				MarkdownDescription: "The converted prices, exclusive of tax, for regions whose local currency Google " +
					"Play does not support. They are what a product's other-regions or new-regions configuration takes.",
				Computed: true,
				Attributes: map[string]schema.Attribute{
					"usd_price": computedMoneyAttribute("The price in USD."),
					"eur_price": computedMoneyAttribute("The price in EUR."),
				},
			},
			"region_version": schema.StringAttribute{
				MarkdownDescription: "The regions version the prices were generated at. It can be passed to a " +
					"product's `regions_version`.",
				Computed: true,
			},
		},
	}
}

func (d *convertedRegionPricesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *convertedRegionPricesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data convertedRegionPricesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	price := expandMoney(data.Price, path.Root("price"), &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	converted, err := d.client.Service.Monetization.ConvertRegionPrices(data.PackageName.ValueString(),
		&androidpublisher.ConvertRegionPricesRequest{
			Price:                  price,
			ProductTaxCategoryCode: data.ProductTaxCategoryCode.ValueString(),
		}).Context(ctx).Do()
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to convert the price", err)

		return
	}

	data.ConvertedRegionPrices = map[string]convertedRegionPriceModel{}
	for region, regional := range converted.ConvertedRegionPrices {
		regionCode := regional.RegionCode
		if regionCode == "" {
			regionCode = region
		}
		data.ConvertedRegionPrices[region] = convertedRegionPriceModel{
			RegionCode: types.StringValue(regionCode),
			Price:      flattenMoney(regional.Price),
			TaxAmount:  flattenMoney(regional.TaxAmount),
		}
	}

	data.ConvertedOtherRegionsPrice = nil
	if other := converted.ConvertedOtherRegionsPrice; other != nil {
		data.ConvertedOtherRegionsPrice = &convertedOtherRegionsPriceModel{
			UsdPrice: flattenMoney(other.UsdPrice),
			EurPrice: flattenMoney(other.EurPrice),
		}
	}

	data.RegionVersion = types.StringNull()
	if converted.RegionVersion != nil {
		data.RegionVersion = stringOrNull(converted.RegionVersion.Version)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
