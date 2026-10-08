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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
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
			"~> **A user with no account-wide permission is created by its first grant.** Google Play refuses " +
			"to create a user who holds no permission at all (`No permissions set for this user`). When " +
			"`developer_account_permissions` is left out, creating this resource calls nothing: it records the " +
			"user in state, with a warning, and the first `googleplay_app_grant` that refers to it invites the " +
			"user and grants the permission in one call. Refer to this resource's `email` from the grant so " +
			"that they are applied in that order. A user of this kind with no grant does not exist in Play " +
			"Console, and every plan proposes to create it.\n\n" +
			"The API has no call that reads one user, and its list cannot be paged, so every read fetches all " +
			"of the account's users in one request. " +
			"Requires the provider's `developer_id`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The user's email address.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"email": schema.StringAttribute{
				MarkdownDescription: "The user's email address. Changing it replaces the user. A difference in case only is not a change.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{keepStateIfEqualFold{}, stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.RegexMatches(emailPattern, "must be an email address"),
				},
			},
			"developer_account_permissions": schema.SetAttribute{
				MarkdownDescription: "Permissions that apply to every app of the developer account. " +
					"Leave it out for a user who only holds per-app grants; such a user is created by its first " +
					"`googleplay_app_grant`. One or more of: " +
					oneOfDescription(developerAccountPermissions...) + ".\n\n" +
					"`CAN_SEE_ALL_APPS` and `CAN_CHANGE_MANAGED_PLAY_SETTING_GLOBAL` are deprecated and produce " +
					"a warning. Google Play may replace a permission with others when it stores a user (it does " +
					"for the app-level `CAN_ACCESS_APP`). The provider never hides that as a silent difference: " +
					"when what was stored is not what was asked for, the apply fails with an error that lists " +
					"both, a user being created is removed again, and the fix is to write the stored permissions.",
				ElementType: types.StringType,
				Optional:    true,
				Validators: []validator.Set{
					setvalidator.ValueStringsAre(stringvalidator.OneOf(developerAccountPermissions...)),
					replacedPermission{deprecated: map[string]string{
						// The client marks both deprecated. Whether Google stores
						// them as written has not been observed, so they warn
						// rather than fail; a replacement is reported at apply.
						"CAN_SEE_ALL_APPS": "the API reference says to use CAN_VIEW_NON_FINANCIAL_DATA_GLOBAL. " +
							"Google Play may store other permissions in its place, in which case the apply fails " +
							"and names them.",
						"CAN_CHANGE_MANAGED_PLAY_SETTING_GLOBAL": "the API reference says it is no longer supported.",
					}},
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
					"`ACCESS_GRANTED` or `ACCESS_EXPIRED`. Null until the next refresh for a user that was " +
					"created by its first grant.",
				Computed: true,
				// Nothing this resource writes changes it; it is refreshed on read.
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"partial": schema.BoolAttribute{
				MarkdownDescription: "Whether the user holds permissions the API does not show, which is the case " +
					"for the account owner and when the credentials cannot manage every app. Such a user cannot be fully managed here.",
				Computed:      true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
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

// userName is the resource name to address the user by. The name the API
// reported is preferred: it carries the address exactly as Play Console holds
// it, which may differ in case from the configured one.
func (r *userResource) userName(state userModel) (string, error) {
	if name := state.Name.ValueString(); name != "" {
		return name, nil
	}

	return r.client.UserName(state.Email.ValueString())
}

// createOrDefer creates the user, or defers it when it has no account-wide
// permission, and returns the state to record.
//
// Google refuses to create a user who would hold no permission at all: the
// call must carry an account permission or a grant. A user declared with only
// per-app grants is therefore not created here. It is recorded in state and
// handed to the client as pending; the first googleplay_app_grant created for
// it makes one users.create call with the grant in it.
func (r *userResource) createOrDefer(ctx context.Context, plan userModel, diags *diag.Diagnostics) (userModel, bool) {
	email := plan.Email.ValueString()

	parent, err := r.client.DeveloperParent()
	if err != nil {
		addAPIError(diags, "Unable to create the user", err)

		return userModel{}, false
	}

	permissions := stringsFromSet(ctx, plan.DeveloperAccountPermissions, diags)
	if diags.HasError() {
		return userModel{}, false
	}

	defer r.client.LockUser(email)()

	if len(permissions) == 0 {
		// No call creates this user, so nothing would refuse a second one:
		// check, rather than silently adopt a user that exists.
		existing, err := r.client.FindUser(ctx, email)
		if err != nil {
			addAPIError(diags, "Unable to create the user", err)

			return userModel{}, false
		}
		if existing != nil {
			diags.AddError("User already exists",
				email+" is already a user of the developer account. Import it to manage it: "+existing.Email)

			return userModel{}, false
		}

		r.client.DeferUser(play.PendingUser{Email: email, ExpirationTime: plan.ExpirationTime.ValueString()})

		diags.AddWarning("User not created yet",
			email+" has no developer_account_permissions, and Google Play creates a user only together with a "+
				"permission. The user is recorded in state and will be invited when its first googleplay_app_grant "+
				"is created, in the same call. If no googleplay_app_grant refers to this user, it does not exist "+
				"in Play Console and the next plan proposes to create it again.")

		return userModel{
			ID:                          types.StringValue(email),
			Email:                       plan.Email,
			DeveloperAccountPermissions: plan.DeveloperAccountPermissions,
			ExpirationTime:              plan.ExpirationTime,
			Name:                        types.StringValue(parent + "/users/" + email),
			AccessState:                 types.StringNull(),
			Partial:                     types.BoolNull(),
		}, true
	}

	created, err := r.client.Service.Users.Create(parent, &androidpublisher.User{
		Email:                       email,
		DeveloperAccountPermissions: permissions,
		ExpirationTime:              plan.ExpirationTime.ValueString(),
	}).Context(ctx).Do()
	if err != nil {
		addAPIError(diags, "Unable to create the user", err)

		return userModel{}, false
	}
	r.client.ForgetPendingUser(email)

	// The response is the created user, but only the list shows it as other
	// reads will see it.
	if listed, err := r.client.FindUser(ctx, email); err == nil && listed != nil {
		created = listed
	}
	if created.Email == "" {
		created.Email = email
	}
	if created.Name == "" {
		created.Name = parent + "/users/" + created.Email
	}

	// If Google stored other permissions than were asked for, say exactly how
	// and take the user back out, rather than leave a resource the framework
	// would taint: replacing a tainted user removes them from the account.
	if permissionsDiffer(permissions, created.DeveloperAccountPermissions) {
		detail := permissionsDifference(permissions, created.DeveloperAccountPermissions)

		err := r.client.Service.Users.Delete(created.Name).Context(ctx).Do()
		if err != nil && !play.IsNotFound(err) {
			diags.AddAttributeError(path.Root("developer_account_permissions"),
				"Google Play stored different permissions than were asked for",
				detail+"\n\nThe user could not be removed again and is recorded as it was stored: "+play.ErrorDetail(err))

			return flattenUser(created, plan, diags), true
		}

		diags.AddAttributeError(path.Root("developer_account_permissions"),
			"Google Play stored different permissions than were asked for",
			detail+"\n\nThe invitation has been withdrawn again, so nothing was left behind. Correct the configuration and apply.")

		return userModel{}, false
	}

	return flattenUser(created, plan, diags), true
}

func (r *userResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state, ok := r.createOrDefer(ctx, plan, &resp.Diagnostics); ok {
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	}
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

	// A user deferred by an earlier apply and never given a grant is in state
	// without existing. Updating it is creating it.
	existing, err := r.client.FindUser(ctx, state.Email.ValueString())
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to update the user", err)

		return
	}
	if existing == nil {
		if newState, ok := r.createOrDefer(ctx, plan, &resp.Diagnostics); ok {
			resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
		}

		return
	}

	name, err := r.userName(state)
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

	// State records what Google stored either way; a difference from what was
	// asked for is an error that names it.
	asked := stringsFromSet(ctx, plan.DeveloperAccountPermissions, &resp.Diagnostics)
	if permissionsDiffer(asked, updated.DeveloperAccountPermissions) {
		resp.Diagnostics.AddAttributeError(path.Root("developer_account_permissions"),
			"Google Play stored different permissions than were asked for",
			permissionsDifference(asked, updated.DeveloperAccountPermissions)+
				"\n\nThe user has been updated and state holds what Google stored.")
	}
}

func (r *userResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state userModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name, err := r.userName(state)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to delete the user", err)

		return
	}

	r.client.ForgetPendingUser(state.Email.ValueString())

	// A 404 covers a deferred user that was never created, and a user Google
	// removed with its last grant. Deleting a user removes its grants too.
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
