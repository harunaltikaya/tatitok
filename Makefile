BINARY    := tatitok
MODULE    := github.com/harunaltikaya/tatitok
DIST      := dist

export CGO_ENABLED := 0

.PHONY: test leakcheck lint build build-all parity-full clean

test: leakcheck
	go test ./...

# Zero-real-names invariant: fails the build on any leak finding. Without
# the local alias map (e.g. CI) it runs structural checks only.
leakcheck:
	python3 scripts/check_fixture_leaks.py

lint:
	go vet ./...
	golangci-lint run

build:
	go build -o $(DIST)/$(BINARY) ./cmd/$(BINARY)

# Cross-compile all four supported targets (CLAUDE.md stack rules).
build-all:
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
	TATITOK_PARITY_FULL=1 go test -v -run TestParityFull ./internal/parity
