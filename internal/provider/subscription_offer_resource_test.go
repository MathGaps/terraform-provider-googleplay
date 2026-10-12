// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"google.golang.org/api/androidpublisher/v3"

	"github.com/MathGaps/terraform-provider-googleplay/internal/fakeplay"
)

const offerName = "googleplay_subscription_offer.test"

// putOfferSubscription stores a subscription with an active monthly
// auto-renewing base plan and a prepaid one, as if made in Play Console.
func putOfferSubscription(fake *fakeplay.Server, productID string) {
	fake.PutSubscription(&androidpublisher.Subscription{
		PackageName: unitPackage,
		ProductId:   productID,
		Listings:    []*androidpublisher.SubscriptionListing{{LanguageCode: "en-US", Title: "Pro"}},
		BasePlans: []*androidpublisher.BasePlan{
			{
				BasePlanId:               "monthly",
				State:                    "ACTIVE",
				AutoRenewingBasePlanType: &androidpublisher.AutoRenewingBasePlanType{BillingPeriodDuration: "P1M"},
				RegionalConfigs: []*androidpublisher.RegionalBasePlanConfig{
					{RegionCode: "US", NewSubscriberAvailability: true, Price: &androidpublisher.Money{CurrencyCode: "USD", Units: 9, Nanos: 990_000_000}},
					{RegionCode: "GB", NewSubscriberAvailability: true, Price: &androidpublisher.Money{CurrencyCode: "GBP", Units: 8, Nanos: 990_000_000}},
				},
			},
			{
				BasePlanId:          "prepaid",
				State:               "ACTIVE",
				PrepaidBasePlanType: &androidpublisher.PrepaidBasePlanType{BillingPeriodDuration: "P1M"},
			},
		},
	})
}

// offerConfig renders an offer on the monthly base plan of "pro": a week's
// free trial, then three discounted months. state, the second phase's GB
// price and targeting are what the steps change.
func offerConfig(offerID, state, gbPrice, targeting string) string {
	stateLine := ""
	if state != "" {
		stateLine = fmt.Sprintf("state = %q", state)
	}

	return fmt.Sprintf(`
resource "googleplay_subscription_offer" "test" {
  package_name = %[1]q
  product_id   = "pro"
  base_plan_id = "monthly"
  offer_id     = %[2]q
  %[3]s

  regional_configs = {
    US = { new_subscriber_availability = true }
    GB = { new_subscriber_availability = true }
  }

  other_regions_config = {
    new_subscriber_availability = true
  }

  phases = [
    {
      duration         = "P1W"
      recurrence_count = 1
      regional_configs = {
        US = { free = {} }
        GB = { free = {} }
      }
      other_regions_config = { free = {} }
    },
    {
      duration         = "P1M"
      recurrence_count = 3
      regional_configs = {
        US = { relative_discount = 0.5 }
        GB = { price = { currency_code = "GBP", amount = %[4]q } }
      }
      other_regions_config = {
        absolute_discount = {
          usd_price = { currency_code = "USD", amount = "1.00" }
          eur_price = { currency_code = "EUR", amount = "1" }
        }
      }
    },
  ]

  %[5]s

  offer_tags = ["intro"]
}`, unitPackage, offerID, stateLine, gbPrice, targeting)
}

const (
	acquisitionTargeting = `targeting = { acquisition_rule = { scope = { this_subscription = {} } } }`
	upgradeTargeting     = `targeting = {
    upgrade_rule = {
      scope                   = { specific_subscription_in_app = "basic" }
      billing_period_duration = "P1M"
      once_per_user           = true
    }
  }`
)

// checkOffer asserts the state of an offer as the API holds it.
func checkOffer(fake *fakeplay.Server, offerID, wantState string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		offer := fake.Offer(unitPackage, "pro", "monthly", offerID)
		if offer == nil {
			return fmt.Errorf("offer %s does not exist", offerID)
		}
		if offer.State != wantState {
			return fmt.Errorf("offer %s is %s, want %s", offerID, offer.State, wantState)
		}

		return nil
	}
}

