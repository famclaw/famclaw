package config

import (
	"fmt"
	"log"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server        ServerConfig        `yaml:"server"`
	LLM           LLMConfig           `yaml:"llm"`
	Users         []UserConfig        `yaml:"users"`
	Gateways      GatewaysConfig      `yaml:"gateways"`
	Policies      PoliciesConfig      `yaml:"policies"`
	Approval      ApprovalConfig      `yaml:"approval"`
	Skills        SkillsConfig        `yaml:"skills"`
	Notifications NotificationsConfig `yaml:"notifications"`
	Storage       StorageConfig       `yaml:"storage"`
	SecCheck      SecCheckConfig      `yaml:"seccheck"`
}

type GatewaysConfig struct {
	Telegram TelegramConfig `yaml:"telegram"`
	WhatsApp WhatsAppConfig `yaml:"whatsapp"`
	Discord  DiscordGWConfig `yaml:"discord"`
}

type TelegramConfig struct {
	Enabled bool   `yaml:"enabled"`
	Token   string `yaml:"token"`
}

type WhatsAppConfig struct {
	Enabled  bool   `yaml:"enabled"`
	DBPath   string `yaml:"db_path"`
}

type DiscordGWConfig struct {
	Enabled bool   `yaml:"enabled"`
	Token   string `yaml:"token"`
}

type ServerConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Secret   string `yaml:"secret"`
	MDNSName string `yaml:"mdns_name"`
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
	Provider          string  `yaml:"provider"`
	BaseURL           string  `yaml:"base_url"`
	Model             string  `yaml:"model"`
	APIKey            string  `yaml:"api_key,omitempty"`
	// Named profiles (takes precedence when set)
	Default           string                 `yaml:"default,omitempty"`
	Profiles          map[string]LLMProfile  `yaml:"profiles,omitempty"`
	// Common settings
	SystemPrompt      string  `yaml:"system_prompt"`
	MaxContextTokens  int     `yaml:"max_context_tokens"`
	MaxResponseTokens int     `yaml:"max_response_tokens"`
	Temperature       float64 `yaml:"temperature"`
}

type UserConfig struct {
	Name        string `yaml:"name"`
	DisplayName string `yaml:"display_name"`
	Role        string `yaml:"role"`   // parent | child
	AgeGroup    string `yaml:"age_group"` // under_8 | age_8_12 | age_13_17
	PIN         string `yaml:"pin"`
	Color       string `yaml:"color"`
	Model      string `yaml:"model"`       // optional per-user model override (legacy)
	LLMProfile string `yaml:"llm_profile,omitempty"` // named LLM profile override
}

type PoliciesConfig struct {
	Dir     string `yaml:"dir"`
	DataDir string `yaml:"data_dir"`
}

type ApprovalConfig struct {
	ExpiryHours int `yaml:"expiry_hours"`
}

type SkillsConfig struct {
	Dir            string                          `yaml:"dir"`
	AutoSecCheck   bool                            `yaml:"auto_seccheck"`
	BlockOnFail    bool                            `yaml:"block_on_fail"`
	OpenClawCompat bool                            `yaml:"openclaw_compat"`
	MCPServers     map[string]MCPServerConfig      `yaml:"mcp_servers,omitempty"`
	Credentials    map[string]map[string]string    `yaml:"credentials,omitempty"` // per-skill env vars
}

type StorageConfig struct {
	DBPath string `yaml:"db_path"`
}

type SecCheckConfig struct {
	Sandbox string `yaml:"sandbox"`
	Timeout string `yaml:"timeout"`
	OSVAPI  string `yaml:"osv_api"`
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
	applyDefaults(&cfg)
	return &cfg, nil
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
	if c.Policies.Dir == "" {
		c.Policies.Dir = "./policies/family"
	}
	if c.Policies.DataDir == "" {
		c.Policies.DataDir = "./policies/data"
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
	if c.SecCheck.Sandbox == "" {
		c.SecCheck.Sandbox = "auto"
	}
	if c.SecCheck.Timeout == "" {
		c.SecCheck.Timeout = "5m"
	}
	if c.SecCheck.OSVAPI == "" {
		c.SecCheck.OSVAPI = "https://api.osv.dev/v1"
	}
	if c.Notifications.Email.SMTPPort == 0 {
		c.Notifications.Email.SMTPPort = 587
	}
	if c.Notifications.Ntfy.URL == "" {
		c.Notifications.Ntfy.URL = "http://localhost:2586"
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

// LLMClientFor resolves the LLM endpoint for a user.
// Priority: user.LLMProfile → cfg.LLM.Default → legacy cfg.LLM.BaseURL/Model.
// Logs a warning if a named profile is not found.
func (c *Config) LLMClientFor(user *UserConfig) (baseURL, model, apiKey string) {
	// Try user's profile override
	if user != nil && user.LLMProfile != "" {
		if p, ok := c.LLM.Profiles[user.LLMProfile]; ok {
			return p.BaseURL, p.Model, p.APIKey
		}
		log.Printf("[config] warning: user %q references unknown LLM profile %q, falling back", user.Name, user.LLMProfile)
	}
	// Try default profile
	if c.LLM.Default != "" {
		if p, ok := c.LLM.Profiles[c.LLM.Default]; ok {
			return p.BaseURL, p.Model, p.APIKey
		}
		log.Printf("[config] warning: default LLM profile %q not found, using legacy config", c.LLM.Default)
	}
	// Fall back to legacy single-endpoint config
	return c.LLM.BaseURL, c.ModelFor(user), c.LLM.APIKey
}
