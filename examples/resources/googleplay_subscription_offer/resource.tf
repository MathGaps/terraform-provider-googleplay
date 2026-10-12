# A week's free trial, then three months at half price, for users who have
# never subscribed to anything in the app. The offer belongs to the monthly
# base plan of the subscription `pro_monthly`.
resource "googleplay_subscription_offer" "pro_monthly_intro" {
  package_name = "com.example.app"
  product_id   = "pro_monthly"
  base_plan_id = "monthly"
  offer_id     = "intro"

  # A new offer is a draft until its state says otherwise.
  state = "ACTIVE"

  # The regions the offer is set up in. Every phase prices each of them.
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
        GB = { price = { currency_code = "GBP", amount = "3.99" } }
      }
      other_regions_config = { relative_discount = 0.5 }
    },
  ]

  targeting = {
    acquisition_rule = {
      scope = { any_subscription_in_app = {} }
    }
  }

  offer_tags = ["intro"]
}

# A first month at a fixed price for users upgrading from the monthly plan of
# another subscription, once per user.
resource "googleplay_subscription_offer" "max_monthly_upgrade" {
  package_name = "com.example.app"
  product_id   = "max_monthly"
  base_plan_id = "monthly"
  offer_id     = "upgrade-from-pro"

  regional_configs = {
    US = { new_subscriber_availability = true }
  }

  phases = [{
    duration         = "P1M"
    recurrence_count = 1
    regional_configs = {
      US = { price = { currency_code = "USD", amount = "9.99" } }
    }
  }]

  targeting = {
    upgrade_rule = {
      scope                   = { specific_subscription_in_app = "pro_monthly" }
      billing_period_duration = "P1M"
      once_per_user           = true
    }
  }
}
