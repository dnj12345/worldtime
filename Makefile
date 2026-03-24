# worldtime - Makefile
#
# Requires Go 1.21+. All targets produce fully static binaries (CGO_ENABLED=0).

BINARY  := worldtime
LDFLAGS := -ldflags="-s -w"
BUILD   := CGO_ENABLED=0 go build $(LDFLAGS)

.PHONY: all clean linux mac mac-arm windows

# Default: build for the current OS/arch
all:
	CGO_ENABLED=0 go build $(LDFLAGS) -o $(BINARY) .
	@echo "Built: ./$(BINARY)"

# Linux x86-64 (works on any Linux, including Alpine/musl)
linux:
	GOOS=linux GOARCH=amd64 $(BUILD) -o $(BINARY)-linux-amd64 .
	@echo "Built: ./$(BINARY)-linux-amd64"

# macOS Apple Silicon
mac-arm:
	GOOS=darwin GOARCH=arm64 $(BUILD) -o $(BINARY)-mac-arm64 .
	@echo "Built: ./$(BINARY)-mac-arm64"

# macOS Intel
mac:
	GOOS=darwin GOARCH=amd64 $(BUILD) -o $(BINARY)-mac-amd64 .
	@echo "Built: ./$(BINARY)-mac-amd64"

# Windows
windows:
	GOOS=windows GOARCH=amd64 $(BUILD) -o $(BINARY).exe .
	@echo "Built: ./$(BINARY).exe"

# All platforms at once
all-platforms: linux mac-arm mac windows

install: all
	cp $(BINARY) /usr/local/bin/$(BINARY)
	@echo "Installed to /usr/local/bin/$(BINARY)"

clean:
	rm -f $(BINARY) $(BINARY)-* $(BINARY).exe
