// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/api/androidpublisher/v3"

	"github.com/MathGaps/terraform-provider-googleplay/internal/play"
)

var (
	_ resource.Resource                   = &trackResource{}
	_ resource.ResourceWithConfigure      = &trackResource{}
	_ resource.ResourceWithImportState    = &trackResource{}
	_ resource.ResourceWithValidateConfig = &trackResource{}
)

const (
	formFactorDefault    = "DEFAULT"
	formFactorWear       = "WEAR"
	formFactorAutomotive = "AUTOMOTIVE"
	trackTypeClosed      = "CLOSED_TESTING"
)

// formFactorPrefixes maps a form factor to the prefix its track names carry,
// "wear:production" for example. The default form factor has none.
var formFactorPrefixes = map[string]string{
	formFactorWear:       "wear:",
	formFactorAutomotive: "automotive:",
}

// trackFormFactor derives the form factor from a track name's prefix.
func trackFormFactor(track string) string {
	for formFactor, prefix := range formFactorPrefixes {
		if strings.HasPrefix(track, prefix) {
			return formFactor
		}
	}

	return formFactorDefault
}

// NewTrackResource returns the googleplay_track resource.
func NewTrackResource() resource.Resource {
	return &trackResource{}
}

type trackResource struct {
	client *play.Client
}

type trackModel struct {
	ID          types.String `tfsdk:"id"`
	PackageName types.String `tfsdk:"package_name"`
	Track       types.String `tfsdk:"track"`
	FormFactor  types.String `tfsdk:"form_factor"`
	Type        types.String `tfsdk:"type"`
}

func (r *trackResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_track"
}

func (r *trackResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A custom closed testing track of an app.\n\n" +
			"This resource manages the existence of the track and nothing else. The releases on it are not " +
			"read, compared or written: builds are expected to be uploaded by a release pipeline.\n\n" +
			"~> **A track cannot be deleted.** The API has no call for it. Destroying this resource only removes " +
			"it from state, with a warning, and the track stays in Play Console. Creating a track whose name is " +
			"already taken fails, so bring an existing track under management with an import.\n\n" +
			"The built-in tracks (`production`, `beta`, `alpha`, `internal`) always exist and need no resource.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "`{package_name}/{track}`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"package_name": schema.StringAttribute{
				MarkdownDescription: packageNameDescription + " Changing it creates a new track.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          packageNameValidators(),
			},
			"track": schema.StringAttribute{
				MarkdownDescription: "The identifier of the track. A track of a form factor other than the default " +
					"carries that form factor's prefix, `wear:` or `automotive:`, for example `wear:qa`. " +
					"Changing it creates a new track.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.RegexMatches(trackPattern, "must not contain a slash or whitespace"),
				},
			},
			"form_factor": schema.StringAttribute{
				MarkdownDescription: "The form factor of the track: " +
					oneOfDescription(formFactorDefault, formFactorWear, formFactorAutomotive) +
					". Defaults to the form factor the prefix of `track` names, `DEFAULT` when there is none. " +
					"Changing it creates a new track.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf(formFactorDefault, formFactorWear, formFactorAutomotive),
				},
			},
			"type": schema.StringAttribute{
				MarkdownDescription: "The type of the track. `CLOSED_TESTING` is the only type the API can create, and the default.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(trackTypeClosed),
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.OneOf(trackTypeClosed),
				},
			},
		},
	}
}

func (r *trackResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

// ValidateConfig rejects a form factor that contradicts the track's prefix,
// which the API requires to match.
func (r *trackResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config trackModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.Track.IsNull() || config.Track.IsUnknown() || config.FormFactor.IsNull() || config.FormFactor.IsUnknown() {
		return
	}

	if implied := trackFormFactor(config.Track.ValueString()); implied != config.FormFactor.ValueString() {
		resp.Diagnostics.AddAttributeError(path.Root("form_factor"), "Form factor does not match the track name",
			"The track \""+config.Track.ValueString()+"\" is a "+implied+" track by its name, but form_factor is "+
				config.FormFactor.ValueString()+". A WEAR track is named wear:<name>, an AUTOMOTIVE track "+
				"automotive:<name>, and a DEFAULT track has no prefix.")
	}
}

func (r *trackResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan trackModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	packageName, track := plan.PackageName.ValueString(), plan.Track.ValueString()
	if plan.FormFactor.IsUnknown() || plan.FormFactor.IsNull() {
		plan.FormFactor = types.StringValue(trackFormFactor(track))
	}

	err := r.client.CommitEdit(ctx, packageName, func(editID string) error {
		// TrackConfig has no releases: this never touches what is on a track.
		_, err := r.client.Service.Edits.Tracks.Create(packageName, editID, &androidpublisher.TrackConfig{
			Track:      track,
			FormFactor: plan.FormFactor.ValueString(),
			Type:       plan.Type.ValueString(),
		}).Context(ctx).Do()

		return err
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create the track", err)

		return
	}

	plan.ID = types.StringValue(packageName + "/" + track)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *trackResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state trackModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	packageName, track := state.PackageName.ValueString(), state.Track.ValueString()

	err := r.client.ReadEdit(ctx, packageName, func(editID string) error {
		// Only the existence of the track matters. Its releases are ignored.
		_, err := r.client.Service.Edits.Tracks.Get(packageName, editID, track).Fields("track").Context(ctx).Do()

		return err
	})
	if play.IsNotFound(err) {
		resp.State.RemoveResource(ctx)

		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read the track", err)

		return
	}

	// The API reports neither the form factor nor the type of a track. The
	// name's prefix determines the first and there is only one of the second.
	state.ID = types.StringValue(packageName + "/" + track)
	state.FormFactor = types.StringValue(trackFormFactor(track))
	if state.Type.IsNull() || state.Type.IsUnknown() {
		state.Type = types.StringValue(trackTypeClosed)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called: every attribute requires replacement.
func (r *trackResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Tracks cannot be updated", "Every attribute of a track requires replacement. This is a bug in the provider.")
}

func (r *trackResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state trackModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.AddWarning("The track was not deleted",
		"The Google Play Developer API cannot delete a track. The track \""+state.Track.ValueString()+"\" of "+
			state.PackageName.ValueString()+" has been removed from state and remains in Play Console, with its testers and releases.")
}

func (r *trackResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := splitImportID(req.ID, "package_name", "track")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import id", err.Error())

		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("package_name"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("track"), parts[1])...)
}
