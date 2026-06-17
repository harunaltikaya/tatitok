BINARY    := tatitok
MODULE    := github.com/harunaltikaya/tatitok
DIST      := dist

export CGO_ENABLED := 0

.PHONY: test leakcheck lint web build build-all parity-full parity-full-codex parity-full-opencode soak clean

test: leakcheck
	go test -timeout 15m ./...

# Zero-real-names invariant: fails the build on any leak finding. Without
# the local alias map (e.g. CI) it runs structural checks only.
leakcheck:
	python3 scripts/check_fixture_leaks.py

lint:
	go vet ./...
	golangci-lint run

# Dashboard bundle (M4 Task 4): pinned toolchain (web/.nvmrc,
# package-lock.json), embedded via go:embed. Frontend unit tests run via
# Node's built-in runner (M6 Task 3, no test-runner dependency); the
# origin check runs after every bundle build — the served dashboard
# makes no external requests.
web:
	cd web && npm ci && npm test && npm run build
	go test -count=1 -run 'TestDistNoExternalOrigins$$' ./web

build: web
	go build -o $(DIST)/$(BINARY) ./cmd/$(BINARY)

# Cross-compile all four supported targets (CLAUDE.md stack rules); the
# embedded web bundle is platform-independent, built once.
build-all: web
	GOOS=linux   GOARCH=arm64 go build -o $(DIST)/$(BINARY)-linux-arm64       ./cmd/$(BINARY)
	GOOS=linux   GOARCH=amd64 go build -o $(DIST)/$(BINARY)-linux-amd64       ./cmd/$(BINARY)
	GOOS=darwin  GOARCH=arm64 go build -o $(DIST)/$(BINARY)-darwin-arm64      ./cmd/$(BINARY)
	GOOS=windows GOARCH=amd64 go build -o $(DIST)/$(BINARY)-windows-amd64.exe ./cmd/$(BINARY)

clean:
	rm -rf $(DIST)

# Owner-run full-history parity gate: recaptures ccusage from the LIVE
# logs at comparison time (pinned version from expected/META.json) —
# never reads an on-disk -full expectation file.
parity-full:
	TATITOK_PARITY_FULL=1 go test -v -run 'TestParityFull$$' ./internal/parity

parity-full-codex:
	TATITOK_PARITY_FULL_CODEX=1 go test -v -run TestParityFullCodex ./internal/parity

parity-full-opencode:
	TATITOK_PARITY_FULL_OPENCODE=1 go test -v -run TestParityFullOpencode ./internal/parity

# Watcher soak (M4 Task 1): replays the fixture corpus as live appends
# against a running watcher, asserts the rollup property afterwards, and
# reports throughput. Owner flavor: TATITOK_SOAK_DB=<copy-of-live-db>
# starts from real history (the given file is copied, never touched).
soak:
	TATITOK_SOAK=1 go test -v -timeout 10m -run 'TestWatcherSoak$$' ./internal/hub
