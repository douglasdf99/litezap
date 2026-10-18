#!/bin/sh
# Instala o LiteZap a partir da última release do GitHub.
#
#   curl -fsSL https://raw.githubusercontent.com/douglasdf99/litezap/main/scripts/install.sh | sh
#
# Variáveis opcionais:
#   LITEZAP_VERSION      tag a instalar (ex.: v0.1.0); padrão: a mais recente
#   LITEZAP_INSTALL_DIR  diretório de destino; padrão: ~/.local/bin se estiver no PATH,
#                        senão /usr/local/bin (usa sudo só se não tiver permissão de escrita)
set -eu

REPO="douglasdf99/litezap"
BIN="litezap"

say() { printf '%s\n' "$*" >&2; }
die() { say "erro: $*"; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

detect_os() {
	case "$(uname -s)" in
		Linux) echo linux ;;
		Darwin) echo darwin ;;
		*) die "sistema não suportado: $(uname -s) (no Windows, baixe o .zip em https://github.com/$REPO/releases)" ;;
	esac
}

detect_arch() {
	case "$(uname -m)" in
		x86_64 | amd64) echo amd64 ;;
		arm64 | aarch64) echo arm64 ;;
		*) die "arquitetura não suportada: $(uname -m)" ;;
	esac
}

download() { # url destino
	if have curl; then
		curl -fsSL -o "$2" "$1"
	elif have wget; then
		wget -q -O "$2" "$1"
	else
		die "preciso de curl ou wget"
	fi
}

sha256() {
	if have sha256sum; then
		sha256sum "$1" | cut -d' ' -f1
	elif have shasum; then
		shasum -a 256 "$1" | cut -d' ' -f1
	else
		die "preciso de sha256sum ou shasum para verificar o download"
	fi
}

in_path() {
	case ":$PATH:" in
		*":$1:"*) return 0 ;;
		*) return 1 ;;
	esac
}

main() {
	os=$(detect_os)
	arch=$(detect_arch)
	asset="${BIN}_${os}_${arch}.tar.gz"
	version="${LITEZAP_VERSION:-latest}"
	if [ "$version" = "latest" ]; then
		base="https://github.com/$REPO/releases/latest/download"
	else
		base="https://github.com/$REPO/releases/download/$version"
	fi

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT INT TERM

	say "Baixando $asset ($version)..."
	download "$base/$asset" "$tmp/$asset"
	download "$base/checksums.txt" "$tmp/checksums.txt"

	expected=$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/checksums.txt")
	[ -n "$expected" ] || die "$asset não está em checksums.txt"
	actual=$(sha256 "$tmp/$asset")
	[ "$expected" = "$actual" ] || die "checksum não confere para $asset (esperado $expected, obtido $actual)"
	say "Checksum sha256 ok."

	tar -xzf "$tmp/$asset" -C "$tmp" "$BIN"

	dir="${LITEZAP_INSTALL_DIR:-}"
	if [ -z "$dir" ]; then
		if in_path "$HOME/.local/bin"; then
			dir="$HOME/.local/bin"
		else
			dir="/usr/local/bin"
		fi
	fi

	sudo=""
	if [ -d "$dir" ]; then
		[ -w "$dir" ] || sudo="sudo"
	elif ! mkdir -p "$dir" 2>/dev/null; then
		sudo="sudo"
	fi
	if [ -n "$sudo" ]; then
		have sudo || die "sem permissão de escrita em $dir; rode como root ou defina LITEZAP_INSTALL_DIR"
		say "Sem permissão de escrita em $dir: usando sudo."
		$sudo mkdir -p "$dir"
	fi
	$sudo install -m 0755 "$tmp/$BIN" "$dir/$BIN"

	say "LiteZap instalado em $dir/$BIN ($("$dir/$BIN" version 2>/dev/null || echo "$version"))."
	in_path "$dir" || say "Aviso: $dir não está no seu PATH."
	say "Comece com: $BIN serve"
}

main "$@"
