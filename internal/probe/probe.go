package probe

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/msjurset/swhatis/internal/credentials"
)

var (
	reCellar       = regexp.MustCompile(`/Cellar/([^/]+)/([^/]+)/`)
	reBrewDesc     = regexp.MustCompile(`(?m)^\s*desc\s+"([^"]+)"`)
	reBrewHomepage = regexp.MustCompile(`(?m)^\s*homepage\s+"([^"]+)"`)
	reANSIEscape   = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	reValidBrewKey = regexp.MustCompile(`^[a-zA-Z0-9._+-]+(@[a-zA-Z0-9._+-]+)?$`)
)

// Options configures a swhatis lookup run.
type Options struct {
	ForceWeb  bool
	ForceAI   bool
	NoCache   bool
	NoHelp    bool
	OpRef     string
	CacheDir  string
	BrewCache string
}

// Result aggregates all discovered information for a command.
type Result struct {
	Command    string   `json:"command"`
	BinPath    string   `json:"bin_path,omitempty"`
	RealPath   string   `json:"real_path,omitempty"`
	ScriptInfo string   `json:"script_info,omitempty"`
	ScriptEco  string   `json:"script_ecosystem,omitempty"`
	ScriptPkg  string   `json:"script_package,omitempty"`
	GoMod      string   `json:"go_mod,omitempty"`
	GoVer      string   `json:"go_version,omitempty"`
	BrewPkg    string   `json:"brew_package,omitempty"`
	BrewDesc   string   `json:"brew_desc,omitempty"`
	BrewHome   string   `json:"brew_homepage,omitempty"`
	ManLines   []string `json:"man_lines,omitempty"`
	HelpLine   string   `json:"help_line,omitempty"`
	WebSummary string   `json:"web_summary,omitempty"`
	WebSource  string   `json:"web_source,omitempty"`
	ElapsedMs  float64  `json:"elapsed_ms"`
}

