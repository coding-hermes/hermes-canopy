// Package config provides configuration types and loading for canopyd.
package config

import (
	"os"
	"strconv"
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
		DBHost:     "localhost",
		DBPort:     5432,
		DBUser:     "canopy",
		DBPassword: "canopy",
		DBName:     "canopy",
		DBSSLMode:  "disable",
		HTTPAddr:   ":8080",
		LogLevel:   "info",
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
	return c
}
