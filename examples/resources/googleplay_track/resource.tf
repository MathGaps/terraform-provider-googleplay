# A custom closed testing track. Builds are uploaded to it by your release
# pipeline: this resource never reads or writes the releases on a track.
resource "googleplay_track" "qa" {
  package_name = "com.example.app"
  track        = "qa"
}

# A closed testing track for the Wear OS form factor.
resource "googleplay_track" "wear_qa" {
  package_name = "com.example.app"
  track        = "wear:qa"
  form_factor  = "WEAR"
}
