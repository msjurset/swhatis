# swhatis

A zero-dependency Go CLI that identifies commands, compiled binaries, scripts, and installed or uninstalled packages in single-digit milliseconds—combining concurrent local binary/filesystem introspection with registry and search-grounded AI fallbacks.

## Install

### Homebrew (macOS & Linux)

```sh
brew install msjurset/tap/swhatis
```

This installs the `swhatis` binary, man page (`man swhatis`), and Zsh/Bash completions.

### Go Install

```sh
go install github.com/msjurset/swhatis@latest
```

### Build from Source

```sh
git clone https://github.com/msjurset/swhatis.git
cd swhatis
make install
```

---

## Features

- **Stage 1 — Concurrent Local Fan-Out (`< 5ms`)**:
  - **`[path]` Symlink Resolution**: Resolves `$PATH` binaries through symlinks (`exec.LookPath` → `filepath.EvalSymlinks`).
  - **`[go]` Native Go Binary Introspection (`debug/buildinfo`)**: Extracts embedded Go module paths (`github.com/owner/repo@version`) and Go compiler versions directly from compiled Go binaries in microseconds.
  - **`[script]` Shebang & Virtualenv Inspection**: Reads `#!` headers and symlink paths to detect `uv` tools (`~/.local/share/uv/tools/<pkg>`), `pipx` venvs, `node_modules` packages, and Python/Node/Ruby/Perl/Shell scripts.
  - **`[brew]` Zero-Subprocess Homebrew Lookups (`0.1ms` installed / `0.9ms` uninstalled)**:
    1. Reads `.brew/<formula>.rb` directly from `Cellar/{formula}/{version}/` if installed.
    2. Seeks directly into Homebrew's local JWS payload cache (`packages.*.jws.json.payload` via `.payload.index`, `formula_aliases.txt`, and `executables.txt`) to resolve **uninstalled** formulae, casks, aliases, and sub-binaries without spawning `brew`'s Ruby runtime.
  - **`[man]` Zero-Subprocess Man Page Parser (`< 0.5ms`)**: Parses `.SH NAME` (GNU roff) and `.Nd` (BSD mdoc) directly from `<cmd>.{1,8,6}[.gz]` across `$MANPATH`, Homebrew, and system man directories, falling back to `/usr/bin/whatis` only if needed.
  - **`[help]` Fast `--help` First-Line Scraper**: If an installed binary has no man page or Homebrew entry, runs `<bin> --help` (350ms timeout) and extracts the first descriptive summary line.

- **Stage 2 — Cached Registry & Optional Search-Grounded AI Fallback**:
  - **30-Day Local Disk Cache**: Stores remote lookups in `os.UserCacheDir()/swhatis/<command>.json` (`~/Library/Caches/swhatis/` on macOS, `~/.cache/swhatis/` on Linux).
  - **Ecosystem Registries**: Queries GitHub REST API (for Go binaries with `github.com/owner/repo` module paths), PyPI JSON API (for `uv`/`pipx` tools), and npm registry (for Node CLIs).
  - **Community Summaries**: Queries `cheat.sh` summary headers with zero API keys required.
  - **Optional Search-Grounded Gemini (`-ai` or fallback)**: Uses Google Gemini with Google Search Grounding (`google_search` tool) for obscure or brand-new tools. Supports standard `GEMINI_API_KEY` environment variables, system Keychain caching, or 1Password CLI (`op://`) biometric unlock.

---

## Usage

```sh
# Identify an installed command (concurrent local probes in ~1-5ms)
swhatis rg

# Query multiple commands at once
swhatis rg jq git

# Look up an uninstalled Homebrew formula or cask in <1ms via local byte-offset index
swhatis a2ps 1password-cli

# Force web/registry lookup even when local man/brew summaries exist
swhatis -w ripgrep

# Force a search-grounded Gemini summary
swhatis -ai some-new-cli

# Structured JSON output (includes elapsed_ms and all probe fields)
swhatis -json rg
```

### Example Output

```text
$ swhatis rg
[path]   /opt/homebrew/bin/rg -> /opt/homebrew/Cellar/ripgrep/15.2.0/bin/rg
[brew]   ripgrep: Search tool like grep and The Silver Searcher (https://github.com/BurntSushi/ripgrep)
[man]    rg(1) - recursively search the current directory for lines matching a pattern
```

### Flags

| Flag | Description |
|------|-------------|
| `-w` | Force web/registry/search lookup even if local man or Homebrew info exists |
| `-ai` | Force a search-grounded Gemini summary |
| `-no-cache` | Bypass the 30-day local disk cache |
| `-no-help` | Skip running `<command> --help` when man and Homebrew descriptions are absent |
| `-json` | Output structured JSON including `elapsed_ms` and all probe fields |
| `-auth [ref\|key]` | Store a Gemini API key (or resolve a 1Password `op://` reference) in the system keychain |
| `-clear-auth` | Remove the cached Gemini API key from the system keychain |
| `-op-ref <ref>` | Custom 1Password `op://` reference for Gemini API key resolution |
| `-completion <shell>` | Print shell completion script (`zsh` or `bash`) |
| `-version` | Print version and exit |

---

## Optional: Configuring Gemini Search-Grounded AI Fallback

`swhatis` works out of the box with zero configuration or API keys (using local probes, GitHub, PyPI, npm, and `cheat.sh`). To enable the optional Google Search-grounded Gemini fallback (or `-ai` flag), choose any of these three methods:

### Option A: Environment Variable

```sh
export GEMINI_API_KEY="your-gemini-api-key"
```

### Option B: Store in System Keychain (macOS Keychain / Linux `secret-tool`)

```sh
swhatis -auth "your-gemini-api-key"
```

### Option C: 1Password CLI (`op://`) with Biometric Unlock

If you store your key in 1Password and have the [1Password CLI](https://developer.1password.com/docs/cli) (`op`) installed:

```sh
# Pass your 1Password secret reference once (triggers Touch ID and caches in Keychain)
swhatis -auth "op://Personal/Gemini API Key/credential"

# Or set GEMINI_OP_REF in your shell profile
export GEMINI_OP_REF="op://Personal/Gemini API Key/credential"
```

To evict the cached key from your keychain at any time:

```sh
swhatis -clear-auth
```

---

## Zsh Shell Alias & Function Wrapper (`~/.zshrc`)

Because interactive shell aliases, shell functions, and Zsh builtins exist only inside your parent shell's memory, add this optional wrapper to `~/.zshrc` so `what <cmd>` prints any Zsh alias/function definition before running `swhatis`:

```zsh
what() {
  local shell_def
  shell_def=$(whence -v "$1" 2>/dev/null)
  [[ -n "$shell_def" && "$shell_def" != *"/$1" ]] && print -P "%F{cyan}[zsh]%f    $shell_def"
  swhatis "$@"
}
```

---

## Shell Completions

If installed via Homebrew (`brew install msjurset/tap/swhatis`), completions for Zsh and Bash are installed automatically. If built from source:

```sh
# Zsh
swhatis -completion zsh > ~/.zsh/completions/_swhatis

# Bash
source <(swhatis -completion bash)
```

## License

[MIT](LICENSE)
