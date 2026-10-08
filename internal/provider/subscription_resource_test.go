// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"google.golang.org/api/androidpublisher/v3"

	"github.com/MathGaps/terraform-provider-googleplay/internal/fakeplay"
)

const subscriptionName = "googleplay_subscription.test"

// subscriptionConfig renders a subscription with a monthly and a yearly base
// plan. extraPlans is appended to base_plans.
func subscriptionConfig(packageName, productID, title, usPrice, monthlyState, yearlyState, extraPlans string) string {
	state := func(value string) string {
		if value == "" {
			return ""
		}

		return fmt.Sprintf("state = %q", value)
	}

	return fmt.Sprintf(`
resource "googleplay_subscription" "test" {
  package_name = %[1]q
  product_id   = %[2]q

  listings = {
    "en-US" = {
      title       = %[3]q
      benefits    = ["No ads", "Offline access"]
      description = "Everything, everywhere."
    }
    "fr-FR" = {
      title = "Premium"
    }
  }

  base_plans = {
    monthly = {
      %[5]s

      auto_renewing = {
        billing_period_duration = "P1M"
      }

      offer_tags = ["standard"]

      regional_configs = {
        US = {
          price                       = { currency_code = "USD", amount = %[4]q }
          new_subscriber_availability = true
        }
        JP = {
          price                       = { currency_code = "JPY", amount = "500" }
          new_subscriber_availability = true
        }
        GB = {
          price = { currency_code = "GBP", amount = "0.99" }
        }
      }

      other_regions_config = {
        usd_price                   = { currency_code = "USD", amount = %[4]q }
        eur_price                   = { currency_code = "EUR", amount = "4.29" }
        new_subscriber_availability = true
      }
    }

    yearly = {
      %[6]s

      prepaid = {
        billing_period_duration = "P1Y"
      }
    }
    %[7]s
  }
}`, packageName, productID, title, usPrice, state(monthlyState), state(yearlyState), extraPlans)
}

func fakeBasePlan(sub *androidpublisher.Subscription, id string) *androidpublisher.BasePlan {
	if sub == nil {
		return nil
	}
	for _, plan := range sub.BasePlans {
		if plan.BasePlanId == id {
			return plan
		}
	}

	return nil
}

func fakeRegionalPrice(plan *androidpublisher.BasePlan, region string) *androidpublisher.Money {
	if plan == nil {
		return nil
	}
	for _, config := range plan.RegionalConfigs {
		if config.RegionCode == region {
			return config.Price
		}
	}

	return nil
}

// checkBasePlan asserts the state of a base plan and one regional price as the
// API holds them.
func checkBasePlan(fake *fakeplay.Server, productID, id, wantState, region string, wantUnits, wantNanos int64) resource.TestCheckFunc {
	return func(*terraform.State) error {
		plan := fakeBasePlan(fake.Subscription(unitPackage, productID), id)
		if plan == nil {
			return fmt.Errorf("base plan %s does not exist", id)
		}
		if plan.State != wantState {
			return fmt.Errorf("base plan %s is %s, want %s", id, plan.State, wantState)
		}
		if region == "" {
			return nil
		}
		price := fakeRegionalPrice(plan, region)
		if price == nil || price.Units != wantUnits || price.Nanos != wantNanos {
			return fmt.Errorf("price of %s in %s = %+v, want %d units and %d nanos", id, region, price, wantUnits, wantNanos)
		}

		return nil
	}
}

