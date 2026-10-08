# Contributing

Thanks for taking the time. The process is light, but a few things about this
provider are unusual enough to be worth reading before you write code.

By participating you agree to the [Code of Conduct](CODE_OF_CONDUCT.md).
Security problems go through [SECURITY.md](SECURITY.md), not the issue tracker.

## Getting set up

```shell
git clone https://github.com/MathGaps/terraform-provider-googleplay
cd terraform-provider-googleplay
make build
make test        # no credentials needed
```

You need Go (the version in `go.mod`; the `toolchain` line makes `go` fetch it),
[OpenTofu](https://opentofu.org/docs/intro/install/) on your `PATH`, and
`golangci-lint` v2 for `make lint`. The tests, `make generate` and
`make validate-examples` all drive `tofu`. To use Terraform instead, set
`TF_BINARY=terraform` for the two make targets and
`TF_ACC_TERRAFORM_PATH=$(which terraform)` for the tests.

To run your working tree against a real developer account, build it and point
the CLI at the binary with a `dev_overrides` block in your CLI configuration:

```hcl
provider_installation {
  dev_overrides {
    "mathgaps/googleplay" = "/path/to/terraform-provider-googleplay"
  }
  direct {}
}
```

## Reporting a bug

Include the provider version, the OpenTofu or Terraform version, the
configuration that reproduces it with credentials redacted, and the error in
full. The provider puts the response body Google sent into the diagnostic, and
that is usually where the reason is.

## Making a change

**Read the API from the generated client, not from memory.** Every type, field
and method comes from `google.golang.org/api/androidpublisher/v3`. Its comments
are the REST reference: which fields are output only, which calls exist at all
(there is no track delete, no user get, no base plan create), which parameters
are required. `go doc google.golang.org/api/androidpublisher/v3 BasePlan` is
faster than guessing.

**Keep the two layers separate.** `internal/play/` is the client: credentials,
retries, serialized edits, errors and money, with no Terraform types in it.
`internal/provider/` is the Terraform layer, one file per resource.
`internal/fakeplay/` is the in-memory API the tests run against.

**Every track or tester call goes through `ReadEdit` or `CommitEdit`.** They
hold the per-app lock and delete an edit that was not committed. An edit opened
any other way can be invalidated by a concurrent commit, or leak.

**Import must plan nothing.** A resource read back from the API has to match a
configuration that describes it. That is what `prior` is for in the `flatten*`
functions (null against empty, the configured spelling of a value), what
`DecimalType` is for, and why server-assigned attributes are computed. Add an
import step to the test of anything you touch; `importSteps` gives you both
kinds.

**Teach the fake before you trust a test.** When you learn how the real API
behaves, make `internal/fakeplay` behave that way, refusals included: it
rejects a paged user list because the API does.

**Register new resources and data sources by hand** in `Resources()` and
`DataSources()` in `internal/provider/provider.go`.

**Regenerate the docs.** `docs/` is built by `tfplugindocs` from the
`MarkdownDescription` strings and the matching directory under `examples/`, and
CI fails on any difference. Run `make generate` and commit the result. Never
edit `docs/` by hand; prose that is not a schema description belongs in
`templates/`.

Then run:

```shell
make fmt lint
make test
make generate           # commit the result
make validate-examples
```

## Style

- `MarkdownDescription`, never `Description`, and list the valid enum values in
  it.
- Describe limits honestly. If the API cannot do something, the description
  says so and says what the resource does instead.
- Never log a credential or put one in a diagnostic.
- Every `.go` and `.sh` file starts with the licence header
  (`make lint` checks):

  ```go
  // Copyright (c) MathGaps
  // SPDX-License-Identifier: MPL-2.0
  ```

- Commits follow [Conventional Commits](https://www.conventionalcommits.org/):
  `type(scope): subject`, imperative and lower-case.

## Tests

Two tiers.

**Credential-free** tests run on every pull request: the real client and the
real provider against `internal/fakeplay` (`resource.UnitTest`), and
table-driven tests of the money and model code. A contributor without a Play
Console account can run all of it.

**Acceptance** tests (`TestAcc*`) run against a real developer account and only
from the manually triggered workflow. They skip themselves unless `TF_ACC` and
`GOOGLEPLAY_TEST_PACKAGE` are set. Read
[ACCEPTANCE_TESTING.md](../ACCEPTANCE_TESTING.md) first: some of what they
create cannot be deleted.

## Releasing

1. In `CHANGELOG.md`, change the heading of the version from
   `## X.Y.Z (Unreleased)` to a dated one, `## X.Y.Z (October 9, 2026)`. The
   release workflow publishes that section as the release notes, and refuses a
   version that still says `(Unreleased)` or has no section.
2. Tag the commit `vX.Y.Z` and push the tag.

The workflow builds with GoReleaser in the `release` environment and signs
`SHA256SUMS` with the project's GPG key. A version cannot be released twice.
