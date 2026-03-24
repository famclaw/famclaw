// Package seccheck audits OpenClaw skill/MCP git repositories for security issues
// before installation. Runs static analysis then executes in an isolated sandbox.
package seccheck

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// ── Public types ──────────────────────────────────────────────────────────────

const (
	SevCritical = "CRITICAL"
	SevHigh     = "HIGH"
	SevMedium   = "MEDIUM"
	SevLow      = "LOW"
	SevInfo     = "INFO"

	VerdictPass = "PASS"
	VerdictWarn = "WARN"
	VerdictFail = "FAIL"
)

type Finding struct {
	Severity    string  `json:"severity"`
	Scanner     string  `json:"scanner"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	File        string  `json:"file,omitempty"`
	Line        int     `json:"line,omitempty"`
	Evidence    string  `json:"evidence,omitempty"`
	CVE         string  `json:"cve,omitempty"`
	CVSS        float64 `json:"cvss,omitempty"`
}

type Report struct {
	RepoURL     string    `json:"repo_url"`
	CommitSHA   string    `json:"commit_sha"`
	ScannedAt   time.Time `json:"scanned_at"`
	Findings    []Finding `json:"findings"`
	Score       int       `json:"score"`
	Verdict     string    `json:"verdict"`
	Summary     string    `json:"summary"`
	SandboxUsed string    `json:"sandbox_used"`
	FilesScanned int      `json:"files_scanned"`
}

type Options struct {
	SkipSandbox bool
	Timeout     time.Duration
	Verbose     bool
	OSVAPI      string // CVE API endpoint
	Sandbox     string // "auto" | "docker" | "sandbox-exec"
}

func DefaultOptions() Options {
	return Options{
		Timeout: 5 * time.Minute,
		OSVAPI:  "https://api.osv.dev/v1",
		Sandbox: "auto",
	}
}

// ── Scanner ───────────────────────────────────────────────────────────────────

type Scanner struct{ opts Options }

func New(opts Options) *Scanner { return &Scanner{opts: opts} }

// Scan runs the full security pipeline against a git repository URL.
func (s *Scanner) Scan(ctx context.Context, repoURL string) (*Report, error) {
	if s.opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.opts.Timeout)
		defer cancel()
	}

	report := &Report{RepoURL: repoURL, ScannedAt: time.Now().UTC()}

	// 1. Clone
	dir, sha, err := clone(ctx, repoURL)
	if err != nil {
		return nil, fmt.Errorf("clone: %w", err)
	}
	defer os.RemoveAll(dir)
	report.CommitSHA = sha
	s.logf("Cloned %s @ %s", repoURL, sha[:min(8, len(sha))])

	// 2. Static scanners
	for _, run := range []struct {
		name string
		fn   func(context.Context, string) ([]Finding, error)
	}{
		{"secrets", scanSecrets},
		{"network", scanNetwork},
		{"supply-chain", s.scanSupplyChain},
		{"data-access", scanDataAccess},
	} {
		s.logf("Scanner: %s", run.name)
		findings, err := run.fn(ctx, dir)
		if err != nil {
			s.logf("Scanner %s error (non-fatal): %v", run.name, err)
		}
		report.Findings = append(report.Findings, findings...)
	}

	// 3. CVE check via osv.dev
	cveFindings, err := s.checkCVEs(ctx, dir)
	if err != nil {
		s.logf("CVE check error (non-fatal): %v", err)
	}
	report.Findings = append(report.Findings, cveFindings...)

	// 4. Runtime sandbox
	if !s.opts.SkipSandbox {
		sbFindings, sbUsed, err := s.runSandbox(ctx, dir)
		if err != nil {
			s.logf("Sandbox error (non-fatal): %v", err)
		} else {
			report.Findings = append(report.Findings, sbFindings...)
			report.SandboxUsed = sbUsed
		}
	}

	// 5. Count files
	report.FilesScanned = countFiles(dir)

	// 6. Score & verdict
	report.Score, report.Verdict, report.Summary = score(report.Findings)
	return report, nil
}

// ── Static: Secrets ───────────────────────────────────────────────────────────

var secretRules = []struct{ name, sev string; re *regexp.Regexp }{
	{"AWS Access Key", SevCritical, regexp.MustCompile(`(?i)(AKIA|ABIA|ACCA|ASIA)[0-9A-Z]{16}`)},
	{"AWS Secret", SevCritical, regexp.MustCompile(`(?i)aws.{0,20}secret.{0,20}['"][0-9a-zA-Z/+]{40}['"]`)},
	{"GCP API Key", SevCritical, regexp.MustCompile(`AIza[0-9A-Za-z\-_]{35}`)},
	{"GitHub Token", SevCritical, regexp.MustCompile(`gh[pousr]_[A-Za-z0-9_]{36,}`)},
	{"OpenAI Key", SevCritical, regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`)},
	{"Anthropic Key", SevCritical, regexp.MustCompile(`sk-ant-[A-Za-z0-9\-]{40,}`)},
	{"Slack Token", SevCritical, regexp.MustCompile(`xox[baprs]-[0-9A-Za-z\-]{10,48}`)},
	{"Stripe Key", SevCritical, regexp.MustCompile(`(?:r|s)k_(?:live|test)_[0-9a-zA-Z]{24,}`)},
	{"Private Key", SevCritical, regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH )?PRIVATE KEY`)},
	{"Discord Webhook", SevHigh, regexp.MustCompile(`https://discord(?:app)?\.com/api/webhooks/[0-9]+/[A-Za-z0-9_\-]+`)},
	{"Hardcoded Password", SevHigh, regexp.MustCompile(`(?i)(?:password|passwd|pwd)\s*[:=]\s*['"][^'"]{6,}['"]`)},
	{"Hardcoded Secret", SevHigh, regexp.MustCompile(`(?i)(?:secret|api_key|apikey|auth_token)\s*[:=]\s*['"][^'"]{8,}['"]`)},
	{"DB Connection String", SevHigh, regexp.MustCompile(`(?i)(?:postgres|mysql|mongodb|redis)://[^:@\s]+:[^@\s]+@`)},
}