// CacheEntry stores a cached remote or AI summary on disk.
type CacheEntry struct {
	Summary   string    `json:"summary"`
	Source    string    `json:"source"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Run executes the concurrent Stage 1 local probes and Stage 2 web/AI fallbacks.
func Run(ctx context.Context, cmd string, opts Options) Result {
	start := time.Now()
	res := Result{Command: cmd}

	if p, err := exec.LookPath(cmd); err == nil {
		res.BinPath = p
		if rp, err := filepath.EvalSymlinks(p); err == nil {
			res.RealPath = rp
		} else {
			res.RealPath = p
		}
	}

	var wg sync.WaitGroup

	// 1a. Man DB (Direct troff/mdoc parser <0.2ms -> /usr/bin/whatis fallback)
	wg.Add(1)
	go func() {
		defer wg.Done()
		res.ManLines = ProbeWhatis(ctx, cmd, res.BinPath)
	}()

	// 1b. Homebrew (Cellar receipt <0.2ms -> local API cache ~10ms -> brew CLI fallback)
	wg.Add(1)
	go func() {
		defer wg.Done()
		res.BrewPkg, res.BrewDesc, res.BrewHome = ProbeBrew(ctx, cmd, res.RealPath, opts.BrewCache)
	}()

	// 1c. Go binary buildinfo & Script shebang inspection
	if res.RealPath != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res.GoMod, res.GoVer = ProbeGoBuildInfo(res.RealPath)
			if res.GoMod == "" {
				res.ScriptInfo, res.ScriptEco, res.ScriptPkg = ProbeScriptHeader(res.BinPath, res.RealPath)
			}
		}()
	}

	wg.Wait()

	hasLocalSummary := len(res.ManLines) > 0 || res.BrewDesc != ""

	// 1d. Fast --help first-line scraper if installed binary has no man or brew summary
	if !hasLocalSummary && !opts.NoHelp && res.BinPath != "" {
		res.HelpLine = ProbeHelpSummary(ctx, res.BinPath, cmd)
		if res.HelpLine != "" {
			hasLocalSummary = true
		}
	}

	// Stage 2: Cached Web / GitHub / PyPI / npm / Search-Grounded Gemini Fallback
	if opts.ForceWeb || opts.ForceAI || !hasLocalSummary {
		res.WebSummary, res.WebSource = ProbeWebWithCache(ctx, cmd, res, opts)
	}

	res.ElapsedMs = float64(time.Since(start).Microseconds()) / 1000.0
	return res
}

// ProbeWhatis resolves manual page summaries in <0.5ms by directly parsing the NAME section
// of man1/man8 files on disk (both roff .SH NAME and BSD mdoc .Nd formats), falling back to
// /usr/bin/whatis only if no direct man page file is found and the binary exists on $PATH.
func ProbeWhatis(parent context.Context, cmd, binPath string) []string {
	if lines := ProbeManFileDirect(cmd, binPath); len(lines) > 0 {
		return lines
	}
	// If the command is not installed on $PATH and has no direct man page file,
	// skip spawning /usr/bin/whatis (which scans every man directory for ~500ms on a miss).
	if binPath == "" {
		return nil
	}

	whatisBin := "/usr/bin/whatis"
	if _, err := os.Stat(whatisBin); err != nil {
		if p, err := exec.LookPath("whatis"); err == nil {
			whatisBin = p
		} else {
			return nil
		}
	}

	ctx, cancel := context.WithTimeout(parent, 500*time.Millisecond)
	defer cancel()

	out, err := exec.CommandContext(ctx, whatisBin, cmd).Output()
	if err != nil {
		return nil
	}

	var lines []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "nothing appropriate") {
			continue
		}
		if seen[line] {
			continue
		}
		seen[line] = true
		lines = append(lines, line)
		if len(lines) == 3 {
			break
		}
	}
	return lines
}

// ProbeManFileDirect checks standard man directories across macOS and Linux
// ($MANPATH, /opt/homebrew/share/man, /usr/local/share/man, /usr/share/man,
// /home/linuxbrew/.linuxbrew/share/man, ~/.local/share/man, and adjacent install directories)
// for <cmd>.{1,8,6}[.gz] and parses the NAME summary directly in <0.2ms without spawning /usr/bin/whatis.
func ProbeManFileDirect(cmd, binPath string) []string {
	safe := filepath.Base(cmd)
	if safe == "" || safe == "." || safe == "/" {
		return nil
	}

	var roots []string
	if mp := os.Getenv("MANPATH"); mp != "" {
		for _, p := range filepath.SplitList(mp) {
			if p != "" {
				roots = append(roots, p)
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(home, ".local", "share", "man"))
	}
	if binPath != "" {
		relShareMan := filepath.Clean(filepath.Join(filepath.Dir(binPath), "..", "share", "man"))
		roots = append(roots, relShareMan)
	}
	roots = append(roots,
		"/opt/homebrew/share/man",
		"/usr/local/share/man",
		"/home/linuxbrew/.linuxbrew/share/man",
		"/usr/share/man",
	)
	if home, err := os.UserHomeDir(); err == nil {
		wsMan := filepath.Join(home, "workspace", "go", safe, safe+".1")
		if desc := ParseManFileSummary(wsMan); desc != "" {
			return []string{fmt.Sprintf("%s(1) - %s", safe, desc)}
		}
	}

	sections := []string{"1", "8", "6"}
	var results []string
	seen := make(map[string]bool)

	for _, root := range roots {
		for _, sec := range sections {
			dir := filepath.Join(root, "man"+sec)
			for _, ext := range []string{"." + sec, "." + sec + ".gz"} {
				p := filepath.Join(dir, safe+ext)
				if desc := ParseManFileSummary(p); desc != "" {
					formatted := fmt.Sprintf("%s(%s) - %s", safe, sec, desc)
					if !seen[formatted] {
						seen[formatted] = true
						results = append(results, formatted)
					}
				}
			}
		}
	}
	return results
}

// ParseManFileSummary reads up to the first 120 lines of a troff/mdoc man page
// (.1 or .1.gz) and extracts the one-line summary from `.SH NAME` or `.Nd`.
func ParseManFileSummary(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return ""
		}
		defer gz.Close()
		r = gz
	}

	sc := bufio.NewScanner(io.LimitReader(r, 32*1024))
	inName := false
	var nameLines []string

	for i := 0; i < 150 && sc.Scan(); i++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, `.\"`) || strings.HasPrefix(line, `'\"`) || line == "." {
			continue
		}

		// Handle .so redirect (e.g., ".so man1/builtin.1")
		if strings.HasPrefix(line, ".so ") {
			targetRel := strings.TrimSpace(line[4:])
			baseManDir := filepath.Dir(filepath.Dir(path))
			return ParseManFileSummary(filepath.Join(baseManDir, targetRel))
		}

		// BSD mdoc summary macro: .Nd <description>
		if strings.HasPrefix(line, ".Nd ") {
			return cleanRoffText(line[4:])
		}

		// Standard roff / mdoc section header
		upper := strings.ToUpper(line)
		if strings.HasPrefix(upper, ".SH ") {
			secName := strings.Trim(strings.TrimSpace(upper[4:]), `"`)
			if secName == "NAME" {
				inName = true
				continue
			} else if inName {
				break
			}
		}

		if inName {
			if strings.HasPrefix(line, ".Nm") {
				continue
			}
			// Skip formatting-only directives
			if strings.HasPrefix(line, ".sp") || strings.HasPrefix(line, ".PP") || strings.HasPrefix(line, ".br") {
				continue
			}
			nameLines = append(nameLines, line)
			joined := strings.Join(nameLines, " ")
			if desc := extractAfterDash(joined); desc != "" {
				return desc
			}
		}
	}

	if len(nameLines) > 0 {
		if desc := extractAfterDash(strings.Join(nameLines, " ")); desc != "" {
			return desc
		}
	}
	return ""
}

