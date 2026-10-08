// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timetypes/timetypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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
	_ resource.Resource                = &userResource{}
	_ resource.ResourceWithConfigure   = &userResource{}
	_ resource.ResourceWithImportState = &userResource{}
)

// developerAccountPermissions are the values of the API's
// DeveloperLevelPermission enum, without the unspecified one.
var developerAccountPermissions = []string{
	"CAN_SEE_ALL_APPS",
	"CAN_VIEW_FINANCIAL_DATA_GLOBAL",
	"CAN_MANAGE_PERMISSIONS_GLOBAL",
	"CAN_EDIT_GAMES_GLOBAL",
	"CAN_PUBLISH_GAMES_GLOBAL",
	"CAN_REPLY_TO_REVIEWS_GLOBAL",
	"CAN_MANAGE_PUBLIC_APKS_GLOBAL",
	"CAN_MANAGE_TRACK_APKS_GLOBAL",
	"CAN_MANAGE_TRACK_USERS_GLOBAL",
	"CAN_MANAGE_PUBLIC_LISTING_GLOBAL",
	"CAN_MANAGE_DRAFT_APPS_GLOBAL",
	"CAN_CREATE_MANAGED_PLAY_APPS_GLOBAL",
	"CAN_CHANGE_MANAGED_PLAY_SETTING_GLOBAL",
	"CAN_MANAGE_ORDERS_GLOBAL",
	"CAN_MANAGE_APP_CONTENT_GLOBAL",
	"CAN_VIEW_NON_FINANCIAL_DATA_GLOBAL",
	"CAN_VIEW_APP_QUALITY_GLOBAL",
	"CAN_MANAGE_DEEPLINKS_GLOBAL",
	"CAN_VIEW_CONNECTED_APPS_GLOBAL",
	"CAN_EDIT_CONNECTED_APPS_GLOBAL",
}

// NewUserResource returns the googleplay_user resource.
func NewUserResource() resource.Resource {
	return &userResource{}
}

type userResource struct {
	client *play.Client
}

type userModel struct {
	ID                          types.String      `tfsdk:"id"`
	Email                       types.String      `tfsdk:"email"`
	DeveloperAccountPermissions types.Set         `tfsdk:"developer_account_permissions"`
	ExpirationTime              timetypes.RFC3339 `tfsdk:"expiration_time"`
	Name                        types.String      `tfsdk:"name"`
	AccessState                 types.String      `tfsdk:"access_state"`
	Partial                     types.Bool        `tfsdk:"partial"`
}

func (r *userResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

func (r *userResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A user of the Play Console developer account, with the permissions that apply " +
			"across the whole account. Per-app permissions are `googleplay_app_grant` resources.\n\n" +
			"Creating the resource invites the address; destroying it removes all of the user's access to the " +
			"developer account, including every per-app grant.\n\n" +
			"The API has no call that reads one user, so every read lists the account's users. " +
			"Requires the provider's `developer_id`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The user's email address.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"email": schema.StringAttribute{
				MarkdownDescription: "The user's email address. Changing it replaces the user.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.RegexMatches(emailPattern, "must be an email address"),
				},
			},
			"developer_account_permissions": schema.SetAttribute{
				MarkdownDescription: "Permissions that apply to every app of the developer account. " +
					"Leave it out for a user who only holds per-app grants. One or more of: " +
					oneOfDescription(developerAccountPermissions...) + ".",
				ElementType: types.StringType,
				Optional:    true,
				Validators: []validator.Set{
					setvalidator.ValueStringsAre(stringvalidator.OneOf(developerAccountPermissions...)),
				},
			},
			"expiration_time": schema.StringAttribute{
				MarkdownDescription: "When the user's access expires, as an RFC 3339 timestamp such as " +
					"`2030-01-01T00:00:00Z`. It must be in the future whenever it is set. Leave it out for access that does not expire.",
				CustomType: timetypes.RFC3339Type{},
				Optional:   true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The API resource name, `developers/{developer}/users/{email}`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"access_state": schema.StringAttribute{
				MarkdownDescription: "The state of the user's access: `INVITED`, `INVITATION_EXPIRED`, " +
					"`ACCESS_GRANTED` or `ACCESS_EXPIRED`.",
				Computed: true,
			},
			"partial": schema.BoolAttribute{
				MarkdownDescription: "Whether the user holds permissions the API does not show, which is the case " +
					"for the account owner and when the credentials cannot manage every app. Such a user cannot be fully managed here.",
				Computed: true,
			},
		},
	}
}