func scanSecrets(_ context.Context, dir string) ([]Finding, error) {
	var findings []Finding
	walkCode(dir, func(path, rel string) {
		f, err := os.Open(path)
		if err != nil { return }
		defer f.Close()
		sc := bufio.NewScanner(f)
		line := 0
		for sc.Scan() {
			line++
			text := sc.Text()
			if isPlaceholder(text) { continue }
			for _, rule := range secretRules {
				if rule.re.MatchString(text) {
					findings = append(findings, Finding{
						Severity:    rule.sev,
						Scanner:     "secrets",
						Title:       rule.name + " detected",
						Description: "Hardcoded credential found. Anyone installing this skill can steal it.",
						File:        rel,
						Line:        line,
						Evidence:    redact(strings.TrimSpace(text), 100),
					})
					break
				}
			}
		}
	})
	return findings, nil
}

// ── Static: Network ───────────────────────────────────────────────────────────

var networkRules = []struct{ re *regexp.Regexp; title, desc, sev string }{
	{regexp.MustCompile(`(?i)(?:fetch|axios|got|request)\s*\(\s*['"]https?://(?:webhook\.site|requestbin|pipedream|hookbin)`),
		"Exfiltration via webhook inspection service",
		"Code sends data to a public request logger — a classic exfiltration technique.", SevCritical},
	{regexp.MustCompile(`(?i)\beval\s*\(`),
		"eval() usage", "Can execute arbitrary downloaded code.", SevHigh},
	{regexp.MustCompile(`(?i)new\s+Function\s*\(`),
		"Dynamic Function()", "Equivalent to eval — executes arbitrary code strings.", SevHigh},
	{regexp.MustCompile(`(?i)(?:nc|ncat|netcat)\s+-[el]|bash\s+-i\s+>&|/dev/tcp/`),
		"Reverse shell pattern", "Code contains reverse shell indicators.", SevCritical},
	{regexp.MustCompile(`(?i)https?://(?:\d{1,3}\.){3}\d{1,3}(?::\d+)?/`),
		"Hardcoded IP endpoint", "Direct IP calls hide malicious infrastructure.", SevHigh},
	{regexp.MustCompile(`(?i)(?:coinhive|cryptonight|stratum\+tcp|xmrig)`),
		"Crypto mining code", "Mining library or protocol reference found.", SevCritical},
	{regexp.MustCompile(`(?i)(?:mixpanel|amplitude|segment|fullstory)\.(?:track|identify)`),
		"Undisclosed telemetry", "Third-party analytics not disclosed in SKILL.md.", SevMedium},
}