func extractAfterDash(s string) string {
	cleaned := cleanRoffText(s)
	for _, sep := range []string{" - ", " -- ", " — ", " – "} {
		if idx := strings.Index(cleaned, sep); idx >= 0 {
			return strings.TrimSpace(cleaned[idx+len(sep):])
		}
	}
	return ""
}

func cleanRoffText(s string) string {
	s = strings.ReplaceAll(s, `\-`, "-")
	s = strings.ReplaceAll(s, `\fB`, "")
	s = strings.ReplaceAll(s, `\fI`, "")
	s = strings.ReplaceAll(s, `\fR`, "")
	s = strings.ReplaceAll(s, `\fP`, "")
	s = strings.ReplaceAll(s, `\(em`, "-")
	s = strings.ReplaceAll(s, `\(en`, "-")
	s = strings.TrimPrefix(s, ".B ")
	s = strings.TrimPrefix(s, ".I ")
	return strings.TrimSpace(s)
}

// ProbeBrew resolves Homebrew formula/cask metadata using:
// 1. Direct Cellar receipt (.brew/<formula>.rb) in <0.2ms if installed.
// 2. Direct Homebrew local API payload index (~/Library/Caches/Homebrew/api/internal/...) in ~5-15ms for uninstalled packages/casks.
// 3. Fallback to `brew info --json=v2` only if local index is absent.
func ProbeBrew(parent context.Context, cmd, realPath, customBrewCache string) (pkg, desc, home string) {
	// Fast path 1: read .brew/<formula>.rb directly from Cellar (<0.2ms)
	if m := reCellar.FindStringSubmatch(realPath); len(m) == 3 {
		formula, ver := m[1], m[2]
		idx := strings.Index(realPath, "/Cellar/")
		rbPath := filepath.Join(realPath[:idx], "Cellar", formula, ver, ".brew", formula+".rb")
		if pkg, desc, home = ParseBrewReceipt(rbPath, formula); desc != "" {
			return pkg, desc, home
		}
	}

	// Fast path 2: read Homebrew 4+ local API cache index & payload (~5-15ms, zero subprocess)
	target := cmd
	if pkg != "" {
		target = pkg
	}
	if p, d, h := ProbeBrewLocalCache(target, customBrewCache); d != "" {
		return p, d, h
	}

	// Fallback path 3: query brew JSON v2 only if local cache didn't match and brew is installed
	if _, err := exec.LookPath("brew"); err != nil {
		return "", "", ""
	}
	// If local cache exists and is populated, avoid spawning slow 600ms brew ruby runtime on complete misses
	if hasPopulatedBrewLocalCache(customBrewCache) {
		return "", "", ""
	}

	ctx, cancel := context.WithTimeout(parent, 1500*time.Millisecond)
	defer cancel()

	out, err := exec.CommandContext(ctx, "brew", "info", "--json=v2", target).Output()
	if err != nil {
		return "", "", ""
	}

	return parseBrewJSONv2(out)
}

// ParseBrewReceipt extracts formula description and homepage from a .brew/<formula>.rb file.
func ParseBrewReceipt(rbPath, formula string) (pkg, desc, home string) {
	data, err := os.ReadFile(rbPath)
	if err != nil {
		return "", "", ""
	}
	pkg = formula
	if dm := reBrewDesc.FindSubmatch(data); len(dm) == 2 {
		desc = string(dm[1])
	}
	if hm := reBrewHomepage.FindSubmatch(data); len(hm) == 2 {
		home = string(hm[1])
	}
	return pkg, desc, home
}

