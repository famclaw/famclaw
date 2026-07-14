package config

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server        ServerConfig        `yaml:"server"`
	LLM           LLMConfig           `yaml:"llm"`
	Inference     InferenceConfig     `yaml:"inference"`
	Users         []UserConfig        `yaml:"users"`
	Gateways      GatewaysConfig      `yaml:"gateways"`
	Policies      PoliciesConfig      `yaml:"policies"`
	Approval      ApprovalConfig      `yaml:"approval"`
	Skills        SkillsConfig        `yaml:"skills"`
	Notifications NotificationsConfig `yaml:"notifications"`
	Storage       StorageConfig       `yaml:"storage"`
	SecCheck      SecCheckConfig      `yaml:"seccheck"` // deprecated — use honeybadger instead
	Tools         ToolsConfig         `yaml:"tools,omitempty"`
}

// ToolsConfig groups configuration for built-in tools registered with the LLM.
type ToolsConfig struct {
	WebFetch  WebFetchConfig  `yaml:"web_fetch,omitempty"`
	WebSearch WebSearchConfig `yaml:"web_search,omitempty"`
	Browser   BrowserConfig   `yaml:"browser,omitempty"`
	ToolCache ToolCacheConfig `yaml:"tool_cache,omitempty"`
	FileRead FileReadConfig `yaml:"file_read,omitempty"`
	FileList FileListConfig `yaml:"file_list,omitempty"`
	SandboxRoot string `yaml:"sandbox_root,omitempty"`
	Sandbox   SandboxConfig   `yaml:"sandbox,omitempty"`
}

// SandboxConfig controls the sandboxing of MCP servers.
type SandboxConfig struct {
	Enabled         bool  `yaml:"enabled"`          // default true
	AllowUnconfined bool  `yaml:"allow_unconfined"` // default false (fail-closed)
}

// BrowserConfig controls the built-in browser_* tools (real browser nav via
// a remote Playwright server). Disabled by default; when enabled the LLM
// can navigate, click, fill, extract on real pages. Host gate reuses
// tools.web_fetch.url_allowlist.
type BrowserConfig struct {
	Enabled          bool     `yaml:"enabled"`
	Endpoint         string   `yaml:"endpoint,omitempty"`           // e.g. "ws://localhost:3000/"; required when Enabled
	AllowedRoles     []string `yaml:"allowed_roles,omitempty"`      // default ["parent"]
	IdleSec          int      `yaml:"idle_seconds,omitempty"`       // per-user session idle close; default 300
	SnapshotMaxChars int      `yaml:"snapshot_max_chars,omitempty"` // cap on browser_navigate/snapshot output; 0 = package default (5000)
}

// WebSearchConfig controls the built-in web_search tool. Disabled by default;
// when enabled the LLM may query a SearXNG (or compatible) JSON endpoint
// and receive structured title/url/snippet results. The endpoint host is
// still gated by tools.web_fetch.url_allowlist — add the search host
// (e.g. "localhost") to that list to use this tool.
type WebSearchConfig struct {
	Enabled      bool     `yaml:"enabled"`
	Endpoint     string   `yaml:"endpoint,omitempty"`        // e.g. "http://localhost:8888"; required when Enabled
	AllowedRoles []string `yaml:"allowed_roles,omitempty"`   // default ["parent"]
	MaxResults   int      `yaml:"max_results,omitempty"`     // default 8, hard-capped at 16
	TimeoutSec   int      `yaml:"timeout_seconds,omitempty"` // default 10
}

// ToolCacheConfig controls the Phase 2 tool-result spillover cache. When
// disabled, the agent falls back to inline-everything (legacy v0.5.x
// behavior — vulnerable to context overflow on big tool results).
type ToolCacheConfig struct {
	Enabled       bool              `yaml:"enabled"`         // default true when config block present; auto-enabled in main.go
	PerUserCapMB  int64             `yaml:"per_user_cap_mb"` // default 100
	TotalCapMB    int64             `yaml:"total_cap_mb"`    // default 1024 (advisory; not enforced yet)
	CacheDir      string            `yaml:"cache_dir"`       // empty = toolcache.DefaultCacheDir()
	SweepInterval string            `yaml:"sweep_interval"`  // Go duration; default "15m"
	TTLByRole     map[string]string `yaml:"ttl"`             // role → duration ("24h", "6h", "1h", "30m")
}

