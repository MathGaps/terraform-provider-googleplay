# Security Policy

## Reporting a vulnerability

**Do not open a public issue for a security problem.**

Use GitHub's private vulnerability reporting: go to the
[Security tab](https://github.com/MathGaps/terraform-provider-googleplay/security/advisories/new)
and choose **Report a vulnerability**. That opens a private thread only the
maintainers can see, and it becomes a published advisory once a fix ships.

Please include the provider version, the OpenTofu or Terraform version, the
configuration that triggers the problem with any credentials redacted, and what
an attacker gains. A proof of concept helps but is not required.

Expect an acknowledgement within a week. There is no formal response SLA beyond
that; you will get a status update whether or not the report is accepted.

## Supported versions

The provider is pre-1.0. Only the latest released version is supported: fixes
ship as a new tag rather than as a patch to an older minor.

## What this provider handles

**The service account credential is account-wide.** A service account invited
to a Play Console developer account with the Admin permission can add and
remove users and change what every app sells. The provider reads the
credential from the `credentials` argument, the `GOOGLEPLAY_CREDENTIALS`
environment variable or application default credentials, and hands it to
Google's auth library. `credentials` is marked sensitive and never appears in
plan output. Anything that causes the credential or an access token to be
logged, written to state, put in a diagnostic, or sent anywhere other than
Google's token and API endpoints is a vulnerability; report it.

Prefer workload identity federation to a long-lived key where your CI supports
it: application default credentials pick it up, and then there is no key to
leak.

**`GOOGLEPLAY_ENDPOINT` is a test seam.** It points the client at a fake API
for the unit tests. When it is set the provider loads no credentials and sends
requests unauthenticated, so it cannot be used to redirect a credential. It is
deliberately not a provider argument.

## What is out of scope

- The Google Play Developer API and Play Console themselves. Report those to
  Google.
- State confidentiality in general. State holds every attribute the provider
  reads, including user email addresses; protecting the backend is the
  operator's job.
- Findings that require an attacker who already holds your service account
  key, or write access to your configuration or state.
- Vulnerability-scanner output with no demonstrated path through this code. A
  CVE in a transitive dependency the provider does not reach is worth a normal
  issue, not a private report; `govulncheck` runs in CI and reports reachable
  ones.

## Release integrity

Releases are built by GoReleaser in
[`.github/workflows/release.yml`](workflows/release.yml), in a GitHub
environment that admits only version tags, and the checksum file is signed with
the project's GPG key, which is registered with the
[OpenTofu Registry](https://search.opentofu.org/provider/mathgaps/googleplay).
`tofu init` verifies that signature for you. Binaries from anywhere other than
the registry or this repository's GitHub Releases are not ours.