func parseBrewJSONv2(out []byte) (pkg, desc, home string) {
	var payload struct {
		Formulae []struct {
			Name     string `json:"name"`
			Desc     string `json:"desc"`
			Homepage string `json:"homepage"`
		} `json:"formulae"`
		Casks []struct {
			Token    string `json:"token"`
			Desc     string `json:"desc"`
			Homepage string `json:"homepage"`
		} `json:"casks"`
	}
	if json.Unmarshal(out, &payload) != nil {
		return "", "", ""
	}
	if len(payload.Formulae) > 0 {
		f := payload.Formulae[0]
		return f.Name, f.Desc, f.Homepage
	}
	if len(payload.Casks) > 0 {
		c := payload.Casks[0]
		return c.Token + " (cask)", c.Desc, c.Homepage
	}
	return "", "", ""
}

func defaultBrewAPIDir(custom string) string {
	if custom != "" {
		return custom
	}
	if envDir := os.Getenv("HOMEBREW_CACHE"); envDir != "" {
		p := filepath.Join(envDir, "api")
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			return p
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	// Check macOS default (~/Library/Caches/Homebrew/api) then Linux/XDG default (~/.cache/Homebrew/api)
	for _, candidate := range []string{
		filepath.Join(home, "Library", "Caches", "Homebrew", "api"),
		filepath.Join(home, ".cache", "Homebrew", "api"),
	} {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return filepath.Join(home, "Library", "Caches", "Homebrew", "api")
}

func hasPopulatedBrewLocalCache(custom string) bool {
	apiDir := defaultBrewAPIDir(custom)
	if apiDir == "" {
		return false
	}
	matches, _ := filepath.Glob(filepath.Join(apiDir, "internal", "packages.*.jws.json.payload.index"))
	return len(matches) > 0
}

type brewPayloadIndex struct {
	Formulae map[string][2]int64 `json:"formulae"`
	Casks    map[string][2]int64 `json:"casks"`
}

// findIndexSpan searches a specific top-level section ("formulae" or "casks")
// inside packages.*.jws.json.payload.index for `"<key>":[offset,length]` using
// raw byte search (~0.3ms vs ~15ms for full json.Unmarshal of 15,000 keys).
func findIndexSpan(idxData []byte, section, key string) (offset, length int64, ok bool) {
	if !reValidBrewKey.MatchString(key) {
		return 0, 0, false
	}
	secMarker := []byte(`"` + section + `":{`)
	secPos := bytes.Index(idxData, secMarker)
	if secPos < 0 {
		// Fallback to whitespace-tolerant unmarshal if format is pretty-printed
		var idx brewPayloadIndex
		if json.Unmarshal(idxData, &idx) != nil {
			return 0, 0, false
		}
		if section == "formulae" {
			if span, exists := idx.Formulae[key]; exists {
				return span[0], span[1], true
			}
		} else if section == "casks" {
			if span, exists := idx.Casks[key]; exists {
				return span[0], span[1], true
			}
		}
		return 0, 0, false
	}

	sub := idxData[secPos+len(secMarker):]
	// Limit search to current section object (up to closing `}`)
	if endBrace := bytes.IndexByte(sub, '}'); endBrace >= 0 {
		sub = sub[:endBrace]
	}

	needle := []byte(`"` + key + `":[`)
	pos := bytes.Index(sub, needle)
	if pos < 0 {
		return 0, 0, false
	}
	// Ensure exact key boundary (preceded by start of section `{` or `,`)
	if pos > 0 && sub[pos-1] != ',' && sub[pos-1] != '{' && sub[pos-1] != '\n' && sub[pos-1] != ' ' {
		return 0, 0, false
	}

	rest := sub[pos+len(needle):]
	endBracket := bytes.IndexByte(rest, ']')
	if endBracket <= 0 || endBracket > 64 {
		return 0, 0, false
	}
	var span [2]int64
	if json.Unmarshal([]byte("["+string(rest[:endBracket])+"]"), &span) != nil {
		return 0, 0, false
	}
	return span[0], span[1], true
}

// ProbeBrewLocalCache reads Homebrew's local API index and seeks directly to the byte offset
// in the uncompressed payload file, also mapping executable names and formula aliases.
func ProbeBrewLocalCache(cmd, customAPIDir string) (pkg, desc, home string) {
	apiDir := defaultBrewAPIDir(customAPIDir)
	if apiDir == "" {
		return "", "", ""
	}

	idxFiles, err := filepath.Glob(filepath.Join(apiDir, "internal", "packages.*.jws.json.payload.index"))
	if err != nil || len(idxFiles) == 0 {
		return "", "", ""
	}
	idxPath := idxFiles[0]
	payloadPath := strings.TrimSuffix(idxPath, ".index")

	idxData, err := os.ReadFile(idxPath)
	if err != nil {
		return "", "", ""
	}

	// 1. Direct formula match
	if off, length, ok := findIndexSpan(idxData, "formulae", cmd); ok {
		if d, h := readBrewPayloadSlice(payloadPath, off, length); d != "" {
			return cmd, d, h
		}
	}

	// 2. Check formula_aliases.txt (format: alias|target)
	if aliasTarget := lookupBrewAlias(filepath.Join(apiDir, "formula_aliases.txt"), cmd); aliasTarget != "" {
		if off, length, ok := findIndexSpan(idxData, "formulae", aliasTarget); ok {
			if d, h := readBrewPayloadSlice(payloadPath, off, length); d != "" {
				return aliasTarget, d, h
			}
		}
	}

	// 3. Direct cask match
	if off, length, ok := findIndexSpan(idxData, "casks", cmd); ok {
		if d, h := readBrewPayloadSlice(payloadPath, off, length); d != "" {
			return cmd + " (cask)", d, h
		}
	}

	// 4. Reverse executable lookup in internal/executables.txt (format: formula:exe1 exe2 ...)
	if formula := lookupBrewExecutable(filepath.Join(apiDir, "internal", "executables.txt"), cmd); formula != "" {
		if off, length, ok := findIndexSpan(idxData, "formulae", formula); ok {
			if d, h := readBrewPayloadSlice(payloadPath, off, length); d != "" {
				return formula, d, h
			}
		}
	}

	return "", "", ""
}

func readBrewPayloadSlice(payloadPath string, offset, length int64) (desc, home string) {
	if length <= 0 || length > 1024*1024 {
		return "", ""
	}
	f, err := os.Open(payloadPath)
	if err != nil {
		return "", ""
	}
	defer f.Close()

	// Line 1 of packages.*.jws.json.payload is the JWS signature header metadata;
	// byte offsets in .payload.index are relative to the start of line 2.
	br := bufio.NewReaderSize(f, 4096)
	line1, err := br.ReadBytes('\n')
	if err != nil {
		return "", ""
	}
	baseOffset := int64(len(line1))

	buf := make([]byte, length)
	if _, err := f.ReadAt(buf, baseOffset+offset); err != nil && err != io.EOF {
		return "", ""
	}

	var entry struct {
		Desc     string `json:"desc"`
		Homepage string `json:"homepage"`
	}
	if err := json.Unmarshal(buf, &entry); err != nil {
		return "", ""
	}
	return entry.Desc, entry.Homepage
}

func lookupBrewAlias(aliasPath, cmd string) string {
	f, err := os.Open(aliasPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	prefix := cmd + "|"
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(line[len(prefix):])
		}
	}
	return ""
}

func lookupBrewExecutable(exePath, cmd string) string {
	f, err := os.Open(exePath)
	if err != nil {
		return ""
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		exes := line[colon+1:]
		if !strings.Contains(exes, cmd) {
			continue
		}
		for _, field := range strings.Fields(exes) {
			if field == cmd {
				return line[:colon]
			}
		}
	}
	return ""
}

// ProbeGoBuildInfo extracts embedded Go module and compiler metadata from a compiled Go binary.
func ProbeGoBuildInfo(realPath string) (mod, goVer string) {
	bi, err := buildinfo.ReadFile(realPath)
	if err != nil {
		return "", ""
	}
	mod = bi.Main.Path
	if mod == "" || mod == "command-line-arguments" {
		mod = bi.Path
	}
	if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		mod = fmt.Sprintf("%s@%s", mod, bi.Main.Version)
	}
	return mod, bi.GoVersion
}

