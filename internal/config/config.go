// Package config provides configuration types and loading for canopyd.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds all configuration for the canopyd server.
type Config struct {
	// Database
	DBHost     string
	DBPort     int
	DBUser     string
	DBPassword string
	DBName     string
	DBSSLMode  string

	// HTTP
	HTTPAddr string

	// Logging
	LogLevel string

	// JWT
	JWTSecret string

	// Metrics
	MetricsEnabled bool

	// Context compiler
	ContextMaxAncestors  int // CONTEXT_MAX_ANCESTORS, default 50
	ContextMaxRefs       int // CONTEXT_MAX_REFS, default 5 (soft) — hard cap is 2x this
	ContextDefaultBudget int // CONTEXT_DEFAULT_BUDGET, default 8000 tokens

	// Plugin sandbox (GAP-002 §4.1)
	PluginMaxSize int // PLUGIN_MAX_SIZE, default 1048576 (1MB)
}

// DSN returns the PostgreSQL connection string.
func (c *Config) DSN() string {
	sslmode := c.DBSSLMode
	if sslmode == "" {
		sslmode = "disable"
	}
	return "postgres://" + c.DBUser + ":" + c.DBPassword +
		"@" + c.DBHost + ":" + strconv.Itoa(c.DBPort) +
		"/" + c.DBName + "?sslmode=" + sslmode
}

// Default returns a Config with sensible development defaults.
func Default() *Config {
	return &Config{
		DBHost:               "localhost",
		DBPort:               5432,
		DBUser:               "canopy",
		DBPassword:           "canopy",
		DBName:               "canopy",
		DBSSLMode:            "disable",
		HTTPAddr:             ":8080",
		LogLevel:             "info",
		JWTSecret:            "dev-secret-change-me",
		MetricsEnabled:       false,
		ContextMaxAncestors:  50,
		ContextMaxRefs:       5,
		ContextDefaultBudget: 8000,
		PluginMaxSize:        1048576,
	}
}

// FromEnv loads configuration from environment variables,
// falling back to Default() values when unset.
func FromEnv() *Config {
	c := Default()
	if v := os.Getenv("DB_HOST"); v != "" {
		c.DBHost = v
	}
	if v := os.Getenv("DB_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			c.DBPort = p
		}
	}
	if v := os.Getenv("DB_USER"); v != "" {
		c.DBUser = v
	}
	if v := os.Getenv("DB_PASSWORD"); v != "" {
		c.DBPassword = v
	}
	if v := os.Getenv("DB_NAME"); v != "" {
		c.DBName = v
	}
	if v := os.Getenv("DB_SSLMODE"); v != "" {
		c.DBSSLMode = v
	}
	if v := os.Getenv("HTTP_ADDR"); v != "" {
		c.HTTPAddr = v
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		c.LogLevel = v
	}
	if v := os.Getenv("JWT_SECRET"); v != "" {
		c.JWTSecret = v
	}
	if v := os.Getenv("METRICS_ENABLED"); v == "true" || v == "1" {
		c.MetricsEnabled = true
	}
	if v := os.Getenv("CONTEXT_MAX_ANCESTORS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.ContextMaxAncestors = n
		}
	}
	if v := os.Getenv("CONTEXT_MAX_REFS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.ContextMaxRefs = n
		}
	}
	if v := os.Getenv("CONTEXT_DEFAULT_BUDGET"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.ContextDefaultBudget = n
		}
	}
	if v := os.Getenv("PLUGIN_MAX_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			// Negative values are rejected at startup (Validate); zero falls
			// back to the 1MB default.
			if n > 0 {
				c.PluginMaxSize = n
			}
		}
	}

	// CANOPY_DB_URL overrides all individual DB_* fields when set.
	// This matches the documented env var in SELF_HOST.md and avoids
	// silent misconfiguration when users set CANOPY_DB_URL expecting
	// it to take effect.
	if v := os.Getenv("CANOPY_DB_URL"); v != "" {
		dsn := v
		// postgres://user:pass@host:port/dbname?sslmode=...
		if len(dsn) > 11 && dsn[:11] == "postgres://" {
			rest := dsn[11:]
			// user:password@host:port/dbname?params
			if atIdx := strings.Index(rest, "@"); atIdx > 0 {
				userInfo := rest[:atIdx]
				hostPart := rest[atIdx+1:]
				if colonIdx := strings.Index(userInfo, ":"); colonIdx > 0 {
					c.DBUser = userInfo[:colonIdx]
					c.DBPassword = userInfo[colonIdx+1:]
				} else {
					c.DBUser = userInfo
				}
				// host:port/dbname?params
				if slashIdx := strings.Index(hostPart, "/"); slashIdx > 0 {
					hostPort := hostPart[:slashIdx]
					dbPart := hostPart[slashIdx+1:]
					if colonIdx := strings.Index(hostPort, ":"); colonIdx > 0 {
						c.DBHost = hostPort[:colonIdx]
						if p, err := strconv.Atoi(hostPort[colonIdx+1:]); err == nil {
							c.DBPort = p
						}
					} else {
						c.DBHost = hostPort
					}
					// dbname?params
					if qIdx := strings.Index(dbPart, "?"); qIdx > 0 {
						c.DBName = dbPart[:qIdx]
						params := dbPart[qIdx+1:]
						for _, pair := range strings.Split(params, "&") {
							if kv := strings.SplitN(pair, "=", 2); len(kv) == 2 && kv[0] == "sslmode" {
								c.DBSSLMode = kv[1]
							}
						}
					} else {
						c.DBName = dbPart
					}
				}
			}
		}
	}
	return c
}

// Validate checks configuration invariants that must fail fast at startup.
// A negative PLUGIN_MAX_SIZE is a hard error (GAP-002 §4.1); zero falls back
// to the 1MB default in FromEnv.
func (c *Config) Validate() error {
	if c.PluginMaxSize < 0 {
		return fmt.Errorf("config: PLUGIN_MAX_SIZE must not be negative (got %d)", c.PluginMaxSize)
	}
	return nil
}
