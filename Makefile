BINARY  := lastwatt
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PREFIX  ?= /usr/local
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build test vet fmt clean install uninstall

all: build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/lastwatt

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal

clean:
	rm -f $(BINARY)

install: build
	install -Dm755 $(BINARY) $(DESTDIR)$(PREFIX)/bin/$(BINARY)
	install -Dm644 packaging/systemd/lastwatt.service $(DESTDIR)/etc/systemd/system/lastwatt.service
	@if [ ! -f $(DESTDIR)/etc/lastwatt/lastwatt.toml ]; then \
		install -Dm644 configs/lastwatt.toml $(DESTDIR)/etc/lastwatt/lastwatt.toml; \
		echo "installed default config to /etc/lastwatt/lastwatt.toml"; \
	else \
		echo "kept existing /etc/lastwatt/lastwatt.toml"; \
	fi
	@echo
	@echo "Next: review /etc/lastwatt/lastwatt.toml, then"
	@echo "  lastwatt simulate      # dry-run, changes nothing"
	@echo "  systemctl daemon-reload && systemctl enable --now lastwatt"

uninstall:
	systemctl disable --now lastwatt 2>/dev/null || true
	rm -f $(DESTDIR)$(PREFIX)/bin/$(BINARY) $(DESTDIR)/etc/systemd/system/lastwatt.service
	@echo "left /etc/lastwatt and /var/lib/lastwatt in place"
