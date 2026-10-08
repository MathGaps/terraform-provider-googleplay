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

const oneTimeProductName = "googleplay_one_time_product.test"

func oneTimeProductConfig(packageName, productID, title, usPrice, buyState, extraOptions string) string {
	state := ""
	if buyState != "" {
		state = fmt.Sprintf("state = %q", buyState)
	}

	return fmt.Sprintf(`
resource "googleplay_one_time_product" "test" {
  package_name = %[1]q
  product_id   = %[2]q

  listings = {
    "en-US" = {
      title       = %[3]q
      description = "Removes every ad, forever."
    }
  }

  offer_tags = ["evergreen"]

  purchase_options = {
    buy = {
      %[5]s

      buy = {}

      regional_configs = {
        US = {
          price = { currency_code = "USD", amount = %[4]q }
        }
        JP = {
          price        = { currency_code = "JPY", amount = "1500" }
          availability = "AVAILABLE"
        }
      }

      new_regions_config = {
        usd_price = { currency_code = "USD", amount = %[4]q }
        eur_price = { currency_code = "EUR", amount = "9.49" }
      }
    }
    %[6]s
  }
}`, packageName, productID, title, usPrice, state, extraOptions)
}

func fakePurchaseOption(product *androidpublisher.OneTimeProduct, id string) *androidpublisher.OneTimeProductPurchaseOption {
	if product == nil {
		return nil
	}
	for _, option := range product.PurchaseOptions {
		if option.PurchaseOptionId == id {
			return option
		}
	}

	return nil
}

func checkPurchaseOption(fake *fakeplay.Server, productID, id, wantState string, wantUSUnits, wantUSNanos int64) resource.TestCheckFunc {
	return func(*terraform.State) error {
		option := fakePurchaseOption(fake.OneTimeProduct(unitPackage, productID), id)
		if option == nil {
			return fmt.Errorf("purchase option %s does not exist", id)
		}
		if option.State != wantState {
			return fmt.Errorf("purchase option %s is %s, want %s", id, option.State, wantState)
		}
		for _, config := range option.RegionalPricingAndAvailabilityConfigs {
			if config.RegionCode != "US" {
				continue
			}
			if config.Price == nil || config.Price.Units != wantUSUnits || config.Price.Nanos != wantUSNanos {
				return fmt.Errorf("US price of %s = %+v, want %d units and %d nanos", id, config.Price, wantUSUnits, wantUSNanos)
			}
		}

		return nil
	}
}

