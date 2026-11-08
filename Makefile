BIN      := groundcontrol
PKG      := ./cmd/groundcontrol
LDFLAGS  := -s -w
PLATFORMS := linux/amd64 linux/386 linux/arm64 linux/arm darwin/amd64 darwin/arm64 windows/amd64 windows/386 windows/arm64

.PHONY: all build test vet dist clean

all: vet test build

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) $(PKG)

test:
	go test ./...

vet:
	go vet ./...

dist:
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "build $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' \
			-o dist/$(BIN)-$$os-$$arch$$ext $(PKG) || exit 1; \
	done

clean:
	rm -rf dist $(BIN)
