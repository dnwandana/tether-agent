VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

# The release targets. Each entry is <goos>-<goarch>.
PLATFORMS := linux-amd64 linux-arm64 darwin-arm64

# staticcheck is a tool, not a module dependency. The lint target uses the
# staticcheck in PATH. If there is none, it uses "go run", which does not
# change go.mod.
STATICCHECK_VERSION ?= 2026.2.2
STATICCHECK ?= $(shell command -v staticcheck 2>/dev/null)
ifeq ($(STATICCHECK),)
STATICCHECK := go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)
endif

.PHONY: build test test-race lint cross test-release fuzz startup-check soak web web-test web-lint web-format

# The fuzz targets, as <package>:<target>. "make fuzz" runs each one for
# FUZZTIME. A failing input goes to testdata/fuzz/ in its package. The target
# runs all of them, then fails if one of them failed.
FUZZ_TARGETS := \
	./internal/tools:FuzzFuzzyFind \
	./internal/tools:FuzzFuzzyFindSpans \
	./internal/tools:FuzzParseDDG \
	./internal/provider:FuzzReadSSE \
	./internal/provider:FuzzResponsesParse \
	./internal/security:FuzzMatchAllDangerous \
	./internal/security:FuzzReadCommand \
	./internal/security:FuzzScan \
	./internal/security:FuzzBindSecrets \
	./internal/telegram:FuzzMarkdownV2 \
	./internal/telegram:FuzzSplitUTF16 \
	./internal/telegram:FuzzSplitMarkdownV2 \
	./internal/telegram:FuzzApprovalMessages \
	./internal/telegram:FuzzRichMessage
FUZZTIME ?= 30s

# The soak length in minutes. For the full release soak, run
# "make soak SOAK_MINUTES=60".
SOAK_MINUTES ?= 5

build:
	@test -f internal/dashboard/dist/index.html || \
		echo "build: the dashboard page is not built, so the dashboard shows only a note. Run make web first."
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/tether ./cmd/tether

test:
	CGO_ENABLED=0 go test ./...

test-race:
	go test -race ./...

# The web dashboard. "make web" needs Node and npm. It builds web/ into
# internal/dashboard/dist/, and the binary embeds it. Git keeps only the
# placeholder dist/.keep, so the release (the GoReleaser "before" hook) runs
# this target and needs Node too.
web:
	rm -rf internal/dashboard/dist
	npm --prefix web ci
	@# npm 11 can skip the install script of esbuild. The rebuild is harmless when the binary exists.
	npm --prefix web rebuild esbuild
	npm --prefix web run build
	node web/scripts/compress.mjs internal/dashboard/dist
	CGO_ENABLED=0 go run ./cmd/tether gen-dashboard-hash
	@# The build deletes dist/. Write the placeholder that git keeps again.
	@printf '%s\n' 'This folder holds the built page of the web dashboard. Run make web to build it.' \
		'Git keeps only this file, so the Go build works before make web runs.' > internal/dashboard/dist/.keep

# The Vitest tests of the web dashboard. "make test" does not run them.
web-test:
	@test -d web/node_modules || npm --prefix web ci
	npm --prefix web run test

# oxlint and the Prettier check of web/. "make lint" does not run them.
web-lint:
	@test -d web/node_modules || npm --prefix web ci
	npm --prefix web run lint
	npm --prefix web run format:check

# Formats web/ with Prettier.
web-format:
	@test -d web/node_modules || npm --prefix web ci
	npm --prefix web run format

# gofmt -l exits 0 also when it lists files, so the target checks its output.
# The target checks only the .go files that git tracks or finds untracked and not ignored, because web/node_modules/
# can hold .go files.
lint:
	@files="$$(git ls-files -z --cached --others --exclude-standard '*.go' | xargs -0 gofmt -l)"; \
	if [ -n "$$files" ]; then \
		echo "lint: gofmt found files that are not formatted. Run gofmt -w on these files:"; \
		echo "$$files"; \
		exit 1; \
	fi
	CGO_ENABLED=0 go vet ./...
	CGO_ENABLED=0 $(STATICCHECK) ./...

cross:
	@for p in $(PLATFORMS); do \
		os=$${p%-*}; arch=$${p#*-}; \
		echo "build bin/tether-$$p"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" \
			-o bin/tether-$$p ./cmd/tether || exit 1; \
	done

test-release:
	bash scripts/check_release_test.sh

fuzz:
	@fail=0; \
	for t in $(FUZZ_TARGETS); do \
		pkg=$${t%%:*}; name=$${t#*:}; \
		echo "fuzz: $$name in $$pkg for $(FUZZTIME)"; \
		CGO_ENABLED=0 go test -run='^$$' -fuzz="^$$name\$$" -fuzztime=$(FUZZTIME) $$pkg || fail=1; \
	done; \
	exit $$fail

startup-check:
	CGO_ENABLED=0 go test -count=1 -run '^TestStartupUnder100ms$$' -v ./cmd/tether/

soak:
	bash scripts/soak.sh $(SOAK_MINUTES)
