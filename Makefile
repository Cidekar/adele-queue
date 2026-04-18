SHELL := /bin/bash

export PACKAGE_BRANCH = main
export PACKAGE_PUBLICATION_TAG ?=
export PACKAGE_PUBLICATION_TAG_NEXT ?=
export OUT ?=

# ┌─────────────────────────────────────────────────────────────────────────────┐
# │  Adele Queue Package                                                        │
# └─────────────────────────────────────────────────────────────────────────────┘

## help: lists available command groups
.SILENT:
help:
	@printf "\n"
	@printf "  ┌─────────────────────────────────────────────────────────────────┐\n"
	@printf "  │  Adele Queue — available command groups                         │\n"
	@printf "  └─────────────────────────────────────────────────────────────────┘\n"
	@printf "\n"
	@printf "  make build\:help    ── build & vet commands\n"
	@printf "  make test\:help     ── test commands (require Postgres)\n"
	@printf "  make release\:help  ── release & tagging workflow\n"
	@printf "\n"

# ┌─────────────────────────────────────────────────────────────────────────────┐
# │  Build                                                                      │
# └─────────────────────────────────────────────────────────────────────────────┘

## build:check — compile all packages and run go vet
.SILENT:
build\:check:
	@echo ""
	@echo "  → go build ./..."
	@go build ./...
	@echo "  → go vet ./..."
	@go vet ./...
	@echo ""
	@echo "  ✓ build check passed"
	@echo ""

## build:fmt — format all Go source files
.SILENT:
build\:fmt:
	@echo ""
	@echo "  → gofmt -w api/ ."
	@gofmt -w api/ .
	@echo "  ✓ formatting complete"
	@echo ""

## build:help — display build command documentation
.SILENT:
build\:help:
	@printf "\n"
	@printf "  ┌─────────────────────────────────────────────────────────────────┐\n"
	@printf "  │  Build commands                                                  │\n"
	@printf "  └─────────────────────────────────────────────────────────────────┘\n"
	@printf "\n"
	@printf "  make build\:check   ── go build ./... && go vet ./...\n"
	@printf "  make build\:fmt     ── gofmt -w api/ .\n"
	@printf "\n"
	@printf "  Tips\n"
	@printf "  ────\n"
	@printf "  • Run build\:check before opening a pull request to catch\n"
	@printf "    compile errors and vet warnings early.\n"
	@printf "  • build\:fmt writes changes in-place; commit the result.\n"
	@printf "\n"
	@printf "  Troubleshooting\n"
	@printf "  ───────────────\n"
	@printf "  • 'cannot find package' → run: go mod tidy\n"
	@printf "  • vet errors on generated files → check api/templates/\n"
	@printf "\n"

# ┌─────────────────────────────────────────────────────────────────────────────┐
# │  Test                                                                       │
# └─────────────────────────────────────────────────────────────────────────────┘

## test:all — clear cache and run every test package
.SILENT:
test\:all:
	@go clean -testcache
	make test:api

## test:api — run tests in ./api/...
.SILENT:
test\:api:
	@go test ./api/...

## test:pressure — run the no-DB pressure suite (panic recovery, retry count, deadlock, goroutine leak)
.SILENT:
test\:pressure:
	@go test ./api/... -run TestPressure -timeout 60s

## test:race — run every test with the race detector enabled
.SILENT:
test\:race:
	@go test -race ./api/...

## test:bench — run all benchmarks once (1s/benchmark, memory stats)
.SILENT:
test\:bench:
	@go test ./api/... -bench=. -benchmem -run=^$$ -benchtime=1s -timeout 180s

## test:bench:long — run all benchmarks with a longer measurement window for stable numbers
.SILENT:
test\:bench\:long:
	@go test ./api/... -bench=. -benchmem -run=^$$ -benchtime=5s -timeout 600s

## test:bench:dispatch — run only the dispatch throughput benchmark (quickest signal)
.SILENT:
test\:bench\:dispatch:
	@go test ./api/... -bench=BenchmarkDispatch -benchmem -run=^$$ -benchtime=5s

## test:bench:before — save current benchmark numbers to before.txt for later comparison
.SILENT:
test\:bench\:before:
	@go test ./api/... -bench=. -benchmem -run=^$$ -benchtime=2s -timeout 300s > before.txt
	@echo "  ✓ wrote before.txt"

