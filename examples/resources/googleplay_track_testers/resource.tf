resource "googleplay_track" "qa" {
  package_name = "com.example.app"
  track        = "qa"
}

# The members of these Google Groups can test the track.
resource "googleplay_track_testers" "qa" {
  package_name = googleplay_track.qa.package_name
  track        = googleplay_track.qa.track

  google_groups = [
    "qa-team@example.com",
    "beta-testers@example.com",
  ]
}

# The built-in tracks need no googleplay_track resource.
resource "googleplay_track_testers" "internal" {
  package_name  = "com.example.app"
  track         = "internal"
  google_groups = ["developers@example.com"]
}
