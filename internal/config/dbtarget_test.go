package config

import (
	"strings"
	"testing"
)

func TestDBTargetOf(t *testing.T) {
	tests := []struct {
		name       string
		config     *Config
		env        map[string]string
		wantHost   string
		wantPort   string
		wantDB     string
		wantSource DBTargetSource
	}{
		{
			name:       "nothing set uses built-in default",
			config:     Default(),
			wantHost:   "localhost",
			wantPort:   "5432",
			wantDB:     "canopy",
			wantSource: DBTargetSourceDefault,
		},
		{
			name:       "DB_PORT selects environment source",
			config:     func() *Config { c := Default(); c.DBPort = 5437; return c }(),
			env:        map[string]string{"DB_PORT": "5437"},
			wantHost:   "localhost",
			wantPort:   "5437",
			wantDB:     "canopy",
			wantSource: DBTargetSourceEnv,
		},
		{
			name: "CANOPY_DB_URL selects environment source",
			config: &Config{
				DBHost: "dbhost",
				DBPort: 5433,
				DBName: "mydb",
			},
			env:        map[string]string{"CANOPY_DB_URL": "postgres://u:p@dbhost:5433/mydb"},
			wantHost:   "dbhost",
			wantPort:   "5433",
			wantDB:     "mydb",
			wantSource: DBTargetSourceEnv,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(name string) string { return tt.env[name] }
			got := DBTargetOf(tt.config, getenv)
			if got.Host != tt.wantHost || got.Port != tt.wantPort || got.Database != tt.wantDB || got.Source != tt.wantSource {
				t.Fatalf("DBTargetOf() = %#v, want host=%q port=%q database=%q source=%q", got, tt.wantHost, tt.wantPort, tt.wantDB, tt.wantSource)
			}
		})
	}
}

func TestDBTargetDescribeNeverIncludesPassword(t *testing.T) {
	t.Setenv("CANOPY_DB_URL", "postgres://u:secret-pass@dbhost:5433/mydb")
	c := FromEnv()
	got := DBTargetOf(c, func(name string) string {
		if name == "CANOPY_DB_URL" {
			return "postgres://u:secret-pass@dbhost:5433/mydb"
		}
		return ""
	}).Describe()
	if strings.Contains(got, "secret-pass") {
		t.Fatalf("DBTarget.Describe() leaked the credential: %q", got)
	}
}

func TestDBTargetDescribeDefaultIncludesDevelopmentPortWarning(t *testing.T) {
	got := DBTargetOf(Default(), func(string) string { return "" }).Describe()
	if !strings.Contains(got, "port=5432") {
		t.Fatalf("default description %q does not contain port=5432", got)
	}
	if !strings.Contains(got, DBPortDefaultWarning) {
		t.Fatalf("default description %q does not contain the :5437 warning", got)
	}
}

func TestDBTargetDescribeEnvironmentOmitsDevelopmentPortWarning(t *testing.T) {
	c := Default()
	c.DBPort = 5437
	got := DBTargetOf(c, func(name string) string {
		if name == "DB_PORT" {
			return "5437"
		}
		return ""
	}).Describe()
	if !strings.Contains(got, "port=5437") {
		t.Fatalf("environment description %q does not contain port=5437", got)
	}
	if strings.Contains(got, DBPortDefaultWarning) {
		t.Fatalf("environment description unexpectedly contains the :5437 warning: %q", got)
	}
}
