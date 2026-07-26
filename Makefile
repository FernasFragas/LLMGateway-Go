# LLMGateway-Go — developer entry points.
# `make verify` is the falsifiability story: config example, spec, and code
# are all fixtures that must prove themselves on every run. The rest are the
# everyday build / run / test loops. Run `make` (or `make help`) for the list.

IMAGE   ?= llm-gateway:dev          # local image tag
CONFIG  ?= config/config.yaml       # operator-local config (gitignored)
PORT    ?= 8080                     # host port for run / docker-run
BIN     ?= bin/gateway              # local build output (bin/ is gitignored)
OPENAPI := openapi.yaml
COVERPROFILE := coverage.out

.DEFAULT_GOAL := help
.PHONY: help verify build run \
        docker-build docker-run docker-stop \
        test test-race cover lint-spec contract-test clean

help:                         ## list the available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
	  | sort \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

# ─── build & run ────────────────────────────────────────────────────────────
# In GoLand: run/debug ./cmd/gateway with program arguments
# "-config config/config.yaml" — or attach to `make run`. `make docker-build`
# produces the exact image Kubernetes schedules.

build:                        ## compile the gateway binary to bin/gateway
	go build -o $(BIN) ./cmd/gateway

run: ## run the gateway locally against $(CONFIG) (Ctrl-C for graceful shutdown)
	go run ./cmd/gateway -config $(CONFIG)

# ─── docker ─────────────────────────────────────────────────────────────────

docker-build:                 ## build the distroless container image ($(IMAGE))
	docker build -t $(IMAGE) .

docker-run: docker-build      ## run the image, mounting config/ read-only on :$(PORT)
	docker run --rm --name llm-gateway -p $(PORT):8080 \
	  -v "$$PWD/config:/config" \
	  $(IMAGE) -config /config/config.yaml

docker-stop:                  ## stop a detached/other-terminal container (SIGTERM drain)
	docker stop llm-gateway

# ─── tests & coverage ───────────────────────────────────────────────────────

test:                         ## unit tests, including config.example.yaml validation
	go test ./...

test-race:                    ## unit tests under the race detector
	go test -race ./...

cover: ## run tests with coverage → coverage.out + coverage.html, print total
	go test ./... -covermode=atomic -coverprofile=$(COVERPROFILE)
	go tool cover -html=$(COVERPROFILE) -o coverage.html
	@go tool cover -func=$(COVERPROFILE) | tail -1
	@echo "open coverage.html for the annotated line-by-line report"

# ─── spec & CI ──────────────────────────────────────────────────────────────

verify: lint-spec test        ## everything CI runs on every push

lint-spec:                    ## the spec is a fixture: built-in + gateway-* contract rules
	npx --yes @stoplight/spectral-cli lint --ruleset .spectral.yaml --fail-severity=warn $(OPENAPI)

contract-test:                ## fuzz the RUNNING gateway against the spec (providers stubbed)
	@# schemathesis derives test cases from the schema and checks every response
	@# conforms — including that errors match ErrorBody and headers are present.
	st run $(OPENAPI) --base-url http://localhost:$(PORT) \
	  --header "Authorization: Bearer $$TEST_APP_KEY" \
	  --checks all --hypothesis-max-examples=200

# ─── housekeeping ───────────────────────────────────────────────────────────

clean:                        ## remove build + coverage artifacts
	rm -rf bin $(COVERPROFILE) coverage.html
