#!/bin/sh
set -eu

version="${1:-dev}"
repository_root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
distribution_directory="${repository_root}/dist"
temporary_directory="$(mktemp -d "${TMPDIR:-/tmp}/rakazo-manager-release.XXXXXX")"
trap 'rm -rf "${temporary_directory}"' EXIT HUP INT TERM
targets="${RAKAZO_MANAGER_TARGETS:-darwin/amd64 darwin/arm64 linux/amd64 linux/arm64}"

mkdir -p "${distribution_directory}"
rm -f \
  "${distribution_directory}"/rakazo-manager_*.tar.gz \
  "${distribution_directory}/checksums.txt"
commit="$(git -C "${repository_root}" rev-parse --short HEAD 2>/dev/null || printf none)"
build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
ldflags="-s -w -X main.version=${version} -X main.commit=${commit} -X main.date=${build_date}"

set -- ${targets}
[ "$#" -gt 0 ] || {
  printf 'RAKAZO_MANAGER_TARGETS must contain at least one target\n' >&2
  exit 1
}

for target do
  case "${target}" in
    darwin/amd64|darwin/arm64|linux/amd64|linux/arm64) ;;
    *)
      printf 'Unsupported release target: %s\n' "${target}" >&2
      exit 1
      ;;
  esac
  operating_system="${target%/*}"
  architecture="${target#*/}"
  staging="${temporary_directory}/${operating_system}-${architecture}"
  asset="rakazo-manager_${operating_system}_${architecture}.tar.gz"
  mkdir -p "${staging}"
  (
    cd "${repository_root}"
    CGO_ENABLED=0 GOOS="${operating_system}" GOARCH="${architecture}" \
      go build -trimpath -ldflags "${ldflags}" \
      -o "${staging}/rakazo-manager" ./cmd/rakazo-manager
  )
  cp "${repository_root}/README.md" "${staging}/README.md"
  tar -czf "${distribution_directory}/${asset}" -C "${staging}" \
    rakazo-manager README.md
done

(
  cd "${distribution_directory}"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum rakazo-manager_*.tar.gz > checksums.txt
  else
    shasum -a 256 rakazo-manager_*.tar.gz > checksums.txt
  fi
)

printf 'Release assets written to %s\n' "${distribution_directory}"