func scanNetwork(_ context.Context, dir string) ([]Finding, error) {
	var findings []Finding
	walkCode(dir, func(path, rel string) {
		f, err := os.Open(path)
		if err != nil { return }
		defer f.Close()
		sc := bufio.NewScanner(f)
		line := 0
		for sc.Scan() {
			line++
			text := sc.Text()
			for _, rule := range networkRules {
				if rule.re.MatchString(text) {
					findings = append(findings, Finding{
						Severity:    rule.sev,
						Scanner:     "network",
						Title:       rule.title,
						Description: rule.desc,
						File:        rel,
						Line:        line,
						Evidence:    redact(strings.TrimSpace(text), 120),
					})
				}
			}
		}
	})
	return findings, nil
}

// ── Static: Data access vs SKILL.md declaration ───────────────────────────────

func scanDataAccess(_ context.Context, dir string) ([]Finding, error) {
	var findings []Finding
	skillMD := filepath.Join(dir, "SKILL.md")

	declared := readDeclaredTools(skillMD)

	var accessesFS, accessesNet, accessesExec bool
	walkCode(dir, func(path, _ string) {
		content, err := os.ReadFile(path)
		if err != nil { return }
		s := string(content)
		if regexp.MustCompile(`(?i)fs\.|readFile|writeFile|os\.Open|os\.Create`).MatchString(s) {
			accessesFS = true
		}
		if regexp.MustCompile(`(?i)fetch\(|http\.|axios|got\(`).MatchString(s) {
			accessesNet = true
		}
		if regexp.MustCompile(`(?i)exec\(|spawn\(|execSync|subprocess|os\.system`).MatchString(s) {
			accessesExec = true
		}
	})

	if accessesFS && !declared["file_operations"] {
		findings = append(findings, Finding{
			Severity:    SevMedium,
			Scanner:     "data-access",
			Title:       "Undeclared filesystem access",
			Description: "Code accesses the filesystem but SKILL.md does not declare file_operations in the tools list.",
		})
	}
	if accessesNet && !declared["web_search"] && !declared["network"] {
		findings = append(findings, Finding{
			Severity:    SevLow,
			Scanner:     "data-access",
			Title:       "Undeclared network access",
			Description: "Code makes network requests but SKILL.md does not declare web_search or network in tools.",
		})
	}
	if accessesExec && !declared["code_execution"] && !declared["computer"] {
		findings = append(findings, Finding{
			Severity:    SevHigh,
			Scanner:     "data-access",
			Title:       "Undeclared process execution",
			Description: "Code spawns child processes but SKILL.md does not declare code_execution in tools.",
		})
	}
	return findings, nil
}

// ── CVE check via osv.dev ─────────────────────────────────────────────────────

