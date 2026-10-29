#!/bin/sh
# DeployPilot installer.
#
# Usage:
#   curl -fsSL https://github.com/66kris-ye/deploypilot/releases/latest/download/install.sh | sh
#
# Environment overrides:
#   DP_VERSION      release tag to install (default: latest)
#   DP_REPO         GitHub owner/repo hosting the releases
#   DP_BASE_URL     full base URL that hosts the release assets; when set,
#                   only this source is used (no GitHub, no mirrors)
#   DP_MIRROR       mirror prefix to try first, e.g. https://gh-proxy.com/
#   DP_INSTALL_DIR  where to place the binary (default: ~/.deploypilot/bin)
#
# The script detects the CPU architecture, downloads the matching binary and
# the checksums file, verifies the SHA-256, then runs it. If GitHub is
# unreachable or too slow, it falls back to public GitHub mirrors.

set -eu

REPO="${DP_REPO:-66kris-ye/deploypilot}"
VERSION="${DP_VERSION:-latest}"
MIRRORS="https://gh-proxy.com/ https://ghfast.top/"

log()  { printf '\033[36m==>\033[0m %s\n' "$1" >&2; }
err()  { printf '\033[31merror:\033[0m %s\n' "$1" >&2; exit 1; }

# --- platform detection -----------------------------------------------------
os="$(uname -s)"
[ "$os" = "Linux" ] || err "DeployPilot currently supports Linux only (got $os)."

case "$(uname -m)" in
  x86_64 | amd64)   arch="amd64" ;;
  aarch64 | arm64)  arch="arm64" ;;
  *) err "unsupported architecture: $(uname -m)" ;;
esac
asset="deploypilot-linux-${arch}"

# --- download sources -------------------------------------------------------
if [ "$VERSION" = "latest" ]; then
  gh_base="https://github.com/${REPO}/releases/latest/download"
else
  gh_base="https://github.com/${REPO}/releases/download/${VERSION}"
fi

if [ -n "${DP_BASE_URL:-}" ]; then
  sources="$DP_BASE_URL"
else
  sources=""
  [ -n "${DP_MIRROR:-}" ] && sources="${DP_MIRROR}${gh_base}"
  sources="$sources $gh_base"
  for m in $MIRRORS; do
    [ "$m" = "${DP_MIRROR:-}" ] || sources="$sources ${m}${gh_base}"
  done
fi

# download <url> <dest>: aborts if the transfer stays below ~50KB/s for 15s,
# so a slow source is skipped quickly instead of crawling for minutes.
download() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --connect-timeout 10 --speed-limit 51200 --speed-time 15 -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q --timeout=20 -O "$2" "$1"
  else
    err "need curl or wget to download"
  fi
}

tmp="$(mktemp -d)"
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT INT TERM

log "下载 $asset ($VERSION) …"
ok=""
for base in $sources; do
  if download "${base}/${asset}" "${tmp}/${asset}" && download "${base}/checksums.txt" "${tmp}/checksums.txt"; then
    ok="$base"
    break
  fi
  log "下载源不可用或太慢，尝试下一个 …"
done
[ -n "$ok" ] || err "所有下载源都失败了，请检查网络后重试"

# --- verify checksum --------------------------------------------------------
# Strip CR so checksum files with Windows line endings still match.
want="$(tr -d '\r' < "${tmp}/checksums.txt" | awk -v f="$asset" '$2==f || $2=="*"f {print $1}')"
[ -n "$want" ] || err "校验和文件中找不到 $asset"

if command -v sha256sum >/dev/null 2>&1; then
  got="$(sha256sum "${tmp}/${asset}" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  got="$(shasum -a 256 "${tmp}/${asset}" | awk '{print $1}')"
else
  err "need sha256sum or shasum to verify download"
fi
[ "$got" = "$want" ] || err "校验和不匹配 (期望 $want, 实际 $got)"
log "校验通过。"

# --- install & run ----------------------------------------------------------
dir="${DP_INSTALL_DIR:-${HOME}/.deploypilot/bin}"
mkdir -p "$dir"
bin="${dir}/deploypilot"
cp "${tmp}/${asset}" "${bin}.new"
chmod +x "${bin}.new"
mv -f "${bin}.new" "$bin"

# exec replaces this shell, so clean up the temp dir first.
cleanup
trap - EXIT INT TERM

log "启动 DeployPilot …"
exec "$bin" "$@"