// ProbeScriptHeader inspects the shebang (#!) and path of a script to identify
// its interpreter and ecosystem (uv tool, pipx, Python virtualenv, Node/npm, Ruby, or Shell).
func ProbeScriptHeader(binPath, realPath string) (info, eco, pkg string) {
	target := realPath
	if target == "" {
		target = binPath
	}
	f, err := os.Open(target)
	if err != nil {
		return "", "", ""
	}
	defer f.Close()

	buf := make([]byte, 256)
	n, err := f.Read(buf)
	if err != nil || n < 2 || buf[0] != '#' || buf[1] != '!' {
		return "", "", ""
	}

	firstLine := string(buf[:n])
	if nl := strings.IndexAny(firstLine, "\r\n"); nl >= 0 {
		firstLine = firstLine[:nl]
	}
	shebang := strings.TrimSpace(firstLine[2:])
	if shebang == "" {
		return "", "", ""
	}

	// Detect interpreter and package manager paths from shebang or realPath
	combined := shebang + " " + realPath + " " + binPath

	// Check uv tools: ~/.local/share/uv/tools/<pkg>/...
	if m := regexp.MustCompile(`/uv/tools/([^/]+)/`).FindStringSubmatch(combined); len(m) == 2 {
		return fmt.Sprintf("Python uv tool %q (%s)", m[1], shebang), "pypi", m[1]
	}
	// Check pipx venvs: /pipx/venvs/<pkg>/...
	if m := regexp.MustCompile(`/pipx/venvs/([^/]+)/`).FindStringSubmatch(combined); len(m) == 2 {
		return fmt.Sprintf("Python pipx package %q (%s)", m[1], shebang), "pypi", m[1]
	}
	// Check node_modules: /node_modules/(@[^/]+/[^/]+|[^/]+)/
	if m := regexp.MustCompile(`/node_modules/(@[^/]+/[^/]+|[^/]+)/`).FindStringSubmatch(combined); len(m) == 2 {
		return fmt.Sprintf("Node.js package %q (%s)", m[1], shebang), "npm", m[1]
	}

	lower := strings.ToLower(shebang)
	switch {
	case strings.Contains(lower, "python"):
		return fmt.Sprintf("Python script (%s)", shebang), "pypi", ""
	case strings.Contains(lower, "node") || strings.Contains(lower, "bun") || strings.Contains(lower, "deno"):
		return fmt.Sprintf("JavaScript/Node script (%s)", shebang), "npm", ""
	case strings.Contains(lower, "ruby"):
		return fmt.Sprintf("Ruby script (%s)", shebang), "rubygems", ""
	case strings.Contains(lower, "perl"):
		return fmt.Sprintf("Perl script (%s)", shebang), "perl", ""
	case strings.Contains(lower, "zsh") || strings.Contains(lower, "bash") || strings.HasSuffix(lower, "/sh") || strings.HasSuffix(lower, " sh"):
		return fmt.Sprintf("Shell script (%s)", shebang), "shell", ""
	default:
		return fmt.Sprintf("Script (%s)", shebang), "script", ""
	}
}

