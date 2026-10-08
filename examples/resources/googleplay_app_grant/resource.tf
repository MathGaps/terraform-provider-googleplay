resource "googleplay_user" "release_manager" {
  email = "release-manager@example.com"
}

# Let them upload builds to the testing tracks of one app and manage its testers.
resource "googleplay_app_grant" "release_manager" {
  email        = googleplay_user.release_manager.email
  package_name = "com.example.app"

  app_level_permissions = [
    "CAN_VIEW_NON_FINANCIAL_DATA",
    "CAN_MANAGE_TRACK_APKS",
    "CAN_MANAGE_TRACK_USERS",
  ]
}
