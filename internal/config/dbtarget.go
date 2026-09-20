package config

import "fmt"

// DBTargetSource says where the resolved PostgreSQL connection parameters came from.
type DBTargetSource string

const (
	DBTargetSourceEnv     DBTargetSource = "env"
	DBTargetSourceDefault DBTargetSource = "default"

	// DBPortDefaultWarning explains why the built-in port differs from the
	// documented development stack port.
	DBPortDefaultWarning = "WARNING: port 5432 is NOT the documented development port: the compose stack, Makefile and every project script use :5437 (docker-compose.yml maps 5437:5432). Set DB_PORT=5437 or CANOPY_DB_URL to select that instance."
)

// DBTarget is the resolved PostgreSQL target of a direct-DB CLI command.
type DBTarget struct {
	Host, Port, Database string
	Source               DBTargetSource
}

// DBTargetOf resolves the CLI-visible target from an already-populated Config
// plus the raw environment, without mutating the Config or connecting. Any
// non-empty database environment variable marks the target as environment
// sourced, even when the populated Config retained a default for a malformed
// individual value.
func DBTargetOf(c *Config, getenv func(string) string) DBTarget {
	source := DBTargetSourceDefault
	for _, name := range []string{"CANOPY_DB_URL", "DB_HOST", "DB_PORT", "DB_NAME", "DB_USER"} {
		if getenv(name) != "" {
			source = DBTargetSourceEnv
			break
		}
	}

	target := DBTarget{
		Host:     c.DBHost,
		Port:     fmt.Sprintf("%d", c.DBPort),
		Database: c.DBName,
		Source:   source,
	}
	return target
}

// Describe renders the resolved target without including credentials.
func (t DBTarget) Describe() string {
	if t.Source == DBTargetSourceDefault {
		description := fmt.Sprintf("DB target: postgres host=%s port=%s database=%s (built-in default; no DB_PORT / CANOPY_DB_URL set)", t.Host, t.Port, t.Database)
		if t.Port == "5432" {
			return description + "\n" + DBPortDefaultWarning
		}
		return description
	}
	return fmt.Sprintf("DB target: postgres host=%s port=%s database=%s (from DB_* / CANOPY_DB_URL)", t.Host, t.Port, t.Database)
}