// ProbeHelpSummary runs `<binPath> --help` with a strict 350ms timeout and extracts
// the first descriptive summary line (skipping blank lines, usage headers, and ASCII banners).
func ProbeHelpSummary(parent context.Context, binPath, cmdName string) string {
	ctx, cancel := context.WithTimeout(parent, 350*time.Millisecond)
	defer cancel()

	c := exec.CommandContext(ctx, binPath, "--help")
	// Prevent interactive prompts
	c.Stdin = nil
	c.Env = append(os.Environ(), "NO_COLOR=1", "TERM=dumb", "PAGER=cat")

	out, _ := c.CombinedOutput()
	if len(out) == 0 {
		return ""
	}
	return ExtractHelpDescription(string(out), cmdName)
}

// ExtractHelpDescription parses the raw output of `--help` and returns the most
// informative 1-line summary.
func ExtractHelpDescription(raw, cmdName string) string {
	clean := reANSIEscape.ReplaceAllString(raw, "")
	lines := strings.Split(clean, "\n")

	var candidate string
	for i := 0; i < len(lines) && i < 25; i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		// Skip Usage/Synopsis/Options headers or flag lines
		if strings.HasPrefix(lower, "usage:") ||
			strings.HasPrefix(lower, "usage ") ||
			strings.HasPrefix(lower, "synopsis") ||
			strings.HasPrefix(lower, "options:") ||
			strings.HasPrefix(lower, "flags:") ||
			strings.HasPrefix(lower, "commands:") ||
			strings.HasPrefix(lower, "available commands:") ||
			strings.HasPrefix(line, "-") ||
			strings.HasPrefix(line, "[") {
			continue
		}
		// Skip pure version lines like "foo v1.2.3" or "foo 1.2.3"
		if isBareVersionLine(line, cmdName) {
			continue
		}
		// Require at least 3 words or 15 characters of readable letters
		if countWords(line) < 3 || len(line) < 12 {
			continue
		}
		candidate = line
		break
	}

	if len(candidate) > 160 {
		candidate = candidate[:157] + "..."
	}
	return candidate
}

func isBareVersionLine(line, cmdName string) bool {
	fields := strings.Fields(line)
	if len(fields) > 3 {
		return false
	}
	if len(fields) == 2 && strings.EqualFold(strings.TrimSuffix(fields[0], ":"), cmdName) {
		v := strings.TrimLeft(fields[1], "vV")
		if len(v) > 0 && unicode.IsDigit(rune(v[0])) {
			return true
		}
	}
	return false
}