// WebFetchConfig controls the built-in web_fetch tool. Disabled by default;
// when enabled the LLM may fetch URLs subject to the per-host allowlist,
// role gate, and OPA policy decision.
type WebFetchConfig struct {
	Enabled      bool     `yaml:"enabled"`
	URLAllowlist []string `yaml:"url_allowlist,omitempty"` // REQUIRED when Enabled — empty list denies all (host-level gate)
	MaxBytes     int64    `yaml:"max_bytes,omitempty"`     // 0 = 256KB default
	TimeoutSec   int      `yaml:"timeout_seconds,omitempty"`
	AllowedRoles []string `yaml:"allowed_roles,omitempty"` // default ["parent"]
	// BlockPrivateNetworks opts INTO rejecting private/loopback/RFC1918/
	// link-local addresses at the dialer. famclaw's product model is a
	// home-LAN self-hosted server that legitimately needs to reach other
	// home-LAN services (SearXNG, Playwright, llama-server, etc.), so
	// the default is FALSE (allow private). Set to true for multi-tenant
	// or hostile-LAN deployments where the bot must not reach internal
	// addresses even when they appear in url_allowlist.
	BlockPrivateNetworks bool `yaml:"block_private_networks,omitempty"`
}

// InferenceConfig controls local LLM inference via llama-server sidecar.
type InferenceConfig struct {
	Backend   string   `yaml:"backend"`    // "llama-server" | "ollama" | "external" (default: "external")
	Binary    string   `yaml:"binary"`     // path to llama-server binary
	ModelPath string   `yaml:"model_path"` // path to GGUF model file
	Port      int      `yaml:"port"`       // sidecar port (default: 8081)
	GPULayers int      `yaml:"gpu_layers"` // layers to offload to GPU
	ExtraArgs []string `yaml:"extra_args"` // e.g. ["--cache-type-k", "q4_0"] for TurboQuant
}

type GatewaysConfig struct {
	Telegram TelegramConfig  `yaml:"telegram"`
	WhatsApp WhatsAppConfig  `yaml:"whatsapp"`
	Discord  DiscordGWConfig `yaml:"discord"`
}

type TelegramConfig struct {
	Enabled bool   `yaml:"enabled"`
	Token   string `yaml:"token"`
}

type WhatsAppConfig struct {
	Enabled bool   `yaml:"enabled"`
	DBPath  string `yaml:"db_path"`
}

type DiscordGWConfig struct {
	Enabled bool   `yaml:"enabled"`
	Token   string `yaml:"token"`
}

type ServerConfig struct {
	Host   string `yaml:"host"`
	Port   int    `yaml:"port"`
	Secret string `yaml:"secret"`
	// Deprecated: mDNS was removed in v0.5.x (issue #110) because it
	// didn't resolve reliably on Windows or many home routers. The field
	// is retained so existing config.yaml files continue to load, and
	// is still used by BaseURL() to construct notification links —
	// users should change this to their device's IP or DNS hostname so
	// approval-notification URLs work for recipients off the LAN.
	MDNSName string `yaml:"mdns_name,omitempty"`
}

func (s ServerConfig) Addr() string {
	return fmt.Sprintf("%s:%d", s.Host, s.Port)
}

func (s ServerConfig) BaseURL() string {
	host := s.MDNSName + ".local"
	if s.Port != 80 {
		return fmt.Sprintf("http://%s:%d", host, s.Port)
	}
	return "http://" + host
}

// LLMProfile is a named LLM endpoint configuration.
type LLMProfile struct {
	Label   string `yaml:"label"`
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
	APIKey  string `yaml:"api_key,omitempty"`
}

