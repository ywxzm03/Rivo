#!/bin/sh
# Rivo installer for macOS (Intel / Apple Silicon)
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/ywxzm03/Rivo/main/install.sh | sh
#
# Environment:
#   RIVO_VERSION       Install a specific release, e.g. v0.1.0
#   RIVO_INSTALL_DIR   Override the install directory (absolute path)
set -eu

REPO="ywxzm03/Rivo"
BIN="rivo"
DEFAULT_INSTALL_DIR="/usr/local/bin"
FALLBACK_INSTALL_DIR="${HOME}/.local/bin"

log() {
	printf '%s\n' "$*"
}

die() {
	printf 'rivo installer: %s\n' "$*" >&2
	exit 1
}

need() {
	command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

detect_platform() {
	os=$(uname -s)
	case "$os" in
		Darwin) ;;
		MINGW* | MSYS* | CYGWIN*) die "install.sh does not support Windows; download the release zip manually" ;;
		*) die "install.sh currently supports macOS only (detected $os)" ;;
	esac

	machine=$(uname -m)
	case "$machine" in
		x86_64 | amd64) arch="amd64" ;;
		arm64 | aarch64) arch="arm64" ;;
		*) die "unsupported macOS architecture: $machine" ;;
	esac
}

resolve_tag() {
	if [ -n "${RIVO_VERSION:-}" ] && [ "${RIVO_VERSION}" != "latest" ]; then
		case "$RIVO_VERSION" in
			v*) tag="$RIVO_VERSION" ;;
			*) tag="v$RIVO_VERSION" ;;
		esac
		return
	fi

	latest_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest") ||
		die "failed to resolve the latest release"
	tag=${latest_url##*/}
	case "$tag" in
		v*) ;;
		*) die "could not resolve a release tag from $latest_url" ;;
	esac
}

install_dir_for() {
	if [ -n "${RIVO_INSTALL_DIR:-}" ]; then
		printf '%s\n' "$RIVO_INSTALL_DIR"
		return
	fi
	if [ -d "$DEFAULT_INSTALL_DIR" ] && [ -w "$DEFAULT_INSTALL_DIR" ]; then
		printf '%s\n' "$DEFAULT_INSTALL_DIR"
		return
	fi
	printf '%s\n' "$FALLBACK_INSTALL_DIR"
}

configure_path() {
	dir=$1
	shell_name=${SHELL:-}
	shell_name=${shell_name##*/}

	case "$shell_name" in
		zsh) profile="${ZDOTDIR:-$HOME}/.zshrc" ;;
		bash)
			if [ "$(uname -s)" = "Darwin" ]; then
				profile="$HOME/.bash_profile"
			else
				profile="$HOME/.bashrc"
			fi
			;;
		*)
			log "Add this to your shell profile:"
			log "  export PATH=\"$dir:\$PATH\""
			return
			;;
	esac

	marker="# >>> rivo installer >>>"
	if [ -f "$profile" ] && grep -F "$marker" "$profile" >/dev/null 2>&1; then
		log "PATH already configured in $profile"
		return
	fi
	if ! touch "$profile" 2>/dev/null; then
		log "Could not write $profile; add this to your shell profile:"
		log "  export PATH=\"$dir:\$PATH\""
		return
	fi

	{
		printf '\n%s\n' "$marker"
		# 写入配置文件时需要保留字面量 $PATH
		# shellcheck disable=SC2016
		printf 'export PATH="%s:$PATH"\n' "$dir"
		printf '%s\n' "# <<< rivo installer <<<"
	} >>"$profile" || die "failed to update $profile"
	log "Added $dir to PATH in $profile"
}

main() {
	need curl
	need tar
	need awk
	need mktemp

	detect_platform
	resolve_tag

	install_dir=$(install_dir_for)
	case "$install_dir" in
		/*) ;;
		*) die "RIVO_INSTALL_DIR must be an absolute path: $install_dir" ;;
	esac
	mkdir -p "$install_dir" || die "failed to create $install_dir"
	[ -w "$install_dir" ] || die "$install_dir is not writable"

	version=${tag#v}
	asset="${BIN}_${version}_darwin_${arch}.tar.gz"
	base_url="https://github.com/${REPO}/releases/download/${tag}"

	tmp=$(mktemp -d "${TMPDIR:-/tmp}/rivo.XXXXXX") || die "failed to create a temporary directory"
	trap 'rm -rf "$tmp"' 0 1 2 15

	log "Downloading Rivo ${version} for darwin/${arch}"
	curl -fsSL "${base_url}/${asset}" -o "${tmp}/${asset}" || die "failed to download ${asset}"
	curl -fsSL "${base_url}/checksums.txt" -o "${tmp}/checksums.txt" || die "failed to download checksums.txt"

	expected=$(awk -v name="$asset" '{ file=$2; sub(/^\*/, "", file); if (file == name) { print $1; exit } }' "${tmp}/checksums.txt")
	[ -n "$expected" ] || die "checksum for ${asset} not found"

	if command -v sha256sum >/dev/null 2>&1; then
		actual=$(sha256sum "${tmp}/${asset}" | awk '{ print $1 }')
	elif command -v shasum >/dev/null 2>&1; then
		actual=$(shasum -a 256 "${tmp}/${asset}" | awk '{ print $1 }')
	else
		die "sha256sum or shasum is required"
	fi
	[ "$actual" = "$expected" ] || die "checksum mismatch for ${asset}"

	tar -xzf "${tmp}/${asset}" -C "$tmp" || die "failed to extract ${asset}"
	[ -f "${tmp}/${BIN}" ] || die "archive does not contain ${BIN}"

	cp "${tmp}/${BIN}" "${install_dir}/${BIN}" || die "failed to install ${BIN}"
	chmod 0755 "${install_dir}/${BIN}"

	log "Installed ${install_dir}/${BIN}"
	if ! "${install_dir}/${BIN}" --version; then
		log "warning: installed binary did not run successfully"
	fi

	case ":${PATH:-}:" in
		*":${install_dir}:"*) ;;
		*)
			configure_path "$install_dir"
			log "Restart your shell or run: export PATH=\"$install_dir:\$PATH\""
			;;
	esac
}

main "$@"
