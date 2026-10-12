data "googleplay_subscriptions" "app" {
  package_name = "com.example.app"
}

output "subscription_product_ids" {
  value = data.googleplay_subscriptions.app.product_ids
}

# The base plans of each subscription, as product id => base plan ids.
output "base_plan_ids" {
  value = {
    for sub in data.googleplay_subscriptions.app.subscriptions :
    sub.product_id => [for plan in sub.base_plans : plan.base_plan_id]
  }
}

# Import ids for every subscription, to write import blocks from.
output "subscription_import_ids" {
  value = [
    for sub in data.googleplay_subscriptions.app.subscriptions :
    "${data.googleplay_subscriptions.app.package_name}/${sub.product_id}"
  ]
}