type LLMConfig struct {
	// Legacy single-endpoint fields (backward compatible)
	Provider string `yaml:"provider"`
	BaseURL  string `yaml:"base_url"`
	Model    string `yaml:"model"`
	APIKey   string `yaml:"api_key,omitempty"`
	// Named profiles (takes precedence when set)
	Default  string                `yaml:"default,omitempty"`
	Profiles map[string]LLMProfile `yaml:"profiles,omitempty"`
	// Common settings
	SystemPrompt      string  `yaml:"system_prompt"`
	MaxContextTokens  int     `yaml:"max_context_tokens"`
	MaxResponseTokens int     `yaml:"max_response_tokens"`
	Temperature       float64 `yaml:"temperature"`
}

type UserConfig struct {
	Name        string `yaml:"name"`
	DisplayName string `yaml:"display_name"`
	Role        string `yaml:"role"`      // parent | child
	AgeGroup    string `yaml:"age_group"` // under_8 | age_8_12 | age_13_17
	PIN         string `yaml:"pin"`
	Color       string `yaml:"color"`
	Model       string `yaml:"model"`                 // optional per-user model override (legacy)
	LLMProfile  string `yaml:"llm_profile,omitempty"` // named LLM profile override
}

// PoliciesConfig overrides the embedded OPA policies. Leave both
// fields empty (the default) to use the built-in policies compiled
// into the binary. Set Dir/DataDir to a filesystem path to load
// custom policies from disk.
type PoliciesConfig struct {
	Dir          string `yaml:"dir"`
	DataDir      string `yaml:"data_dir"`
	ExpectedHash string `yaml:"expected_hash,omitempty"` // SHA-256 hex digest for integrity verification
}

type ApprovalConfig struct {
	ExpiryHours int `yaml:"expiry_hours"`
}

type SkillsConfig struct {
	Dir            string                       `yaml:"dir"`
	AutoSecCheck   bool                         `yaml:"auto_seccheck"`
	BlockOnFail    bool                         `yaml:"block_on_fail"`
	OpenClawCompat bool                         `yaml:"openclaw_compat"`
	MCPServers     map[string]MCPServerConfig   `yaml:"mcp_servers,omitempty"`
	Credentials    map[string]map[string]string `yaml:"credentials,omitempty"` // per-skill env vars
	RoleEnablement map[string][]string          `yaml:"role_enablement,omitempty"`
}

type StorageConfig struct {
	DBPath string `yaml:"db_path"`
}

type SecCheckConfig struct {
	// Master switch — when false, no scanning anywhere.
	Enabled bool `yaml:"enabled"`

	// Install-time scanning
	AutoSecCheck bool   `yaml:"auto_seccheck"` // scan on `famclaw skill install`
	BlockOnFail  bool   `yaml:"block_on_fail"` // refuse install on FAIL verdict
	Paranoia     string `yaml:"paranoia"`      // minimal | family | strict | paranoid

	// Runtime scanning (async quarantine pattern)
	RuntimeScan        bool   `yaml:"runtime_scan"`         // enable async background scans
	RescanInterval     string `yaml:"rescan_interval"`      // e.g. "168h" for weekly
	AsyncScanTimeout   string `yaml:"async_scan_timeout"`   // per-scan timeout, e.g. "60s"
	QuarantineOnFail   bool   `yaml:"quarantine_on_fail"`   // block tools after FAIL
	NotifyOnQuarantine bool   `yaml:"notify_on_quarantine"` // send parent notification
}

type NotificationsConfig struct {
	Email   EmailConfig   `yaml:"email"`
	Slack   SlackConfig   `yaml:"slack"`
	Discord DiscordConfig `yaml:"discord"`
	SMS     SMSConfig     `yaml:"sms"`
	Ntfy    NtfyConfig    `yaml:"ntfy"`
}

type EmailConfig struct {
	Enabled  bool     `yaml:"enabled"`
	SMTPHost string   `yaml:"smtp_host"`
	SMTPPort int      `yaml:"smtp_port"`
	From     string   `yaml:"from"`
	Password string   `yaml:"password"`
	To       []string `yaml:"to"`
}

type SlackConfig struct {
	Enabled    bool   `yaml:"enabled"`
	WebhookURL string `yaml:"webhook_url"`
}

type DiscordConfig struct {
	Enabled    bool   `yaml:"enabled"`
	WebhookURL string `yaml:"webhook_url"`
}

