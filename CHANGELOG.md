<!--
The release workflow publishes the section for the tagged version as the
release notes, and refuses to release a version whose heading still says
"(Unreleased)". Before tagging vX.Y.Z, change the heading to a dated one:

    ## X.Y.Z (October 9, 2026)
-->

## 0.1.0 (October 8, 2026)

The first release.

FEATURES:

* **`googleplay_user` and `googleplay_app_grant`.** The users of a Play Console
  developer account, their account-wide permissions and their per-app
  permissions. Import ids: `email` and `email/package_name`.
* **`googleplay_track` and `googleplay_track_testers`.** Custom closed testing
  tracks, and the Google Groups that can test a track. Changes go through the
  API's edits, serialized per app. Import id: `package_name/track`.
* **`googleplay_subscription`.** A subscription with its listings, tax and
  compliance settings, and its base plans: auto-renewing, prepaid or
  installments, with regional prices and a state reached through the
  activate and deactivate calls. Import id: `package_name/product_id`.
* **`googleplay_one_time_product`.** A one-time product on the
  `monetization.onetimeproducts` API, with its listings, tax settings and
  purchase options. Import id: `package_name/product_id`.
* **`googleplay_tracks` and `googleplay_converted_region_prices` data sources.**
  The tracks of an app, and one price converted into every region's.
* **`googleplay_users` data source.** Every user of the developer account with
  their account-wide permissions and per-app grants, for writing import blocks
  from what already exists.

NOTES:

* Prices are decimal strings converted to the API's units and nanos exactly;
  `"4.50"` and `"4.5"` are the same amount.
* The API cannot delete a track, a base plan that has been activated, or a
  subscription that has had one. See the README for how each resource behaves.
* Subscription offers and price migration for existing subscribers are not
  managed yet.
