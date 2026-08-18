#!/usr/bin/env bash
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
GUARD_SCRIPT="$SCRIPT_DIR/go-test-with-agent-cli-guard.sh"

usage() {
  echo "usage: $0 [--race]" >&2
}

go_test_args=(test)
case "$#" in
  0) ;;
  1)
    if [ "$1" != "--race" ]; then
      usage
      exit 2
    fi
    go_test_args+=(-race)
    ;;
  *)
    usage
    exit 2
    ;;
esac

cd "$REPO_ROOT/server"

go_command=$(command -v go)
if [ -z "$go_command" ]; then
  echo "go executable not found" >&2
  exit 1
fi

# A bare `export` in the Makefile can omit inherited Windows variables while
# leaving HOME in MSYS form. Go then cannot derive its default module cache.
# Seed native cache paths before asking the Go command for any environment.
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*)
    windows_home=${USERPROFILE:-}
    if [ -z "$windows_home" ] && command -v cygpath >/dev/null 2>&1; then
      windows_home=$(cygpath -w "$HOME")
    fi
    if [ -n "$windows_home" ]; then
      if [ -z "${USERPROFILE:-}" ]; then
        USERPROFILE=$windows_home
        export USERPROFILE
      fi
      windows_temp=${LOCALAPPDATA:-${windows_home%[\\/]}\\AppData\\Local}\\Temp
      if [ -z "${TEMP:-}" ]; then
        TEMP=$windows_temp
        export TEMP
      fi
      if [ -z "${TMP:-}" ]; then
        TMP=$windows_temp
        export TMP
      fi
      if [ -z "${GOPATH:-}" ]; then
        GOPATH=${windows_home%[\\/]}\\go
        export GOPATH
        if [ -z "${GOCACHE:-}" ]; then
          GOCACHE=${LOCALAPPDATA:-${windows_home%[\\/]}\\AppData\\Local}\\go-build
          export GOCACHE
        fi
      fi
    fi
    if [ -z "${GOMODCACHE:-}" ] && [ -n "${GOPATH:-}" ]; then
      GOMODCACHE=${GOPATH%[\\/]}\\pkg\\mod
      export GOMODCACHE
    fi
    ;;
esac

if [ -z "${GOPATH:-}" ]; then
  GOPATH=$("$go_command" env GOPATH)
  export GOPATH
fi
if [ -z "${GOMODCACHE:-}" ]; then
  GOMODCACHE=$("$go_command" env GOMODCACHE)
  export GOMODCACHE
fi
if [ -z "${GOCACHE:-}" ]; then
  GOCACHE=$("$go_command" env GOCACHE)
  export GOCACHE
fi

# On Windows, cgo may find GCC through a later PATH entry while loading
# incompatible MSYS DLLs from an earlier Git installation. Put the selected
# compiler's own directory first so race builds can start cc1 reliably.
if [ "$("$go_command" env GOOS)" = "windows" ]; then
  cc_command=${CC:-gcc}
  cc_path=$(command -v "$cc_command" 2>/dev/null || true)
  if [ -n "$cc_path" ]; then
    PATH=$(dirname "$cc_path"):$PATH
    export PATH
  fi
fi

packages=$("$go_command" list ./...)
regular_packages=()
for package in $packages; do
  case "$package" in
    */pkg/agent|*/pkg/agent/*) ;;
    *) regular_packages+=("$package") ;;
  esac
done

"$GUARD_SCRIPT" -- "$go_command" "${go_test_args[@]}" "${regular_packages[@]}"
# Subprocess-backed agent tests have hard deadlines. Limit both package and
# within-package parallelism so race builds do not starve their parent loops.
"$GUARD_SCRIPT" -- "$go_command" "${go_test_args[@]}" -p 2 -parallel 2 ./pkg/agent/...