type SMSConfig struct {
	Enabled    bool     `yaml:"enabled"`
	AccountSID string   `yaml:"twilio_account_sid"`
	AuthToken  string   `yaml:"twilio_auth_token"`
	FromNumber string   `yaml:"from_number"`
	ToNumbers  []string `yaml:"to_numbers"`
}

// NtfyConfig is for ntfy.sh — ideal for fully-local push notifications.
type NtfyConfig struct {
	Enabled bool   `yaml:"enabled"`
	URL     string `yaml:"url"`
	Topic   string `yaml:"topic"`
	Token   string `yaml:"token"`
}

// MCPServerConfig defines a single MCP tool server's transport and connection details.
type MCPServerConfig struct {
	Transport string            `yaml:"transport"`          // stdio | http | sse
	Command   string            `yaml:"command,omitempty"`  // stdio only
	Args      []string          `yaml:"args,omitempty"`     // stdio only
	URL       string            `yaml:"url,omitempty"`      // http/sse only
	Headers   map[string]string `yaml:"headers,omitempty"`  // http/sse only
	Disabled  bool              `yaml:"disabled,omitempty"` // false = enabled (default)
}

// ValidateMCPServer checks that an MCP server config has the required fields for its transport.
func ValidateMCPServer(name string, cfg MCPServerConfig) error {
	transport := cfg.Transport
	if transport == "" {
		if cfg.Command != "" {
			transport = "stdio"
		} else if cfg.URL != "" {
			transport = "http"
		} else {
			return fmt.Errorf("MCP server %q: transport, command, or url required", name)
		}
	}
	switch transport {
	case "stdio":
		if cfg.Command == "" {
			return fmt.Errorf("MCP server %q: stdio transport requires command", name)
		}
	case "http", "sse":
		if cfg.URL == "" {
			return fmt.Errorf("MCP server %q: %s transport requires url", name, transport)
		}
	default:
		return fmt.Errorf("MCP server %q: unknown transport %q (use stdio, http, or sse)", name, transport)
	}
	return nil
}

// Load reads and parses the config file, expanding environment variables.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %q: %w", path, err)
	}
	expanded := os.ExpandEnv(string(raw))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	resolveLLMAPIKey(&cfg)
	applyDefaults(&cfg)
	return &cfg, nil
}

// resolveLLMAPIKey applies the env-var-over-YAML precedence for the LLM API key.
// Precedence (highest wins):
//
//  1. FAMCLAW_LLM_API_KEY environment variable
//  2. YAML llm.api_key field
//
// If the env var is set it overrides the YAML value silently.
// If only YAML is set (and non-empty) a warning is logged — the key is
// never logged itself.  When both are empty the config field is left
// untouched (existing behaviour — caller validates at startup).
func resolveLLMAPIKey(c *Config) {
	if envKey := os.Getenv("FAMCLAW_LLM_API_KEY"); envKey != "" {
		c.LLM.APIKey = envKey
		return
	}
	if c.LLM.APIKey != "" {
		log.Printf("[config] llm.api_key: loaded from plaintext YAML config — prefer FAMCLAW_LLM_API_KEY environment variable")
	}
}