func TestSubscriptionOfferResource_lifecycle(t *testing.T) {
	fake := newUnitFake(t)
	putOfferSubscription(fake, "pro")

	patches := func() int { return len(requestsMatching(fake, `^PATCH .*/offers/intro`)) }
	var patchesBefore int

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			// An offer that has been active cannot be deleted: destroy leaves
			// it inactive and forgets it.
			return checkOffer(fake, "intro", "INACTIVE")(nil)
		},
		Steps: []resource.TestStep{
			{
				Config: offerConfig("intro", "ACTIVE", "4.50", acquisitionTargeting),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(offerName, "id", unitPackage+"/pro/monthly/intro"),
					resource.TestCheckResourceAttr(offerName, "state", "ACTIVE"),
					resource.TestCheckResourceAttr(offerName, "regions_version", "2022/02"),
					resource.TestCheckResourceAttr(offerName, "phases.#", "2"),
					resource.TestCheckResourceAttr(offerName, "phases.1.regional_configs.US.relative_discount", "0.5"),
					resource.TestCheckResourceAttr(offerName, "phases.1.regional_configs.GB.price.amount", "4.50"),
					resource.TestCheckResourceAttr(offerName, "targeting.upgrade_rule.%", "0"),
					checkOffer(fake, "intro", "ACTIVE"),
					func(*terraform.State) error {
						offer := fake.Offer(unitPackage, "pro", "monthly", "intro")
						trial := offer.Phases[0]
						if trial.Duration != "P1W" || trial.RegionalConfigs[0].Free == nil || trial.OtherRegionsConfig.Free == nil {
							return fmt.Errorf("the free trial was not sent as free: %+v", trial)
						}
						gb := offer.Phases[1].RegionalConfigs[0]
						if gb.RegionCode != "GB" || gb.Price.Units != 4 || gb.Price.Nanos != 500_000_000 {
							return fmt.Errorf("GB price = %+v, want 4 units and 500000000 nanos", gb)
						}
						if offer.Targeting.AcquisitionRule.Scope.ThisSubscription == nil {
							return fmt.Errorf("the acquisition rule was not sent: %+v", offer.Targeting)
						}
						want := `^POST .*/subscriptions/pro/basePlans/monthly/offers\?.*offerId=intro.*regionsVersion\.version=2022%2F02`
						if len(requestsMatching(fake, want)) != 1 {
							return fmt.Errorf("create did not send the offer id and regions version: %v",
								requestsMatching(fake, `^POST .*/offers`))
						}

						return nil
					},
				),
			},
			importSteps(offerName, "phases.1.regional_configs.GB.price.amount",
				"phases.1.other_regions_config.absolute_discount.usd_price.amount")[0],
			importSteps(offerName)[1],
			{
				// A price and the targeting change: one patch naming both.
				Config: offerConfig("intro", "ACTIVE", "3.99", upgradeTargeting),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(offerName, "phases.1.regional_configs.GB.price.amount", "3.99"),
					resource.TestCheckResourceAttr(offerName, "targeting.upgrade_rule.scope.specific_subscription_in_app", "basic"),
					resource.TestCheckResourceAttr(offerName, "targeting.upgrade_rule.once_per_user", "true"),
					checkOffer(fake, "intro", "ACTIVE"),
					func(*terraform.State) error {
						want := `^PATCH .*/offers/intro\?.*regionsVersion\.version=2022%2F02.*updateMask=phases%2Ctargeting$`
						if len(requestsMatching(fake, want)) != 1 {
							return fmt.Errorf("unexpected patch requests: %v", requestsMatching(fake, `^PATCH `))
						}
						patchesBefore = patches()

						return nil
					},
				),
			},
			{
				// The same amount written differently is not a change.
				Config:   offerConfig("intro", "ACTIVE", "3.990", upgradeTargeting),
				PlanOnly: true,
			},
			{
				// A state change alone is made with the state calls: no patch.
				Config: offerConfig("intro", "INACTIVE", "3.99", upgradeTargeting),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(offerName, "state", "INACTIVE"),
					checkOffer(fake, "intro", "INACTIVE"),
					func(*terraform.State) error {
						if got := patches(); got != patchesBefore {
							return fmt.Errorf("a state change sent %d patch requests", got-patchesBefore)
						}

						return nil
					},
				),
			},
			{
				// An offer that has left DRAFT cannot return to it.
				Config:      offerConfig("intro", "DRAFT", "3.99", upgradeTargeting),
				ExpectError: regexp.MustCompile(`it is INACTIVE and cannot return to DRAFT`),
			},
			{
				// Back to active, so that destroy has to deactivate it.
				Config: offerConfig("intro", "ACTIVE", "3.99", upgradeTargeting),
				Check:  checkOffer(fake, "intro", "ACTIVE"),
			},
		},
	})

	if len(requestsMatching(fake, `^POST .*/offers/intro:deactivate`)) != 2 {
		t.Errorf("destroy did not deactivate the active offer: %v", requestsMatching(fake, `:deactivate`))
	}
}

