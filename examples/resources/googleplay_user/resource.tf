# Invite a colleague with read-only access to every app of the account.
resource "googleplay_user" "analyst" {
  email                         = "analyst@example.com"
  developer_account_permissions = ["CAN_VIEW_NON_FINANCIAL_DATA_GLOBAL"]
}

# A contractor whose access ends by itself, and who holds no account-wide
# permission: what they can do is granted per app with googleplay_app_grant.
resource "googleplay_user" "contractor" {
  email           = "contractor@example.com"
  expiration_time = "2030-01-01T00:00:00Z"
}
