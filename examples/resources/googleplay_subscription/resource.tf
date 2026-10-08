resource "googleplay_subscription" "premium" {
  package_name = "com.example.app"
  product_id   = "premium"

  listings = {
    "en-US" = {
      title       = "Premium"
      description = "Everything in the app, on every device."
      benefits    = ["No ads", "Offline access", "Family sharing"]
    }
    "fr-FR" = {
      title = "Premium"
    }
  }

  base_plans = {
    # A new base plan is a draft until its state says otherwise.
    monthly = {
      state = "ACTIVE"

      auto_renewing = {
        billing_period_duration = "P1M"
        grace_period_duration   = "P7D"
      }

      regional_configs = {
        US = {
          price                       = { currency_code = "USD", amount = "4.99" }
          new_subscriber_availability = true
        }
        GB = {
          price                       = { currency_code = "GBP", amount = "4.49" }
          new_subscriber_availability = true
        }
        JP = {
          price                       = { currency_code = "JPY", amount = "800" }
          new_subscriber_availability = true
        }
      }

      # Prices for any region Google Play launches in later.
      other_regions_config = {
        usd_price                   = { currency_code = "USD", amount = "4.99" }
        eur_price                   = { currency_code = "EUR", amount = "4.59" }
        new_subscriber_availability = true
      }
    }

    yearly = {
      state = "ACTIVE"

      auto_renewing = {
        billing_period_duration = "P1Y"
      }

      offer_tags = ["best-value"]

      regional_configs = {
        US = {
          price                       = { currency_code = "USD", amount = "49.99" }
          new_subscriber_availability = true
        }
      }
    }

    # A base plan that has been active cannot be deleted. Retire it instead,
    # and keep it declared.
    weekly = {
      state = "INACTIVE"

      auto_renewing = {
        billing_period_duration = "P1W"
      }

      regional_configs = {
        US = {
          price = { currency_code = "USD", amount = "1.99" }
        }
      }
    }
  }

  tax_and_compliance_settings = {
    eea_withdrawal_right_type = "WITHDRAWAL_RIGHT_SERVICE"
  }
}