func TestOneTimeProductResource_lifecycle(t *testing.T) {
	fake := newUnitFake(t)

	const productID = "remove_ads"
	config := func(title, usPrice, buyState, extraOptions string) string {
		return oneTimeProductConfig(unitPackage, productID, title, usPrice, buyState, extraOptions)
	}
	buy := "purchase_options.buy."

	const rental = `
    rental = {
      state = "INACTIVE"

      rent = {
        rental_period = "P30D"
      }

      regional_configs = {
        US = {
          price = { currency_code = "USD", amount = "1.99" }
        }
      }
    }`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if fake.OneTimeProduct(unitPackage, productID) != nil {
				return fmt.Errorf("the one-time product was not deleted")
			}

			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: config("Remove ads", "9.90", "ACTIVE", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(oneTimeProductName, "id", unitPackage+"/"+productID),
					resource.TestCheckResourceAttr(oneTimeProductName, "regions_version", "2022/02"),
					resource.TestCheckResourceAttr(oneTimeProductName, buy+"state", "ACTIVE"),
					resource.TestCheckResourceAttr(oneTimeProductName, buy+"buy.legacy_compatible", "false"),
					resource.TestCheckResourceAttr(oneTimeProductName, buy+"regional_configs.US.price.amount", "9.90"),
					resource.TestCheckResourceAttr(oneTimeProductName, buy+"regional_configs.US.availability", "AVAILABLE"),
					resource.TestCheckResourceAttr(oneTimeProductName, buy+"new_regions_config.availability", "AVAILABLE"),
					// Assigned by the server, read back.
					resource.TestCheckResourceAttr(oneTimeProductName, buy+"withdrawal_right_type", "WITHDRAWAL_RIGHT_DIGITAL_CONTENT"),
					checkPurchaseOption(fake, productID, "buy", "ACTIVE", 9, 900_000_000),
					func(*terraform.State) error {
						// The API has no create call: creation is a patch that
						// allows the product to be missing.
						want := `^PATCH .*/onetimeproducts/remove_ads\?.*allowMissing=true.*regionsVersion\.version=2022%2F02`
						if len(requestsMatching(fake, want)) != 1 {
							return fmt.Errorf("unexpected create requests: %v", requestsMatching(fake, `^PATCH `))
						}

						return nil
					},
				),
			},
			importSteps(oneTimeProductName,
				buy+"regional_configs.US.price.amount",
				buy+"new_regions_config.usd_price.amount")[0],
			importSteps(oneTimeProductName)[1],
			{
				// The same amounts written differently plan nothing.
				Config:   config("Remove ads", "9.9", "ACTIVE", ""),
				PlanOnly: true,
			},
			{
				// A title change, a price change, and a second purchase option
				// that goes straight to inactive.
				Config: config("Remove all ads", "7.49", "ACTIVE", rental),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(oneTimeProductName, "listings.en-US.title", "Remove all ads"),
					resource.TestCheckResourceAttr(oneTimeProductName, "purchase_options.rental.state", "INACTIVE"),
					resource.TestCheckResourceAttr(oneTimeProductName, "purchase_options.rental.rent.rental_period", "P30D"),
					checkPurchaseOption(fake, productID, "buy", "ACTIVE", 7, 490_000_000),
					checkPurchaseOption(fake, productID, "rental", "INACTIVE", 1, 990_000_000),
					func(*terraform.State) error {
						want := `^PATCH .*/onetimeproducts/remove_ads\?.*updateMask=listings%2CpurchaseOptions$`
						if len(requestsMatching(fake, want)) != 1 {
							return fmt.Errorf("unexpected patch requests: %v", requestsMatching(fake, `^PATCH `))
						}

						return nil
					},
				),
			},
			{
				// Removing a purchase option deletes it, and the other is retired.
				Config: config("Remove all ads", "7.49", "INACTIVE", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(oneTimeProductName, "purchase_options.%", "1"),
					checkPurchaseOption(fake, productID, "buy", "INACTIVE", 7, 490_000_000),
					func(*terraform.State) error {
						if fakePurchaseOption(fake.OneTimeProduct(unitPackage, productID), "rental") != nil {
							return fmt.Errorf("the purchase option was not deleted")
						}
						if len(requestsMatching(fake, `purchaseOptions:batchDelete`)) != 1 {
							return fmt.Errorf("the purchase option was not deleted with purchaseOptions.batchDelete")
						}

						return nil
					},
				),
			},
		},
	})
}

// Creating must not adopt a product that already exists.
func TestOneTimeProductResource_createExistingFails(t *testing.T) {
	fake := newUnitFake(t)

	config := oneTimeProductConfig(unitPackage, "remove_ads", "Remove ads", "9.99", "", "")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				// Forget the product, then try to create it again.
				Config: fmt.Sprintf(`
removed {
  from = googleplay_one_time_product.test

  lifecycle {
    destroy = false
  }
}

data "googleplay_tracks" "seed" {
  package_name = %q
}`, unitPackage),
			},
			{
				Config:      config,
				ExpectError: regexp.MustCompile(`One-time product already exists`),
			},
		},
	})

	if fake.OneTimeProduct(unitPackage, "remove_ads") == nil {
		t.Error("the existing product was deleted")
	}
}

