# LLMGateway-Go — verification entry points.
# `make verify` is the falsifiability story: config example, spec, and code
# are all fixtures that must prove themselves on every run.

OPENAPI := api/openapi.yaml

.PHONY: verify lint-spec test contract-test build

verify: lint-spec test        ## everything CI runs on every push

lint-spec:                    ## the spec is a fixture: built-in + gateway-* contract rules
	npx --yes @stoplight/spectral-cli lint --ruleset .spectral.yaml --fail-severity=warn $(OPENAPI)

test:                         ## unit tests, including config.example.yaml validation
	go test ./...

build:
	go build ./cmd/gateway

contract-test:                ## fuzz the RUNNING gateway against the spec (providers stubbed)
	@# schemathesis derives test cases from the schema and checks every response
	@# conforms — including that errors match ErrorBody and headers are present.
	st run $(OPENAPI) --base-url http://localhost:8080 \
	  --header "Authorization: Bearer $$TEST_APP_KEY" \
	  --checks all --hypothesis-max-examples=200
