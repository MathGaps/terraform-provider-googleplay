// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"google.golang.org/api/androidpublisher/v3"
)

func TestUserResource_lifecycle(t *testing.T) {
	fake := newUnitFake(t)
	for _, email := range []string{"aaa@example.com", "zzz@example.com"} {
		fake.PutUser(&androidpublisher.User{Email: email, Name: "developers/" + unitDeveloperID + "/users/" + email})
	}

	const name = "googleplay_user.test"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if fake.User("grace@example.com") != nil {
				return fmt.Errorf("the user still exists after destroy")
			}

			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: `
resource "googleplay_user" "test" {
  email                         = "grace@example.com"
  developer_account_permissions = ["CAN_VIEW_NON_FINANCIAL_DATA_GLOBAL", "CAN_REPLY_TO_REVIEWS_GLOBAL"]
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(name, "id", "grace@example.com"),
					resource.TestCheckResourceAttr(name, "name", "developers/"+unitDeveloperID+"/users/grace@example.com"),
					resource.TestCheckResourceAttr(name, "access_state", "INVITED"),
					resource.TestCheckResourceAttr(name, "developer_account_permissions.#", "2"),
					resource.TestCheckNoResourceAttr(name, "expiration_time"),
					func(*terraform.State) error {
						// The list cannot be paged: every read asks for all users.
						if lists := requestsMatching(fake, `^GET .*/users\?`); len(lists) == 0 ||
							len(requestsMatching(fake, `^GET .*/users\?.*pageSize=-1`)) != len(lists) {
							return fmt.Errorf("user lists must send pageSize=-1: %v", lists)
						}

						return nil
					},
				),
			},
			{
				// Change one field, set the other: the mask names both.
				Config: `
resource "googleplay_user" "test" {
  email                         = "grace@example.com"
  developer_account_permissions = ["CAN_MANAGE_PERMISSIONS_GLOBAL"]
  expiration_time               = "2099-01-01T00:00:00+00:00"
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(name, "developer_account_permissions.#", "1"),
					// The fake stores what it was sent; the configured spelling is kept.
					resource.TestCheckResourceAttr(name, "expiration_time", "2099-01-01T00:00:00+00:00"),
					func(*terraform.State) error {
						user := fake.User("grace@example.com")
						if !slices.Equal(user.DeveloperAccountPermissions, []string{"CAN_MANAGE_PERMISSIONS_GLOBAL"}) {
							return fmt.Errorf("permissions = %v", user.DeveloperAccountPermissions)
						}

						return nil
					},
				),
			},
			importSteps(name)[0],
			importSteps(name)[1],
			{
				// Clearing both optional attributes sends them as empty.
				Config: `
resource "googleplay_user" "test" {
  email = "grace@example.com"
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(name, "developer_account_permissions"),
					resource.TestCheckNoResourceAttr(name, "expiration_time"),
					func(*terraform.State) error {
						user := fake.User("grace@example.com")
						if len(user.DeveloperAccountPermissions) != 0 || user.ExpirationTime != "" {
							return fmt.Errorf("the user was not cleared: %+v", user)
						}

						return nil
					},
				),
			},
		},
	})
}

// A user removed in Play Console is planned for creation again, not an error.
func TestUserResource_removedOutOfBand(t *testing.T) {
	fake := newUnitFake(t)

	const config = `
resource "googleplay_user" "test" {
  email                         = "grace@example.com"
  developer_account_permissions = ["CAN_VIEW_APP_QUALITY_GLOBAL"]
}`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					if fake.User("grace@example.com") == nil {
						t.Fatal("the user was not created")
					}
					fake.DeleteUser("grace@example.com")
				},
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestAppGrantResource_lifecycle(t *testing.T) {
	fake := newUnitFake(t)

	const name = "googleplay_app_grant.test"
	config := func(permissions string) string {
		return fmt.Sprintf(`
resource "googleplay_user" "test" {
  email = "grace@example.com"
}

resource "googleplay_app_grant" "test" {
  email                 = googleplay_user.test.email
  package_name          = %q
  app_level_permissions = [%s]
}`, unitPackage, permissions)
	}

	grants := func() []*androidpublisher.Grant {
		if user := fake.User("grace@example.com"); user != nil {
			return user.Grants
		}

		return nil
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(`"CAN_MANAGE_TRACK_APKS", "CAN_VIEW_NON_FINANCIAL_DATA"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(name, "id", "grace@example.com/"+unitPackage),
					resource.TestCheckResourceAttr(name, "name",
						"developers/"+unitDeveloperID+"/users/grace@example.com/grants/"+unitPackage),
					resource.TestCheckResourceAttr(name, "app_level_permissions.#", "2"),
				),
			},
			{
				Config: config(`"CAN_MANAGE_PERMISSIONS"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(name, "app_level_permissions.#", "1"),
					func(*terraform.State) error {
						if g := grants(); len(g) != 1 || !slices.Equal(g[0].AppLevelPermissions, []string{"CAN_MANAGE_PERMISSIONS"}) {
							return fmt.Errorf("grants = %+v", g)
						}

						return nil
					},
				),
			},
			importSteps(name)[0],
			importSteps(name)[1],
			{
				// Destroying the grant leaves the user.
				Config: `
resource "googleplay_user" "test" {
  email = "grace@example.com"
}`,
				Check: func(*terraform.State) error {
					if fake.User("grace@example.com") == nil {
						return fmt.Errorf("the user was deleted with the grant")
					}
					if g := grants(); len(g) != 0 {
						return fmt.Errorf("the grant still exists: %+v", g)
					}

					return nil
				},
			},
		},
	})
}

// The import-first flow of an account that already has users, grants and
// tester groups: each is imported by its id and the matching configuration
// plans nothing and writes nothing.
func TestImportExistingAccount(t *testing.T) {
	fake := newUnitFake(t)

	for i := range 10 {
		email := fmt.Sprintf("colleague%d@example.com", i)
		fake.PutUser(&androidpublisher.User{
			Email:       email,
			Name:        "developers/" + unitDeveloperID + "/users/" + email,
			AccessState: "ACCESS_GRANTED",
		})
	}
	fake.PutUser(&androidpublisher.User{
		// Play Console keeps the capitalisation the user was invited with.
		Email:                       "Ada@Example.com",
		Name:                        "developers/" + unitDeveloperID + "/users/Ada@Example.com",
		AccessState:                 "ACCESS_GRANTED",
		DeveloperAccountPermissions: []string{"CAN_VIEW_APP_QUALITY_GLOBAL", "CAN_REPLY_TO_REVIEWS_GLOBAL"},
		ExpirationTime:              "2099-06-30T12:00:00Z",
		Grants: []*androidpublisher.Grant{
			{
				Name:                "developers/" + unitDeveloperID + "/users/Ada@Example.com/grants/com.example.other",
				PackageName:         "com.example.other",
				AppLevelPermissions: []string{"CAN_MANAGE_PERMISSIONS"},
			},
			{
				Name:                "developers/" + unitDeveloperID + "/users/Ada@Example.com/grants/" + unitPackage,
				PackageName:         unitPackage,
				AppLevelPermissions: []string{"CAN_MANAGE_TRACK_APKS", "CAN_VIEW_NON_FINANCIAL_DATA"},
			},
		},
	})
	// A user with nothing account-wide, only grants.
	fake.PutUser(&androidpublisher.User{
		Email:       "grants-only@example.com",
		Name:        "developers/" + unitDeveloperID + "/users/grants-only@example.com",
		AccessState: "INVITED",
	})
	fake.PutTesters(unitPackage, "internal", "team@example.com", "qa@example.com")

	config := fmt.Sprintf(`
resource "googleplay_user" "ada" {
  email                         = "ada@example.com"
  developer_account_permissions = ["CAN_REPLY_TO_REVIEWS_GLOBAL", "CAN_VIEW_APP_QUALITY_GLOBAL"]
  expiration_time               = "2099-06-30T12:00:00Z"
}

resource "googleplay_user" "grants_only" {
  email = "grants-only@example.com"
}

resource "googleplay_app_grant" "ada" {
  email                 = googleplay_user.ada.email
  package_name          = %[1]q
  app_level_permissions = ["CAN_VIEW_NON_FINANCIAL_DATA", "CAN_MANAGE_TRACK_APKS"]
}

resource "googleplay_track_testers" "internal" {
  package_name  = %[1]q
  track         = "internal"
  google_groups = ["qa@example.com", "team@example.com"]
}`, unitPackage)

	importStep := func(name, id string) resource.TestStep {
		return resource.TestStep{
			Config:             config,
			ResourceName:       name,
			ImportState:        true,
			ImportStateId:      id,
			ImportStatePersist: true,
		}
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			// Imported with the capitalisation Play Console shows, configured
			// in lower case: not a replacement.
			importStep("googleplay_user.ada", "Ada@Example.com"),
			importStep("googleplay_user.grants_only", "grants-only@example.com"),
			importStep("googleplay_app_grant.ada", "Ada@Example.com/"+unitPackage),
			importStep("googleplay_track_testers.internal", unitPackage+"/internal"),
			{
				Config:   config,
				PlanOnly: true,
			},
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("googleplay_user.ada", "email", "Ada@Example.com"),
					resource.TestCheckResourceAttr("googleplay_user.ada", "access_state", "ACCESS_GRANTED"),
					resource.TestCheckNoResourceAttr("googleplay_user.grants_only", "developer_account_permissions"),
					resource.TestCheckResourceAttr("googleplay_app_grant.ada", "app_level_permissions.#", "2"),
					resource.TestCheckResourceAttr("googleplay_track_testers.internal", "google_groups.#", "2"),
					func(*terraform.State) error {
						// An edit is opened and deleted to read testers; nothing
						// else may have been written, and no edit committed.
						for _, request := range requestsMatching(fake, `^(POST|PATCH|PUT|DELETE) `) {
							if !regexp.MustCompile(`^(POST .*/edits\?|DELETE .*/edits/[^/:?]+\?)`).MatchString(request) {
								return fmt.Errorf("importing wrote to the API: %s", request)
							}
						}
						if open := fake.OpenEdits(unitPackage); open != 0 {
							return fmt.Errorf("%d edits were left open", open)
						}

						return nil
					},
				),
			},
		},
	})
}

// Google refuses to create a user who holds no permission at all, so a user
// declared with only a per-app grant is created by that grant, in one call.
func TestAppGrantResource_createsPermissionlessUser(t *testing.T) {
	fake := newUnitFake(t)

	config := fmt.Sprintf(`
resource "googleplay_user" "test" {
  email           = "grace@example.com"
  expiration_time = "2099-01-01T00:00:00Z"
}

resource "googleplay_app_grant" "test" {
  email                 = googleplay_user.test.email
  package_name          = %q
  app_level_permissions = ["CAN_VIEW_NON_FINANCIAL_DATA", "CAN_MANAGE_TRACK_APKS"]
}`, unitPackage)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if fake.User("grace@example.com") != nil {
				return fmt.Errorf("the user still exists after destroy")
			}

			return nil
		},
		Steps: []resource.TestStep{
			{
				// The framework also asserts that the plan after this apply is
				// empty, with and without a refresh.
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("googleplay_user.test", "id", "grace@example.com"),
					resource.TestCheckNoResourceAttr("googleplay_user.test", "developer_account_permissions"),
					resource.TestCheckResourceAttr("googleplay_app_grant.test", "app_level_permissions.#", "2"),
					resource.TestCheckResourceAttr("googleplay_app_grant.test", "name",
						"developers/"+unitDeveloperID+"/users/grace@example.com/grants/"+unitPackage),
					func(*terraform.State) error {
						// One users.create carrying the grant; never a create
						// without permissions, and no separate grants.create.
						if creates := requestsMatching(fake, `^POST .*/users\?`); len(creates) != 1 {
							return fmt.Errorf("want exactly one users.create, got %v", creates)
						}
						if creates := requestsMatching(fake, `^POST .*/grants\?`); len(creates) != 0 {
							return fmt.Errorf("the first grant must travel in users.create, got %v", creates)
						}

						user := fake.User("grace@example.com")
						if user == nil || len(user.Grants) != 1 || user.Grants[0].PackageName != unitPackage ||
							len(user.Grants[0].AppLevelPermissions) != 2 {
							return fmt.Errorf("the user was not created with its grant: %+v", user)
						}
						// The user's own settings travel with it.
						if user.ExpirationTime != "2099-01-01T00:00:00Z" || len(user.DeveloperAccountPermissions) != 0 {
							return fmt.Errorf("the user was created as %+v", user)
						}

						return nil
					},
				),
			},
			{
				Config:   config,
				PlanOnly: true,
			},
			{
				// The user resource finished before the grant created the
				// user, so what Google reports about it (access_state, partial)
				// reaches state with the next refresh.
				RefreshState: true,
				Check:        resource.TestCheckResourceAttr("googleplay_user.test", "access_state", "INVITED"),
			},
			importSteps("googleplay_user.test")[0],
			importSteps("googleplay_user.test")[1],
			importSteps("googleplay_app_grant.test")[0],
			importSteps("googleplay_app_grant.test")[1],
		},
	})
}

// Two grants for a user who does not exist yet: whichever runs first creates
// the user with its grant, and the other adds its own to the user it finds.
func TestAppGrantResource_twoGrantsForNewUser(t *testing.T) {
	fake := newUnitFake(t)

	config := fmt.Sprintf(`
resource "googleplay_user" "test" {
  email = "grace@example.com"
}

resource "googleplay_app_grant" "first" {
  email                 = googleplay_user.test.email
  package_name          = %q
  app_level_permissions = ["CAN_VIEW_NON_FINANCIAL_DATA"]
}

resource "googleplay_app_grant" "second" {
  email                 = googleplay_user.test.email
  package_name          = "com.example.other"
  app_level_permissions = ["CAN_MANAGE_TRACK_APKS", "CAN_MANAGE_TRACK_USERS"]
}`, unitPackage)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("googleplay_app_grant.first", "app_level_permissions.#", "1"),
					resource.TestCheckResourceAttr("googleplay_app_grant.second", "app_level_permissions.#", "2"),
					func(*terraform.State) error {
						if creates := requestsMatching(fake, `^POST .*/users\?`); len(creates) != 1 {
							return fmt.Errorf("want exactly one users.create, got %v", creates)
						}
						if creates := requestsMatching(fake, `^POST .*/grants\?`); len(creates) != 1 {
							return fmt.Errorf("want exactly one grants.create for the other grant, got %v", creates)
						}

						user := fake.User("grace@example.com")
						if user == nil || len(user.Grants) != 2 {
							return fmt.Errorf("the user does not hold both grants: %+v", user)
						}

						return nil
					},
				),
			},
			{
				Config:   config,
				PlanOnly: true,
			},
			{
				// Dropping one grant leaves the user and the other grant.
				Config: fmt.Sprintf(`
resource "googleplay_user" "test" {
  email = "grace@example.com"
}

resource "googleplay_app_grant" "first" {
  email                 = googleplay_user.test.email
  package_name          = %q
  app_level_permissions = ["CAN_VIEW_NON_FINANCIAL_DATA"]
}`, unitPackage),
				Check: func(*terraform.State) error {
					user := fake.User("grace@example.com")
					if user == nil || len(user.Grants) != 1 || user.Grants[0].PackageName != unitPackage {
						return fmt.Errorf("unexpected user after dropping a grant: %+v", user)
					}

					return nil
				},
			},
		},
	})
}

// A user with no permission and no grant cannot exist. Nothing is called, the
// apply warns, and the next plan still proposes the user.
func TestUserResource_permissionlessWithoutGrant(t *testing.T) {
	fake := newUnitFake(t)

	const config = `
resource "googleplay_user" "test" {
  email = "grace@example.com"
}`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:             config,
				ExpectNonEmptyPlan: true,
				Check: func(*terraform.State) error {
					if writes := requestsMatching(fake, `^(POST|PATCH|PUT) `); len(writes) != 0 {
						return fmt.Errorf("a permissionless user must not be sent to the API: %v", writes)
					}
					if fake.User("grace@example.com") != nil {
						return fmt.Errorf("the user exists")
					}

					return nil
				},
			},
			{
				// Giving it a permission creates it for real.
				Config: `
resource "googleplay_user" "test" {
  email                         = "grace@example.com"
  developer_account_permissions = ["CAN_VIEW_APP_QUALITY_GLOBAL"]
}`,
				Check: func(*terraform.State) error {
					if user := fake.User("grace@example.com"); user == nil || len(user.DeveloperAccountPermissions) != 1 {
						return fmt.Errorf("the user was not created: %+v", user)
					}

					return nil
				},
			},
		},
	})
}

// A grant for an address nobody declared does not create a user by itself.
func TestAppGrantResource_undeclaredUser(t *testing.T) {
	fake := newUnitFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "googleplay_app_grant" "test" {
  email                 = "nobody@example.com"
  package_name          = %q
  app_level_permissions = ["CAN_VIEW_NON_FINANCIAL_DATA"]
}`, unitPackage),
			ExpectError: regexp.MustCompile(`User not found`),
		}},
	})

	if fake.User("nobody@example.com") != nil {
		t.Error("a grant created a user that no googleplay_user declares")
	}
}