func TestSubscriptionResource_lifecycle(t *testing.T) {
	fake := newUnitFake(t)

	const productID = "premium"
	config := func(title, usPrice, monthlyState, yearlyState, extraPlans string) string {
		return subscriptionConfig(unitPackage, productID, title, usPrice, monthlyState, yearlyState, extraPlans)
	}
	monthly := "base_plans.monthly."

	patches := func() int { return len(requestsMatching(fake, `^PATCH .*/subscriptions/premium`)) }
	var patchesBefore int

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			// A subscription that has had a base plan active cannot be deleted:
			// destroy retires it and forgets it.
			sub := fake.Subscription(unitPackage, productID)
			if sub == nil {
				return fmt.Errorf("the fake deleted a published subscription")
			}
			for _, plan := range sub.BasePlans {
				if plan.State == "ACTIVE" {
					return fmt.Errorf("base plan %s was left active by destroy", plan.BasePlanId)
				}
			}

			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: config("Premium", "4.50", "ACTIVE", "", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(subscriptionName, "id", unitPackage+"/"+productID),
					resource.TestCheckResourceAttr(subscriptionName, "regions_version", "2022/02"),
					resource.TestCheckResourceAttr(subscriptionName, "listings.%", "2"),
					resource.TestCheckResourceAttr(subscriptionName, "listings.en-US.benefits.1", "Offline access"),
					resource.TestCheckNoResourceAttr(subscriptionName, "listings.fr-FR.description"),
					resource.TestCheckResourceAttr(subscriptionName, monthly+"state", "ACTIVE"),
					// The amount stays as written; the API holds it exactly.
					resource.TestCheckResourceAttr(subscriptionName, monthly+"regional_configs.US.price.amount", "4.50"),
					resource.TestCheckResourceAttr(subscriptionName, monthly+"regional_configs.GB.new_subscriber_availability", "false"),
					// Server defaults are read back, not planned as changes.
					resource.TestCheckResourceAttr(subscriptionName, monthly+"auto_renewing.grace_period_duration", "P3D"),
					resource.TestCheckResourceAttr(subscriptionName, monthly+"auto_renewing.resubscribe_state", "RESUBSCRIBE_STATE_ACTIVE"),
					resource.TestCheckResourceAttr(subscriptionName, monthly+"auto_renewing.legacy_compatible", "false"),
					resource.TestCheckNoResourceAttr(subscriptionName, monthly+"auto_renewing.account_hold_duration"),
					resource.TestCheckResourceAttr(subscriptionName, "base_plans.yearly.state", "DRAFT"),
					resource.TestCheckResourceAttr(subscriptionName, "base_plans.yearly.prepaid.time_extension", "TIME_EXTENSION_ACTIVE"),
					resource.TestCheckResourceAttr(subscriptionName,
						"tax_and_compliance_settings.eea_withdrawal_right_type", "WITHDRAWAL_RIGHT_DIGITAL_CONTENT"),
					checkBasePlan(fake, productID, "monthly", "ACTIVE", "US", 4, 500_000_000),
					checkBasePlan(fake, productID, "monthly", "ACTIVE", "JP", 500, 0),
					checkBasePlan(fake, productID, "monthly", "ACTIVE", "GB", 0, 990_000_000),
					checkBasePlan(fake, productID, "yearly", "DRAFT", "", 0, 0),
					func(*terraform.State) error {
						if len(requestsMatching(fake, `^POST .*/subscriptions\?.*productId=premium.*regionsVersion\.version=2022%2F02`)) != 1 {
							return fmt.Errorf("create did not send the product id and regions version: %v",
								requestsMatching(fake, `^POST .*/subscriptions\?`))
						}

						return nil
					},
				),
			},
			importSteps(subscriptionName,
				monthly+"regional_configs.US.price.amount",
				monthly+"other_regions_config.usd_price.amount")[0],
			importSteps(subscriptionName)[1],
			{
				// A listing and a price change, and both plans change state.
				Config: config("Premium Plus", "5.99", "INACTIVE", "ACTIVE", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(subscriptionName, "listings.en-US.title", "Premium Plus"),
					resource.TestCheckResourceAttr(subscriptionName, monthly+"state", "INACTIVE"),
					resource.TestCheckResourceAttr(subscriptionName, "base_plans.yearly.state", "ACTIVE"),
					checkBasePlan(fake, productID, "monthly", "INACTIVE", "US", 5, 990_000_000),
					checkBasePlan(fake, productID, "yearly", "ACTIVE", "", 0, 0),
					func(*terraform.State) error {
						// Only what changed is named in the mask, and the tax
						// settings the server assigned are not sent back.
						want := `^PATCH .*/subscriptions/premium\?.*regionsVersion\.version=2022%2F02.*updateMask=listings%2CbasePlans$`
						if len(requestsMatching(fake, want)) != 1 {
							return fmt.Errorf("unexpected patch requests: %v", requestsMatching(fake, `^PATCH `))
						}
						patchesBefore = patches()

						return nil
					},
				),
			},
			{
				// A state change alone is made with the state calls: no patch.
				Config: config("Premium Plus", "5.99", "ACTIVE", "ACTIVE", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					checkBasePlan(fake, productID, "monthly", "ACTIVE", "", 0, 0),
					func(*terraform.State) error {
						if got := patches(); got != patchesBefore {
							return fmt.Errorf("a state change sent %d patch requests", got-patchesBefore)
						}

						return nil
					},
				),
			},
			{
				// The same amount written differently is not a change.
				Config:   config("Premium Plus", "5.990", "ACTIVE", "ACTIVE", ""),
				PlanOnly: true,
			},
			{
				// A base plan that has been active cannot go back to draft...
				Config:      config("Premium Plus", "5.99", "DRAFT", "ACTIVE", ""),
				ExpectError: regexp.MustCompile(`it is ACTIVE and cannot return to DRAFT`),
			},
			{
				// ...and cannot be deleted either.
				Config: strings.Replace(config("Premium Plus", "5.99", "ACTIVE", "ACTIVE", ""),
					"yearly = {", "annual = {", 1),
				ExpectError: regexp.MustCompile(`Base plan cannot be deleted`),
			},
			{
				// A draft base plan can be added...
				Config: config("Premium Plus", "5.99", "ACTIVE", "ACTIVE", `
    weekly = {
      installments = {
        billing_period_duration  = "P1M"
        committed_payments_count = 12
        renewal_type             = "RENEWAL_TYPE_RENEWS_WITHOUT_COMMITMENT"
      }
    }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(subscriptionName, "base_plans.weekly.state", "DRAFT"),
					resource.TestCheckResourceAttr(subscriptionName, "base_plans.weekly.installments.committed_payments_count", "12"),
					checkBasePlan(fake, productID, "weekly", "DRAFT", "", 0, 0),
				),
			},
			{
				// ...and removed again, with the dedicated delete call.
				Config: config("Premium Plus", "5.99", "ACTIVE", "ACTIVE", ""),
				Check: func(*terraform.State) error {
					if fakeBasePlan(fake.Subscription(unitPackage, productID), "weekly") != nil {
						return fmt.Errorf("the draft base plan was not deleted")
					}
					if len(requestsMatching(fake, `^DELETE .*/subscriptions/premium/basePlans/weekly\?`)) != 1 {
						return fmt.Errorf("the base plan was not deleted with basePlans.delete")
					}

					return nil
				},
			},
		},
	})
}

// A subscription whose base plans were never activated is really deleted.
func TestSubscriptionResource_draftIsDeleted(t *testing.T) {
	fake := newUnitFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if fake.Subscription(unitPackage, "draft_only") != nil {
				return fmt.Errorf("the draft subscription was not deleted")
			}

			return nil
		},
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "googleplay_subscription" "test" {
  package_name      = %q
  product_id        = "draft_only"
  regions_version   = "2025/03"
  latency_tolerance = "PRODUCT_UPDATE_LATENCY_TOLERANCE_LATENCY_TOLERANT"

  listings = {
    "en-US" = { title = "Draft" }
  }
}`, unitPackage),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckNoResourceAttr(subscriptionName, "base_plans"),
				resource.TestCheckResourceAttr(subscriptionName, "regions_version", "2025/03"),
				func(*terraform.State) error {
					if len(requestsMatching(fake, `regionsVersion\.version=2025%2F03`)) != 1 {
						return fmt.Errorf("the configured regions version was not sent")
					}

					return nil
				},
			),
		}},
	})
}

