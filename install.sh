#!/bin/sh
set -eu

repository="${RAKAZO_MANAGER_REPOSITORY:-nicolaeser/RakazoManager}"
install_dir="${RAKAZO_MANAGER_INSTALL_DIR:-/usr/local/bin}"
version="${RAKAZO_MANAGER_VERSION:-latest}"

usage() {
  cat <<'EOF'
Install Rakazo Manager from a verified GitHub release.

Usage:
  install.sh [--system | --user | --install-dir DIR] [--version TAG]

Options:
  --system          Install to /usr/local/bin (default; may use sudo)
  --user            Install to $HOME/.local/bin without sudo
  --install-dir DIR Install to a custom directory
  --version TAG     Install a specific release tag instead of latest
  -h, --help        Show this help

Environment:
  RAKAZO_MANAGER_REPOSITORY   GitHub owner/repository
  RAKAZO_MANAGER_INSTALL_DIR  Default installation directory
  RAKAZO_MANAGER_VERSION      Default release tag
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --system)
      install_dir="/usr/local/bin"
      shift
      ;;
    --user)
      [ -n "${HOME:-}" ] || {
        printf 'HOME must be set for --user\n' >&2
        exit 1
      }
      install_dir="${HOME}/.local/bin"
      shift
      ;;
    --install-dir)
      [ "$#" -ge 2 ] || {
        printf '%s requires a directory\n' "$1" >&2
        exit 1
      }
      install_dir="$2"
      shift 2
      ;;
    --version)
      [ "$#" -ge 2 ] || {
        printf '%s requires a release tag\n' "$1" >&2
        exit 1
      }
      version="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'Unknown option: %s\n' "$1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

case "${install_dir}" in
  /*) ;;
  *)
    printf 'Installation directory must be an absolute path: %s\n' "${install_dir}" >&2
    exit 1
    ;;
esac

for dependency in curl tar awk install; do
  command -v "${dependency}" >/dev/null 2>&1 || {
    printf 'Required command is not installed: %s\n' "${dependency}" >&2
    exit 1
  }
done

case "$(uname -s)" in
  Darwin) operating_system="darwin" ;;
  Linux) operating_system="linux" ;;
  *) printf 'Unsupported operating system: %s\n' "$(uname -s)" >&2; exit 1 ;;
esac

case "$(uname -m)" in
  x86_64|amd64) architecture="amd64" ;;
  arm64|aarch64) architecture="arm64" ;;
  *) printf 'Unsupported architecture: %s\n' "$(uname -m)" >&2; exit 1 ;;
esac

asset="rakazo-manager_${operating_system}_${architecture}.tar.gz"
if [ "${version}" = "latest" ]; then
  base_url="https://github.com/${repository}/releases/latest/download"
else
  base_url="https://github.com/${repository}/releases/download/${version}"
fi

temporary_directory="$(mktemp -d "${TMPDIR:-/tmp}/rakazo-manager-install.XXXXXX")"
trap 'rm -rf "${temporary_directory}"' EXIT HUP INT TERM
umask 077

download() {
  curl --fail --location --silent --show-error \
    --proto '=https' \
    --proto-redir '=https' \
    --connect-timeout 15 \
    --max-time 300 \
    --retry 3 \
    "$1" \
    --output "$2"
}

printf 'Downloading Rakazo Manager for %s/%s...\n' "${operating_system}" "${architecture}"
download \
  "${base_url}/${asset}" \
  "${temporary_directory}/${asset}"
download \
  "${base_url}/checksums.txt" \
  "${temporary_directory}/checksums.txt"

expected_checksum="$(
  awk -v filename="${asset}" '$2 == filename { print $1; exit }' \
    "${temporary_directory}/checksums.txt"
)"
[ -n "${expected_checksum}" ] || {
  printf 'No checksum found for %s\n' "${asset}" >&2
  exit 1
}

if command -v sha256sum >/dev/null 2>&1; then
  actual_checksum="$(sha256sum "${temporary_directory}/${asset}" | awk '{print $1}')"
else
  actual_checksum="$(shasum -a 256 "${temporary_directory}/${asset}" | awk '{print $1}')"
fi
[ "${actual_checksum}" = "${expected_checksum}" ] || {
  printf 'Checksum verification failed for %s\n' "${asset}" >&2
  exit 1
}

tar -xzf "${temporary_directory}/${asset}" -C "${temporary_directory}" rakazo-manager
[ -f "${temporary_directory}/rakazo-manager" ] &&
  [ ! -L "${temporary_directory}/rakazo-manager" ] || {
  printf 'Release archive does not contain a regular rakazo-manager binary\n' >&2
  exit 1
}

install_target="${install_dir}/rakazo-manager"
if mkdir -p "${install_dir}" 2>/dev/null &&
  { [ -w "${install_dir}" ] || [ "$(id -u)" -eq 0 ]; }; then
  install -m 0755 "${temporary_directory}/rakazo-manager" "${install_target}"
else
  command -v sudo >/dev/null 2>&1 || {
    printf 'Cannot write to %s and sudo is unavailable.\n' "${install_dir}" >&2
    printf 'Use --user or choose a writable --install-dir.\n' >&2
    exit 1
  }
  printf 'Installing the system command in %s (sudo may prompt)...\n' "${install_dir}"
  sudo mkdir -p "${install_dir}"
  sudo install -m 0755 "${temporary_directory}/rakazo-manager" "${install_target}"
fi

printf 'Installed Rakazo Manager command: %s\n' "${install_target}"
case ":${PATH}:" in
  *":${install_dir}:"*) printf 'Run: rakazo-manager version\n' ;;
  *)
    printf '%s is not currently in PATH.\n' "${install_dir}"
    printf 'Add it to PATH, then run: rakazo-manager version\n'
    ;;
esac
