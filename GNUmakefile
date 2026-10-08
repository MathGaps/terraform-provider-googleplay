default: fmt lint install generate

build:
	go build -v ./...

install: build
	go install -v ./...

lint:
	golangci-lint run
	./scripts/check-headers.sh

# Formats examples/ and regenerates docs/ with tfplugindocs. Needs `tofu` on
# PATH (or TF_BINARY=terraform); see scripts/generate-docs.sh.
generate:
	./scripts/generate-docs.sh

# `make generate` only formats examples/, which cannot catch configuration the
# provider schema rejects. This builds the provider and runs `tofu validate` in
# every example directory.
validate-examples:
	./scripts/validate-examples.sh

fmt:
	gofmt -s -w -e .

# Credential-free: httptest fakes of the Google Play Developer API.
test:
	go test -v -cover -race -timeout=300s ./...

# Runs against a real Play Console developer account. See ACCEPTANCE_TESTING.md.
testacc:
	TF_ACC=1 go test ./internal/provider/ -v -timeout 60m -run '^TestAcc'

.PHONY: default fmt lint test testacc build install generate validate-examples
