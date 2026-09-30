#!/usr/bin/env bash
# Test the native router/bridge executable against Ergo and a OneBot fixture.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
project_dir="$PWD"
cache_dir="${E2E_CACHE_DIR:-$project_dir/bridge/onebot/.cache/e2e}"
mkdir -p "$cache_dir"
cache_dir="$(cd "$cache_dir" && pwd)"

ergo_version=2.19.1
if [[ -z "${E2E_ERGO:-}" ]]; then
  case "$(uname -s)-$(uname -m)" in
    Linux-x86_64) platform=linux-x86_64; checksum=c275f2de8e8eb074c38707136392290172400b5b78616cb9f660351527924b47 ;;
    Linux-aarch64) platform=linux-arm64; checksum=3f68fee0ede09eeeb205a553fe9727b409c686752e1cc2fea34ae2f9d64517fb ;;
    Darwin-arm64) platform=macos-arm64; checksum=1bd97a0917036061e2dcdfc29149c2e252e3bc856282e0956afa2aaaa54dd787 ;;
    Darwin-x86_64) platform=macos-x86_64; checksum=68b388a49034257082ee0292703874630a2c10cfba86c73bb2adc37b515c57a3 ;;
    *) echo "Set E2E_ERGO to an Ergo binary on this platform" >&2; exit 1 ;;
  esac
  archive="ergo-$ergo_version-$platform"
  export E2E_ERGO="$cache_dir/$archive/ergo"
  if [[ ! -x "$E2E_ERGO" ]]; then
    curl --fail --location --retry 3 --max-time 120 \
      "https://github.com/ergochat/ergo/releases/download/v$ergo_version/$archive.tar.gz" \
      --output "$cache_dir/$archive.tar.gz.download"
    if command -v sha256sum >/dev/null; then
      actual=$(sha256sum "$cache_dir/$archive.tar.gz.download")
    else
      actual=$(shasum -a 256 "$cache_dir/$archive.tar.gz.download")
    fi
    [[ "${actual%% *}" == "$checksum" ]] || { echo "Ergo checksum mismatch" >&2; exit 1; }
    tar -xzf "$cache_dir/$archive.tar.gz.download" -C "$cache_dir"
  fi
fi
export E2E_ERGO
"$E2E_ERGO" --version

# Set E2E_RACE=1 to also check the full process, including other protocols.
# The harness and OneBot package tests always retain -race.
race_flags=()
if [[ "${E2E_RACE:-0}" == 1 ]]; then race_flags=(-race); fi
go build -tags "${BUILD_TAGS:-goolm}" "${race_flags[@]}" -o "$cache_dir/matterbridge" .
export E2E_RELAY="$cache_dir/matterbridge"
go test -race -tags "integration,${BUILD_TAGS:-goolm}" -count=1 -v -timeout 180s ./bridge/onebot/test/e2e
