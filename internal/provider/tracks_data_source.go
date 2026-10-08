// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/api/androidpublisher/v3"

	"github.com/MathGaps/terraform-provider-googleplay/internal/play"
)

var (
	_ datasource.DataSource              = &tracksDataSource{}
	_ datasource.DataSourceWithConfigure = &tracksDataSource{}
)

// NewTracksDataSource returns the googleplay_tracks data source.
func NewTracksDataSource() datasource.DataSource {
	return &tracksDataSource{}
}

type tracksDataSource struct {
	client *play.Client
}

type tracksDataSourceModel struct {
	PackageName types.String          `tfsdk:"package_name"`
	Names       types.Set             `tfsdk:"names"`
	Tracks      []tracksDataItemModel `tfsdk:"tracks"`
}

type tracksDataItemModel struct {
	Track    types.String             `tfsdk:"track"`
	Releases []tracksDataReleaseModel `tfsdk:"releases"`
}

type tracksDataReleaseModel struct {
	Name         types.String  `tfsdk:"name"`
	Status       types.String  `tfsdk:"status"`
	VersionCodes types.List    `tfsdk:"version_codes"`
	UserFraction types.Float64 `tfsdk:"user_fraction"`
}

func (d *tracksDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tracks"
}

func (d *tracksDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The tracks of an app, built-in and custom, with the releases currently on each.",
		Attributes: map[string]schema.Attribute{
			"package_name": schema.StringAttribute{
				MarkdownDescription: packageNameDescription,
				Required:            true,
				Validators:          packageNameValidators(),
			},
			"names": schema.SetAttribute{
				MarkdownDescription: "The identifiers of every track of the app.",
				ElementType:         types.StringType,
				Computed:            true,
			},
			"tracks": schema.ListNestedAttribute{
				MarkdownDescription: "Every track of the app, in the order the API lists them.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"track": schema.StringAttribute{
							MarkdownDescription: "The identifier of the track.",
							Computed:            true,
						},
						"releases": schema.ListNestedAttribute{
							MarkdownDescription: "The active releases on the track.",
							Computed:            true,
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"name": schema.StringAttribute{
										MarkdownDescription: "The name of the release.",
										Computed:            true,
									},
									"status": schema.StringAttribute{
										MarkdownDescription: "The status of the release: `draft`, `inProgress`, `halted` or `completed`.",
										Computed:            true,
									},
									"version_codes": schema.ListAttribute{
										MarkdownDescription: "The version codes of the release, as strings.",
										ElementType:         types.StringType,
										Computed:            true,
									},
									"user_fraction": schema.Float64Attribute{
										MarkdownDescription: "The fraction of users a staged release is served to. Null unless the release is staged.",
										Computed:            true,
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

func (d *tracksDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *tracksDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data tracksDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	packageName := data.PackageName.ValueString()

	var list *androidpublisher.TracksListResponse
	err := d.client.ReadEdit(ctx, packageName, func(editID string) error {
		var err error
		list, err = d.client.Service.Edits.Tracks.List(packageName, editID).Context(ctx).Do()

		return err
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to list the tracks of the app", err)

		return
	}

	names := make([]string, 0, len(list.Tracks))
	data.Tracks = make([]tracksDataItemModel, 0, len(list.Tracks))
	for _, track := range list.Tracks {
		names = append(names, track.Track)

		item := tracksDataItemModel{
			Track:    types.StringValue(track.Track),
			Releases: make([]tracksDataReleaseModel, 0, len(track.Releases)),
		}
		for _, release := range track.Releases {
			codes := make([]string, len(release.VersionCodes))
			for i, code := range release.VersionCodes {
				codes[i] = strconv.FormatInt(code, 10)
			}
			versionCodes, diags := types.ListValueFrom(ctx, types.StringType, codes)
			resp.Diagnostics.Append(diags...)

			fraction := types.Float64Null()
			if release.UserFraction != 0 {
				fraction = types.Float64Value(release.UserFraction)
			}

			item.Releases = append(item.Releases, tracksDataReleaseModel{
				Name:         stringOrNull(release.Name),
				Status:       stringOrNull(release.Status),
				VersionCodes: versionCodes,
				UserFraction: fraction,
			})
		}
		data.Tracks = append(data.Tracks, item)
	}
	data.Names = stringSetValue(names)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