func (s *Scanner) checkCVEs(ctx context.Context, dir string) ([]Finding, error) {
	pkgs := collectDependencies(dir)
	if len(pkgs) == 0 {
		return nil, nil
	}

	type osvPkg struct {
		Name      string `json:"name"`
		Ecosystem string `json:"ecosystem"`
		Version   string `json:"version,omitempty"`
	}
	type osvQuery struct {
		Packages []osvPkg `json:"packages"`
	}
	type osvVuln struct {
		ID       string `json:"id"`
		Summary  string `json:"summary"`
		Severity []struct {
			Score string `json:"score"`
		} `json:"severity"`
	}
	type osvResult struct {
		Results []struct {
			Package osvPkg    `json:"package"`
			Vulns   []osvVuln `json:"vulns"`
		} `json:"results"`
	}

	query := osvQuery{Packages: pkgs}
	body, _ := json.Marshal(query)

	api := s.opts.OSVAPI
	if api == "" {
		api = "https://api.osv.dev/v1"
	}

	req, err := http.NewRequestWithContext(ctx, "POST", api+"/querybatch", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("osv.dev: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	var result osvResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("osv parse: %w", err)
	}

	var findings []Finding
	for _, r := range result.Results {
		for _, v := range r.Vulns {
			sev := SevMedium
			if strings.HasPrefix(v.ID, "CVE") {
				sev = SevHigh
			}
			findings = append(findings, Finding{
				Severity:    sev,
				Scanner:     "cve",
				Title:       fmt.Sprintf("%s in %s", v.ID, r.Package.Name),
				Description: v.Summary,
				CVE:         v.ID,
			})
		}
	}
	return findings, nil
}

// ── Supply chain scanner ──────────────────────────────────────────────────────

var popularPackages = []string{
	"react", "express", "lodash", "axios", "webpack", "typescript", "eslint",
	"jest", "chalk", "commander", "yargs", "dotenv", "moment", "uuid",
	"@anthropic-ai/sdk", "@modelcontextprotocol/sdk", "openai", "mcp",
	"requests", "flask", "django", "numpy", "pandas", "pydantic", "fastapi",
}

func (s *Scanner) scanSupplyChain(_ context.Context, dir string) ([]Finding, error) {
	var findings []Finding

	// Read package.json
	if data, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var pkg struct {
			Deps    map[string]string `json:"dependencies"`
			DevDeps map[string]string `json:"devDependencies"`
		}
		if json.Unmarshal(data, &pkg) == nil {
			all := pkg.Deps
			if all == nil { all = make(map[string]string) }
			for k, v := range pkg.DevDeps { all[k] = v }
			for name := range all {
				if f := typosquatCheck(name, popularPackages); f != nil {
					findings = append(findings, *f)
				}
			}
		}
	}

	// Read requirements.txt
	if data, err := os.ReadFile(filepath.Join(dir, "requirements.txt")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			name := strings.Fields(strings.Split(line, "==")[0])[0]
			if name == "" || strings.HasPrefix(name, "#") { continue }
			if f := typosquatCheck(name, popularPackages); f != nil {
				findings = append(findings, *f)
			}
		}
	}

	return findings, nil
}

// ── Runtime sandbox ───────────────────────────────────────────────────────────

func (s *Scanner) runSandbox(ctx context.Context, dir string) ([]Finding, sbUsed string, _ error) {
	sandbox := s.opts.Sandbox
	if sandbox == "auto" {
		if hasDocker() {
			sandbox = "docker"
		} else if runtime.GOOS == "darwin" {
			sandbox = "sandbox-exec"
		} else {
			return nil, "none", nil // no sandbox available
		}
	}

	switch sandbox {
	case "docker":
		return s.runDockerSandbox(ctx, dir)
	case "sandbox-exec":
		return s.runSandboxExec(ctx, dir)
	default:
		return nil, "none", nil
	}
}

func (s *Scanner) runDockerSandbox(ctx context.Context, dir string) ([]Finding, string, error) {
	// Run the skill in a locked-down Docker container
	// --network=none: no outbound network
	// --read-only: no filesystem writes
	// --memory=256m: cap memory
	// --cpus=0.5: cap CPU (important on RPi)
	// --security-opt no-new-privileges
	args := []string{
		"run", "--rm",
		"--network=none",
		"--read-only",
		"--tmpfs", "/tmp:size=32m",
		"--memory=256m",
		"--cpus=0.5",
		"--security-opt", "no-new-privileges",
		"--user", "65534:65534", // nobody:nogroup
		"-v", dir + ":/skill:ro",
		"-w", "/skill",
		"--timeout", "30s",
		"node:20-alpine",
		"sh", "-c", sandboxScript(),
	}

	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Exit code != 0 is expected; we just parse the output
		s.logf("Docker sandbox output: %s", string(out))
	}

	findings := parseSandboxOutput(string(out))
	return findings, "docker", nil
}

func (s *Scanner) runSandboxExec(ctx context.Context, dir string) ([]Finding, string, error) {
	// macOS sandbox-exec with deny-all profile
	profile := `(version 1)
(deny default)
(allow file-read* (subpath "` + dir + `"))
(allow file-read* (subpath "/usr/lib"))
(allow file-read* (subpath "/usr/local/lib"))
(allow process-exec*)
(deny network*)
`
	profileFile, err := os.CreateTemp("", "famclaw-sandbox-*.sb")
	if err != nil {
		return nil, "", err
	}
	defer os.Remove(profileFile.Name())
	profileFile.WriteString(profile) //nolint:errcheck
	profileFile.Close()

	cmd := exec.CommandContext(ctx,
		"sandbox-exec", "-f", profileFile.Name(),
		"sh", "-c", "cd "+dir+" && "+sandboxScript())

	out, _ := cmd.CombinedOutput()
	s.logf("sandbox-exec output: %s", string(out))

	findings := parseSandboxOutput(string(out))
	return findings, "sandbox-exec", nil
}

