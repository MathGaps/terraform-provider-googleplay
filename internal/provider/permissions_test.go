// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestPermissionsDifference(t *testing.T) {
	asked := []string{"CAN_MANAGE_TRACK_APKS", "CAN_ACCESS_APP"}
	stored := []string{"CAN_VIEW_NON_FINANCIAL_DATA", "CAN_MANAGE_TRACK_APKS", "CAN_VIEW_APP_QUALITY"}

	if !permissionsDiffer(asked, stored) {
		t.Fatal("different permissions were reported as the same")
	}
	if permissionsDiffer([]string{"B", "A"}, []string{"A", "B", "A"}) {
		t.Error("order and repetition are not a difference")
	}
	if permissionsDiffer(nil, []string{}) {
		t.Error("no permissions and an empty list are the same")
	}

	detail := permissionsDifference(asked, stored)
	for _, want := range []string{
		"Asked for: CAN_ACCESS_APP, CAN_MANAGE_TRACK_APKS.",
		"Google stored: CAN_MANAGE_TRACK_APKS, CAN_VIEW_APP_QUALITY, CAN_VIEW_NON_FINANCIAL_DATA.",
		"Asked for and not stored: CAN_ACCESS_APP.",
		"Stored and not asked for: CAN_VIEW_APP_QUALITY, CAN_VIEW_NON_FINANCIAL_DATA.",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("the description is missing %q:\n%s", want, detail)
		}
	}

	if !strings.Contains(permissionsDifference([]string{"A"}, nil), "Google stored: (none).") {
		t.Error("an empty stored list must read as (none)")
	}
}

func TestReplacedPermissionValidator(t *testing.T) {
	v := replacedPermission{
		rejected:   map[string]string{"OLD": "write NEW instead."},
		deprecated: map[string]string{"FADING": "it is going away."},
	}

	run := func(values ...string) *validator.SetResponse {
		resp := &validator.SetResponse{}
		v.ValidateSet(context.Background(), validator.SetRequest{
			Path:        path.Root("permissions"),
			ConfigValue: stringSetValue(values),
		}, resp)

		return resp
	}

	if resp := run("FINE"); resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 0 {
		t.Errorf("an ordinary permission produced %v", resp.Diagnostics)
	}
	if resp := run("FINE", "OLD"); !resp.Diagnostics.HasError() ||
		!strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "write NEW instead.") {
		t.Errorf("a replaced permission produced %v", resp.Diagnostics)
	}
	if resp := run("FADING"); resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 {
		t.Errorf("a deprecated permission produced %v", resp.Diagnostics)
	}

	if slices.Contains(managedAppLevelPermissions(), "CAN_ACCESS_APP") || len(managedAppLevelPermissions()) != len(appLevelPermissions)-1 {
		t.Error("the documented app-level permissions must be all but CAN_ACCESS_APP")
	}
}

