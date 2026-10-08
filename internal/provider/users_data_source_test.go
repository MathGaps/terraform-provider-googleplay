// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"google.golang.org/api/androidpublisher/v3"
)

func TestUsersDataSource(t *testing.T) {
	fake := newUnitFake(t)

	prefix := "developers/" + unitDeveloperID + "/users/"
	fake.PutUser(&androidpublisher.User{
		Email:                       "Zoe@example.com",
		Name:                        prefix + "Zoe@example.com",
		AccessState:                 "ACCESS_GRANTED",
		Partial:                     true,
		DeveloperAccountPermissions: []string{"CAN_MANAGE_PERMISSIONS_GLOBAL"},
	})
	fake.PutUser(&androidpublisher.User{
		Email:          "ada@example.com",
		Name:           prefix + "ada@example.com",
		AccessState:    "INVITED",
		ExpirationTime: "2099-06-30T12:00:00Z",
		Grants: []*androidpublisher.Grant{
			{
				Name:                prefix + "ada@example.com/grants/com.example.other",
				PackageName:         "com.example.other",
				AppLevelPermissions: []string{"CAN_MANAGE_PERMISSIONS"},
			},
			{
				Name:                prefix + "ada@example.com/grants/" + unitPackage,
				PackageName:         unitPackage,
				AppLevelPermissions: []string{"CAN_MANAGE_TRACK_APKS", "CAN_VIEW_NON_FINANCIAL_DATA"},
			},
			// A draft app has no package name, only an id in the name.
			{
				Name:                prefix + "ada@example.com/grants/4970000000000000000",
				AppLevelPermissions: []string{"CAN_MANAGE_DRAFT_APPS"},
			},
		},
	})

	const name = "data.googleplay_users.test"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
data "googleplay_users" "test" {}

output "grant_import_ids" {
  value = join(",", flatten([
    for user in data.googleplay_users.test.users : [
      for grant in user.grants : "${user.email}/${grant.package_name}" if grant.package_name != null
    ]
  ]))
}`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(name, "users.#", "2"),
				// Ordered by email, case-insensitively.
				resource.TestCheckResourceAttr(name, "users.0.email", "ada@example.com"),
				resource.TestCheckResourceAttr(name, "users.0.name", prefix+"ada@example.com"),
				resource.TestCheckResourceAttr(name, "users.0.access_state", "INVITED"),
				resource.TestCheckResourceAttr(name, "users.0.expiration_time", "2099-06-30T12:00:00Z"),
				resource.TestCheckResourceAttr(name, "users.0.partial", "false"),
				resource.TestCheckResourceAttr(name, "users.0.developer_account_permissions.#", "0"),
				resource.TestCheckResourceAttr(name, "users.0.grants.#", "3"),
				// Grants are ordered by package name; the draft app sorts first.
				resource.TestCheckNoResourceAttr(name, "users.0.grants.0.package_name"),
				resource.TestCheckResourceAttr(name, "users.0.grants.0.name", prefix+"ada@example.com/grants/4970000000000000000"),
				resource.TestCheckResourceAttr(name, "users.0.grants.1.package_name", unitPackage),
				resource.TestCheckResourceAttr(name, "users.0.grants.1.app_level_permissions.#", "2"),
				resource.TestCheckTypeSetElemAttr(name, "users.0.grants.1.app_level_permissions.*", "CAN_MANAGE_TRACK_APKS"),
				resource.TestCheckResourceAttr(name, "users.0.grants.2.package_name", "com.example.other"),
				resource.TestCheckResourceAttr(name, "users.1.email", "Zoe@example.com"),
				resource.TestCheckResourceAttr(name, "users.1.access_state", "ACCESS_GRANTED"),
				resource.TestCheckResourceAttr(name, "users.1.partial", "true"),
				resource.TestCheckNoResourceAttr(name, "users.1.expiration_time"),
				resource.TestCheckResourceAttr(name, "users.1.grants.#", "0"),
				resource.TestCheckTypeSetElemAttr(name, "users.1.developer_account_permissions.*", "CAN_MANAGE_PERMISSIONS_GLOBAL"),
				// The import ids a consumer derives from it.
				resource.TestCheckOutput("grant_import_ids",
					fmt.Sprintf("ada@example.com/%s,ada@example.com/com.example.other", unitPackage)),
				func(*terraform.State) error {
					lists := requestsMatching(fake, `^GET .*/users\?`)
					if len(lists) == 0 || len(requestsMatching(fake, `^GET .*/users\?.*pageSize=-1`)) != len(lists) {
						return fmt.Errorf("user lists must send pageSize=-1: %v", lists)
					}
					if writes := requestsMatching(fake, `^(POST|PATCH|PUT|DELETE) `); len(writes) != 0 {
						return fmt.Errorf("the data source wrote to the API: %v", writes)
					}

					return nil
				},
			),
		}},
	})
}

func TestUsersDataSource_requiresDeveloperID(t *testing.T) {
	newUnitFake(t)
	t.Setenv(envDeveloperID, "")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config:      `data "googleplay_users" "test" {}`,
			ExpectError: regexp.MustCompile(`Missing developer account id`),
		}},
	})
}

// --- acceptance --------------------------------------------------------------

// TestAccUsersDataSource_basic only reads: the account always has its owner.
func TestAccUsersDataSource_basic(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t, envDeveloperID) },
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: `data "googleplay_users" "test" {}`,
			Check:  resource.TestCheckResourceAttrSet("data.googleplay_users.test", "users.0.email"),
		}},
	})
}
