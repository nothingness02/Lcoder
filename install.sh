#!/usr/bin/env bash
# Lcoder 一键安装脚本。
# 用法：
#   curl -fsSL https://raw.githubusercontent.com/nothingness02/lcoder/master/install.sh | bash
#   curl -fsSL https://raw.githubusercontent.com/nothingness02/lcoder/master/install.sh | bash -s -- --version v1.0.0
#   ./install.sh --binary /path/to/lcoder     # 从本地二进制安装
set -euo pipefail

APP=lcoder
OWNER=nothingness02
REPO=lcoder
INSTALL_DIR=${LCODER_INSTALL_DIR:-$HOME/.lcoder/bin}

MUTED='\033[0;2m'
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

usage() {
    cat <<EOF
Lcoder Installer

Usage: install.sh [options]

Options:
    -h, --help              Display this help message
    -v, --version <version> Install a specific version (e.g., v1.0.0; default: latest)
    -b, --binary <path>     Install from a local binary instead of downloading
        --no-modify-path    Don't modify shell config files (.zshrc, .bashrc, etc.)

Examples:
    curl -fsSL https://raw.githubusercontent.com/${OWNER}/${REPO}/master/install.sh | bash
    curl -fsSL https://raw.githubusercontent.com/${OWNER}/${REPO}/master/install.sh | bash -s -- --version v1.0.0
EOF
}

requested_version="latest"
no_modify_path=false
binary_path=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        -h|--help)
            usage
            exit 0
            ;;
        -v|--version)
            if [[ -n "${2:-}" ]]; then
                requested_version="$2"
                shift 2
            else
                echo -e "${RED}Error: --version requires an argument${NC}" >&2
                exit 1
            fi
            ;;
        -b|--binary)
            if [[ -n "${2:-}" ]]; then
                binary_path="$2"
                shift 2
            else
                echo -e "${RED}Error: --binary requires a path argument${NC}" >&2
                exit 1
            fi
            ;;
        --no-modify-path)
            no_modify_path=true
            shift
            ;;
        *)
            echo -e "${RED}Warning: Unknown option '$1'${NC}" >&2
            shift
            ;;
    esac
done

# ── 目录 ──────────────────────────────────────────────────────────────
mkdir -p "$INSTALL_DIR"

install_from_binary() {
    local src="$1"
    if [[ ! -f "$src" ]]; then
        echo -e "${RED}Error: Binary not found at ${src}${NC}" >&2
        exit 1
    fi
    cp "$src" "$INSTALL_DIR/$APP"
    chmod +x "$INSTALL_DIR/$APP"
    echo -e "${GREEN}Installed ${APP} to ${INSTALL_DIR}/${APP}${NC}"
}

download_and_install() {
    # ── 检测 OS/arch ───────────────────────────────────────────────────
    local raw_os arch
    raw_os=$(uname -s)
    case "$raw_os" in
        Darwin*) os="darwin" ;;
        Linux*)  os="linux" ;;
        MINGW*|MSYS*|CYGWIN*) os="windows" ;;
        *)
            echo -e "${RED}Unsupported OS: $raw_os${NC}" >&2
            exit 1
            ;;
    esac

    arch=$(uname -m)
    case "$arch" in
        x86_64|amd64)  arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        *)
            echo -e "${RED}Unsupported arch: $arch${NC}" >&2
            exit 1
            ;;
    esac

    # Windows arm64 不发布（goreleaser ignore 配置），直接拒绝。
    if [[ "$os" == "windows" && "$arch" == "arm64" ]]; then
        echo -e "${RED}Windows arm64 is not supported yet${NC}" >&2
        exit 1
    fi

    # ── 解析版本号（latest → 查 GitHub API）───────────────────────────
    local version="$1"
    if [[ "$version" == "latest" ]]; then
        version=$(curl -fsSL "https://api.github.com/repos/${OWNER}/${REPO}/releases/latest" | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": "\(.*\)".*/\1/')
        if [[ -z "$version" ]]; then
            echo -e "${RED}Error: could not resolve latest version${NC}" >&2
            exit 1
        fi
    fi
    echo -e "${MUTED}Installing ${APP} ${version} (${os}-${arch})${NC}"

    # ── 下载并解压 ────────────────────────────────────────────────────
    local base="https://github.com/${OWNER}/${REPO}/releases/download/${version}"
    local ext="tar.gz"
    [[ "$os" == "windows" ]] && ext="zip"

    local tmp
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT

    local url="${base}/${APP}_${version#v}_${os}_${arch}.${ext}"
    echo -e "${MUTED}Downloading ${url}${NC}"
    curl -fsSL "$url" -o "$tmp/archive.$ext"

    if [[ "$ext" == "zip" ]]; then
        # unzip 解出 lcoder（归档内顶层文件名就是 lcoder）
        unzip -o "$tmp/archive.$ext" -d "$tmp/extract" >/dev/null
    else
        tar -xzf "$tmp/archive.$ext" -C "$tmp"
    fi

    # 归档内可能有子目录（goreleaser tar.gz 顶层是目录名），递归找二进制。
    local bin
    bin=$(find "$tmp" -type f -name "$APP" -o -type f -name "$APP.exe" | head -1)
    if [[ -z "$bin" ]]; then
        echo -e "${RED}Error: binary not found in archive${NC}" >&2
        exit 1
    fi

    cp "$bin" "$INSTALL_DIR/$APP"
    chmod +x "$INSTALL_DIR/$APP"
    echo -e "${GREEN}Installed ${APP} ${version} to ${INSTALL_DIR}/${APP}${NC}"
}

# ── 主流程 ──────────────────────────────────────────────────────────────
if [[ -n "$binary_path" ]]; then
    install_from_binary "$binary_path"
else
    download_and_install "$requested_version"
fi

# ── PATH 修改 ───────────────────────────────────────────────────────────
if [[ "$no_modify_path" == false ]]; then
    shell_rc=""
    case "$SHELL" in
        */zsh) shell_rc="$HOME/.zshrc" ;;
        */bash) shell_rc="$HOME/.bashrc" ;;
    esac
    if [[ -n "$shell_rc" ]]; then
        if ! grep -q "$INSTALL_DIR" "$shell_rc" 2>/dev/null; then
            echo "export PATH=\"$INSTALL_DIR:\$PATH\"" >> "$shell_rc"
            echo -e "${MUTED}Added ${INSTALL_DIR} to PATH in ${shell_rc}${NC}"
        fi
    else
        echo -e "${MUTED}Unknown shell; add ${INSTALL_DIR} to PATH manually${NC}"
    fi
fi

# ── 自检 ────────────────────────────────────────────────────────────────
if "$INSTALL_DIR/$APP" --version >/dev/null 2>&1; then
    echo -e "${GREEN}${APP} is ready. Run:${NC} ${APP}"
else
    echo -e "${RED}Warning: ${APP} installed but self-check failed${NC}" >&2
    exit 1
fi
