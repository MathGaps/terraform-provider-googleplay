# Examples

- `provider/` configures the provider.
- `resources/<name>/` and `data-sources/<name>/` hold the snippet shown on each
  page of the generated documentation, and the `import.sh` for its import
  section.
- `existing-app/` is a complete configuration that imports an app's existing
  users, testers and subscription, which is where every use of this provider
  starts: the Google Play Developer API cannot create an app.

Every directory is checked against the provider schema by
`make validate-examples`. `docs/` is generated from these files by
`make generate`; edit the example, not the page.