// CAN_ACCESS_APP is an alias Google replaces when it stores a grant, so it is
// refused before anything is planned, with the two permissions to write.
func TestAppGrantResource_rejectsCanAccessApp(t *testing.T) {
	fake := newUnitFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "googleplay_user" "test" {
  email = "grace@example.com"
}

resource "googleplay_app_grant" "test" {
  email                 = googleplay_user.test.email
  package_name          = %q
  app_level_permissions = ["CAN_ACCESS_APP", "CAN_MANAGE_PUBLIC_LISTING", "CAN_MANAGE_TRACK_APKS", "CAN_MANAGE_TRACK_USERS"]
}`, unitPackage),
			ExpectError: regexp.MustCompile(`(?s)CAN_ACCESS_APP cannot be managed.*CAN_VIEW_APP_QUALITY\s+and\s+CAN_VIEW_NON_FINANCIAL_DATA`),
		}},
	})

	if writes := requestsMatching(fake, `^(POST|PATCH|PUT|DELETE) `); len(writes) != 0 {
		t.Errorf("a rejected configuration reached the API: %v", writes)
	}
}

// The configuration that failed against the live API, written the way Google
// stores it: a permissionless user and one grant, applied with no error and
// planning nothing afterwards.
func TestAppGrantResource_expandedPermissionsApplyCleanly(t *testing.T) {
	fake := newUnitFake(t)

	config := fmt.Sprintf(`
resource "googleplay_user" "test" {
  email = "grace@example.com"
}

resource "googleplay_app_grant" "test" {
  email        = googleplay_user.test.email
  package_name = %q

  app_level_permissions = [
    "CAN_VIEW_APP_QUALITY",
    "CAN_VIEW_NON_FINANCIAL_DATA",
    "CAN_MANAGE_PUBLIC_LISTING",
    "CAN_MANAGE_TRACK_APKS",
    "CAN_MANAGE_TRACK_USERS",
  ]
}`, unitPackage)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			// A step fails on any error diagnostic, so this asserts a clean apply.
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("googleplay_app_grant.test", "app_level_permissions.#", "5"),
					func(*terraform.State) error {
						if user := fake.User("grace@example.com"); user == nil || len(user.Grants) != 1 {
							return fmt.Errorf("the user was not created with its grant: %+v", user)
						}

						return nil
					},
				),
			},
			{Config: config, PlanOnly: true},
		},
	})
}

// When Google stores other permissions than were asked for, the apply says
// which, and a grant being created is taken out again: nothing is left in Play
// Console that state does not know, and nothing is tainted.
func TestAppGrantResource_storedPermissionsDiffer(t *testing.T) {
	fake := newUnitFake(t)
	// A replacement the validator does not know about.
	fake.Expansions["CAN_MANAGE_ORDERS"] = []string{"CAN_VIEW_FINANCIAL_DATA", "CAN_REPLY_TO_REVIEWS"}

	config := func(userPermissions, grantPermissions string) string {
		return fmt.Sprintf(`
resource "googleplay_user" "test" {
  email = "grace@example.com"
  %s
}

resource "googleplay_app_grant" "test" {
  email                 = googleplay_user.test.email
  package_name          = %q
  app_level_permissions = [%s]
}`, userPermissions, unitPackage, grantPermissions)
	}
	const withPermission = `developer_account_permissions = ["CAN_VIEW_APP_QUALITY_GLOBAL"]`

	differs := regexp.MustCompile(`(?s)Google Play stored different permissions than were asked for.*` +
		`Asked for and not stored: CAN_MANAGE_ORDERS\..*` +
		`Stored and not asked for: CAN_REPLY_TO_REVIEWS, CAN_VIEW_FINANCIAL_DATA\.`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				// The grant creates the user, and both are taken out again.
				Config:      config("", `"CAN_MANAGE_ORDERS", "CAN_MANAGE_TRACK_APKS"`),
				ExpectError: differs,
			},
			{
				PreConfig: func() {
					if user := fake.User("grace@example.com"); user != nil {
						t.Errorf("the user created with the refused grant was left behind: %+v", user)
					}
				},
				// An existing user this time: only the grant is taken out.
				Config:      config(withPermission, `"CAN_MANAGE_ORDERS", "CAN_MANAGE_TRACK_APKS"`),
				ExpectError: differs,
			},
			{
				PreConfig: func() {
					user := fake.User("grace@example.com")
					if user == nil || len(user.Grants) != 0 {
						t.Errorf("want the user without the refused grant, got %+v", user)
					}
				},
				// Written the way Google stores it, it applies, and is not a
				// replacement of anything tainted.
				Config: config(withPermission, `"CAN_VIEW_FINANCIAL_DATA", "CAN_REPLY_TO_REVIEWS", "CAN_MANAGE_TRACK_APKS"`),
				Check:  resource.TestCheckResourceAttr("googleplay_app_grant.test", "app_level_permissions.#", "3"),
			},
			{
				// An update that Google stores differently is applied, reported,
				// and recorded as stored.
				Config:      config(withPermission, `"CAN_MANAGE_ORDERS"`),
				ExpectError: differs,
			},
			{
				PreConfig: func() {
					user := fake.User("grace@example.com")
					if user == nil || len(user.Grants) != 1 || len(user.Grants[0].AppLevelPermissions) != 2 {
						t.Errorf("want the grant as the server expanded it, got %+v", user)
					}
				},
				// State holds what was stored, so the corrected configuration
				// plans nothing.
				Config:   config(withPermission, `"CAN_VIEW_FINANCIAL_DATA", "CAN_REPLY_TO_REVIEWS"`),
				PlanOnly: true,
			},
		},
	})
}

// The same for the account-wide permissions of a user being created: the
// invitation is withdrawn rather than left as a tainted resource, whose
// replacement would remove the user from the account.
func TestUserResource_storedPermissionsDiffer(t *testing.T) {
	fake := newUnitFake(t)
	fake.Expansions["CAN_SEE_ALL_APPS"] = []string{"CAN_VIEW_NON_FINANCIAL_DATA_GLOBAL", "CAN_VIEW_APP_QUALITY_GLOBAL"}

	config := func(permissions string) string {
		return fmt.Sprintf(`
resource "googleplay_user" "test" {
  email                         = "grace@example.com"
  developer_account_permissions = [%s]
}`, permissions)
	}

	differs := regexp.MustCompile(`(?s)Google Play stored different permissions than were asked for.*` +
		`Asked for and not stored: CAN_SEE_ALL_APPS\.`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      config(`"CAN_SEE_ALL_APPS"`),
				ExpectError: differs,
			},
			{
				PreConfig: func() {
					if user := fake.User("grace@example.com"); user != nil {
						t.Errorf("the refused user was left behind: %+v", user)
					}
				},
				Config: config(`"CAN_VIEW_NON_FINANCIAL_DATA_GLOBAL", "CAN_VIEW_APP_QUALITY_GLOBAL"`),
				Check:  resource.TestCheckResourceAttr("googleplay_user.test", "developer_account_permissions.#", "2"),
			},
			{
				Config:      config(`"CAN_SEE_ALL_APPS", "CAN_REPLY_TO_REVIEWS_GLOBAL"`),
				ExpectError: differs,
			},
			{
				Config:   config(`"CAN_VIEW_NON_FINANCIAL_DATA_GLOBAL", "CAN_VIEW_APP_QUALITY_GLOBAL", "CAN_REPLY_TO_REVIEWS_GLOBAL"`),
				PlanOnly: true,
			},
		},
	})
}