// Destroying an active offer, which the API refuses to delete, retires it:
// it is deactivated and dropped from state with a warning, never left on sale.
func TestSubscriptionOfferResource_activeIsRetired(t *testing.T) {
	fake := newUnitFake(t)
	putOfferSubscription(fake, "pro")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if len(requestsMatching(fake, `^DELETE .*/offers/launch`)) != 1 {
				return fmt.Errorf("destroy did not try to delete the offer first")
			}

			return checkOffer(fake, "launch", "INACTIVE")(nil)
		},
		Steps: []resource.TestStep{{
			Config: offerConfig("launch", "ACTIVE", "4.50", ""),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckNoResourceAttr(offerName, "targeting"),
				checkOffer(fake, "launch", "ACTIVE"),
			),
		}},
	})
}

// A step that fails after the offer was created leaves it in state, untainted,
// as configured: its id can never be used again, so an offer Terraform forgot,
// tainted, or refused as an inconsistent result would have to be created a
// second time, which Google Play refuses.
func TestSubscriptionOfferResource_failureAfterCreate(t *testing.T) {
	for name, tc := range map[string]struct {
		method, path string
		// pending is whether the step that failed is left for the next apply.
		pending bool
	}{
		"the read after create":       {"GET", `/offers/stranded$`, false},
		"the activation after create": {"POST", `/offers/stranded:activate$`, true},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newUnitFake(t)
			putOfferSubscription(fake, "pro")
			fake.FailNext(tc.method, tc.path, 403, "PERMISSION_DENIED")

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories,
				Steps: []resource.TestStep{
					{
						// The failure is a warning; what is left shows in the next plan.
						Config:             offerConfig("stranded", "ACTIVE", "4.50", ""),
						ExpectNonEmptyPlan: tc.pending,
					},
					{
						// The same configuration again finishes the job in place: a
						// forgotten or tainted offer would be created a second time,
						// and the fake refuses a taken id.
						Config: offerConfig("stranded", "ACTIVE", "4.50", ""),
						Check: resource.ComposeAggregateTestCheckFunc(
							resource.TestCheckResourceAttr(offerName, "state", "ACTIVE"),
							checkOffer(fake, "stranded", "ACTIVE"),
							func(*terraform.State) error {
								if creates := requestsMatching(fake, `^POST .*/offers\?.*offerId=stranded`); len(creates) != 1 {
									return fmt.Errorf("the offer was created %d times, want once: %v", len(creates), creates)
								}

								return nil
							},
						),
					},
				},
			})
		})
	}
}

// A draft offer is really deleted, and its id cannot be used again.
func TestSubscriptionOfferResource_draftIsDeleted(t *testing.T) {
	fake := newUnitFake(t)
	putOfferSubscription(fake, "pro")

	config := offerConfig("trial", "", "4.50", acquisitionTargeting)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if fake.Offer(unitPackage, "pro", "monthly", "trial") != nil {
				return fmt.Errorf("the draft offer was not deleted")
			}

			return nil
		},
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(offerName, "state", "DRAFT"),
				checkOffer(fake, "trial", "DRAFT"),
			),
		}},
	})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`cannot be reused`),
		}},
	})
}

