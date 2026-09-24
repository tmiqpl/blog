# 博客系统构建脚本
#
# 常用命令：
#   make            # 编译当前平台
#   make linux      # 编译 Linux x86_64（静态链接，单文件）
#   make all        # 一次编译 Linux / Windows / macOS 三平台
#   make dist       # 生成带 systemd 单元的发布包
#   make test       # 运行全部测试
#
# 版本号可用 make linux VERSION=1.2.3 覆盖，默认取当前日期。

BINARY   := blog
VERSION  ?= 1.0.0-$(shell date +%Y%m%d)
DIST     := dist

# -s -w 去掉符号表与调试信息（体积约减 30%）
# -trimpath 抹掉本机绝对路径，保证构建可复现
LDFLAGS  := -s -w -X main.version=$(VERSION)
GOFLAGS  := -trimpath

# modernc.org/sqlite 是纯 Go 实现，关闭 CGO 即可交叉编译，无需 C 工具链
export CGO_ENABLED = 0

.PHONY: all build linux windows darwin test vet fmt dist clean help

help:
	@echo "可用目标："
	@echo "  build    编译当前平台 → $(BINARY)$(shell go env GOEXE)"
	@echo "  linux    Linux x86_64  → $(DIST)/$(BINARY)-linux-amd64"
	@echo "  windows  Windows x64   → $(DIST)/$(BINARY)-windows-amd64.exe"
	@echo "  darwin   macOS (Apple Silicon + Intel)"
	@echo "  all      以上三个平台"
	@echo "  dist     生成 tar.gz 发布包（含 systemd 单元）"
	@echo "  test     运行全部测试"
	@echo "  vet/fmt  静态检查 / 格式化"
	@echo "  clean    清理构建产物"

build:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY)$(shell go env GOEXE) .

linux:
	@mkdir -p $(DIST)
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-amd64 .

windows:
	@mkdir -p $(DIST)
	GOOS=windows GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-windows-amd64.exe .

darwin:
	@mkdir -p $(DIST)
	GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-arm64 .
	GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-amd64 .

all: linux windows darwin

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

# 发布包：二进制 + 文档 + systemd 单元 + 环境变量模板
# 逐个文件设置权限，不要用 tar --mode：那是全局的，会把含密码模板的
# blog.env.example 也变成 755。
dist: linux
	@rm -rf $(DIST)/stage
	@mkdir -p $(DIST)/stage/blog/content
	@cp $(DIST)/$(BINARY)-linux-amd64 $(DIST)/stage/blog/
	@cp README.md deploy/blog.service deploy/blog.env.example $(DIST)/stage/blog/
	@cp content/about.md $(DIST)/stage/blog/content/
	@chmod 755 $(DIST)/stage/blog/$(BINARY)-linux-amd64
	@chmod 600 $(DIST)/stage/blog/blog.env.example
	@chmod 644 $(DIST)/stage/blog/README.md $(DIST)/stage/blog/blog.service \
	          $(DIST)/stage/blog/content/about.md
	tar --owner=0 --group=0 -czf $(DIST)/$(BINARY)-linux-amd64.tar.gz -C $(DIST)/stage blog
	@cd $(DIST) && sha256sum $(BINARY)-linux-amd64 $(BINARY)-linux-amd64.tar.gz > SHA256SUMS
	@echo "发布包已生成：$(DIST)/$(BINARY)-linux-amd64.tar.gz"

clean:
	rm -rf $(DIST) $(BINARY) $(BINARY).exe
