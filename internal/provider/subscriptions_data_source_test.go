// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"google.golang.org/api/androidpublisher/v3"
)

func TestSubscriptionsDataSource(t *testing.T) {
	fake := newUnitFake(t)
	// One item per page, so every list takes several pages and the data source
	// has to follow the page tokens to see everything.
	fake.SetPageLimit(1)

	monthly := &androidpublisher.AutoRenewingBasePlanType{BillingPeriodDuration: "P1M"}
	fake.PutSubscription(&androidpublisher.Subscription{
		PackageName: unitPackage,
		ProductId:   "pro_monthly",
		Listings: []*androidpublisher.SubscriptionListing{
			{LanguageCode: "en-US", Title: "Pro"},
			{LanguageCode: "fr-FR", Title: "Pro (FR)"},
		},
		BasePlans: []*androidpublisher.BasePlan{{BasePlanId: "monthly", State: "ACTIVE", AutoRenewingBasePlanType: monthly}},
	})
	fake.PutSubscription(&androidpublisher.Subscription{
		PackageName: unitPackage,
		ProductId:   "max_yearly",
		Listings:    []*androidpublisher.SubscriptionListing{{LanguageCode: "en-US", Title: "Max"}},
		BasePlans: []*androidpublisher.BasePlan{
			{BasePlanId: "yearly", State: "DRAFT", AutoRenewingBasePlanType: &androidpublisher.AutoRenewingBasePlanType{BillingPeriodDuration: "P1Y"}},
			{BasePlanId: "top-up", State: "INACTIVE", PrepaidBasePlanType: &androidpublisher.PrepaidBasePlanType{BillingPeriodDuration: "P1M"}},
		},
	})
	fake.PutSubscription(&androidpublisher.Subscription{
		PackageName: unitPackage,
		ProductId:   "basic",
		Listings:    []*androidpublisher.SubscriptionListing{{LanguageCode: "en-US", Title: "Basic"}},
	})
	for _, offer := range []struct{ product, plan, id, state string }{
		{"pro_monthly", "monthly", "trial", "ACTIVE"},
		{"pro_monthly", "monthly", "intro", "DRAFT"},
		{"max_yearly", "yearly", "upgrade", "INACTIVE"},
	} {
		fake.PutOffer(&androidpublisher.SubscriptionOffer{
			PackageName: unitPackage, ProductId: offer.product, BasePlanId: offer.plan, OfferId: offer.id, State: offer.state,
		})
	}

	const name = "data.googleplay_subscriptions.test"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "googleplay_subscriptions" "test" {
  package_name = %q
}

output "offer_import_ids" {
  value = join(",", flatten([
    for sub in data.googleplay_subscriptions.test.subscriptions : [
      for plan in sub.base_plans : [
        for offer in plan.offers : "${data.googleplay_subscriptions.test.package_name}/${sub.product_id}/${plan.base_plan_id}/${offer.offer_id}"
      ]
    ]
  ]))
}`, unitPackage),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(name, "product_ids.#", "3"),
				resource.TestCheckTypeSetElemAttr(name, "product_ids.*", "basic"),
				// Ordered by product id.
				resource.TestCheckResourceAttr(name, "subscriptions.#", "3"),
				resource.TestCheckResourceAttr(name, "subscriptions.0.product_id", "basic"),
				resource.TestCheckResourceAttr(name, "subscriptions.0.base_plans.#", "0"),
				resource.TestCheckResourceAttr(name, "subscriptions.1.product_id", "max_yearly"),
				// Base plans are ordered by id.
				resource.TestCheckResourceAttr(name, "subscriptions.1.base_plans.0.base_plan_id", "top-up"),
				resource.TestCheckResourceAttr(name, "subscriptions.1.base_plans.0.type", "prepaid"),
				resource.TestCheckResourceAttr(name, "subscriptions.1.base_plans.0.state", "INACTIVE"),
				resource.TestCheckResourceAttr(name, "subscriptions.1.base_plans.0.offers.#", "0"),
				resource.TestCheckResourceAttr(name, "subscriptions.1.base_plans.1.base_plan_id", "yearly"),
				resource.TestCheckResourceAttr(name, "subscriptions.1.base_plans.1.billing_period_duration", "P1Y"),
				resource.TestCheckResourceAttr(name, "subscriptions.1.base_plans.1.offers.0.offer_id", "upgrade"),
				resource.TestCheckResourceAttr(name, "subscriptions.2.product_id", "pro_monthly"),
				resource.TestCheckResourceAttr(name, "subscriptions.2.titles.fr-FR", "Pro (FR)"),
				resource.TestCheckResourceAttr(name, "subscriptions.2.base_plans.0.type", "auto_renewing"),
				// Offers are ordered by id.
				resource.TestCheckResourceAttr(name, "subscriptions.2.base_plans.0.offers.#", "2"),
				resource.TestCheckResourceAttr(name, "subscriptions.2.base_plans.0.offers.0.offer_id", "intro"),
				resource.TestCheckResourceAttr(name, "subscriptions.2.base_plans.0.offers.0.state", "DRAFT"),
				resource.TestCheckResourceAttr(name, "subscriptions.2.base_plans.0.offers.1.offer_id", "trial"),
				// The import ids a consumer derives from it.
				resource.TestCheckOutput("offer_import_ids", fmt.Sprintf(
					"%[1]s/max_yearly/yearly/upgrade,%[1]s/pro_monthly/monthly/intro,%[1]s/pro_monthly/monthly/trial", unitPackage)),
				func(*terraform.State) error {
					// The offers are read in one list across the app, not per base plan.
					offerLists := requestsMatching(fake, `^GET .*/offers`)
					if len(requestsMatching(fake, `^GET .*/subscriptions/-/basePlans/-/offers\?`)) != len(offerLists) {
						return fmt.Errorf("offers must be listed across the app: %v", offerLists)
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

// --- acceptance --------------------------------------------------------------

// TestAccSubscriptionsDataSource_basic only reads. The test app may have no
// subscription at all, so it checks only that the list is read.
func TestAccSubscriptionsDataSource_basic(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "googleplay_subscriptions" "test" {
  package_name = %q
}`, os.Getenv(envTestPackage)),
			Check: resource.TestCheckResourceAttrSet("data.googleplay_subscriptions.test", "product_ids.#"),
		}},
	})
}
