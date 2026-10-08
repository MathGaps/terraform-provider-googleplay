# What Google Play would charge in every region for a 4.99 USD product, at
# today's exchange rates.
data "googleplay_converted_region_prices" "premium" {
  package_name = "com.example.app"

  price = {
    currency_code = "USD"
    amount        = "4.99"
  }
}

output "price_in_japan" {
  value = data.googleplay_converted_region_prices.premium.converted_region_prices["JP"].price
}

# The result has the shape a product's regional configuration takes. Note that
# it moves with exchange rates: a product wired to it like this is repriced
# whenever they do.
resource "googleplay_subscription" "premium" {
  package_name    = "com.example.app"
  product_id      = "premium"
  regions_version = data.googleplay_converted_region_prices.premium.region_version

  listings = {
    "en-US" = { title = "Premium" }
  }

  base_plans = {
    monthly = {
      auto_renewing = {
        billing_period_duration = "P1M"
      }

      regional_configs = {
        for region, converted in data.googleplay_converted_region_prices.premium.converted_region_prices :
        region => {
          price                       = converted.price
          new_subscriber_availability = true
        }
      }

      other_regions_config = {
        usd_price                   = data.googleplay_converted_region_prices.premium.converted_other_regions_price.usd_price
        eur_price                   = data.googleplay_converted_region_prices.premium.converted_other_regions_price.eur_price
        new_subscriber_availability = true
      }
    }
  }
}