func countWords(s string) int {
	count := 0
	for _, f := range strings.Fields(s) {
		hasLetter := false
		for _, r := range f {
			if unicode.IsLetter(r) {
				hasLetter = true
				break
			}
		}
		if hasLetter {
			count++
		}
	}
	return count
}

// ProbeWebWithCache resolves remote package descriptions (GitHub, PyPI, npm, cheat.sh,
// and paid-tier search-grounded Gemini via 1Password/Keychain) with a 30-day local cache.
func ProbeWebWithCache(ctx context.Context, cmd string, local Result, opts Options) (string, string) {
	cacheFile := GetCachePath(opts.CacheDir, cmd)
	if !opts.NoCache && !opts.ForceAI && cacheFile != "" {
		if data, err := os.ReadFile(cacheFile); err == nil {
			var entry CacheEntry
			if json.Unmarshal(data, &entry) == nil && time.Since(entry.UpdatedAt) < 30*24*time.Hour && entry.Summary != "" {
				return entry.Summary, entry.Source + " (cached)"
			}
		}
	}

	// If user explicitly requested AI (-ai), jump straight to Gemini Search Grounding
	if opts.ForceAI {
		if desc, modelUsed := FetchGeminiGrounded(ctx, cmd, local, opts.OpRef); desc != "" {
			src := "gemini:" + modelUsed
			SaveCache(cacheFile, desc, src)
			return desc, src
		}
	}

	// 1. If it's a GitHub-backed Go binary, pull the exact repo description
	if strings.HasPrefix(local.GoMod, "github.com/") {
		parts := strings.Split(strings.Split(local.GoMod, "@")[0], "/")
		if len(parts) >= 3 {
			if desc := FetchGitHubDesc(ctx, parts[1], parts[2]); desc != "" {
				SaveCache(cacheFile, desc, "github")
				return desc, "github"
			}
		}
	}

	// 2. If it's a uv/pipx Python package or npm package, check PyPI / npm registry
	if local.ScriptEco == "pypi" && local.ScriptPkg != "" {
		if desc := FetchPyPIDesc(ctx, local.ScriptPkg); desc != "" {
			SaveCache(cacheFile, desc, "pypi")
			return desc, "pypi"
		}
	}
	if local.ScriptEco == "npm" && local.ScriptPkg != "" {
		if desc := FetchNPMDesc(ctx, local.ScriptPkg); desc != "" {
			SaveCache(cacheFile, desc, "npm")
			return desc, "npm"
		}
	}

	// 3. Fast zero-key community check: cheat.sh header lines
	if desc := FetchCheatSh(ctx, cmd); desc != "" {
		SaveCache(cacheFile, desc, "cheat.sh")
		return desc, "cheat.sh"
	}

	// 4. Paid-Tier Search-Grounded Gemini (via Keychain / 1Password biometric check)
	if desc, modelUsed := FetchGeminiGrounded(ctx, cmd, local, opts.OpRef); desc != "" {
		src := "gemini:" + modelUsed
		SaveCache(cacheFile, desc, src)
		return desc, src
	}

	return "", ""
}

// FetchGitHubDesc queries GitHub's REST API for a repository description.
func FetchGitHubDesc(parent context.Context, owner, repo string) string {
	ctx, cancel := context.WithTimeout(parent, 1500*time.Millisecond)
	defer cancel()

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return ""
	}
	defer resp.Body.Close()

	var repoInfo struct {
		Description string `json:"description"`
		HTMLURL     string `json:"html_url"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&repoInfo) == nil && repoInfo.Description != "" {
		return fmt.Sprintf("%s (%s)", repoInfo.Description, repoInfo.HTMLURL)
	}
	return ""
}

// FetchPyPIDesc queries PyPI's JSON API for a Python package summary.
func FetchPyPIDesc(parent context.Context, pkg string) string {
	ctx, cancel := context.WithTimeout(parent, 1500*time.Millisecond)
	defer cancel()

	url := fmt.Sprintf("https://pypi.org/pypi/%s/json", pkg)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return ""
	}
	defer resp.Body.Close()

	var payload struct {
		Info struct {
			Summary string `json:"summary"`
			HomeURL string `json:"home_page"`
			ProjURL string `json:"package_url"`
		} `json:"info"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&payload) == nil && payload.Info.Summary != "" {
		u := payload.Info.HomeURL
		if u == "" {
			u = payload.Info.ProjURL
		}
		if u != "" {
			return fmt.Sprintf("%s (%s)", payload.Info.Summary, u)
		}
		return payload.Info.Summary
	}
	return ""
}