// The import-first flow: an offer made in Play Console is imported and the
// matching configuration plans nothing.
func TestSubscriptionOfferResource_importExisting(t *testing.T) {
	fake := newUnitFake(t)
	putOfferSubscription(fake, "pro")

	usd := func(units, nanos int64) *androidpublisher.Money {
		return &androidpublisher.Money{CurrencyCode: "USD", Units: units, Nanos: nanos}
	}
	fake.PutOffer(&androidpublisher.SubscriptionOffer{
		PackageName: unitPackage,
		ProductId:   "pro",
		BasePlanId:  "monthly",
		OfferId:     "winback",
		State:       "ACTIVE",
		RegionalConfigs: []*androidpublisher.RegionalSubscriptionOfferConfig{
			{RegionCode: "US", NewSubscriberAvailability: true},
		},
		Phases: []*androidpublisher.SubscriptionOfferPhase{{
			Duration:        "P1M",
			RecurrenceCount: 2,
			RegionalConfigs: []*androidpublisher.RegionalSubscriptionOfferPhaseConfig{
				{RegionCode: "US", AbsoluteDiscount: usd(2, 500_000_000)},
			},
			OtherRegionsConfig: &androidpublisher.OtherRegionsSubscriptionOfferPhaseConfig{
				OtherRegionsPrices: &androidpublisher.OtherRegionsSubscriptionOfferPhasePrices{
					UsdPrice: usd(4, 0),
					EurPrice: &androidpublisher.Money{CurrencyCode: "EUR", Units: 4},
				},
			},
		}},
		Targeting: &androidpublisher.SubscriptionOfferTargeting{
			AcquisitionRule: &androidpublisher.AcquisitionTargetingRule{
				Scope: &androidpublisher.TargetingRuleScope{AnySubscriptionInApp: &androidpublisher.TargetingRuleScopeAnySubscriptionInApp{}},
			},
		},
	})

	config := fmt.Sprintf(`
resource "googleplay_subscription_offer" "test" {
  package_name = %q
  product_id   = "pro"
  base_plan_id = "monthly"
  offer_id     = "winback"
  state        = "ACTIVE"

  regional_configs = {
    US = { new_subscriber_availability = true }
  }

  phases = [{
    duration         = "P1M"
    recurrence_count = 2
    regional_configs = {
      US = { absolute_discount = { currency_code = "USD", amount = "2.50" } }
    }
    other_regions_config = {
      price = {
        usd_price = { currency_code = "USD", amount = "4.00" }
        eur_price = { currency_code = "EUR", amount = "4" }
      }
    }
  }]

  targeting = { acquisition_rule = { scope = { any_subscription_in_app = {} } } }
}`, unitPackage)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:             config,
				ResourceName:       offerName,
				ImportState:        true,
				ImportStateId:      unitPackage + "/pro/monthly/winback",
				ImportStatePersist: true,
			},
			{
				Config:   config,
				PlanOnly: true,
			},
			{
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

// Only an auto-renewing base plan can have offers; the API's refusal is shown.
func TestSubscriptionOfferResource_prepaidBasePlanRefused(t *testing.T) {
	fake := newUnitFake(t)
	putOfferSubscription(fake, "pro")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "googleplay_subscription_offer" "test" {
  package_name     = %q
  product_id       = "pro"
  base_plan_id     = "prepaid"
  offer_id         = "trial"
  regional_configs = { US = {} }
  phases = [{
    duration         = "P1W"
    recurrence_count = 1
    regional_configs = { US = { free = {} } }
  }]
}`, unitPackage),
			ExpectError: regexp.MustCompile(`(?s)Only auto-renewing base plans can have\s+subscription offers`),
		}},
	})
}

func TestSubscriptionOfferResource_validation(t *testing.T) {
	newUnitFake(t)

	config := func(phases, extra string) string {
		return fmt.Sprintf(`
resource "googleplay_subscription_offer" "test" {
  package_name     = %q
  product_id       = "pro"
  base_plan_id     = "monthly"
  offer_id         = "trial"
  regional_configs = { US = {}, GB = {} }
  phases           = [%s]
  %s
}`, unitPackage, phases, extra)
	}
	free := `{ duration = "P1W", recurrence_count = 1, regional_configs = { US = { free = {} }, GB = { free = {} } } }`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      config(`{ duration = "P1W", recurrence_count = 1, regional_configs = { US = { free = {} } } }`, ""),
				ExpectError: regexp.MustCompile(`Phase does not price the offer's regions`),
			},
			{
				Config: config(`{ duration = "P1W", recurrence_count = 1, regional_configs = {
  US = { free = {}, relative_discount = 0.5 }
  GB = { free = {} }
} }`, ""),
				ExpectError: regexp.MustCompile(`(?s)price.*absolute_discount.*relative_discount.*free`),
			},
			{
				Config: config(`{ duration = "P1W", recurrence_count = 1, regional_configs = {
  US = { relative_discount = 1 }
  GB = { free = {} }
} }`, ""),
				ExpectError: regexp.MustCompile(`relative_discount`),
			},
			{
				Config:      config(free+", "+free+", "+free, ""),
				ExpectError: regexp.MustCompile(`(?s)phases.*at most 2`),
			},
			{
				Config: config(free, `targeting = {
  acquisition_rule = { scope = { this_subscription = {} } }
  upgrade_rule     = { scope = { this_subscription = {} } }
}`),
				ExpectError: regexp.MustCompile(`(?s)acquisition_rule.*upgrade_rule`),
			},
			{
				Config:      config(free, `targeting = { acquisition_rule = { scope = { this_subscription = {}, any_subscription_in_app = {} } } }`),
				ExpectError: regexp.MustCompile(`(?s)this_subscription.*any_subscription_in_app`),
			},
		},
	})
}

