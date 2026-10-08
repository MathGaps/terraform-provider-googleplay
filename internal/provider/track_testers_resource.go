// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
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
	_ resource.Resource                = &trackTestersResource{}
	_ resource.ResourceWithConfigure   = &trackTestersResource{}
	_ resource.ResourceWithImportState = &trackTestersResource{}
)

// NewTrackTestersResource returns the googleplay_track_testers resource.
func NewTrackTestersResource() resource.Resource {
	return &trackTestersResource{}
}

type trackTestersResource struct {
	client *play.Client
}

type trackTestersModel struct {
	ID           types.String `tfsdk:"id"`
	PackageName  types.String `tfsdk:"package_name"`
	Track        types.String `tfsdk:"track"`
	GoogleGroups types.Set    `tfsdk:"google_groups"`
}

func (r *trackTestersResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_track_testers"
}

func (r *trackTestersResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The Google Groups whose members can test a track of an app. The resource is " +
			"authoritative: it replaces the whole list of groups, and destroying it leaves the track with none.\n\n" +
			"~> **Google Groups only.** The API exposes the tester groups of a track and nothing else. Testers " +
			"added in Play Console as email lists are not visible to this resource, are not changed by it, and " +
			"cannot be managed with it. To manage testers as configuration, put them in a Google Group.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "`{package_name}/{track}`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"package_name": schema.StringAttribute{
				MarkdownDescription: packageNameDescription + " Changing it replaces the resource.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          packageNameValidators(),
			},
			"track": schema.StringAttribute{
				MarkdownDescription: "The identifier of the track, for example `internal`, `alpha` or the `track` " +
					"of a `googleplay_track`. Changing it replaces the resource.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.RegexMatches(trackPattern, "must not contain a slash or whitespace"),
				},
			},
			"google_groups": schema.SetAttribute{
				MarkdownDescription: "The email addresses of the Google Groups whose members are testers of the track.",
				ElementType:         types.StringType,
				Required:            true,
				Validators: []validator.Set{
					setvalidator.ValueStringsAre(stringvalidator.RegexMatches(emailPattern, "must be the email address of a Google Group")),
				},
			},
		},
	}
}

func (r *trackTestersResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

// put replaces the tester groups of the track in a committed edit.
func (r *trackTestersResource) put(ctx context.Context, packageName, track string, groups []string) error {
	testers := &androidpublisher.Testers{GoogleGroups: groups}
	if len(groups) == 0 {
		// Say "no groups" explicitly rather than leaving the field out.
		testers.GoogleGroups = []string{}
		testers.ForceSendFields = []string{"GoogleGroups"}
	}

	return r.client.CommitEdit(ctx, packageName, func(editID string) error {
		_, err := r.client.Service.Edits.Testers.Update(packageName, editID, track, testers).Context(ctx).Do()

		return err
	})
}

func (r *trackTestersResource) apply(ctx context.Context, plan *trackTestersModel) error {
	packageName, track := plan.PackageName.ValueString(), plan.Track.ValueString()

	var groups []string
	for _, element := range plan.GoogleGroups.Elements() {
		if value, ok := element.(types.String); ok {
			groups = append(groups, value.ValueString())
		}
	}

	if err := r.put(ctx, packageName, track, groups); err != nil {
		return err
	}

	plan.ID = types.StringValue(packageName + "/" + track)

	return nil
}

func (r *trackTestersResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan trackTestersModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.apply(ctx, &plan); err != nil {
		addAPIError(&resp.Diagnostics, "Unable to set the testers of the track", err)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *trackTestersResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state trackTestersModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	packageName, track := state.PackageName.ValueString(), state.Track.ValueString()

	var testers *androidpublisher.Testers
	err := r.client.ReadEdit(ctx, packageName, func(editID string) error {
		var err error
		testers, err = r.client.Service.Edits.Testers.Get(packageName, editID, track).Context(ctx).Do()

		return err
	})
	if play.IsNotFound(err) {
		resp.State.RemoveResource(ctx)

		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read the testers of the track", err)

		return
	}

	state.ID = types.StringValue(packageName + "/" + track)
	state.GoogleGroups = stringSetValue(testers.GoogleGroups)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *trackTestersResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan trackTestersModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.apply(ctx, &plan); err != nil {
		addAPIError(&resp.Diagnostics, "Unable to set the testers of the track", err)

		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *trackTestersResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state trackTestersModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.put(ctx, state.PackageName.ValueString(), state.Track.ValueString(), nil)
	if err != nil && !play.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to clear the testers of the track", err)
	}
}

func (r *trackTestersResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := splitImportID(req.ID, "package_name", "track")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import id", err.Error())

		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("package_name"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("track"), parts[1])...)
}
