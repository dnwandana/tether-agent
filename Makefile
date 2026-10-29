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

.PHONY: build test test-race lint cross test-release fuzz startup-check soak

# The fuzz targets, as <package>:<target>. "make fuzz" runs each one for
# FUZZTIME. A failing input goes to testdata/fuzz/ in its package. The target
# runs all of them, then fails if one of them failed.
FUZZ_TARGETS := \
	./internal/tools:FuzzFuzzyFind \
	./internal/tools:FuzzFuzzyFindSpans \
	./internal/tools:FuzzParseDDG \
	./internal/provider:FuzzReadSSE \
	./internal/telegram:FuzzMarkdownV2 \
	./internal/telegram:FuzzSplitUTF16 \
	./internal/telegram:FuzzSplitMarkdownV2
FUZZTIME ?= 30s

# The soak length in minutes. For the full release soak, run
# "make soak SOAK_MINUTES=60".
SOAK_MINUTES ?= 5

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/tether ./cmd/tether

test:
	CGO_ENABLED=0 go test ./...

test-race:
	go test -race ./...

# gofmt -l exits 0 also when it lists files, so the target checks its output.
lint:
	@files="$$(gofmt -l .)"; \
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
