// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

// Package provider implements the googleplay provider: its configuration, its
// resources and its data sources.
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/MathGaps/terraform-provider-googleplay/internal/play"
)

const (
	// Address is the registry address this provider is published under.
	Address = "registry.opentofu.org/mathgaps/googleplay"

	envCredentials = "GOOGLEPLAY_CREDENTIALS"
	envDeveloperID = "GOOGLEPLAY_DEVELOPER_ID"
	// envEndpoint replaces the API base URL. It is for tests that run the
	// provider against a fake and is deliberately not a schema attribute.
	envEndpoint = "GOOGLEPLAY_ENDPOINT"
)

var _ provider.Provider = &googlePlayProvider{}

// New returns the provider factory for the given version.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &googlePlayProvider{version: version}
	}
}

type googlePlayProvider struct {
	version string
}

type providerModel struct {
	Credentials types.String `tfsdk:"credentials"`
	DeveloperID types.String `tfsdk:"developer_id"`
}

func (p *googlePlayProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "googleplay"
	resp.Version = p.version
}

func (p *googlePlayProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Google Play Console developer account through the " +
			"[Google Play Developer API](https://developers.google.com/android-publisher): " +
			"users and their per-app permissions, closed testing tracks and their testers, " +
			"subscriptions and one-time products.\n\n" +
			"The API cannot create an app. Create it in Play Console, then refer to it by package name.",
		Attributes: map[string]schema.Attribute{
			"credentials": schema.StringAttribute{
				MarkdownDescription: "The JSON text of a Google service account key (not a path to it). " +
					"Can also be set with the `" + envCredentials + "` environment variable. When neither is set, " +
					"[application default credentials](https://cloud.google.com/docs/authentication/application-default-credentials) " +
					"are used, which includes a key file named by `GOOGLE_APPLICATION_CREDENTIALS`. " +
					"The credentials are used with the `" + play.Scope + "` scope.",
				Optional:  true,
				Sensitive: true,
			},
			"developer_id": schema.StringAttribute{
				MarkdownDescription: "The Play Console developer account id: the number after `/developers/` in a " +
					"Play Console URL. Can also be set with the `" + envDeveloperID + "` environment variable. " +
					"Only `googleplay_user` and `googleplay_app_grant` need it.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(developerIDPattern, "must be the numeric developer account id"),
				},
			},
		},
	}
}

func (p *googlePlayProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.Credentials.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("credentials"), "Unknown credentials",
			"The provider cannot be configured with credentials that are not known until apply. "+
				"Set them statically or with the "+envCredentials+" environment variable.")
	}
	if config.DeveloperID.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("developer_id"), "Unknown developer account id",
			"The provider cannot be configured with a developer account id that is not known until apply. "+
				"Set it statically or with the "+envDeveloperID+" environment variable.")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	credentials := config.Credentials.ValueString()
	if credentials == "" {
		credentials = os.Getenv(envCredentials)
	}

	developerID := config.DeveloperID.ValueString()
	if developerID == "" {
		developerID = os.Getenv(envDeveloperID)
	}
	if developerID != "" && !developerIDPattern.MatchString(developerID) {
		resp.Diagnostics.AddAttributeError(path.Root("developer_id"), "Invalid developer account id",
			"The developer account id from "+envDeveloperID+" must be the numeric id that follows /developers/ in a Play Console URL.")

		return
	}

	// Errors from NewClient never include the credentials themselves.
	client, err := play.NewClient(ctx, play.Config{
		CredentialsJSON: credentials,
		Endpoint:        os.Getenv(envEndpoint),
		DeveloperID:     developerID,
		UserAgent:       "terraform-provider-googleplay/" + p.version,
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to configure the Google Play client",
			err.Error()+"\n\nSet the provider's credentials attribute or the "+envCredentials+
				" environment variable to the JSON text of a service account key, or make application "+
				"default credentials available (for example with GOOGLE_APPLICATION_CREDENTIALS).")

		return
	}

	resp.DataSourceData = client
	resp.ResourceData = client
}

func (p *googlePlayProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewUserResource,
		NewAppGrantResource,
		NewTrackResource,
		NewTrackTestersResource,
		NewSubscriptionResource,
		NewOneTimeProductResource,
	}
}

func (p *googlePlayProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewTracksDataSource,
		NewConvertedRegionPricesDataSource,
	}
}