// The import-first flow: a subscription made in Play Console is imported and
// the matching configuration plans nothing, although it writes amounts and
// leaves server-assigned settings out in its own way.
func TestSubscriptionResource_importExisting(t *testing.T) {
	fake := newUnitFake(t)
	fake.PutSubscription(&androidpublisher.Subscription{
		PackageName: unitPackage,
		ProductId:   "legacy",
		Listings: []*androidpublisher.SubscriptionListing{
			{LanguageCode: "fr-FR", Title: "Premium"},
			{LanguageCode: "en-US", Title: "Premium", Benefits: []string{"No ads", "Offline access"}, Description: "Everything, everywhere."},
		},
		BasePlans: []*androidpublisher.BasePlan{
			{
				BasePlanId: "yearly",
				State:      "DRAFT",
				PrepaidBasePlanType: &androidpublisher.PrepaidBasePlanType{
					BillingPeriodDuration: "P1Y",
					TimeExtension:         "TIME_EXTENSION_ACTIVE",
				},
			},
			{
				BasePlanId: "monthly",
				State:      "ACTIVE",
				AutoRenewingBasePlanType: &androidpublisher.AutoRenewingBasePlanType{
					BillingPeriodDuration: "P1M",
					GracePeriodDuration:   "P7D",
					ResubscribeState:      "RESUBSCRIBE_STATE_ACTIVE",
					ProrationMode:         "SUBSCRIPTION_PRORATION_MODE_CHARGE_ON_NEXT_BILLING_DATE",
				},
				OfferTags: []*androidpublisher.OfferTag{{Tag: "standard"}},
				RegionalConfigs: []*androidpublisher.RegionalBasePlanConfig{
					{RegionCode: "JP", NewSubscriberAvailability: true, Price: &androidpublisher.Money{CurrencyCode: "JPY", Units: 500}},
					{RegionCode: "GB", Price: &androidpublisher.Money{CurrencyCode: "GBP", Nanos: 990_000_000}},
					{RegionCode: "US", NewSubscriberAvailability: true, Price: &androidpublisher.Money{CurrencyCode: "USD", Units: 4, Nanos: 500_000_000}},
				},
				OtherRegionsConfig: &androidpublisher.OtherRegionsBasePlanConfig{
					NewSubscriberAvailability: true,
					UsdPrice:                  &androidpublisher.Money{CurrencyCode: "USD", Units: 4, Nanos: 500_000_000},
					EurPrice:                  &androidpublisher.Money{CurrencyCode: "EUR", Units: 4, Nanos: 290_000_000},
				},
			},
		},
		TaxAndComplianceSettings: &androidpublisher.SubscriptionTaxAndComplianceSettings{
			EeaWithdrawalRightType: "WITHDRAWAL_RIGHT_SERVICE",
			TaxRateInfoByRegionCode: map[string]androidpublisher.RegionalTaxRateInfo{
				"US": {EligibleForStreamingServiceTaxRate: true},
			},
		},
	})

	config := subscriptionConfig(unitPackage, "legacy", "Premium", "4.50", "ACTIVE", "", "")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:             config,
				ResourceName:       subscriptionName,
				ImportState:        true,
				ImportStateId:      unitPackage + "/legacy",
				ImportStatePersist: true,
			},
			{
				Config:   config,
				PlanOnly: true,
			},
			{
				// Applying it is a no-op too: nothing has been written so far.
				Config: config,
				Check: func(*terraform.State) error {
					if writes := requestsMatching(fake, `^(POST|PATCH|PUT|DELETE) `); len(writes) != 0 {
						return fmt.Errorf("importing wrote to the API: %v", writes)
					}

					return nil
				},
			},
		},
	})
}

