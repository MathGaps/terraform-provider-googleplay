# Acceptance testing

The acceptance tests (`TestAcc*`) run the provider against a real Play Console
developer account. They are not part of the pull request checks: they run from
[`.github/workflows/acceptance.yml`](.github/workflows/acceptance.yml), by hand,
in the `acceptance` GitHub environment.

Everything else is covered without credentials by `make test`, which drives
the same provider against an in-memory fake of the API.

> **Use an app and an account you can afford to scribble on.** These tests
> invite and remove a user, replace the tester groups of a track, and create
> products. Some of what they create is permanent.

## What you need

1. **A Play Console developer account and an app in it.** The API cannot create
   an app. A dedicated test app is strongly preferred; a draft app that has
   never been published is fine.
2. **A service account** with the Google Play Android Developer API enabled in
   its Google Cloud project, invited in Play Console under **Users and
   permissions** with:
   - **Admin (all permissions)** on the account, for the user and grant tests;
   - otherwise, on the test app: Release apps to testing tracks, Manage testing
     tracks and edit tester lists, Manage store presence, and View app
     information.
3. **A Google Group** you control, for the tester test. It is added to and
   removed from one track's tester list.
4. **A user email address** you control, for the user test. It is sent a real
   invitation to the developer account.
5. **The test track.** A custom closed testing track, by default named
   `tf-acc`. Create it once, in Play Console or with the opt-in test below.

## Configuration

| Variable | Needed by | What it is |
|---|---|---|
| `TF_ACC` | all | Set to `1` to run acceptance tests at all |
| `GOOGLEPLAY_CREDENTIALS` | all | The JSON text of the service account key (or use application default credentials) |
| `GOOGLEPLAY_TEST_PACKAGE` | all | The package name of the test app |
| `GOOGLEPLAY_DEVELOPER_ID` | users, grants | The developer account id |
| `GOOGLEPLAY_TEST_USER_EMAIL` | users, grants | The address to invite and remove |
| `GOOGLEPLAY_TEST_GROUP` | track testers | The email address of the Google Group |
| `GOOGLEPLAY_TEST_TRACK` | tracks, track testers | The test track; defaults to `tf-acc` |
| `GOOGLEPLAY_TEST_CREATE_TRACK` | track creation | Set to opt in to creating the test track |

A test whose variables are missing skips itself with a message that names
them. `GOOGLEPLAY_ENDPOINT` must not be set: it is the unit tests' seam, and
the acceptance tests refuse to run with it.

In GitHub, `GOOGLEPLAY_CREDENTIALS` is a secret of the `acceptance` environment
and the rest are variables of it. The workflow refuses to start without the
secret and `GOOGLEPLAY_TEST_PACKAGE`, rather than go green having skipped
everything.

Locally:

```shell
export TF_ACC=1
export GOOGLEPLAY_CREDENTIALS="$(cat key.json)"
export GOOGLEPLAY_TEST_PACKAGE=com.example.testapp
export GOOGLEPLAY_DEVELOPER_ID=1234567890123456789
export GOOGLEPLAY_TEST_GROUP=testers@example.com
export GOOGLEPLAY_TEST_USER_EMAIL=someone@example.com
make testacc
```

Do not run the suite twice at once against the same app: the tests share one
track, and an edit committed by one run invalidates the other's.

## What each test does

| Test | Creates | Afterwards |
|---|---|---|
| `TestAccUserResource_basic` | Invites `GOOGLEPLAY_TEST_USER_EMAIL` with a read-only permission and grants it read-only access to the test app | The user is removed from the account, with the grant |
| `TestAccTrackTestersResource_basic` | Sets the tester groups of the test track to `GOOGLEPLAY_TEST_GROUP` | The track's group list is emptied. **Groups that were on the track before are not restored** |
| `TestAccTrackResource_create` (opt-in) | The test track | **The track stays forever** |
| `TestAccSubscriptionResource_basic` | A subscription `tfacc_sub_<random>` with one draft base plan | Deleted. **The product id stays reserved** |
| `TestAccOneTimeProductResource_basic` | A one-time product `tfacc_otp_<random>` with one draft purchase option | Deleted. **The product id stays reserved** |
| `TestAccTracksDataSource_basic`, `TestAccConvertedRegionPricesDataSource_basic` | Nothing | Nothing |

## What cannot be undone

- **Product ids are reserved forever.** Google Play never frees the id of a
  subscription or one-time product, even after the product is deleted. The
  tests therefore use a random id every run, so a leftover never blocks the
  next run, and every run permanently consumes two ids on the test app. There
  is no cleanup for this and no known limit that it approaches.
- **A custom track cannot be deleted.** The API has no call for it. The suite
  therefore uses one fixed track and never invents a name.
  `TestAccTrackResource_create` creates it and can succeed once per app, ever:
  a second run fails because the track exists. It runs only when
  `GOOGLEPLAY_TEST_CREATE_TRACK` is set (the workflow's `create_track` input).
- **A base plan or purchase option that has been activated cannot be deleted,**
  and neither can its subscription. No acceptance test activates anything, for
  that reason: state transitions are covered by the unit tests against the
  fake.

## Cleaning up after a failed run

A run that is killed mid-apply never reaches its destroy. Check, in Play
Console:

- **Users and permissions**: remove `GOOGLEPLAY_TEST_USER_EMAIL` if it is still
  listed.
- **Testing → Closed testing → the test track → Testers**: remove the test
  group.
- **Monetize → Products**: delete draft subscriptions and one-time products
  named `tfacc_*`. Their ids stay reserved either way.

## What has not been verified

The unit tests run against a fake written from the API's published client and
its documentation, plus the behaviours confirmed against the live API so far
(the user list refuses to page). Where the fake had to guess, the guess is
conservative, and it is the acceptance tests' job to confirm it: whether a
patch may drop a base plan or purchase option (the provider never relies on
it and uses the delete calls), the exact status code of a refused delete, and
which values the server fills in for an omitted field.