// --- acceptance --------------------------------------------------------------

// TestAccSubscriptionOfferResource_basic creates a subscription with a draft
// auto-renewing base plan and a draft free-trial offer on it, under a
// randomised product id, updates the offer and deletes both. Nothing is ever
// activated, so both deletes succeed; the product id and the offer id stay
// reserved.
func TestAccSubscriptionOfferResource_basic(t *testing.T) {
	packageName := os.Getenv(envTestPackage)
	productID := "tfacc_offer_" + acctest.RandStringFromCharSet(10, "abcdefghijklmnopqrstuvwxyz0123456789")

	config := func(trial string) string {
		return fmt.Sprintf(`
resource "googleplay_subscription" "test" {
  package_name = %[1]q
  product_id   = %[2]q

  listings = {
    "en-US" = { title = "Terraform acceptance test" }
  }

  base_plans = {
    monthly = {
      auto_renewing = {
        billing_period_duration = "P1M"
      }

      regional_configs = {
        US = {
          price                       = { currency_code = "USD", amount = "4.99" }
          new_subscriber_availability = true
        }
      }
    }
  }
}

resource "googleplay_subscription_offer" "test" {
  package_name = googleplay_subscription.test.package_name
  product_id   = googleplay_subscription.test.product_id
  base_plan_id = "monthly"
  offer_id     = "trial"

  regional_configs = {
    US = { new_subscriber_availability = true }
  }

  phases = [{
    duration         = %[3]q
    recurrence_count = 1
    regional_configs = {
      US = { free = {} }
    }
  }]

  targeting = { acquisition_rule = { scope = { this_subscription = {} } } }
}`, packageName, productID, trial)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("P1W"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(offerName, "state", "DRAFT"),
					resource.TestCheckResourceAttr(offerName, "phases.0.duration", "P1W"),
				),
			},
			{
				ResourceName:      offerName,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config: config("P2W"),
				Check:  resource.TestCheckResourceAttr(offerName, "phases.0.duration", "P2W"),
			},
		},
	})
}
