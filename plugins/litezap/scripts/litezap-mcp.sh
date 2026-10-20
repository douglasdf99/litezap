#!/bin/sh
# MCP server of the LiteZap Claude Code plugin (.mcp.json runs `sh litezap-mcp.sh`).
#
# Like the Claude Desktop extension shim (cmd/litezap-mcpb, internal/mcpb), it carries no
# tools of its own: it finds the LiteZap installed on this computer and replaces itself
# with its stdio MCP server, so the tools always match the installed version and no token
# is copied anywhere. stdout is the MCP channel: messages go to stderr only.
#
# Order:
#   1. LITEZAP_TOKEN set and `litezap` on PATH: `litezap mcp` (server/CLI install; it
#      reads LITEZAP_URL, default http://127.0.0.1:8787, and LITEZAP_TOKEN).
#   2. The desktop app, `<app> mcp`: LITEZAP_APP_EXE, then the app-exe file the app writes
#      in its folder every time it opens, then the usual install places.
#   3. `litezap` on PATH without a token: run it anyway, it says what is missing.
#
# Only a file named litezap-app (LiteZap.exe on Windows) is run as the app and, on macOS
# and Linux, only when neither it nor its folder can be changed by other users.
set -u

say() { printf 'litezap-mcp: %s\n' "$*" >&2; }

os=$(uname -s 2>/dev/null || echo unknown)
case $os in
MINGW* | MSYS* | CYGWIN*) windows=1 ;;
*) windows=0 ;;
esac

# winpath turns C:\x into /c/x under Git Bash/MSYS (no-op elsewhere).
winpath() {
	if [ "$windows" = 1 ] && command -v cygpath >/dev/null 2>&1; then
		cygpath -u "$1" 2>/dev/null || printf '%s\n' "$1"
	else
		printf '%s\n' "$1"
	fi
}

# usable PATH: an executable regular file with the app's name that only its owner can
# change (Unix: no group/other write bit on it or on its folder).
usable() {
	p=$1
	[ -n "$p" ] && [ -f "$p" ] && [ -x "$p" ] || return 1
	case ${p##*/} in
	litezap-app) ;;
	LiteZap.exe | litezap.exe) [ "$windows" = 1 ] || return 1 ;;
	*) return 1 ;;
	esac
	[ "$windows" = 1 ] && return 0
	d=$(dirname -- "$p")
	[ -z "$(find "$p" "$d" -prune \( -perm -020 -o -perm -002 \) -print 2>/dev/null)" ]
}

run_app() {
	p=$(winpath "$1")
	if usable "$p"; then
		exec "$p" mcp
	fi
}

if [ -n "${LITEZAP_TOKEN:-}" ] && command -v litezap >/dev/null 2>&1; then
	exec litezap mcp
fi

# 1. Explicit path.
[ -n "${LITEZAP_APP_EXE:-}" ] && run_app "$LITEZAP_APP_EXE"

# 2. The path the app recorded in its folder (see internal/mcpb.AppDirFor).
appdir=${LITEZAP_APP_DIR:-}
if [ -z "$appdir" ]; then
	case $os in
	Darwin) appdir="${HOME:-}/Library/Application Support/LiteZap" ;;
	MINGW* | MSYS* | CYGWIN*) [ -n "${APPDATA:-}" ] && appdir="$(winpath "$APPDATA")/LiteZap" ;;
	*)
		case ${XDG_DATA_HOME:-} in
		/*) appdir="$XDG_DATA_HOME/litezap" ;;
		*) appdir="${HOME:-}/.local/share/litezap" ;;
		esac
		;;
	esac
fi
if [ -n "$appdir" ] && [ -f "$appdir/app-exe" ]; then
	IFS= read -r recorded <"$appdir/app-exe" || true
	recorded=$(printf '%s' "${recorded:-}" | tr -d '\r' | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
	[ -n "$recorded" ] && run_app "$recorded"
fi

# 3. Usual install places.
case $os in
Darwin)
	run_app "/Applications/LiteZap.app/Contents/MacOS/litezap-app"
	[ -n "${HOME:-}" ] && run_app "$HOME/Applications/LiteZap.app/Contents/MacOS/litezap-app"
	;;
MINGW* | MSYS* | CYGWIN*)
	[ -n "${LOCALAPPDATA:-}" ] && run_app "$LOCALAPPDATA\\Programs\\LiteZap\\LiteZap.exe"
	[ -n "${LOCALAPPDATA:-}" ] && run_app "$LOCALAPPDATA\\LiteZap\\LiteZap.exe"
	[ -n "${PROGRAMFILES:-}" ] && run_app "$PROGRAMFILES\\LiteZap\\LiteZap.exe"
	;;
*)
	if [ -n "${HOME:-}" ]; then
		run_app "$HOME/LiteZap/litezap-app"
		run_app "$HOME/Applications/LiteZap/litezap-app"
		run_app "$HOME/.local/bin/litezap-app"
	fi
	run_app "/opt/LiteZap/litezap-app"
	;;
esac

if command -v litezap >/dev/null 2>&1; then
	exec litezap mcp
fi

say "o app LiteZap não foi encontrado neste computador: instale-o (https://litezap.com/docs/baixar/), abra-o uma vez e tente de novo (no Claude Code, /mcp reconecta)."
exit 1