## test:bench:after — save current benchmark numbers to after.txt and run benchstat against before.txt
.SILENT:
test\:bench\:after:
	@go test ./api/... -bench=. -benchmem -run=^$$ -benchtime=2s -timeout 300s > after.txt
	@echo "  ✓ wrote after.txt"
	@if command -v benchstat >/dev/null 2>&1; then \
		echo ""; \
		benchstat before.txt after.txt; \
	else \
		echo "  (install benchstat for a diff: go install golang.org/x/perf/cmd/benchstat@latest)"; \
	fi

## test:coverage — display per-package coverage summary
.SILENT:
test\:coverage:
	@go test -cover ./...

## test:coverage:browser — generate and open HTML coverage report
.SILENT:
test\:coverage\:browser:
	@go test -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out

## test:help — display test command documentation
.SILENT:
test\:help:
	@printf "\n"
	@printf "  ┌─────────────────────────────────────────────────────────────────┐\n"
	@printf "  │  Test commands                                                   │\n"
	@printf "  └─────────────────────────────────────────────────────────────────┘\n"
	@printf "\n"
	@printf "  make test\:all               ── clear cache + run all packages\n"
	@printf "  make test\:api               ── go test ./api/...\n"
	@printf "  make test\:pressure          ── no-DB pressure suite (panic, retry, leak)\n"
	@printf "  make test\:race              ── full suite with the race detector\n"
	@printf "  make test\:bench             ── all benchmarks (1s each)\n"
	@printf "  make test\:bench\:long        ── all benchmarks (5s each, stable numbers)\n"
	@printf "  make test\:bench\:dispatch    ── dispatch throughput only (5s)\n"
	@printf "  make test\:bench\:before      ── save current bench numbers to before.txt\n"
	@printf "  make test\:bench\:after       ── save to after.txt and run benchstat\n"
	@printf "  make test\:coverage          ── coverage summary for all packages\n"
	@printf "  make test\:coverage\:browser  ── open HTML coverage report\n"
	@printf "\n"
	@printf "  ⚠  Postgres required for test\:all / test\:api / test\:race / test\:coverage\n"
	@printf "  ────────────────────────────────────────────────────────────────────────\n"
	@printf "  DB-backed tests connect to a live Postgres instance. Set DATABASE_*\n"
	@printf "  env vars in api/testdata/.test.env before running those targets.\n"
	@printf "\n"
	@printf "  ✓  No DB required for test\:pressure and test\:bench\*\n"
	@printf "  ───────────────────────────────────────────────────\n"
	@printf "  Pressure tests and benchmarks use the memory backend only\n"
	@printf "  and are safe to run in any environment (including CI).\n"
	@printf "\n"
	@printf "  Tips\n"
	@printf "  ────\n"
	@printf "  • test\:all clears the test cache first — use it for a clean run.\n"
	@printf "  • test\:coverage\:browser writes coverage.out to the repo root.\n"
	@printf "  • Benchmark workflow: test\:bench\:before → edit code → test\:bench\:after\n"
	@printf "    prints a benchstat diff if benchstat is installed.\n"
	@printf "  • Install benchstat: go install golang.org/x/perf/cmd/benchstat@latest\n"
	@printf "\n"
	@printf "  Troubleshooting\n"
	@printf "  ───────────────\n"
	@printf "  • 'dial tcp … connection refused' → Postgres is not reachable.\n"
	@printf "  • 'pq: role … does not exist'     → check DB credentials.\n"
	@printf "  • Test cache stale results         → run test\:all to reset.\n"
	@printf "  • Benchmark hangs at BenchmarkRetryStorm → expected, retry backoff\n"
	@printf "    sleeps up to 6s per iteration. Use -timeout 600s for -benchtime >1s.\n"
	@printf "\n"

# ┌─────────────────────────────────────────────────────────────────────────────┐
# │  Release                                                                    │
# └─────────────────────────────────────────────────────────────────────────────┘

## release — combined entry point: preamble → current tag → capture → verify
release:
	@make release:preamble release:get-current-tag release:capture release:verify
	@echo ""
	@echo "  done"
	@echo ""

## release:preamble — print semantic versioning format guide
.SILENT:
release\:preamble:
	@printf "\n"
	@printf "  ┌─────────────────────────────────────────────────────────────────┐\n"
	@printf "  │  Adele Queue — release workflow                                 │\n"
	@printf "  └─────────────────────────────────────────────────────────────────┘\n"
	@printf "\n"
	@printf "  Tags must follow semantic versioning: vMAJOR.MINOR.PATCH\n"
	@printf "  Examples: v1.0.0   v1.2.3   v2.0.0\n"
	@printf "\n"