func TestSubscriptionResource_validation(t *testing.T) {
	newUnitFake(t)

	config := func(body string) string {
		return fmt.Sprintf(`
resource "googleplay_subscription" "test" {
  package_name = %q
  product_id   = "premium"
  listings     = { "en-US" = { title = "Premium" } }
  base_plans   = { monthly = { %s } }
}`, unitPackage, body)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      config(``),
				ExpectError: regexp.MustCompile(`(?s)auto_renewing.*prepaid.*installments`),
			},
			{
				Config: config(`
auto_renewing = { billing_period_duration = "P1M" }
prepaid       = { billing_period_duration = "P1M" }`),
				ExpectError: regexp.MustCompile(`(?s)auto_renewing.*prepaid`),
			},
			{
				Config: config(`
auto_renewing    = { billing_period_duration = "P1M" }
regional_configs = { US = { price = { currency_code = "USD", amount = "4,99" } } }`),
				ExpectError: regexp.MustCompile(`Invalid decimal amount`),
			},
			{
				Config: config(`
auto_renewing    = { billing_period_duration = "one month" }`),
				ExpectError: regexp.MustCompile(`ISO 8601 duration`),
			},
		},
	})
}

// --- acceptance --------------------------------------------------------------

// TestAccSubscriptionResource_basic creates a subscription with a draft base
// plan under a randomised product id, updates it and deletes it. Nothing is
// ever activated, so the delete succeeds; the product id stays reserved.
func TestAccSubscriptionResource_basic(t *testing.T) {
	packageName := os.Getenv(envTestPackage)
	productID := "tfacc_sub_" + acctest.RandStringFromCharSet(10, "abcdefghijklmnopqrstuvwxyz0123456789")

	config := func(title, price string) string {
		return fmt.Sprintf(`
resource "googleplay_subscription" "test" {
  package_name = %q
  product_id   = %q

  listings = {
    "en-US" = {
      title    = %q
      benefits = ["Acceptance test"]
    }
  }

  base_plans = {
    monthly = {
      auto_renewing = {
        billing_period_duration = "P1M"
      }

      regional_configs = {
        US = {
          price                       = { currency_code = "USD", amount = %q }
          new_subscriber_availability = true
        }
      }
    }
  }
}`, packageName, productID, title, price)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("Terraform acceptance test", "4.99"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(subscriptionName, "base_plans.monthly.state", "DRAFT"),
					resource.TestCheckResourceAttr(subscriptionName, "base_plans.monthly.regional_configs.US.price.amount", "4.99"),
				),
			},
			{
				ResourceName:      subscriptionName,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config: config("Terraform acceptance test (updated)", "5.49"),
				Check:  resource.TestCheckResourceAttr(subscriptionName, "base_plans.monthly.regional_configs.US.price.amount", "5.49"),
			},
		},
	})
}
