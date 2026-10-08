resource "googleplay_one_time_product" "remove_ads" {
  package_name = "com.example.app"
  product_id   = "remove_ads"

  listings = {
    "en-US" = {
      title       = "Remove ads"
      description = "Removes every ad from the app, for good."
    }
  }

  purchase_options = {
    # A new purchase option is a draft until its state says otherwise.
    standard = {
      state = "ACTIVE"

      # A plain purchase. Use `rent = { rental_period = "P30D" }` for a rental.
      buy = {
        legacy_compatible = true
      }

      regional_configs = {
        US = {
          price = { currency_code = "USD", amount = "9.99" }
        }
        GB = {
          price = { currency_code = "GBP", amount = "8.99" }
        }
        JP = {
          price = { currency_code = "JPY", amount = "1500" }
        }
      }

      # Prices for any region Google Play launches in later.
      new_regions_config = {
        usd_price = { currency_code = "USD", amount = "9.99" }
        eur_price = { currency_code = "EUR", amount = "9.49" }
      }
    }
  }
}
