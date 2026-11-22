VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS = -s -w -X main.version=$(VERSION)

build: dist/tnas-backup-gui-linux-arm64

dist/tnas-backup-gui-linux-arm64: $(wildcard *.go) $(wildcard web/*)
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $@ .

dist: build
	mkdir -p dist/pkg && cp dist/tnas-backup-gui-linux-arm64 dist/pkg/tnas-backup-gui && cp deploy/* dist/pkg/
	tar -czf dist/tnas-backup-gui-$(VERSION)-linux-arm64.tar.gz -C dist/pkg .

vet:
	go vet ./...

clean:
	rm -rf dist

.PHONY: build dist vet clean