func (r *userResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

// flattenUser writes the API's user into the model. prior supplies what the
// configuration said, so that an equivalent value is not reported as a change.
func flattenUser(user *androidpublisher.User, prior userModel, diags *diag.Diagnostics) userModel {
	model := userModel{
		ID:                          types.StringValue(user.Email),
		Email:                       types.StringValue(user.Email),
		DeveloperAccountPermissions: setFromStrings(user.DeveloperAccountPermissions, prior.DeveloperAccountPermissions),
		ExpirationTime:              timetypes.NewRFC3339Null(),
		Name:                        types.StringValue(user.Name),
		AccessState:                 stringOrNull(user.AccessState),
		Partial:                     types.BoolValue(user.Partial),
	}

	// Email addresses are matched case-insensitively; keep the configured
	// spelling.
	if !prior.Email.IsNull() && strings.EqualFold(prior.Email.ValueString(), user.Email) {
		model.Email = prior.Email
	}

	if user.ExpirationTime != "" {
		value, d := timetypes.NewRFC3339Value(user.ExpirationTime)
		diags.Append(d...)
		model.ExpirationTime = value
	}

	return model
}

func (r *userResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	parent, err := r.client.DeveloperParent()
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create the user", err)

		return
	}

	user := &androidpublisher.User{
		Email:                       plan.Email.ValueString(),
		DeveloperAccountPermissions: stringsFromSet(ctx, plan.DeveloperAccountPermissions, &resp.Diagnostics),
		ExpirationTime:              plan.ExpirationTime.ValueString(),
	}
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.client.Service.Users.Create(parent, user).Context(ctx).Do()
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create the user", err)

		return
	}

	// The response is the created user, but only the list shows it as other
	// reads will see it.
	if listed, err := r.client.FindUser(ctx, plan.Email.ValueString()); err == nil && listed != nil {
		created = listed
	}
	if created.Email == "" {
		created.Email = plan.Email.ValueString()
	}
	if created.Name == "" {
		created.Name = parent + "/users/" + created.Email
	}

	state := flattenUser(created, plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *userResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state userModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	user, err := r.client.FindUser(ctx, state.Email.ValueString())
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read the user", err)

		return
	}
	if user == nil {
		resp.State.RemoveResource(ctx)

		return
	}

	state = flattenUser(user, state, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *userResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name, err := r.client.UserName(state.Email.ValueString())
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to update the user", err)

		return
	}

	user := &androidpublisher.User{}
	var mask []string

	if !plan.DeveloperAccountPermissions.Equal(state.DeveloperAccountPermissions) {
		mask = append(mask, "developerAccountPermissions")
		user.DeveloperAccountPermissions = stringsFromSet(ctx, plan.DeveloperAccountPermissions, &resp.Diagnostics)
		if len(user.DeveloperAccountPermissions) == 0 {
			// Send the empty list rather than leaving the field out.
			user.DeveloperAccountPermissions = []string{}
			user.ForceSendFields = append(user.ForceSendFields, "DeveloperAccountPermissions")
		}
	}
	if !plan.ExpirationTime.Equal(state.ExpirationTime) {
		mask = append(mask, "expirationTime")
		user.ExpirationTime = plan.ExpirationTime.ValueString()
		if user.ExpirationTime == "" {
			user.NullFields = append(user.NullFields, "ExpirationTime")
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}

	if len(mask) > 0 {
		if _, err := r.client.Service.Users.Patch(name, user).UpdateMask(strings.Join(mask, ",")).Context(ctx).Do(); err != nil {
			addAPIError(&resp.Diagnostics, "Unable to update the user", err)

			return
		}
	}

	updated, err := r.client.FindUser(ctx, state.Email.ValueString())
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read the user after updating it", err)

		return
	}
	if updated == nil {
		resp.Diagnostics.AddError("User not found after update",
			"The user "+state.Email.ValueString()+" is no longer in the developer account.")

		return
	}

	newState := flattenUser(updated, plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *userResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state userModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name, err := r.client.UserName(state.Email.ValueString())
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to delete the user", err)

		return
	}

	if err := r.client.Service.Users.Delete(name).Context(ctx).Do(); err != nil && !play.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete the user", err)
	}
}

func (r *userResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !emailPattern.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid import id", "Expected the user's email address, got \""+req.ID+"\".")

		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("email"), req.ID)...)
}
