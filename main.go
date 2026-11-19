package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/msjurset/swhatis/internal/credentials"
	"github.com/msjurset/swhatis/internal/probe"
)

var version = "0.1.0"

const (
	colReset   = "\033[0m"
	colCyan    = "\033[36m"
	colGreen   = "\033[32m"
	colYellow  = "\033[33m"
	colBlue    = "\033[34m"
	colMagenta = "\033[35m"
	colDim     = "\033[2m"
)

func main() {
	forceWeb := flag.Bool("w", false, "force web/search lookup even if local info exists")
	forceAI := flag.Bool("ai", false, "force search-grounded Gemini summary (paid tier via 1Password/Keychain)")
	noCache := flag.Bool("no-cache", false, "bypass local web summary cache")
	noHelp := flag.Bool("no-help", false, "skip --help first-line fallback probe")
	jsonOut := flag.Bool("json", false, "output structured JSON")
	authCmd := flag.Bool("auth", false, "authenticate with 1Password (biometric check) and cache Gemini API key in macOS Keychain")
	clearAuth := flag.Bool("clear-auth", false, "remove cached Gemini API key from macOS Keychain")
	opRef := flag.String("op-ref", "", "1Password op:// reference or API key for Gemini authentication")
	compShell := flag.String("completion", "", "print shell completion script (zsh or bash)")
	showVer := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVer {
		fmt.Printf("swhatis %s\n", version)
		return
	}

	if *compShell != "" {
		if err := printCompletion(*compShell); err != nil {
			fmt.Fprintf(os.Stderr, "swhatis: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *clearAuth {
		_ = credentials.Delete(credentials.DefaultKeyName)
		_ = credentials.Delete(credentials.OpRefKeyName)
		fmt.Println("Cleared cached Gemini API key from system keychain.")
		return
	}

	if *authCmd {
		targetRef := *opRef
		if targetRef == "" && flag.NArg() > 0 {
			targetRef = flag.Arg(0)
		}
		fmt.Fprintf(os.Stderr, "Authenticating and caching Gemini API key in system keychain...\n")
		key, err := credentials.ResolveGeminiKey(targetRef, true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "swhatis: authentication failed: %v\n", err)
			os.Exit(1)
		}
		masked := "***"
		if len(key) > 8 {
			masked = key[:4] + "..." + key[len(key)-4:]
		}
		fmt.Printf("Authenticated and cached Gemini API key (%s) in system keychain (service: swhatis).\n", masked)
		return
	}

	if flag.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s [-w] [-ai] [-no-cache] [-no-help] [-json] [-auth] <command> [command...]\n", filepath.Base(os.Args[0]))
		os.Exit(1)
	}

	opts := probe.Options{
		ForceWeb: *forceWeb,
		ForceAI:  *forceAI,
		NoCache:  *noCache,
		NoHelp:   *noHelp,
		OpRef:    *opRef,
	}

	args := flag.Args()
	var results []probe.Result
	exitCode := 0

	for i, cmd := range args {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		res := probe.Run(ctx, cmd, opts)
		cancel()

		if *jsonOut {
			results = append(results, res)
			continue
		}

		if i > 0 {
			fmt.Println()
		}
		if len(args) > 1 {
			fmt.Printf("%s=== %s ===%s\n", colDim, cmd, colReset)
		}
		if !render(cmd, res) {
			exitCode = 1
		}
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if len(results) == 1 {
			_ = enc.Encode(results[0])
		} else {
			_ = enc.Encode(results)
		}
	}

	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

func render(cmd string, r probe.Result) bool {
	found := false

	if r.BinPath != "" {
		found = true
		loc := r.BinPath
		if r.RealPath != "" && r.RealPath != r.BinPath {
			loc = fmt.Sprintf("%s %s-> %s%s", r.BinPath, colDim, r.RealPath, colReset)
		}
		fmt.Printf("%s[path]%s   %s\n", colCyan, colReset, loc)
	}

	if r.GoMod != "" {
		found = true
		fmt.Printf("%s[go]%s     %s %s(%s)%s\n", colBlue, colReset, r.GoMod, colDim, r.GoVer, colReset)
	}

	if r.ScriptInfo != "" {
		found = true
		fmt.Printf("%s[script]%s %s\n", colBlue, colReset, r.ScriptInfo)
	}

	if r.BrewDesc != "" {
		found = true
		homeSuffix := ""
		if r.BrewHome != "" {
			homeSuffix = fmt.Sprintf(" %s(%s)%s", colDim, r.BrewHome, colReset)
		}
		fmt.Printf("%s[brew]%s   %s: %s%s\n", colYellow, colReset, r.BrewPkg, r.BrewDesc, homeSuffix)
	}

	if len(r.ManLines) > 0 {
		found = true
		for i, line := range r.ManLines {
			prefix := fmt.Sprintf("%s[man]%s    ", colGreen, colReset)
			if i > 0 {
				prefix = "         "
			}
			fmt.Printf("%s%s\n", prefix, line)
		}
	}

	if r.HelpLine != "" {
		found = true
		fmt.Printf("%s[help]%s   %s\n", colGreen, colReset, r.HelpLine)
	}

	if r.WebSummary != "" {
		found = true
		fmt.Printf("%s[%s]%s %s\n", colMagenta, r.WebSource, colReset, r.WebSummary)
	}

	if !found {
		fmt.Fprintf(os.Stderr, "%s: no local or web description found\n", cmd)
		return false
	}
	return true
}

func printCompletion(shell string) error {
	switch shell {
	case "zsh":
		fmt.Print(zshCompletion)
		return nil
	case "bash":
		fmt.Print(bashCompletion)
		return nil
	default:
		return fmt.Errorf("unsupported shell %q (supported: zsh, bash)", shell)
	}
}
