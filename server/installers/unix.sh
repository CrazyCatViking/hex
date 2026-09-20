#!/usr/bin/env bash
set -euo pipefail
umask 077

fail() {
    printf 'Hex installation failed: %s\n' "$*" >&2
    exit 1
}

os=$(uname -s)
architecture=$(uname -m)
[[ "$os" == '{{.ExpectedOS}}' ]] || fail 'Download the installer for this operating system from your Hex platform.'
case "$os/$architecture" in
    Darwin/arm64) artifact=hex-darwin-arm64 ;;
    Darwin/x86_64) artifact=hex-darwin-amd64 ;;
    Linux/x86_64) artifact=hex-linux-amd64 ;;
    *) fail "Unsupported operating system or architecture: $os/$architecture" ;;
esac

command -v curl >/dev/null || fail 'curl is required. Install it using your operating system package manager, then rerun this script.'
command -v base64 >/dev/null || fail 'base64 is required.'
if command -v sha256sum >/dev/null; then
    checksum_command=(sha256sum)
elif command -v shasum >/dev/null; then
    checksum_command=(shasum -a 256)
else
    fail 'sha256sum or shasum is required to verify the download.'
fi

decode() {
    if [[ "$os" == Darwin ]]; then
        base64 -D
    else
        base64 --decode
    fi
}

release_url=$(printf '%s' '{{.ReleaseURL}}' | decode)
temporary=$(mktemp -d)
pending_binary=''
cleanup() {
    rm -rf "$temporary"
    if [[ -n "$pending_binary" ]]; then
        rm -f "$pending_binary"
    fi
}
trap cleanup EXIT

download() {
    curl --fail --silent --show-error --location --retry 3 \
        --proto '{{.Protocols}}' --proto-redir '{{.Protocols}}' \
        "$release_url/$1" --output "$2"
}

printf 'Downloading the latest Hex CLI for %s/%s...\n' "$os" "$architecture"
download SHA256SUMS "$temporary/SHA256SUMS"
download "$artifact" "$temporary/hex"
expected=$(awk -v name="$artifact" '$2 == name { print $1 }' "$temporary/SHA256SUMS")
[[ "$expected" =~ ^[[:xdigit:]]{64}$ ]] || fail 'The release has no unique SHA-256 checksum for this binary.'
actual=$("${checksum_command[@]}" "$temporary/hex")
actual=${actual%% *}
[[ "$actual" == "$expected" ]] || fail 'Checksum mismatch. No binary was installed. Download and run the installer again.'

printf '%s' '{{.Connection}}' | decode > "$temporary/platform.json"
bin_directory="$HOME/.local/bin"
mkdir -p "$bin_directory"
pending_binary=$(mktemp "$bin_directory/.hex-install.XXXXXX")
cp "$temporary/hex" "$pending_binary"
chmod 755 "$pending_binary"
mv -f "$pending_binary" "$bin_directory/hex"
pending_binary=''
"$bin_directory/hex" setup --file "$temporary/platform.json"

path_line='export PATH="$HOME/.local/bin:$PATH" # Hex CLI'
add_path() {
    local profile=$1
    local line=$2
    if [[ ! -f "$profile" ]] || ! grep -Fqx "$line" "$profile"; then
        printf '\n%s\n' "$line" >> "$profile"
    fi
}
add_path "$HOME/.profile" "$path_line"
add_path "$HOME/.bashrc" "$path_line"
add_path "${ZDOTDIR:-$HOME}/.zshrc" "$path_line"
for profile in "$HOME/.bash_profile" "$HOME/.bash_login"; do
    if [[ -f "$profile" ]]; then
        add_path "$profile" "$path_line"
    fi
done
if [[ "${SHELL:-}" == */fish ]]; then
    mkdir -p "${XDG_CONFIG_HOME:-$HOME/.config}/fish/conf.d"
    add_path "${XDG_CONFIG_HOME:-$HOME/.config}/fish/conf.d/hex.fish" 'fish_add_path --path "$HOME/.local/bin"'
fi

printf '\nHex is installed at %s/hex and connected to your platform.\n' "$bin_directory"
printf 'Open a new terminal, then run: hex init my-app\n'
