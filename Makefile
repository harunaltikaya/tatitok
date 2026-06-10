BINARY    := tatitok
MODULE    := github.com/harunaltikaya/tatitok
DIST      := dist

export CGO_ENABLED := 0

.PHONY: test lint build build-all clean

test:
	go test ./...

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