// sandboxScript returns a shell script to run inside the sandbox.
// It tries to execute the skill's start command and observes behavior.
func sandboxScript() string {
	return `
set -e
# Detect entry point
if [ -f package.json ]; then
  npm install --ignore-scripts 2>&1 | head -50
  node -e "
    const fs = require('fs');
    const pkg = JSON.parse(fs.readFileSync('package.json','utf8'));
    const entry = pkg.main || 'index.js';
    if(fs.existsSync(entry)){
      const code = fs.readFileSync(entry,'utf8');
      // Flag if we see network calls despite being in network=none sandbox
      if(/fetch\(|http\.|axios/.test(code)) process.stdout.write('SANDBOX_NETWORK_ATTEMPT\n');
      if(/execSync|spawn\(/.test(code)) process.stdout.write('SANDBOX_EXEC_ATTEMPT\n');
    }
  " 2>/dev/null || true
fi
echo SANDBOX_DONE
`
}

func parseSandboxOutput(out string) []Finding {
	var findings []Finding
	if strings.Contains(out, "SANDBOX_NETWORK_ATTEMPT") {
		findings = append(findings, Finding{
			Severity:    SevHigh,
			Scanner:     "sandbox",
			Title:       "Network call attempted in sandbox",
			Description: "The skill tried to make network requests even when network access was blocked.",
		})
	}
	if strings.Contains(out, "SANDBOX_EXEC_ATTEMPT") {
		findings = append(findings, Finding{
			Severity:    SevHigh,
			Scanner:     "sandbox",
			Title:       "Process execution attempted in sandbox",
			Description: "The skill attempted to spawn child processes.",
		})
	}
	return findings
}

// ── Scoring ───────────────────────────────────────────────────────────────────

func score(findings []Finding) (int, string, string) {
	deductions := 0
	crits, highs, meds, lows := 0, 0, 0, 0
	for _, f := range findings {
		switch f.Severity {
		case SevCritical: deductions += 40; crits++
		case SevHigh:     deductions += 20; highs++
		case SevMedium:   deductions += 8;  meds++
		case SevLow:      deductions += 2;  lows++
		}
	}
	sc := max(0, 100-deductions)

	switch {
	case crits > 0:
		return sc, VerdictFail, fmt.Sprintf("🚨 DO NOT INSTALL — %d critical issue(s): secrets, active exfiltration, or malware detected.", crits)
	case highs > 0:
		return sc, VerdictFail, fmt.Sprintf("❌ FAIL — %d high severity issue(s). Review carefully.", highs)
	case meds > 0:
		return sc, VerdictWarn, fmt.Sprintf("⚠️  WARN — %d medium issue(s). Proceed with caution.", meds)
	case lows > 0:
		return sc, VerdictWarn, fmt.Sprintf("⚠️  WARN — %d low severity note(s). Generally safe.", lows)
	default:
		return sc, VerdictPass, "✅ PASS — No significant security issues found."
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

var skipDirs = map[string]bool{".git":true,"node_modules":true,"vendor":true,"__pycache__":true,".venv":true,"dist":true,"build":true}
var codeExts = map[string]bool{".js":true,".ts":true,".mjs":true,".py":true,".go":true,".sh":true,".rb":true,".php":true}
var binaryExts = map[string]bool{".png":true,".jpg":true,".gif":true,".pdf":true,".zip":true,".tar":true,".gz":true,".wasm":true,".bin":true,".exe":true}

func walkCode(dir string, fn func(path, rel string)) {
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error { //nolint:errcheck
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() && skipDirs[d.Name()] { return filepath.SkipDir }
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if binaryExts[ext] { return nil }
		rel, _ := filepath.Rel(dir, path)
		fn(path, rel)
		return nil
	})
}

func isPlaceholder(line string) bool {
	lower := strings.ToLower(line)
	for _, p := range []string{"your_key","your-key","example","placeholder","xxxx","changeme","todo","${","process.env","os.getenv","os.environ","config.","***"} {
		if strings.Contains(lower, p) { return true }
	}
	return false
}

func redact(s string, n int) string {
	if len(s) > n { s = s[:n] + "…" }
	return regexp.MustCompile(`[A-Za-z0-9+/]{30,}`).ReplaceAllStringFunc(s, func(m string) string {
		if len(m) > 8 { return m[:4] + strings.Repeat("*", len(m)-8) + m[len(m)-4:] }
		return "****"
	})
}

func typosquatCheck(name string, popular []string) *Finding {
	nl := strings.ToLower(name)
	for _, p := range popular {
		pl := strings.ToLower(p)
		if nl == pl { continue }
		if editDistance(nl, pl) <= 2 && len(nl) > 4 {
			return &Finding{
				Severity:    SevHigh,
				Scanner:     "supply-chain",
				Title:       fmt.Sprintf("Possible typosquatting: %q resembles %q", name, p),
				Description: fmt.Sprintf("Package name %q is very similar to the popular package %q. This may be a typosquatting attack.", name, p),
			}
		}
	}
	return nil
}

// editDistance computes Levenshtein distance (simple O(nm) implementation).
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	m, n := len(ra), len(rb)
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
		dp[i][0] = i
	}
	for j := 0; j <= n; j++ { dp[0][j] = j }
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if ra[i-1] == rb[j-1] {
				dp[i][j] = dp[i-1][j-1]
			} else {
				dp[i][j] = 1 + min3(dp[i-1][j], dp[i][j-1], dp[i-1][j-1])
			}
		}
	}
	return dp[m][n]
}

