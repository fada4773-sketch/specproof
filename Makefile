# Local entry points.
# Tools are invoked with a pinned version via `go run` so that they do not
# end up in the library's go.mod (NFR-02).

GO            ?= go
OUT           ?= $(CURDIR)/out
TIMEOUT       ?= 10m
COVER_MIN     ?= 85
GOLANGCI_LINT := github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
GOVULNCHECK   := golang.org/x/vuln/cmd/govulncheck@v1.8.0
COVERPKG       = $(shell $(GO) list ./... | grep -v /internal/testserver | paste -sd, -)

.PHONY: test check vet lint vuln deps cover perf trace test-public stability update-golden

test: ## L1 + L2
	@mkdir -p $(OUT)
	$(GO) test -race -timeout $(TIMEOUT) -coverpkg=$(COVERPKG) -coverprofile=$(OUT)/cover.out ./...

check: vet lint vuln deps test cover ## L4 + L1 + L2; covers: TP-L4-01 TP-L4-02 TP-L4-03

vet:
	$(GO) vet ./...

lint:
	$(GO) run $(GOLANGCI_LINT) run ./...

vuln:
	$(GO) run $(GOVULNCHECK) ./...

deps: ## TP-L4-02
	./scripts/deps-check.sh

perf: ## NFR-04 without the race detector (TP-L2-25)
	$(GO) test -count=1 -run TestTPL2_25 -v ./apitest

cover: ## NFR-11, enforced from phase 2 on
	./scripts/cover-check.sh $(OUT)/cover.out $(COVER_MIN)

trace: ## test-plan IDs without a matching test (needs the local mydocs/plan.md)
	./scripts/trace.sh

test-public: ## L3, needs Docker/Podman
	cd examples/petstore && $(GO) test -tags=integration -count=1 -timeout 15m -v ./...

stability: ## TP-L3-03
	./scripts/stability.sh 20

update-golden:
	$(GO) test ./internal/report -run Golden -update
