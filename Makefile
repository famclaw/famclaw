BINARY    := famclaw
BUILD_DIR := ./bin
CMD       := ./cmd/famclaw
VERSION   := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS   := -ldflags "-s -w -X main.Version=$(VERSION)"

.PHONY: all build run dev test cross clean install install-service

## build: Build for current machine
build:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY) $(CMD)
	@echo "✅ $(BUILD_DIR)/$(BINARY)"

## run: Build and run with default config
run: build
	$(BUILD_DIR)/$(BINARY) --config config.yaml

## dev: Run with live reload (requires watchexec: brew install watchexec)
dev:
	watchexec -r -e go,html,js,css,rego,yaml -- $(MAKE) run

## test: Run all tests
test:
	go test ./... -v

## opa-test: Run OPA policy unit tests
opa-test:
	opa test ./policies -v

## cross: Build for all supported platforms
cross: cross-rpi3 cross-rpi4 cross-rpi5 cross-android cross-mac-intel cross-mac-arm cross-linux64

## cross-rpi3: Raspberry Pi 2/3/Zero (ARMv7)
cross-rpi3:
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 \
		go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-linux-armv7 $(CMD)
	@echo "✅ RPi 2/3/Zero → $(BUILD_DIR)/$(BINARY)-linux-armv7"

## cross-rpi4: Raspberry Pi 4/5 (ARM64)
cross-rpi4:
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
		go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-linux-arm64 $(CMD)
	@echo "✅ RPi 4/5 → $(BUILD_DIR)/$(BINARY)-linux-arm64"

cross-rpi5: cross-rpi4   # RPi 5 uses same arm64 binary

## cross-android: Old Android phones via Termux (arm64 + armv7)
cross-android:
	@mkdir -p $(BUILD_DIR)
	GOOS=android GOARCH=arm64 CGO_ENABLED=0 \
		go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-android-arm64 $(CMD)
	GOOS=android GOARCH=arm GOARM=7 CGO_ENABLED=0 \
		go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-android-armv7 $(CMD)
	@echo "✅ Android → $(BUILD_DIR)/$(BINARY)-android-*"

## cross-mac-intel: Mac Intel
cross-mac-intel:
	@mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 \
		go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-darwin-amd64 $(CMD)

## cross-mac-arm: Mac Apple Silicon / Mac Mini M-series
cross-mac-arm:
	@mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 \
		go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-darwin-arm64 $(CMD)

## cross-linux64: Generic Linux x86_64
cross-linux64:
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
		go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-linux-amd64 $(CMD)

## install: Install binary to /usr/local/bin
install: build
	sudo cp $(BUILD_DIR)/$(BINARY) /usr/local/bin/$(BINARY)

## install-rpi: Deploy to RPi over SSH (set RPI_HOST env var)
install-rpi: cross-rpi4
	ssh $(RPI_HOST) "sudo systemctl stop famclaw || true"
	scp $(BUILD_DIR)/$(BINARY)-linux-arm64 $(RPI_HOST):/usr/local/bin/$(BINARY)
	ssh $(RPI_HOST) "sudo systemctl start famclaw"
	@echo "✅ Deployed to $(RPI_HOST)"

## install-service: Install as a systemd service (Linux) or launchd (Mac)
install-service:
	@if [ "$$(uname)" = "Darwin" ]; then $(MAKE) install-launchd; else $(MAKE) install-systemd; fi

install-systemd: install
	sudo cp scripts/famclaw.service /etc/systemd/system/
	sudo systemctl daemon-reload
	sudo systemctl enable famclaw
	sudo systemctl start famclaw
	@echo "✅ systemd service running. Logs: journalctl -u famclaw -f"

install-launchd: install
	cp scripts/com.famclaw.plist ~/Library/LaunchAgents/
	sed -i '' "s|FAMCLAW_DIR|$(shell pwd)|g" ~/Library/LaunchAgents/com.famclaw.plist
	launchctl load ~/Library/LaunchAgents/com.famclaw.plist
	@echo "✅ launchd service running. Logs: tail -f ~/Library/Logs/famclaw.log"

## clean: Remove build artifacts
clean:
	rm -rf $(BUILD_DIR)
	go clean

help:
	@grep -E '^##' Makefile | sed 's/## /  /'
