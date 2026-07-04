# leash - durable agent spend governor.
# Standard library plus github.com/sylvester-francis/rerun; nothing else.

GO ?= go
BINARY := leash
PKG := ./...

.PHONY: all build vet test race cover mutate fuzz bench lint ascii-check doc-check docker tidy fmt clean

# Pinned so CI and local runs agree; staticcheck is a tool, not a module dep.
STATICCHECK_VERSION ?= 2026.1

all: build vet lint test ascii-check doc-check

build:
	$(GO) build $(PKG)
	$(GO) build -o $(BINARY) ./cmd/leash

vet:
	$(GO) vet $(PKG)

# Static analysis and formatting gate: gofmt must be clean and staticcheck must
# pass with all checks (bugs, style, simplifications).
lint:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi; \
	echo "gofmt: ok"
	$(GO) run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) -checks=all $(PKG)

test:
	$(GO) test $(PKG)

race:
	$(GO) test -race $(PKG)

cover:
	$(GO) test -coverprofile=coverage.out $(PKG)
	$(GO) tool cover -func=coverage.out | tail -n 1

# Mutation testing with gremlins (https://github.com/go-gremlins/gremlins).
# Install: go install github.com/go-gremlins/gremlins/cmd/gremlins@latest
# The timeout coefficient gives each mutant enough headroom to recompile; with a
# fast test suite the default derives a sub-second timeout and every mutant times
# out. The README reports a kill rate only when one was actually measured; never
# claim an unmeasured rate.
#
# mutate      - the deterministic core (policy + meter): fast and repeatable.
# mutate-all  - the whole module, including the network and subprocess tests.
mutate:
	@if command -v gremlins >/dev/null 2>&1; then \
		gremlins unleash --timeout-coefficient 30 ./internal/policy/ ./internal/meter/ ; \
	else \
		echo "gremlins not installed."; \
		echo "install: go install github.com/go-gremlins/gremlins/cmd/gremlins@latest"; \
		echo "run:     gremlins unleash --timeout-coefficient 30 ./internal/policy/ ./internal/meter/"; \
		exit 1; \
	fi

mutate-all:
	gremlins unleash --timeout-coefficient 30 ./...

# Fuzz the parsers, leash's attack surface. Native Go fuzzing runs one target
# per invocation, so iterate over the three. FUZZTIME keeps each run short enough
# for CI; raise it for a deeper local soak (e.g. make fuzz FUZZTIME=2m).
FUZZTIME ?= 15s
fuzz:
	@for fz in FuzzParseUsageJSON FuzzStreamMeterTee FuzzInjectIncludeUsage; do \
		echo "== $$fz =="; \
		$(GO) test ./internal/meter/ -run '^$$' -fuzz "^$$fz$$" -fuzztime $(FUZZTIME) || exit 1; \
	done

# Benchmarks: end-to-end governed-call overhead at journal sizes 0/100/1k/10k,
# plus fold and stream-meter throughput. Numbers are machine-dependent; always
# state the machine when reporting them, and report only measured numbers.
bench:
	$(GO) test -run '^$$' -bench . -benchmem ./internal/policy/ ./internal/meter/ ./internal/proxy/

# Fail on any undocumented exported symbol (std-lib AST walker, no deps).
doc-check:
	$(GO) run ./tools/doccheck .

# Build the distroless container image, stamping the version from git.
IMAGE ?= leash:dev
docker:
	docker build -t $(IMAGE) --build-arg VERSION=$$(git describe --tags --always --dirty 2>/dev/null || echo dev) .

# Fail on any non-ASCII byte in .go and .md files. Tabs and newlines are
# allowed; everything outside printable ASCII plus tab is rejected.
ascii-check:
	@tab=$$(printf '\t'); \
	if LC_ALL=C grep -rn "[^$$tab -~]" --include='*.go' --include='*.md' . ; then \
		echo "ascii-check: non-ASCII bytes found (see above)"; exit 1; \
	else echo "ascii-check: ok"; fi

tidy:
	$(GO) mod tidy

fmt:
	$(GO) fmt $(PKG)

clean:
	rm -f $(BINARY) coverage.out
	$(GO) clean