## release:get-current-tag — fetch tags from origin and display the latest
.SILENT:
release\:get-current-tag:
	@git fetch --tags
	$(eval PACKAGE_PUBLICATION_TAG=$(shell git describe --tags --abbrev=0 2>/dev/null || echo "(no tags yet)"))
	@echo "  Current tag: $(PACKAGE_PUBLICATION_TAG)"
	@echo ""

## release:capture — prompt for the next release tag
.SILENT:
release\:capture:
	$(eval PACKAGE_PUBLICATION_TAG_NEXT=$(shell bash -c 'read -p "  Please enter a new tag for this release: " \
		RESPONSE; echo $$RESPONSE'))

## release:verify — validate semver, confirm, tag, and push
.SILENT:
release\:verify:
	@echo ""
	@echo "  The next Queue package release will be tagged: $(PACKAGE_PUBLICATION_TAG_NEXT)"
	@echo ""

	if [[ $$PACKAGE_PUBLICATION_TAG_NEXT =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$$ ]]; then \
		echo "" ; \
	else \
		echo "  Error: tag does not follow semantic versioning format (vMAJOR.MINOR.PATCH)." ; \
		exit 1; \
	fi

	@if git rev-parse "$(PACKAGE_PUBLICATION_TAG_NEXT)" >/dev/null 2>&1; then \
		echo "  Error: tag $(PACKAGE_PUBLICATION_TAG_NEXT) already exists."; \
		exit 1; \
	fi

	@read -p "  Do you wish to proceed with the release? [y/N] " ans && ans=$${ans:-N} ; \
	if [ $${ans} = y ] || [ $${ans} = Y ]; then \
		printf "  Creating tag: " ; \
		git tag ${PACKAGE_PUBLICATION_TAG_NEXT}; \
		git push origin tag ${PACKAGE_PUBLICATION_TAG_NEXT}; \
		echo "  ✓ tag pushed successfully"; \
	else \
		echo "  Release aborted."; \
		exit 1; \
	fi

## release:pull — fetch and pull latest from origin branch
.SILENT:
release\:pull:
	$(eval OUT=$(shell git pull origin ${PACKAGE_BRANCH}))
	if [[ $$OUT = "" ]]; then \
		echo "  Repository is in a bad state — please fix-up your local branch"; \
		exit 1; \
	else \
		echo ""; \
	fi

## release:help — display full release workflow documentation
.SILENT:
release\:help:
	@printf "\n"
	@printf "  ┌─────────────────────────────────────────────────────────────────┐\n"
	@printf "  │  Release commands                                                │\n"
	@printf "  └─────────────────────────────────────────────────────────────────┘\n"
	@printf "\n"
	@printf "  make release                  ── full interactive release workflow\n"
	@printf "  make release\:preamble         ── print semver format guide\n"
	@printf "  make release\:get-current-tag  ── fetch tags, show latest\n"
	@printf "  make release\:capture          ── prompt for next tag\n"
	@printf "  make release\:verify           ── validate, confirm, tag & push\n"
	@printf "  make release\:pull             ── fetch + pull from origin\n"
	@printf "\n"
	@printf "  Workflow overview\n"
	@printf "  ─────────────────\n"
	@printf "  1. Run: make release\n"
	@printf "     This chains preamble → get-current-tag → capture → verify.\n"
	@printf "  2. You will be shown the current latest tag.\n"
	@printf "  3. Enter the next tag (must match vMAJOR.MINOR.PATCH).\n"
	@printf "  4. Confirm with y — the tag is created and pushed to origin.\n"
	@printf "\n"
	@printf "  Tag format: vMAJOR.MINOR.PATCH\n"
	@printf "  Examples  : v1.0.0   v1.2.3   v2.0.0\n"
	@printf "\n"
	@printf "  Tips\n"
	@printf "  ────\n"
	@printf "  • Always run from a clean, up-to-date branch (PACKAGE_BRANCH=$(PACKAGE_BRANCH)).\n"
	@printf "  • The workflow will reject tags that already exist.\n"
	@printf "  • Consumers import this package via go get with the tag, so\n"
	@printf "    once pushed a tag should not be deleted or force-moved.\n"
	@printf "\n"
	@printf "  Troubleshooting\n"
	@printf "  ───────────────\n"
	@printf "  • 'tag already exists'  → choose a higher version number.\n"
	@printf "  • 'bad state' on pull   → resolve diverged history first.\n"
	@printf "  • Push permission error → confirm your remote credentials.\n"
	@printf "\n"
