package probe

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestParseBrewReceipt(t *testing.T) {
	tmp := t.TempDir()
	rbPath := filepath.Join(tmp, "ripgrep.rb")
	content := `class Ripgrep < Formula
  desc "Search tool like grep and The Silver Searcher"
  homepage "https://github.com/BurntSushi/ripgrep"
  url "https://github.com/BurntSushi/ripgrep/archive/refs/tags/14.1.1.tar.gz"
end
`
	if err := os.WriteFile(rbPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp rb: %v", err)
	}

	pkg, desc, home := ParseBrewReceipt(rbPath, "ripgrep")
	if pkg != "ripgrep" {
		t.Errorf("pkg = %q, want %q", pkg, "ripgrep")
	}
	if desc != "Search tool like grep and The Silver Searcher" {
		t.Errorf("desc = %q, want %q", desc, "Search tool like grep and The Silver Searcher")
	}
	if home != "https://github.com/BurntSushi/ripgrep" {
		t.Errorf("home = %q, want %q", home, "https://github.com/BurntSushi/ripgrep")
	}
}

func TestProbeBrewLocalCache(t *testing.T) {
	tmp := t.TempDir()
	internalDir := filepath.Join(tmp, "internal")
	if err := os.MkdirAll(internalDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	line1 := `{"protected":"abc","signature":"def"}` + "\n"
	f1JSON := `{"desc":"Clone of cat(1) with syntax highlighting and Git integration","homepage":"https://github.com/sharkdp/bat"}`
	c1JSON := `{"desc":"Command-line interface for 1Password","homepage":"https://developer.1password.com/docs/cli"}`
	payload := line1 + f1JSON + c1JSON

	payloadPath := filepath.Join(internalDir, "packages.arm64_tahoe.jws.json.payload")
	if err := os.WriteFile(payloadPath, []byte(payload), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	idxContent := fmt.Sprintf(`{
  "formulae": {
    "bat": [0, %d]
  },
  "casks": {
    "1password-cli": [%d, %d]
  }
}`, len(f1JSON), len(f1JSON), len(c1JSON))
	if err := os.WriteFile(payloadPath+".index", []byte(idxContent), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}

	if err := os.WriteFile(filepath.Join(tmp, "formula_aliases.txt"), []byte("batcat|bat\n"), 0o644); err != nil {
		t.Fatalf("write aliases: %v", err)
	}
	if err := os.WriteFile(filepath.Join(internalDir, "executables.txt"), []byte("bat:bat bat-extra\n"), 0o644); err != nil {
		t.Fatalf("write executables: %v", err)
	}

	tests := []struct {
		name     string
		query    string
		wantPkg  string
		wantDesc string
	}{
		{
			name:     "direct formula lookup",
			query:    "bat",
			wantPkg:  "bat",
			wantDesc: "Clone of cat(1) with syntax highlighting and Git integration",
		},
		{
			name:     "formula alias lookup",
			query:    "batcat",
			wantPkg:  "bat",
			wantDesc: "Clone of cat(1) with syntax highlighting and Git integration",
		},
		{
			name:     "reverse executable lookup",
			query:    "bat-extra",
			wantPkg:  "bat",
			wantDesc: "Clone of cat(1) with syntax highlighting and Git integration",
		},
		{
			name:     "direct cask lookup",
			query:    "1password-cli",
			wantPkg:  "1password-cli (cask)",
			wantDesc: "Command-line interface for 1Password",
		},
		{
			name:     "nonexistent package",
			query:    "does-not-exist-xyz",
			wantPkg:  "",
			wantDesc: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pkg, desc, _ := ProbeBrewLocalCache(tt.query, tmp)
			if pkg != tt.wantPkg {
				t.Errorf("pkg = %q, want %q", pkg, tt.wantPkg)
			}
			if desc != tt.wantDesc {
				t.Errorf("desc = %q, want %q", desc, tt.wantDesc)
			}
		})
	}
}

func TestProbeScriptHeader(t *testing.T) {
	tmp := t.TempDir()

	tests := []struct {
		name     string
		relPath  string
		content  string
		wantEco  string
		wantPkg  string
	}{
		{
			name:    "uv tool script",
			relPath: "uv/tools/ruff/bin/ruff-script",
			content: "#!/Users/test/.local/share/uv/tools/ruff/bin/python\nimport sys\n",
			wantEco: "pypi",
			wantPkg: "ruff",
		},
		{
			name:    "pipx tool script",
			relPath: "pipx/venvs/black/bin/black",
			content: "#!/Users/test/.local/pipx/venvs/black/bin/python\nimport sys\n",
			wantEco: "pypi",
			wantPkg: "black",
		},
		{
			name:    "npm module script",
			relPath: "lib/node_modules/typescript/bin/tsc",
			content: "#!/usr/bin/env node\nconsole.log('hi');\n",
			wantEco: "npm",
			wantPkg: "typescript",
		},
		{
			name:    "zsh script",
			relPath: "bin/my-helper",
			content: "#!/bin/zsh\necho hello\n",
			wantEco: "shell",
			wantPkg: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			full := filepath.Join(tmp, tt.relPath)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(full, []byte(tt.content), 0o755); err != nil {
				t.Fatalf("write: %v", err)
			}
			info, eco, pkg := ProbeScriptHeader(full, full)
			if info == "" {
				t.Errorf("expected non-empty script info")
			}
			if eco != tt.wantEco {
				t.Errorf("eco = %q, want %q", eco, tt.wantEco)
			}
			if pkg != tt.wantPkg {
				t.Errorf("pkg = %q, want %q", pkg, tt.wantPkg)
			}
		})
	}
}

func TestExtractHelpDescription(t *testing.T) {
	tests := []struct {
		name    string
		cmdName string
		raw     string
		want    string
	}{
		{
			name:    "skips usage and version line",
			cmdName: "mytool",
			raw: `mytool v1.4.2
Usage: mytool [flags] <target>

Fast local static analysis and dependency linter for Go workspaces.

Flags:
  -h, --help   Show help
`,
			want: "Fast local static analysis and dependency linter for Go workspaces.",
		},
		{
			name:    "strips ansi escape codes",
			cmdName: "colortool",
			raw:     "\x1b[36mA high-speed terminal multiplexer and session manager.\x1b[0m\nUsage: colortool\n",
			want:    "A high-speed terminal multiplexer and session manager.",
		},
		{
			name:    "ignores flags only",
			cmdName: "flagsonly",
			raw: `Usage: flagsonly [-v]
  -v  verbose output
  -h  show help
`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractHelpDescription(tt.raw, tt.cmdName); got != tt.want {
				t.Errorf("ExtractHelpDescription() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseManFileSummary(t *testing.T) {
	tmp := t.TempDir()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "roff SH NAME format",
			content: `.TH RG 1 "2026-07-15" "15.2.0" "User Commands"
.SH NAME
rg \- recursively search the current directory for lines matching a pattern
.SH SYNOPSIS
`,
			want: "recursively search the current directory for lines matching a pattern",
		},
		{
			name: "bsd mdoc Nd format",
			content: `.Dd August 31, 2020
.Dt LS 1
.Os
.Sh NAME
.Nm ls
.Nd list directory contents
.Sh SYNOPSIS
`,
			want: "list directory contents",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(tmp, tt.name+".1")
			if err := os.WriteFile(p, []byte(tt.content), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got := ParseManFileSummary(p); got != tt.want {
				t.Errorf("ParseManFileSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