func applyDefaults(c *Config) {
	if c.Server.Host == "" {
		c.Server.Host = "0.0.0.0"
	}
	if c.Server.Port == 0 {
		c.Server.Port = 8080
	}
	if c.Server.MDNSName == "" {
		c.Server.MDNSName = "famclaw"
	}
	// No defaults for LLM BaseURL/Model — empty triggers the first-boot wizard.
	// User configures via web UI or config.yaml.
	if c.LLM.MaxContextTokens == 0 {
		c.LLM.MaxContextTokens = 4096
	}
	if c.LLM.MaxResponseTokens == 0 {
		c.LLM.MaxResponseTokens = 512
	}
	if c.LLM.Temperature == 0 {
		c.LLM.Temperature = 0.7
	}
	if c.Approval.ExpiryHours == 0 {
		c.Approval.ExpiryHours = 24
	}
	if c.Skills.Dir == "" {
		c.Skills.Dir = "./skills"
	}
	if c.Storage.DBPath == "" {
		c.Storage.DBPath = "./data/famclaw.db"
	}
	// SecCheck defaults — auto-enable master switch if any scanning feature is on
	if !c.SecCheck.Enabled && (c.SecCheck.AutoSecCheck || c.SecCheck.RuntimeScan) {
		c.SecCheck.Enabled = true
	}
	if c.SecCheck.Paranoia == "" {
		c.SecCheck.Paranoia = "family"
	}
	if c.SecCheck.RescanInterval == "" {
		c.SecCheck.RescanInterval = "168h"
	}
	if c.SecCheck.AsyncScanTimeout == "" {
		c.SecCheck.AsyncScanTimeout = "60s"
	}
	if c.Notifications.Email.SMTPPort == 0 {
		c.Notifications.Email.SMTPPort = 587
	}
	if c.Notifications.Ntfy.URL == "" {
		c.Notifications.Ntfy.URL = "http://localhost:2586"
	}
	// web_fetch defaults — only meaningful when Enabled, but applied
	// unconditionally so the values are usable if a runtime toggle ever exists.
	if c.Tools.WebFetch.MaxBytes == 0 {
		c.Tools.WebFetch.MaxBytes = 256 * 1024
	}
	if c.Tools.WebFetch.TimeoutSec == 0 {
		c.Tools.WebFetch.TimeoutSec = 15
	}
	if len(c.Tools.WebFetch.AllowedRoles) == 0 {
		c.Tools.WebFetch.AllowedRoles = []string{"parent"}
	}
	// web_search defaults
	if c.Tools.WebSearch.MaxResults == 0 {
		c.Tools.WebSearch.MaxResults = 8
	}
	if c.Tools.WebSearch.TimeoutSec == 0 {
		c.Tools.WebSearch.TimeoutSec = 10
	}
	if len(c.Tools.WebSearch.AllowedRoles) == 0 {
		c.Tools.WebSearch.AllowedRoles = []string{"parent"}
	}
	// sandbox defaults
	if c.Tools.Sandbox.Enabled == false { // zero value is false, so we explicitly check for zero
		// INTENTIONAL DESIGN — secure-by-default. A fresh deployment
		// enables the landlock+seccomp sandbox launcher for every stdio
		// MCP server; operators who genuinely want unconfined subprocesses
		// must set tools.sandbox.enabled=false explicitly. The startup
		// checks elsewhere in the codebase assume this default is on
		// and produce a fail-closed error if it is enabled but the
		// kernel cannot support it.
		c.Tools.Sandbox.Enabled = true
	}
	// default sandbox root when not configured
	if c.Tools.SandboxRoot == "" {
		c.Tools.SandboxRoot = "./data/sandbox"
	}
	// browser defaults
	if c.Tools.Browser.IdleSec == 0 {
		c.Tools.Browser.IdleSec = 300
	}
	if len(c.Tools.Browser.AllowedRoles) == 0 {
		c.Tools.Browser.AllowedRoles = []string{"parent"}
	}
}

