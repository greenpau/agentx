APP_VERSION:=$(shell awk 'NR == 1 && $$0 ~ /^[0-9]+\.[0-9]+\.[0-9]+$$/ { print; exit }' VERSION)
VERBOSE:=-v
TEST_DIR:=./...
BINARY:=./bin/agentx

ifeq ($(APP_VERSION),)
$(error VERSION must contain a major.minor.patch release version)
endif

ifdef TEST
	TEST_ARGS:=-run $(TEST)
endif

.PHONY: all
all: info build
	@echo "$@: complete"

.PHONY: info
info:
	@git_branch="$$(git rev-parse --abbrev-ref HEAD -- | head -n 1)"; \
	git_commit="$$(git describe --dirty --always)"; \
	build_user="$$(whoami)"; \
	build_date="$$(date +"%Y-%m-%d")"; \
	printf 'Version: %s, Branch: %s, Revision: %s\n' '$(APP_VERSION)' "$$git_branch" "$$git_commit"; \
	printf 'Build on %s by %s\n' "$$build_date" "$$build_user"

.PHONY: build
build: mod-tidy
	@mkdir -p ./bin
	@rm -f $(BINARY)
	@git_branch="$$(git rev-parse --abbrev-ref HEAD -- | head -n 1)"; \
	git_commit="$$(git describe --dirty --always)"; \
	build_user="$$(whoami)"; \
	build_date="$$(date +"%Y-%m-%d")"; \
	CGO_ENABLED=0 go build -o $(BINARY) $(VERBOSE) \
		-trimpath \
		-ldflags="-w -s \
		-X main.appVersion=$(APP_VERSION) \
		-X main.gitBranch=$$git_branch \
		-X main.gitCommit=$$git_commit \
		-X main.buildUser=$$build_user \
		-X main.buildDate=$$build_date" \
		.
	@$(BINARY) --version
	@$(BINARY) --help
	@echo "$@: complete"

.PHONY: linter
linter:
	@echo "Running formatting and vet checks"
	@files="$$(gofmt -l ./*.go $$(find ./pkg -type f -name '*.go'))"; \
	if [ -n "$$files" ]; then \
		echo "The following Go files require gofmt:"; \
		echo "$$files"; \
		exit 1; \
	fi
	@go vet $(TEST_DIR)
	@echo "$@: complete"

.PHONY: run-tests
run-tests:
	@echo "$@: started"
	@go test $(VERBOSE) $(TEST_ARGS) $(TEST_DIR)
	@echo "$@: complete"

.PHONY: run-race-tests
run-race-tests:
	@echo "$@: started"
	@go test -race $(TEST_ARGS) $(TEST_DIR)
	@echo "$@: complete"

.PHONY: run-shuffle-tests
run-shuffle-tests:
	@echo "$@: started"
	@go test $(TEST_ARGS) $(TEST_DIR) -shuffle=on -count=3
	@echo "$@: complete"

.PHONY: run-boundary-tests
run-boundary-tests:
	@echo "$@: started"
	@go test -count=1 -v ./pkg/signals ./pkg/testing
	@echo "$@: complete"

.PHONY: test
test: linter run-tests run-race-tests run-shuffle-tests run-boundary-tests
	@echo "$@: complete"

.PHONY: cross-build
cross-build:
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...
	@CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build ./...
	@CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./...
	@for arch in amd64 386 arm64; do \
		CGO_ENABLED=0 GOOS=windows GOARCH=$$arch go build ./... || exit 1; \
		CGO_ENABLED=0 GOOS=windows GOARCH=$$arch go test -run '^$$' -exec=/usr/bin/true ./... || exit 1; \
	done
	@echo "$@: complete"

.PHONY: audit
audit:
	@ruby .codex/skills/implementation-conformance-audit/scripts/build_source_coverage.rb --check
	@ruby .codex/skills/implementation-conformance-audit/scripts/build_contract_scenario_coverage.rb --check
	@ruby .codex/skills/implementation-conformance-audit/scripts/audit_architecture.rb --source-evidence-self-test
	@ruby .codex/skills/implementation-architecture/scripts/generate_drawio.rb --check
	@ruby .codex/skills/implementation-architecture/scripts/enhance_custom_drawio.rb --check
	@ruby .codex/skills/implementation-conformance-audit/scripts/audit_architecture.rb
	@echo "$@: complete"

.PHONY: docs
docs:
	@mkdir -p .doc
	@go doc -all > .doc/index.txt
	@cat .doc/index.txt
	@echo "$@: complete"

.PHONY: clean
clean:
	@rm -rf .doc
	@rm -rf .coverage
	@rm -rf ./bin
	@echo "$@: complete"

.PHONY: dep
dep:
	@echo "Making dependencies check ..."
	@go install github.com/greenpau/versioned/cmd/versioned@latest
	@echo "$@: complete"

.PHONY: upgrade
upgrade:
	@echo "Making upgrade"
	@go get -u $(TEST_DIR)
	@go mod tidy
	@echo "$@: complete"

.PHONY: mod-tidy
mod-tidy:
	@echo "$@: started"
	@go mod tidy
	@go mod verify
	@echo "$@: complete"

.PHONY: release-git-check
release-git-check: dep mod-tidy
	@echo "$@: started"
	@git_branch="$$(git rev-parse --abbrev-ref HEAD -- | head -n 1)"; \
	if [ "$$git_branch" != "main" ]; then \
		printf 'cannot release from non-main branch %s\n' "$$git_branch"; \
		false; \
	fi
	@git diff-index --quiet HEAD -- || (echo "git directory is dirty, commit changes first" && false)
	@echo "$@: complete"

.PHONY: release-update-version
release-update-version: release-git-check
	@echo "$@: started"
	@versioned -patch
	@versioned -prerelease -sync ./main.go
	@git add VERSION ./main.go
	@echo "$@: complete"

.PHONY: release-git-commit
release-git-commit: release-update-version
	@echo "$@: started"
	@git commit -m "ops: released v$$(head -n 1 VERSION)"
	@git tag -a "v$$(head -n 1 VERSION)" -m "v$$(head -n 1 VERSION)"
	@git push
	@git push --tags
	@echo "If necessary, run the following commands:"
	@release_version="$$(head -n 1 VERSION)"; \
	printf '  git push --delete origin v%s\n' "$$release_version"; \
	printf '  git tag --delete v%s\n' "$$release_version"; \
	printf '  go mod edit -retract v%s\n' "$$release_version"
	@echo "$@: complete"

.PHONY: release
release: release-git-commit
	@echo "$@: complete"