func TestConvertedRegionPricesDataSource(t *testing.T) {
	fake := newUnitFake(t)

	const name = "data.googleplay_converted_region_prices.test"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "googleplay_converted_region_prices" "test" {
  package_name = %[1]q
  price        = { currency_code = "USD", amount = "4.99" }
}

# The prices feed a product directly.
resource "googleplay_subscription" "derived" {
  package_name    = %[1]q
  product_id      = "derived"
  regions_version = data.googleplay_converted_region_prices.test.region_version

  listings = { "en-US" = { title = "Derived" } }

  base_plans = {
    monthly = {
      auto_renewing = { billing_period_duration = "P1M" }

      regional_configs = {
        for region, converted in data.googleplay_converted_region_prices.test.converted_region_prices :
        region => { price = converted.price, new_subscriber_availability = true }
      }

      other_regions_config = {
        usd_price = data.googleplay_converted_region_prices.test.converted_other_regions_price.usd_price
        eur_price = data.googleplay_converted_region_prices.test.converted_other_regions_price.eur_price
      }
    }
  }
}`, unitPackage),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(name, "region_version", fakeplay.RegionsVersion),
				resource.TestCheckResourceAttr(name, "converted_region_prices.%", "3"),
				resource.TestCheckResourceAttr(name, "converted_region_prices.US.price.currency_code", "USD"),
				resource.TestCheckResourceAttr(name, "converted_region_prices.US.price.amount", "4.99"),
				resource.TestCheckResourceAttr(name, "converted_region_prices.US.tax_amount.amount", "0"),
				resource.TestCheckResourceAttr(name, "converted_region_prices.GB.price.amount", "4.49"),
				resource.TestCheckResourceAttr(name, "converted_region_prices.GB.tax_amount.amount", "0.75"),
				resource.TestCheckResourceAttr(name, "converted_region_prices.JP.price.amount", "800"),
				resource.TestCheckResourceAttr(name, "converted_region_prices.JP.region_code", "JP"),
				resource.TestCheckResourceAttr(name, "converted_other_regions_price.eur_price.amount", "4.59"),
				resource.TestCheckResourceAttr("googleplay_subscription.derived", "base_plans.monthly.regional_configs.%", "3"),
				func(*terraform.State) error {
					plan := fakeBasePlan(fake.Subscription(unitPackage, "derived"), "monthly")
					price := fakeRegionalPrice(plan, "GB")
					if price == nil || price.CurrencyCode != "GBP" || price.Units != 4 || price.Nanos != 490_000_000 {
						return fmt.Errorf("the derived GB price = %+v", price)
					}

					return nil
				},
			),
		}},
	})
}

// --- acceptance --------------------------------------------------------------

// TestAccOneTimeProductResource_basic creates a one-time product with a draft
// purchase option under a randomised product id, updates it and deletes it.
func TestAccOneTimeProductResource_basic(t *testing.T) {
	packageName := os.Getenv(envTestPackage)
	productID := "tfacc_otp_" + acctest.RandStringFromCharSet(10, "abcdefghijklmnopqrstuvwxyz0123456789")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: oneTimeProductConfig(packageName, productID, "Terraform acceptance test", "9.99", "", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(oneTimeProductName, "purchase_options.buy.state", "DRAFT"),
					resource.TestCheckResourceAttr(oneTimeProductName, "purchase_options.buy.regional_configs.US.price.amount", "9.99"),
				),
			},
			{
				ResourceName:      oneTimeProductName,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config: oneTimeProductConfig(packageName, productID, "Terraform acceptance test (updated)", "8.99", "", ""),
				Check:  resource.TestCheckResourceAttr(oneTimeProductName, "purchase_options.buy.regional_configs.US.price.amount", "8.99"),
			},
		},
	})
}

func TestAccConvertedRegionPricesDataSource_basic(t *testing.T) {
	const name = "data.googleplay_converted_region_prices.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "googleplay_converted_region_prices" "test" {
  package_name = %q
  price        = { currency_code = "USD", amount = "4.99" }
}`, os.Getenv(envTestPackage)),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet(name, "region_version"),
				resource.TestCheckResourceAttr(name, "converted_region_prices.US.price.currency_code", "USD"),
				resource.TestCheckResourceAttrSet(name, "converted_other_regions_price.eur_price.amount"),
			),
		}},
	})
}