// --- acceptance --------------------------------------------------------------

// TestAccUserResource_basic invites GOOGLEPLAY_TEST_USER_EMAIL to the developer
// account with a read-only permission, grants it read-only access to the test
// app, and removes both. See ACCEPTANCE_TESTING.md before running it.
func TestAccUserResource_basic(t *testing.T) {
	email, packageName := os.Getenv(envTestUser), os.Getenv(envTestPackage)

	config := func(permission string) string {
		return fmt.Sprintf(`
resource "googleplay_user" "test" {
  email                         = %q
  developer_account_permissions = ["CAN_VIEW_APP_QUALITY_GLOBAL"]
}

resource "googleplay_app_grant" "test" {
  email                 = googleplay_user.test.email
  package_name          = %q
  app_level_permissions = [%q]
}`, email, packageName, permission)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t, envDeveloperID, envTestUser) },
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("CAN_VIEW_NON_FINANCIAL_DATA"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("googleplay_user.test", "email", email),
					resource.TestCheckResourceAttrSet("googleplay_user.test", "access_state"),
					resource.TestCheckResourceAttr("googleplay_app_grant.test", "app_level_permissions.#", "1"),
				),
			},
			{Config: config("CAN_VIEW_APP_QUALITY")},
			{ResourceName: "googleplay_user.test", ImportState: true, ImportStateVerify: true},
			{ResourceName: "googleplay_app_grant.test", ImportState: true, ImportStateVerify: true},
		},
	})
}