// Validate checks that critical config values are set and safe.
// Called at startup — fails fast with plain-language errors.
func (c *Config) Validate() error {
	if len(c.Server.Secret) > 0 && len(c.Server.Secret) < 32 {
		return fmt.Errorf(
			"server secret is too short (%d chars, need 32+).\n"+
				"Generate one: openssl rand -hex 32\n"+
				"Then set: FAMCLAW_SECRET=<value>",
			len(c.Server.Secret),
		)
	}
	for _, u := range c.Users {
		if u.Role == "parent" && u.PIN != "" && len(u.PIN) < 4 {
			return fmt.Errorf(
				"parent PIN for %q is too short (%d chars, need 4+).\n"+
					"Set a longer PIN in config or via PARENT_PIN env var.",
				u.Name, len(u.PIN),
			)
		}
	}
	for name, mcpCfg := range c.Skills.MCPServers {
		if mcpCfg.Disabled {
			continue
		}
		if err := ValidateMCPServer(name, mcpCfg); err != nil {
			return err
		}
	}
	if c.Tools.WebFetch.Enabled {
		if c.Tools.WebFetch.MaxBytes <= 0 {
			return fmt.Errorf("tools.web_fetch.max_bytes must be > 0 (got %d)", c.Tools.WebFetch.MaxBytes)
		}
		if c.Tools.WebFetch.TimeoutSec <= 0 {
			return fmt.Errorf("tools.web_fetch.timeout_seconds must be > 0 (got %d)", c.Tools.WebFetch.TimeoutSec)
		}
		hasHost := false
		for _, host := range c.Tools.WebFetch.URLAllowlist {
			if strings.TrimSpace(host) != "" {
				hasHost = true
				break
			}
		}
		if !hasHost {
			return fmt.Errorf("tools.web_fetch.url_allowlist must list at least one non-empty host when enabled (empty list denies all fetches as an SSRF guard)")
		}
	}
	if c.Tools.WebSearch.Enabled {
		if strings.TrimSpace(c.Tools.WebSearch.Endpoint) == "" {
			return fmt.Errorf("tools.web_search.endpoint must be set when enabled (e.g. http://localhost:8888)")
		}
		if !c.Tools.WebFetch.Enabled {
			return fmt.Errorf("tools.web_search requires tools.web_fetch.enabled=true (it reuses the web_fetch URL allowlist as its host gate)")
		}
		if c.Tools.WebSearch.MaxResults < 0 {
			return fmt.Errorf("tools.web_search.max_results must be >= 0 (got %d)", c.Tools.WebSearch.MaxResults)
		}
		if c.Tools.WebSearch.TimeoutSec <= 0 {
			return fmt.Errorf("tools.web_search.timeout_seconds must be > 0 (got %d)", c.Tools.WebSearch.TimeoutSec)
		}
	}
	if c.Tools.FileRead.MaxBytes < 0 {
		return fmt.Errorf("tools.file_read.max_bytes must be >= 0 (got %d)", c.Tools.FileRead.MaxBytes)
	}
	if c.Tools.FileList.MaxEntries < 0 {
		return fmt.Errorf("tools.file_list.max_entries must be >= 0 (got %d)", c.Tools.FileList.MaxEntries)
	}
	if c.Tools.Browser.Enabled {
		if strings.TrimSpace(c.Tools.Browser.Endpoint) == "" {
			return fmt.Errorf("tools.browser.endpoint must be set when enabled (e.g. ws://localhost:3000/)")
		}
		if !c.Tools.WebFetch.Enabled {
			return fmt.Errorf("tools.browser requires tools.web_fetch.enabled=true (browser_navigate reuses the web_fetch URL allowlist as its host gate)")
		}
		if c.Tools.Browser.IdleSec <= 0 {
			return fmt.Errorf("tools.browser.idle_seconds must be > 0 (got %d)", c.Tools.Browser.IdleSec)
		}
	}
	if c.Tools.Browser.SnapshotMaxChars < 0 {
		return fmt.Errorf("tools.browser.snapshot_max_chars must be >= 0 (got %d)", c.Tools.Browser.SnapshotMaxChars)
	}
	// Validate sandbox root if set.
	if c.Tools.SandboxRoot != "" {
		// Make absolute if not already.
		if !filepath.IsAbs(c.Tools.SandboxRoot) {
			abs, err := filepath.Abs(c.Tools.SandboxRoot)
			if err != nil {
				return fmt.Errorf("tools.sandbox_root: failed to get absolute path: %w", err)
			}
			c.Tools.SandboxRoot = abs
		}
		// Clean the path (removes trailing slashes, resolves . and .. components).
		cleaned := filepath.Clean(c.Tools.SandboxRoot)
		// Reject unsafe values: root directory or current directory.
		if cleaned == "/" || cleaned == "." {
			return fmt.Errorf("tools.sandbox_root must not be the root directory (\"/\") or current directory (\".\")")
		}
		// Check that the directory exists.
		info, err := os.Stat(cleaned)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("tools.sandbox_root: directory does not exist: %w", err)
			}
			return fmt.Errorf("tools.sandbox_root: failed to stat directory: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("tools.sandbox_root: not a directory: %w", err)
		}
		// Optionally, ensure the parent directory exists? Not required; we can create later.
		c.Tools.SandboxRoot = cleaned
	}
	return nil
}