// FetchNPMDesc queries the npm registry for a package description.
func FetchNPMDesc(parent context.Context, pkg string) string {
	ctx, cancel := context.WithTimeout(parent, 1500*time.Millisecond)
	defer cancel()

	url := fmt.Sprintf("https://registry.npmjs.org/%s/latest", pkg)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return ""
	}
	defer resp.Body.Close()

	var payload struct {
		Description string `json:"description"`
		Homepage    string `json:"homepage"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&payload) == nil && payload.Description != "" {
		if payload.Homepage != "" {
			return fmt.Sprintf("%s (%s)", payload.Description, payload.Homepage)
		}
		return payload.Description
	}
	return ""
}

// FetchCheatSh queries cheat.sh for leading `#` description comment lines.
func FetchCheatSh(parent context.Context, cmd string) string {
	ctx, cancel := context.WithTimeout(parent, 1500*time.Millisecond)
	defer cancel()

	url := fmt.Sprintf("https://cheat.sh/%s?T&q", cmd)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "curl/8.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return ""
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if err != nil {
		return ""
	}

	var comments []string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "#") {
			clean := strings.TrimSpace(strings.TrimLeft(line, "#"))
			if clean != "" && !strings.EqualFold(clean, cmd) {
				comments = append(comments, clean)
			}
		} else if len(comments) > 0 {
			break
		}
		if len(comments) == 2 {
			break
		}
	}
	return strings.Join(comments, " ")
}

// GroundedModelChain defines the paid-tier search-grounded model chain.
// Fast failover with zero multi-pass sleep loops.
var GroundedModelChain = []string{
	"gemini-2.5-flash",
	"gemini-3.1-pro-preview",
}

// FetchGeminiGrounded resolves the paid API key (via Keychain or 1Password CLI biometric prompt)
// and queries Gemini with Google Search Grounding enabled.
func FetchGeminiGrounded(parent context.Context, cmd string, local Result, opRef string) (string, string) {
	apiKey, err := credentials.ResolveGeminiKey(opRef, false)
	if err != nil || apiKey == "" {
		return "", ""
	}

	var contextHints []string
	if local.GoMod != "" {
		contextHints = append(contextHints, "Go module: "+local.GoMod)
	}
	if local.ScriptInfo != "" {
		contextHints = append(contextHints, "Script info: "+local.ScriptInfo)
	}
	if local.HelpLine != "" {
		contextHints = append(contextHints, "CLI help summary: "+local.HelpLine)
	}
	hintText := ""
	if len(contextHints) > 0 {
		hintText = " Context from local binary inspection: " + strings.Join(contextHints, "; ") + "."
	}

	prompt := fmt.Sprintf(
		"Provide a crisp 1-2 sentence Unix 'whatis' style description for the CLI command or package %q.%s "+
			"State strictly what it does and its primary ecosystem, with no introductory filler or markdown formatting.",
		cmd, hintText)

	reqBody := map[string]any{
		"contents": []map[string]any{
			{"parts": []map[string]string{{"text": prompt}}},
		},
		"tools": []map[string]any{
			{"google_search": map[string]any{}},
		},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", ""
	}

	for _, model := range GroundedModelChain {
		if text := callGeminiModel(parent, apiKey, model, bodyBytes); text != "" {
			return text, model
		}
	}
	return "", ""
}

func callGeminiModel(parent context.Context, apiKey, model string, bodyBytes []byte) string {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", model, apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var gemResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 128*1024)).Decode(&gemResp) != nil {
		return ""
	}
	if len(gemResp.Candidates) == 0 {
		return ""
	}

	var parts []string
	for _, p := range gemResp.Candidates[0].Content.Parts {
		if p.Thought || strings.TrimSpace(p.Text) == "" {
			continue
		}
		parts = append(parts, strings.TrimSpace(p.Text))
	}
	return strings.Join(parts, " ")
}

// GetCachePath returns the path to the JSON cache file for a command.
func GetCachePath(customDir, cmd string) string {
	dir := customDir
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(base, "swhatis")
	}
	safe := filepath.Base(cmd)
	if safe == "" || safe == "." || safe == "/" {
		return ""
	}
	return filepath.Join(dir, safe+".json")
}

// SaveCache persists a web/AI lookup summary to disk.
func SaveCache(path, summary, source string) {
	if path == "" || summary == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	data, err := json.Marshal(CacheEntry{
		Summary:   summary,
		Source:    source,
		UpdatedAt: time.Now(),
	})
	if err == nil {
		_ = os.WriteFile(path, data, 0o644)
	}
}