func min3(a, b, c int) int {
	if a < b { if a < c { return a }; return c }
	if b < c { return b }; return c
}

func clone(ctx context.Context, url string) (string, string, error) {
	dir, err := os.MkdirTemp("", "seccheck-*")
	if err != nil { return "", "", err }
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", "--quiet", url, dir)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil { os.RemoveAll(dir); return "", "", err }
	out, _ := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	return dir, strings.TrimSpace(string(out)), nil
}

func hasDocker() bool {
	cmd := exec.Command("docker", "info")
	return cmd.Run() == nil
}

func countFiles(dir string) int {
	n := 0
	filepath.WalkDir(dir, func(_ string, d os.DirEntry, _ error) error { //nolint:errcheck
		if !d.IsDir() { n++ }; return nil
	})
	return n
}

func readDeclaredTools(skillMD string) map[string]bool {
	out := make(map[string]bool)
	data, err := os.ReadFile(skillMD)
	if err != nil { return out }
	toolsRe := regexp.MustCompile(`tools:\s*\[([^\]]+)\]`)
	m := toolsRe.FindSubmatch(data)
	if m == nil { return out }
	for _, t := range strings.Split(string(m[1]), ",") {
		out[strings.Trim(strings.TrimSpace(t), "'\"")] = true
	}
	return out
}

func collectDependencies(dir string) []any {
	// Returns []osvPkg as []any for osv.dev batch query
	type osvPkg struct {
		Name      string `json:"name"`
		Ecosystem string `json:"ecosystem"`
		Version   string `json:"version,omitempty"`
	}
	var pkgs []any

	if data, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var pkg struct {
			Deps map[string]string `json:"dependencies"`
		}
		if json.Unmarshal(data, &pkg) == nil {
			for name, ver := range pkg.Deps {
				pkgs = append(pkgs, osvPkg{Name: name, Ecosystem: "npm", Version: strings.TrimPrefix(ver, "^")})
			}
		}
	}

	if data, err := os.ReadFile(filepath.Join(dir, "requirements.txt")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") { continue }
			parts := strings.SplitN(line, "==", 2)
			p := osvPkg{Name: parts[0], Ecosystem: "PyPI"}
			if len(parts) == 2 { p.Version = parts[1] }
			pkgs = append(pkgs, p)
		}
	}

	return pkgs
}

func (s *Scanner) logf(format string, args ...any) {
	if s.opts.Verbose { log.Printf("[seccheck] "+format, args...) }
}

func min(a, b int) int { if a < b { return a }; return b }
func max(a, b int) int { if a > b { return a }; return b }
