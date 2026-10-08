# Google Play provider for OpenTofu

[![Tests](https://github.com/MathGaps/terraform-provider-googleplay/actions/workflows/test.yml/badge.svg)](https://github.com/MathGaps/terraform-provider-googleplay/actions/workflows/test.yml)

Manage a Google Play Console developer account as configuration, through the
[Google Play Developer API](https://developers.google.com/android-publisher):
who has access to what, who can test, and what the app sells.

```terraform
terraform {
  required_providers {
    googleplay = {
      source  = "mathgaps/googleplay"
      version = "~> 0.1"
    }
  }
}
```

The provider is published to the
[OpenTofu Registry](https://search.opentofu.org/provider/mathgaps/googleplay) as
`mathgaps/googleplay`, and `tofu init` installs it. It is **not on the Terraform
Registry**. It speaks the same plugin protocol, so it works with Terraform too,
but only when installed from a
[filesystem or network mirror](https://developer.hashicorp.com/terraform/cli/config/config-file#provider-installation)
that you fill from the [GitHub releases](https://github.com/MathGaps/terraform-provider-googleplay/releases).

## What it manages

| Resource | What it is | Import id |
|---|---|---|
| [`googleplay_user`](docs/resources/user.md) | A user of the developer account and their account-wide permissions | `email` |
| [`googleplay_app_grant`](docs/resources/app_grant.md) | A user's permissions on one app | `email/package_name` |
| [`googleplay_track`](docs/resources/track.md) | A custom closed testing track | `package_name/track` |
| [`googleplay_track_testers`](docs/resources/track_testers.md) | The Google Groups that can test a track | `package_name/track` |
| [`googleplay_subscription`](docs/resources/subscription.md) | A subscription: listings, tax settings, base plans and their regional prices | `package_name/product_id` |
| [`googleplay_one_time_product`](docs/resources/one_time_product.md) | A one-time product: listings, tax settings, purchase options and their regional prices | `package_name/product_id` |

| Data source | What it reads |
|---|---|
| [`googleplay_tracks`](docs/data-sources/tracks.md) | The tracks of an app and the releases on them |
| [`googleplay_converted_region_prices`](docs/data-sources/converted_region_prices.md) | One price converted into the price of every region |

## What it does not manage

Some of this is the API's limit, and some is simply not written yet.

**The API cannot do it:**

- **Creating an app.** Create the app in Play Console; everything here refers
  to an existing app by package name.
- **Deleting a track.** Destroying a `googleplay_track` removes it from state
  with a warning. The track stays in Play Console.
- **Tester email lists.** The API exposes the Google Groups of a track and
  nothing else. Testers added in Play Console as email lists are invisible to
  `googleplay_track_testers`: it neither shows nor changes them.
- **Reusing a product id.** Google Play reserves the id of every subscription
  and one-time product forever, even after the product is deleted.
- **Deleting what has been sold.** A base plan can be deleted only while it is
  a draft, and a subscription only if none of its base plans was ever
  activated. Retire a base plan with `state = "INACTIVE"`. Destroying a
  subscription that cannot be deleted deactivates its base plans and removes it
  from state, with a warning.

**Not yet:**

- Subscription offers (free trials, introductory prices) and offers on a
  one-time product's purchase options.
- Migrating existing subscribers to a new price (`migratePrices`). Changing a
  regional price changes it for new subscribers.
- Releases. Builds and rollouts belong to a release pipeline, and
  `googleplay_track` never reads or writes the releases on a track.
- Store listings, screenshots and app details.
- The deprecated `inappproducts` API. One-time products use
  `monetization.onetimeproducts`.

## Authentication

The provider authenticates as a Google Cloud service account with the
`https://www.googleapis.com/auth/androidpublisher` scope, taking credentials
from the first of:

1. the provider's `credentials` argument: the **JSON text** of a service
   account key, not a path;
2. the `GOOGLEPLAY_CREDENTIALS` environment variable: the same JSON text;
3. [application default credentials](https://cloud.google.com/docs/authentication/application-default-credentials):
   a key file named by `GOOGLE_APPLICATION_CREDENTIALS`, `gcloud auth
   application-default login`, or workload identity federation.

`googleplay_user` and `googleplay_app_grant` also need the developer account
id: the provider's `developer_id` argument or the `GOOGLEPLAY_DEVELOPER_ID`
environment variable. It is the number after `/developers/` in any Play Console
URL.

```terraform
provider "googleplay" {
  developer_id = "1234567890123456789"
}
```

### Setting up the service account

1. In a Google Cloud project, enable the **Google Play Android Developer API**
   (APIs & Services → Library). Calls fail with a 403 until it is enabled.
2. In the same project, create a service account. It needs no IAM role in the
   project: its permissions come from Play Console.
3. Create a JSON key for it, or better, let your CI impersonate it with
   workload identity federation so that no key exists.
4. In [Play Console](https://play.google.com/console), open **Users and
   permissions**, choose **Invite new users**, and invite the service account's
   email address (`name@project.iam.gserviceaccount.com`). Give it the
   permissions for what you manage, on the account or on individual apps:

   | To manage | The service account needs |
   |---|---|
   | `googleplay_user`, `googleplay_app_grant` | Admin (all permissions) on the account |
   | `googleplay_track`, `googleplay_track_testers` | Release apps to testing tracks, and Manage testing tracks and edit tester lists |
   | `googleplay_subscription`, `googleplay_one_time_product`, `googleplay_converted_region_prices` | Manage store presence |
   | `googleplay_tracks` | View app information |

   Google does not publish which permission each API method checks, so treat
   the table as a starting point. A refused call fails with a 403 whose reason
   is shown in the error.

A new permission can take a while to reach the API.

## Start with an import

Since the API cannot create an app, every configuration starts from something
that already exists. Declare it as it is, import it, and check that the plan is
empty before changing anything:

```terraform
import {
  to = googleplay_user.release_manager
  id = "release-manager@example.com"
}

resource "googleplay_user" "release_manager" {
  email = "release-manager@example.com"
}

import {
  to = googleplay_app_grant.release_manager
  id = "release-manager@example.com/com.example.app"
}

resource "googleplay_app_grant" "release_manager" {
  email                 = googleplay_user.release_manager.email
  package_name          = "com.example.app"
  app_level_permissions = ["CAN_VIEW_NON_FINANCIAL_DATA", "CAN_MANAGE_TRACK_APKS"]
}

import {
  to = googleplay_track_testers.internal
  id = "com.example.app/internal"
}

resource "googleplay_track_testers" "internal" {
  package_name  = "com.example.app"
  track         = "internal"
  google_groups = ["developers@example.com"]
}
```

```console
$ tofu plan
Plan: 3 to import, 0 to add, 0 to change, 0 to destroy.
```

A plan that proposes a change shows you where the configuration and Play
Console disagree. [`examples/existing-app`](examples/existing-app) is a complete
configuration of this kind, including a subscription.

## Things worth knowing

- **Money is exact.** A price is a currency code and a decimal string,
  `{ currency_code = "USD", amount = "4.99" }`, converted to the API's units
  and nanos without ever passing through a floating-point number. `"4.50"` and
  `"4.5"` are the same amount and never show as a difference.
- **Edits are serialized.** Track and tester changes go through the API's
  edits, and committing one invalidates the app's other open edits. The
  provider runs the edits of one app one at a time, and deletes any edit it
  opened and did not commit. Edits made by something else at the same moment
  (a release pipeline uploading a build, say) can still collide with an apply;
  run it again.
- **`regions_version`.** Calls that touch prices must name the version of
  Google Play's region list the prices were written against. It is an attribute
  of the two product resources, defaulting to `2022/02`. The current version is
  in [this article](https://support.google.com/googleplay/android-developer/answer/10532353),
  and `googleplay_converted_region_prices` reports it.
- **The user list is read whole.** The API has no call that reads one user or
  one grant, and its user list cannot be paged, so each read of a
  `googleplay_user` or `googleplay_app_grant` fetches every user of the account.
- **Retries.** A 429 or 5xx response is retried up to five times with
  exponential backoff, honouring `Retry-After`.

## Developing

```shell
make build
make test               # no credentials needed; needs `tofu` on PATH
make lint
make generate           # regenerate docs/ from the schema and examples/
make validate-examples
```

See [CONTRIBUTING.md](.github/CONTRIBUTING.md), and
[ACCEPTANCE_TESTING.md](ACCEPTANCE_TESTING.md) for the tests that run against a
real developer account.

## Licence

[MPL-2.0](LICENSE).
