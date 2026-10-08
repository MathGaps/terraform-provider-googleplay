data "googleplay_tracks" "app" {
  package_name = "com.example.app"
}

output "track_names" {
  value = data.googleplay_tracks.app.names
}

# The version codes currently on the production track.
output "production_version_codes" {
  value = flatten([
    for track in data.googleplay_tracks.app.tracks :
    [for release in track.releases : release.version_codes]
    if track.track == "production"
  ])
}
