// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"regexp"

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
	_ resource.Resource                = &appGrantResource{}
	_ resource.ResourceWithConfigure   = &appGrantResource{}
	_ resource.ResourceWithImportState = &appGrantResource{}
)

var emailPattern = regexp.MustCompile(`^[^@\s/]+@[^@\s/]+$`)

// appLevelPermissions are the values of the API's AppLevelPermission enum,
// without the unspecified one.
var appLevelPermissions = []string{
	"CAN_ACCESS_APP",
	"CAN_VIEW_FINANCIAL_DATA",
	"CAN_MANAGE_PERMISSIONS",
	"CAN_REPLY_TO_REVIEWS",
	"CAN_MANAGE_PUBLIC_APKS",
	"CAN_MANAGE_TRACK_APKS",
	"CAN_MANAGE_TRACK_USERS",
	"CAN_MANAGE_PUBLIC_LISTING",
	"CAN_MANAGE_DRAFT_APPS",
	"CAN_MANAGE_ORDERS",
	"CAN_MANAGE_APP_CONTENT",
	"CAN_VIEW_NON_FINANCIAL_DATA",
	"CAN_VIEW_APP_QUALITY",
	"CAN_MANAGE_DEEPLINKS",
}

// NewAppGrantResource returns the googleplay_app_grant resource.
func NewAppGrantResource() resource.Resource {
	return &appGrantResource{}
}

type appGrantResource struct {
	client *play.Client
}

type appGrantModel struct {
	ID                  types.String `tfsdk:"id"`
	Email               types.String `tfsdk:"email"`
	PackageName         types.String `tfsdk:"package_name"`
	AppLevelPermissions types.Set    `tfsdk:"app_level_permissions"`
	Name                types.String `tfsdk:"name"`
}

func (r *appGrantResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_grant"
}

