BINARY_NAME = ghostkvm
VERSION    ?= $(shell git describe --tags --always --dirty="-dev" 2>/dev/null || echo "0.1.0")
LDFLAGS    := -s -w -X=github.com/highercomve/GhostKVM/internal/version.Version=$(VERSION)

.PHONY: all build bletest icons screenshots release-tarball packages test vet run install-local uninstall-local clean help

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

# ---------------------------------------------------------------- release
# ARCH uses the release naming (x86_64 | aarch64); it must match the machine
# building, since the Fyne GUI needs cgo.
ARCH        ?= $(shell uname -m)
DEB_ARCH     = $(if $(filter aarch64,$(ARCH)),arm64,amd64)
PKG_VERSION  = $(patsubst v%,%,$(VERSION))
TARBALL_DIR  = release/ghostkvm-linux-$(ARCH)

release-tarball: build ## Build release/ghostkvm-linux-$(ARCH).tar.xz (the self-update bundle)
	rm -rf $(TARBALL_DIR) && mkdir -p $(TARBALL_DIR)/bin $(TARBALL_DIR)/share/applications $(TARBALL_DIR)/share/icons/hicolor/256x256/apps $(TARBALL_DIR)/share/icons/hicolor/scalable/apps
	install -m 755 bin/$(BINARY_NAME) $(TARBALL_DIR)/bin/ghostkvm
	install -m 644 packaging/ghostkvm.desktop $(TARBALL_DIR)/share/applications/
	install -m 644 Icon.png $(TARBALL_DIR)/share/icons/hicolor/256x256/apps/ghostkvm.png
	install -m 644 assets/ghostkvm.svg $(TARBALL_DIR)/share/icons/hicolor/scalable/apps/ghostkvm.svg
	install -m 644 README.md LICENSE.md CHANGELOG.md $(TARBALL_DIR)/
	tar -C release -cJf release/ghostkvm-linux-$(ARCH).tar.xz ghostkvm-linux-$(ARCH)
	@echo "built release/ghostkvm-linux-$(ARCH).tar.xz ($(VERSION))"

packages: build ## Build .deb, .rpm and Arch packages into release/ (needs nfpm)
	@mkdir -p release
	VERSION=$(PKG_VERSION) ARCH=$(DEB_ARCH) BINARY=bin/$(BINARY_NAME) envsubst < nfpm.yaml > release/nfpm-$(DEB_ARCH).yaml
	for p in deb rpm archlinux; do nfpm pkg --config release/nfpm-$(DEB_ARCH).yaml --packager $$p --target release/ || exit 1; done
	rm -f release/nfpm-$(DEB_ARCH).yaml

vet: ## Run go vet (including the screenshots build)
	go vet ./...
	go vet -tags screenshots ./...

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
	-update-desktop-database ~/.local/share/applications 2>/dev/null
	-gtk-update-icon-cache -q -t ~/.local/share/icons/hicolor 2>/dev/null
	@echo "Installed. Make sure ~/.local/bin is in your PATH to run: $(BINARY_NAME)"

uninstall-local: ## Remove what install-local added
	rm -f ~/.local/bin/$(BINARY_NAME) ~/.local/share/applications/ghostkvm.desktop
	rm -f ~/.local/share/icons/hicolor/256x256/apps/ghostkvm.png ~/.local/share/icons/hicolor/scalable/apps/ghostkvm.svg

clean: ## Remove build output
	rm -rf bin release

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'
