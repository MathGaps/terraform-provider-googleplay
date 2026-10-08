// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/MathGaps/terraform-provider-googleplay/internal/fakeplay"
)

const (
	// unitPackage is the app the fake API starts with.
	unitPackage = "com.example.app"
	// unitDeveloperID is the developer account id unit tests configure.
	unitDeveloperID = "1234567890123456789"

	envTestPackage = "GOOGLEPLAY_TEST_PACKAGE"
	envTestGroup   = "GOOGLEPLAY_TEST_GROUP"
	envTestTrack   = "GOOGLEPLAY_TEST_TRACK"
	envTestUser    = "GOOGLEPLAY_TEST_USER_EMAIL"
)

// TestMain makes the tests run under OpenTofu when it is installed and the
// caller has not chosen a binary. terraform-plugin-testing drives a real CLI
// even for unit tests, and would otherwise look for (or download) Terraform.
func TestMain(m *testing.M) {
	if os.Getenv("TF_ACC_TERRAFORM_PATH") == "" && os.Getenv("TF_ACC_TERRAFORM_VERSION") == "" {
		if tofu, err := exec.LookPath("tofu"); err == nil {
			_ = os.Setenv("TF_ACC_TERRAFORM_PATH", tofu)
			// OpenTofu resolves a provider with no source to its own registry.
			_ = os.Setenv("TF_ACC_PROVIDER_HOST", "registry.opentofu.org")
		}
	}

	os.Exit(m.Run())
}

// testProviderFactories instantiate the provider in-process for both tiers.
var testProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"googleplay": providerserver.NewProtocol6WithError(New("test")()),
}

// newUnitFake starts a fake Google Play Developer API and points the provider
// at it for the duration of the test. No credentials are involved.
func newUnitFake(t *testing.T) *fakeplay.Server {
	t.Helper()

	fake := fakeplay.New(unitPackage)
	t.Cleanup(fake.Close)

	t.Setenv(envEndpoint, fake.URL)
	t.Setenv(envDeveloperID, unitDeveloperID)
	t.Setenv(envCredentials, "")

	return fake
}

// testAccPreCheck skips an acceptance test unless it can reach a real
// developer account. Skipping, rather than failing, keeps a checkout without
// credentials green: that is the normal case for contributors and forks.
func testAccPreCheck(t *testing.T, required ...string) {
	t.Helper()

	if os.Getenv("TF_ACC") == "" {
		t.Skip("skipping acceptance test: TF_ACC is not set")
	}
	if os.Getenv(envEndpoint) != "" {
		t.Fatalf("%s is set: acceptance tests must run against the real API", envEndpoint)
	}

	var missing []string
	for _, name := range append([]string{envTestPackage}, required...) {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Skipf("skipping acceptance test: %s must be set (see ACCEPTANCE_TESTING.md)", strings.Join(missing, ", "))
	}
}

// importSteps are the two import checks every resource gets, placed after a
// step that applied a configuration: the classic import, whose resulting state
// must equal the applied one, and an import block planned together with that
// configuration, which must plan no change to the imported resource.
func importSteps(resourceName string) []resource.TestStep {
	return []resource.TestStep{
		{
			ResourceName:      resourceName,
			ImportState:       true,
			ImportStateVerify: true,
		},
		{
			ResourceName:    resourceName,
			ImportState:     true,
			ImportStateKind: resource.ImportBlockWithID,
		},
	}
}

// requestsMatching returns the logged requests that match the pattern.
func requestsMatching(fake *fakeplay.Server, pattern string) []string {
	re := regexp.MustCompile(pattern)

	var matched []string
	for _, request := range fake.Requests() {
		if re.MatchString(request) {
			matched = append(matched, request)
		}
	}

	return matched
}

func TestProvider_requiresDeveloperIDForUsers(t *testing.T) {
	newUnitFake(t)
	t.Setenv(envDeveloperID, "")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
resource "googleplay_user" "test" {
  email = "ada@example.com"
}`,
			ExpectError: regexp.MustCompile(`Missing developer account id`),
		}},
	})
}

func TestProvider_rejectsMalformedCredentials(t *testing.T) {
	// No endpoint override: the credentials are parsed, and nothing is called
	// because the configuration fails first.
	t.Setenv(envEndpoint, "")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
provider "googleplay" {
  credentials = "super-secret-not-json"
}

data "googleplay_tracks" "test" {
  package_name = %q
}`, unitPackage),
			ExpectError: regexp.MustCompile(`the credentials are not valid JSON`),
		}},
	})
}

func TestProvider_invalidDeveloperID(t *testing.T) {
	newUnitFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
provider "googleplay" {
  developer_id = "not-a-number"
}

resource "googleplay_user" "test" {
  email = "ada@example.com"
}`,
			ExpectError: regexp.MustCompile(`must be the numeric developer account id`),
		}},
	})
}
