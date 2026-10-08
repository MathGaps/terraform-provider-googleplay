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
	_ datasource.DataSource              = &usersDataSource{}
	_ datasource.DataSourceWithConfigure = &usersDataSource{}
)

// NewUsersDataSource returns the googleplay_users data source.
func NewUsersDataSource() datasource.DataSource {
	return &usersDataSource{}
}

type usersDataSource struct {
	client *play.Client
}

type usersDataSourceModel struct {
	Users []usersDataUserModel `tfsdk:"users"`
}

type usersDataUserModel struct {
	Email                       types.String          `tfsdk:"email"`
	Name                        types.String          `tfsdk:"name"`
	DeveloperAccountPermissions types.Set             `tfsdk:"developer_account_permissions"`
	ExpirationTime              types.String          `tfsdk:"expiration_time"`
	AccessState                 types.String          `tfsdk:"access_state"`
	Partial                     types.Bool            `tfsdk:"partial"`
	Grants                      []usersDataGrantModel `tfsdk:"grants"`
}

type usersDataGrantModel struct {
	PackageName         types.String `tfsdk:"package_name"`
	Name                types.String `tfsdk:"name"`
	AppLevelPermissions types.Set    `tfsdk:"app_level_permissions"`
}

func (d *usersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_users"
}

func (d *usersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Every user of the Play Console developer account, with their account-wide " +
			"permissions and their per-app grants. Use it to see what already exists and to write the " +
			"`import` blocks for `googleplay_user` (id: `email`) and `googleplay_app_grant` " +
			"(id: `email/package_name`).\n\n" +
			"The API returns all users in one response and cannot page. Requires the provider's `developer_id`.",
		Attributes: map[string]schema.Attribute{
			"users": schema.ListNestedAttribute{
				MarkdownDescription: "The users of the developer account, ordered by email address.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"email": schema.StringAttribute{
							MarkdownDescription: "The user's email address, as Play Console holds it. It is the import id of a `googleplay_user`.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "The API resource name, `developers/{developer}/users/{email}`.",
							Computed:            true,
						},
						"developer_account_permissions": schema.SetAttribute{
							MarkdownDescription: "The permissions that apply to every app of the account. Empty for a user who only holds per-app grants.",
							ElementType:         types.StringType,
							Computed:            true,
						},
						"expiration_time": schema.StringAttribute{
							MarkdownDescription: "When the user's access expires, as an RFC 3339 timestamp. Null for access that does not expire.",
							Computed:            true,
						},
						"access_state": schema.StringAttribute{
							MarkdownDescription: "The state of the user's access: `INVITED`, `INVITATION_EXPIRED`, `ACCESS_GRANTED` or `ACCESS_EXPIRED`.",
							Computed:            true,
						},
						"partial": schema.BoolAttribute{
							MarkdownDescription: "Whether the user holds permissions the API does not show, which is the case " +
								"for the account owner and when the credentials cannot manage every app. Such a user cannot be fully managed.",
							Computed: true,
						},
						"grants": schema.ListNestedAttribute{
							MarkdownDescription: "The user's per-app permissions, ordered by package name.",
							Computed:            true,
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"package_name": schema.StringAttribute{
										MarkdownDescription: "The package name of the app. Null for a draft app, which the API identifies " +
											"only in `name`; such a grant cannot be managed as a `googleplay_app_grant`.",
										Computed: true,
									},
									"name": schema.StringAttribute{
										MarkdownDescription: "The API resource name, `developers/{developer}/users/{email}/grants/{package_name}`.",
										Computed:            true,
									},
									"app_level_permissions": schema.SetAttribute{
										MarkdownDescription: "The permissions granted on the app.",
										ElementType:         types.StringType,
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

func (d *usersDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *usersDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	users, err := d.client.ListUsers(ctx)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to list the users of the developer account", err)

		return
	}

	// A stable order, whatever order the API lists in.
	slices.SortFunc(users, func(a, b *androidpublisher.User) int {
		return strings.Compare(strings.ToLower(a.Email), strings.ToLower(b.Email))
	})

	data := usersDataSourceModel{Users: make([]usersDataUserModel, 0, len(users))}
	for _, user := range users {
		grants := slices.Clone(user.Grants)
		slices.SortFunc(grants, func(a, b *androidpublisher.Grant) int {
			return strings.Compare(a.PackageName+"\x00"+a.Name, b.PackageName+"\x00"+b.Name)
		})

		item := usersDataUserModel{
			Email:                       types.StringValue(user.Email),
			Name:                        stringOrNull(user.Name),
			DeveloperAccountPermissions: stringSetValue(user.DeveloperAccountPermissions),
			ExpirationTime:              stringOrNull(user.ExpirationTime),
			AccessState:                 stringOrNull(user.AccessState),
			Partial:                     types.BoolValue(user.Partial),
			Grants:                      make([]usersDataGrantModel, 0, len(grants)),
		}
		for _, grant := range grants {
			item.Grants = append(item.Grants, usersDataGrantModel{
				PackageName:         stringOrNull(grant.PackageName),
				Name:                stringOrNull(grant.Name),
				AppLevelPermissions: stringSetValue(grant.AppLevelPermissions),
			})
		}
		data.Users = append(data.Users, item)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
