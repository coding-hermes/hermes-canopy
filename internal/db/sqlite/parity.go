package sqlite

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// coreSchemaColumns is the wave-1 contract for the objects the SQLite runtime
// must expose. It intentionally excludes the private migration ledger, which
// is checked by Store.ApplyCoreSchema itself.
var coreSchemaColumns = map[string][]string{
	"trees":              {"id", "owner_id", "title", "description", "root_node_id", "metadata", "created_at", "edited_at", "deleted_at"},
	"nodes":              {"id", "tree_id", "parent_id", "author_id", "content", "content_format", "node_type", "sequence_num", "metadata", "created_at", "edited_at", "deleted_at", "content_hash"},
	"edges":              {"id", "tree_id", "source_id", "target_id", "edge_type", "sequence_num", "metadata", "created_at", "deleted_at"},
	"tree_snapshots":     {"id", "tree_id", "parent_hash", "hash", "node_count", "edge_count", "snapshot_data", "created_at"},
	"tree_events":        {"id", "tree_id", "snapshot_id", "event_type", "node_id", "edge_id", "payload", "sequence_num", "created_at"},
	"tree_event_seq":     {"tree_id", "next_seq"},
	"users":              {"id", "hermes_user_id", "email", "display_name", "avatar_url", "created_at", "updated_at", "last_seen_at", "is_active", "deleted_at"},
	"profiles":           {"id", "owner_id", "profile_type", "name", "display_name", "description", "config_json", "can_auto_respond", "context_window_size", "is_public", "created_at", "updated_at", "deleted_at"},
	"tree_members":       {"id", "tree_id", "user_id", "profile_id", "role", "is_visible", "auto_approved", "joined_at", "invited_by"},
	"profile_invites":    {"id", "tree_id", "profile_id", "invited_by", "invite_token", "status", "proposed_role", "created_at", "expires_at", "accepted_at", "declined_at"},
	"approvals":          {"id", "tree_id", "node_id", "owner_id", "requested_by", "status", "denied_reason", "auto_rule_id", "decided_by", "created_at", "decided_at", "expires_at"},
	"approval_rules":     {"id", "tree_id", "owner_id", "scope_type", "scope_target", "decision", "priority", "is_active", "created_at", "updated_at"},
	"approval_audit_log": {"id", "approval_id", "action", "actor", "previous_status", "new_status", "details", "created_at"},
	"profile_route":      {"id", "workspace_id", "profile_name", "display_name", "is_active", "model_preference", "profile_token_encrypted", "mapped_at", "last_used_at"},
}

// CheckCoreSchema checks the live SQLite catalog against the translated
// migrations. It fails closed on missing, extra, or column-drifted tables.
func CheckCoreSchema(ctx context.Context, store *Store) error {
	if store == nil || store.DB() == nil {
		return fmt.Errorf("sqlite: schema parity: nil store")
	}
	rows, err := store.DB().QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return fmt.Errorf("sqlite: schema parity: list tables: %w", err)
	}
	defer func() { _ = rows.Close() }()
	actualTables := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("sqlite: schema parity: scan table: %w", err)
		}
		if name != schemaMigrationsTable {
			actualTables[name] = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlite: schema parity: list tables: %w", err)
	}

	var problems []string
	for table, expected := range coreSchemaColumns {
		if !actualTables[table] {
			problems = append(problems, fmt.Sprintf("TABLE MISSING: %s", table))
			continue
		}
		got, err := tableColumns(ctx, store, table)
		if err != nil {
			return err
		}
		if !sameStrings(got, expected) {
			problems = append(problems, fmt.Sprintf("COLUMNS DRIFT: %s: got %v want %v", table, got, expected))
		}
		delete(actualTables, table)
	}
	for table := range actualTables {
		problems = append(problems, fmt.Sprintf("ORPHAN TABLE: %s", table))
	}
	if len(problems) != 0 {
		sort.Strings(problems)
		return fmt.Errorf("sqlite: schema parity failed: %s", strings.Join(problems, "; "))
	}
	return nil
}

func tableColumns(ctx context.Context, store *Store, table string) ([]string, error) {
	rows, err := store.DB().QueryContext(ctx, `PRAGMA table_info(`+quoteIdent(table)+`)`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: schema parity: table_info %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	var columns []string
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, fmt.Errorf("sqlite: schema parity: scan %s: %w", table, err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: schema parity: read %s: %w", table, err)
	}
	return columns, nil
}

func quoteIdent(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
