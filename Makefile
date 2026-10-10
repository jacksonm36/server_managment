GO      ?= go
PROTOC  ?= protoc
VERSION ?= $(shell git describe --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
export PATH := $(HOME)/go/bin:$(PATH)

.PHONY: all proto web panel agent run dev-web vet clean

all: web panel agent

## proto: regenerate Go code from proto/ (needs protoc, protoc-gen-go, protoc-gen-go-grpc)
proto:
	$(PROTOC) -I proto \
		--go_out=. --go_opt=module=github.com/jacksonm36/servermgr \
		--go-grpc_out=. --go-grpc_opt=module=github.com/jacksonm36/servermgr \
		proto/agent/v1/agent.proto

## web: build the UI into web/dist (embedded into the panel binary)
web:
	cd web && npm ci --no-audit --no-fund && npm run build
	touch web/dist/.gitkeep

panel:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o bin/panel ./panel

agent:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o bin/servermgr-agent-linux-amd64 ./agent
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o bin/servermgr-agent-linux-arm64 ./agent
	echo "$(VERSION)" > bin/agent.version

## run: run the panel in the foreground with settings from .env
run: panel
	set -a; . ./.env; set +a; ./bin/panel

## dev-web: Vite dev server with API proxied to the panel on :8090
dev-web:
	cd web && npm run dev

vet:
	$(GO) vet ./...

clean:
	rm -rf bin web/dist/assets web/dist/index.html
	$(GO) clean -cache