func (c *Config) GetUser(name string) *UserConfig {
	for i := range c.Users {
		if strings.EqualFold(c.Users[i].Name, name) {
			return &c.Users[i]
		}
	}
	return nil
}

func (c *Config) ModelFor(user *UserConfig) string {
	if user != nil && user.Model != "" {
		return user.Model
	}
	return c.LLM.Model
}

// LLMEndpoint holds resolved LLM connection details for a user.
type LLMEndpoint struct {
	BaseURL string
	Model   string
	APIKey  string
}

// LLMEndpointFor resolves the full LLM endpoint for a user.
// Priority: user.LLMProfile → cfg.LLM.Default → legacy cfg.LLM.BaseURL/Model.
func (c *Config) LLMEndpointFor(user *UserConfig) LLMEndpoint {
	// Try user's profile override
	if user != nil && user.LLMProfile != "" {
		if p, ok := c.LLM.Profiles[user.LLMProfile]; ok {
			return LLMEndpoint{BaseURL: p.BaseURL, Model: p.Model, APIKey: p.APIKey}
		}
		log.Printf("[config] warning: user %q references unknown LLM profile %q, falling back", user.Name, user.LLMProfile)
	}
	// Try default profile
	if c.LLM.Default != "" {
		if p, ok := c.LLM.Profiles[c.LLM.Default]; ok {
			return LLMEndpoint{BaseURL: p.BaseURL, Model: p.Model, APIKey: p.APIKey}
		}
		log.Printf("[config] warning: default LLM profile %q not found, using legacy config", c.LLM.Default)
	}
	// Fall back to legacy single-endpoint config
	ep := LLMEndpoint{BaseURL: c.LLM.BaseURL, Model: c.ModelFor(user), APIKey: c.LLM.APIKey}
	if ep.BaseURL == "" {
		userName := "<nil>"
		if user != nil {
			userName = user.Name
		}
		log.Printf("[config] warning: LLM endpoint is empty for user %q — check llm.base_url in config", userName)
	}
	return ep
}

// LLMEndpointForProfile resolves an LLM endpoint by profile name directly.
// Used by subagents that specify a target LLM profile rather than a user.
func (c *Config) LLMEndpointForProfile(profileName string) LLMEndpoint {
	if profileName == "" {
		return c.LLMEndpointFor(nil) // use default
	}
	if p, ok := c.LLM.Profiles[profileName]; ok {
		return LLMEndpoint{BaseURL: p.BaseURL, Model: p.Model, APIKey: p.APIKey}
	}
	log.Printf("[config] warning: LLM profile %q not found", profileName)
	ep := LLMEndpoint{BaseURL: c.LLM.BaseURL, Model: c.LLM.Model, APIKey: c.LLM.APIKey}
	if ep.BaseURL == "" {
		log.Printf("[config] warning: LLM endpoint is empty for profile %q — check llm.base_url in config", profileName)
	}
	return ep
}


// FileReadConfig controls the built-in file_read tool.
type FileReadConfig struct {
	// MaxBytes is the maximum number of bytes to read from a file.
	// If 0, there is no limit.
	MaxBytes int `yaml:"max_bytes,omitempty"`
}

// FileListConfig controls the built-in file_list tool.
type FileListConfig struct {
	// MaxEntries is the maximum number of directory entries to return.
// If 0, there is no limit.
	MaxEntries int `yaml:"max_entries,omitempty"`
}

// ValidateProvider checks that the configured LLM provider is valid.
// If Provider is "claude_cli", it verifies the claude binary exists in $PATH.
// Empty string and "openai" are always valid (openai is the default).
func (c *LLMConfig) ValidateProvider() error {
	switch c.Provider {
	case "", "openai":
		return nil
	case "claude_cli":
		if _, err := exec.LookPath("claude"); err != nil {
			return fmt.Errorf("provider %q requires the claude binary in $PATH: %w", c.Provider, err)
		}
		return nil
	default:
		return fmt.Errorf("unknown llm provider %q: valid values are \"openai\", \"claude_cli\"", c.Provider)
	}
}
