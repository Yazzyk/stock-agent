SHELL := /bin/bash
APP := guanlan

.PHONY: help build run dev test verify headless tidy clean fmt vet lint

help:
	@echo "make build      # 编译 Go（embed 默认前端产物）"
	@echo "make dev        # Wails 热重载"
	@echo "make run        # 桌面端运行（构建后打开）"
	@echo "make test       # go test ./..."
	@echo "make verify     # 一键验收（含前端构建）"
	@echo "make headless T=selftest  # 无界面任务"
	@echo "make fmt vet    # 格式化与静态检查"

build:
	go build -o bin/$(APP) .

dev:
	wails dev

run: build
	./bin/$(APP)

test:
	go test ./... -count=1

verify:
	bash scripts/verify.sh

headless:
	@if [ -z "$(T)" ]; then echo "用法: make headless T=selftest"; exit 2; fi
	go run . --headless --task=$(T)

fmt:
	gofmt -l -w $$(find . -name '*.go' -not -path './frontend/*' -not -path './build/*')
	cd frontend && npx --no-install vue-tsc --noEmit || true

vet:
	go vet ./...

clean:
	rm -rf bin build/bin frontend/dist/assets
