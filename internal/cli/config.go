package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// appConfig is the optional on-disk configuration. Flags always win over
// environment variables, which win over this file.
type appConfig struct {
	DB        string     `json:"db"`
	Workspace string     `json:"workspace"`
	Web       webConf    `json:"web"`
	Context   contextCfg `json:"context"`
}

type webConf struct {
	Addr string `json:"addr"`
	Open bool   `json:"open"`
}

type contextCfg struct {
	TitleWeight      float64 `json:"title_weight"`
	ContentWeight    float64 `json:"content_weight"`
	TagWeight        float64 `json:"tag_weight"`
	CategoryWeight   float64 `json:"category_weight"`
	ImportanceWeight float64 `json:"importance_weight"`
	RecencyWeight    float64 `json:"recency_weight"`
	FeedbackWeight   float64 `json:"feedback_weight"`
}

// configPath returns the config file location.
func configPath() string {
	if p := os.Getenv("AIMEMO_CONFIG"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "aimemo", "config.json")
}

// loadConfig reads the config file and ignores any error: a broken config
// should never stop the CLI from working.
func loadConfig() appConfig {
	var cfg appConfig
	p := configPath()
	if p == "" {
		return cfg
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(b, &cfg)
	return cfg
}

// envBool parses common truthy spellings.
func envBool(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// defaultWorkspace resolves the workspace from env or config.
func defaultWorkspace(cfg appConfig) string {
	if v := os.Getenv("AIMEMO_WORKSPACE"); v != "" {
		return v
	}
	return cfg.Workspace
}

// effectiveDB resolves the database path with flag handled by the caller.
func effectiveDB(cfg appConfig) string {
	if v := os.Getenv("AIMEMO_DB"); v != "" {
		return v
	}
	if cfg.DB != "" {
		return cfg.DB
	}
	return defaultDBPath()
}

func intEnv(name string, fallback int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