func (r *appGrantResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The permissions one user of the developer account holds on one app. " +
			"Declare the user with `googleplay_user` and refer to its `email`.\n\n" +
			"A user that `googleplay_user` declares with no account-wide permission does not exist until its " +
			"first grant: Google Play creates a user only together with a permission. Creating the first grant " +
			"of such a user invites the user and grants the permission in a single call; further grants are " +
			"added to the user as usual. Grants of one user are applied one at a time.\n\n" +
			"The API has no call that reads one grant, and its user list cannot be paged, so every read fetches " +
			"all of the account's users in one request. " +
			"Requires the provider's `developer_id`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "`{email}/{package_name}`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"email": schema.StringAttribute{
				MarkdownDescription: "The email address of the user. Changing it replaces the grant. A difference in case only is not a change.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{keepStateIfEqualFold{}, stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.RegexMatches(emailPattern, "must be an email address"),
				},
			},
			"package_name": schema.StringAttribute{
				MarkdownDescription: packageNameDescription + " Changing it replaces the grant.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          packageNameValidators(),
			},
			"app_level_permissions": schema.SetAttribute{
				MarkdownDescription: "The permissions granted on the app. One or more of: " +
					oneOfDescription(appLevelPermissions...) + ".",
				ElementType: types.StringType,
				Required:    true,
				Validators: []validator.Set{
					setvalidator.SizeAtLeast(1),
					setvalidator.ValueStringsAre(stringvalidator.OneOf(appLevelPermissions...)),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The API resource name, `developers/{developer}/users/{email}/grants/{package_name}`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *appGrantResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

// findGrant returns the user's grant on the package, or nil when the user or
// the grant does not exist.
func (r *appGrantResource) findGrant(ctx context.Context, email, packageName string) (*androidpublisher.Grant, error) {
	user, err := r.client.FindUser(ctx, email)
	if err != nil || user == nil {
		return nil, err
	}

	for _, grant := range user.Grants {
		if grant.PackageName == packageName {
			return grant, nil
		}
	}

	return nil, nil
}

// refresh reads the grant back and writes it to state. It reports whether the
// grant was found.
func (r *appGrantResource) refresh(ctx context.Context, model *appGrantModel) (bool, error) {
	email, packageName := model.Email.ValueString(), model.PackageName.ValueString()

	grant, err := r.findGrant(ctx, email, packageName)
	if err != nil || grant == nil {
		return false, err
	}

	name := grant.Name
	if name == "" {
		if name, err = r.client.GrantName(email, packageName); err != nil {
			return false, err
		}
	}

	model.ID = types.StringValue(email + "/" + packageName)
	model.AppLevelPermissions = stringSetValue(grant.AppLevelPermissions)
	model.Name = types.StringValue(name)

	return true, nil
}

// grantName is the resource name to address the grant by, preferring the one
// the API reported, as userResource.userName does.
func (r *appGrantResource) grantName(state appGrantModel) (string, error) {
	if name := state.Name.ValueString(); name != "" {
		return name, nil
	}

	return r.client.GrantName(state.Email.ValueString(), state.PackageName.ValueString())
}

// createGrant grants the permissions, creating the user with them when the
// user is declared but could not be created without a permission. It reports
// whether it succeeded.
//
// One user is handled at a time. Of two grants for a user who does not exist
// yet, whichever runs first creates the user with its grant, and the second
// finds the user and adds its own.
func (r *appGrantResource) createGrant(ctx context.Context, email string, grant *androidpublisher.Grant, diags *diag.Diagnostics) bool {
	defer r.client.LockUser(email)()

	user, err := r.client.FindUser(ctx, email)
	if err != nil {
		addAPIError(diags, "Unable to create the app grant", err)

		return false
	}

	if user != nil {
		// Address the user by the name the API holds, which may differ in case
		// from the configured address.
		parent := user.Name
		if parent == "" {
			if parent, err = r.client.UserName(email); err != nil {
				addAPIError(diags, "Unable to create the app grant", err)

				return false
			}
		}

		if _, err := r.client.Service.Grants.Create(parent, grant).Context(ctx).Do(); err != nil {
			addAPIError(diags, "Unable to create the app grant", err)

			return false
		}

		return true
	}

	// The user does not exist. A googleplay_user with no account-wide
	// permission waits for exactly this: Google creates a user only together
	// with a permission, so the user and this grant are created in one call.
	pending, declared := r.client.PendingUser(email)
	if !declared {
		diags.AddAttributeError(path.Root("email"), "User not found",
			email+" is not a user of the developer account. Declare it with a googleplay_user resource and "+
				"refer to that resource's email, so that the user is created with this grant. (A user that a "+
				"googleplay_user declared with no account-wide permission is created by its first grant; if "+
				"this apply ran with -refresh=false, run it again with a refresh.)")

		return false
	}

	parent, err := r.client.DeveloperParent()
	if err != nil {
		addAPIError(diags, "Unable to create the app grant", err)

		return false
	}

	_, err = r.client.Service.Users.Create(parent, &androidpublisher.User{
		Email:          pending.Email,
		ExpirationTime: pending.ExpirationTime,
		Grants:         []*androidpublisher.Grant{grant},
	}).Context(ctx).Do()
	if err != nil {
		addAPIError(diags, "Unable to create the user with its app grant", err)

		return false
	}
	r.client.ForgetPendingUser(email)

	return true
}

func (r *appGrantResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan appGrantModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	email := plan.Email.ValueString()
	grant := &androidpublisher.Grant{
		PackageName:         plan.PackageName.ValueString(),
		AppLevelPermissions: stringsFromSet(ctx, plan.AppLevelPermissions, &resp.Diagnostics),
	}
	if resp.Diagnostics.HasError() {
		return
	}

	if !r.createGrant(ctx, email, grant, &resp.Diagnostics) {
		return
	}

	found, err := r.refresh(ctx, &plan)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read the app grant after creating it", err)

		return
	}
	if !found {
		resp.Diagnostics.AddError("App grant not found after create",
			"The grant was created but the developer account's user list does not show it.")

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *appGrantResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state appGrantModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	found, err := r.refresh(ctx, &state)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read the app grant", err)

		return
	}
	if !found {
		resp.State.RemoveResource(ctx)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *appGrantResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state appGrantModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name, err := r.grantName(state)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to update the app grant", err)

		return
	}

	grant := &androidpublisher.Grant{
		AppLevelPermissions: stringsFromSet(ctx, plan.AppLevelPermissions, &resp.Diagnostics),
	}
	if resp.Diagnostics.HasError() {
		return
	}

	if _, err := r.client.Service.Grants.Patch(name, grant).UpdateMask("appLevelPermissions").Context(ctx).Do(); err != nil {
		addAPIError(&resp.Diagnostics, "Unable to update the app grant", err)

		return
	}

	found, err := r.refresh(ctx, &plan)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read the app grant after updating it", err)

		return
	}
	if !found {
		resp.Diagnostics.AddError("App grant not found after update",
			"The grant was updated but the developer account's user list does not show it.")

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *appGrantResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state appGrantModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name, err := r.grantName(state)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to delete the app grant", err)

		return
	}

	if err := r.client.Service.Grants.Delete(name).Context(ctx).Do(); err != nil && !play.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete the app grant", err)
	}
}

func (r *appGrantResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// A package name has no slash and neither does an email address.
	parts, err := splitImportID(req.ID, "email", "package_name")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import id", err.Error())

		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("email"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("package_name"), parts[1])...)
}
