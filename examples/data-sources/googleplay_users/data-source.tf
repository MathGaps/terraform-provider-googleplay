data "googleplay_users" "all" {}

# Who is in the developer account, and what each can do across it.
output "users" {
  value = {
    for user in data.googleplay_users.all.users :
    user.email => user.developer_account_permissions
  }
}

# The import id of every per-app grant, ready to paste into import blocks:
# "email/package_name" for googleplay_app_grant, "email" for googleplay_user.
output "app_grant_import_ids" {
  value = flatten([
    for user in data.googleplay_users.all.users : [
      for grant in user.grants : "${user.email}/${grant.package_name}"
      if grant.package_name != null
    ]
  ])
}
