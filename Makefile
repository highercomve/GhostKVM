BINARY_NAME = ghostkvm
VERSION    ?= $(shell git describe --tags --always --dirty="-dev" 2>/dev/null || echo "0.1.0")
LDFLAGS    := -s -w -X=github.com/highercomve/GhostKVM/internal/version.Version=$(VERSION)

.PHONY: all build bletest icons screenshots test vet run install-local uninstall-local clean help

all: build

build: ## Build the desktop app (tray + window)
	@mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME) ./cmd/ghostkvm

bletest: ## Build the standalone Bluetooth keyboard/mouse test (static binary)
	@mkdir -p bin
	CGO_ENABLED=0 go build -ldflags "-s -w" -o bin/ghostkvm-bletest ./cmd/ghostkvm-bletest

icons: ## Render assets/*.png and Icon.png from the SVG sources (needs rsvg-convert)
	rsvg-convert -w 256 -h 256 assets/ghostkvm.svg -o assets/ghostkvm.png
	rsvg-convert -w 512 -h 512 assets/ghostkvm.svg -o assets/ghostkvm-512.png
	sed 's/@COLOR@/#6FDDB8/' assets/tray.svg.in | rsvg-convert -w 64 -h 64 -o assets/tray-connected.png
	sed 's/@COLOR@/#A9B0BD/' assets/tray.svg.in | rsvg-convert -w 64 -h 64 -o assets/tray-waiting.png
	sed 's/@COLOR@/#86A8FF/' assets/tray.svg.in | rsvg-convert -w 64 -h 64 -o assets/tray-active.png
	cp assets/ghostkvm.png Icon.png

screenshots: ## Render the README/website screenshots from the real UI with sample data
	go run -tags screenshots -ldflags "-X=github.com/highercomve/GhostKVM/internal/version.Version=$(VERSION)" ./cmd/ghostkvm-screenshots docs/images

vet: ## Run go vet
	go vet ./...

test: ## Run the tests
	go test ./...

run: build ## Build and run the app
	./bin/$(BINARY_NAME)

install-local: build ## Install to ~/.local with a launcher entry and icon
	@mkdir -p ~/.local/bin ~/.local/share/applications ~/.local/share/icons/hicolor/256x256/apps ~/.local/share/icons/hicolor/scalable/apps
	install -m 755 bin/$(BINARY_NAME) ~/.local/bin/$(BINARY_NAME)
	sed "s|\$$HOME|$(HOME)|g" packaging/ghostkvm-local.desktop > ~/.local/share/applications/ghostkvm.desktop
	chmod 644 ~/.local/share/applications/ghostkvm.desktop
	install -m 644 Icon.png ~/.local/share/icons/hicolor/256x256/apps/ghostkvm.png
	install -m 644 assets/ghostkvm.svg ~/.local/share/icons/hicolor/scalable/apps/ghostkvm.svg
	@echo "Installed. Make sure ~/.local/bin is in your PATH to run: $(BINARY_NAME)"

uninstall-local: ## Remove what install-local added
	rm -f ~/.local/bin/$(BINARY_NAME) ~/.local/share/applications/ghostkvm.desktop
	rm -f ~/.local/share/icons/hicolor/256x256/apps/ghostkvm.png ~/.local/share/icons/hicolor/scalable/apps/ghostkvm.svg

clean: ## Remove build output
	rm -rf bin

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'
