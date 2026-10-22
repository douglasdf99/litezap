package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// The tables of the removed Cardápio (0016 and 0017).
var menuTables = []string{"menu_categories", "menu_items", "menu_options", "menu_item_images", "menu_collections",
	"menu_collection_items", "order_drafts"}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

// A fresh database migrates 0001 -> latest and ends without the removed Cardápio tables.
func TestMigrateFreshDropsMenu(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, tb := range menuTables {
		if tableExists(t, st.db, tb) {
			t.Errorf("table %s still exists", tb)
		}
	}
	if !tableExists(t, st.db, "orders") || !tableExists(t, st.db, "attendant_agents") {
		t.Fatal("native catalog orders or attendant tables are missing")
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = '0018_drop_menu.sql'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("0018 applied = %d, %v", n, err)
	}
}

// A database that already has 0016/0017 (with menu data and attendant profiles on the
// menu) migrates: the tables go, and the profiles fall back to catalog or manual.
func TestMigrateExistingMenuDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := (&Store{db: db}).migrateUpTo(ctx, "0017_menu_catalog.sql"); err != nil {
		t.Fatal(err)
	}
	if !tableExists(t, db, "order_drafts") {
		t.Fatal("0017 was not applied")
	}
	const now = "2026-10-22T00:00:00Z"
	stmts := []string{
		`INSERT INTO instances (id, name, state, token_hash, created_at, updated_at) VALUES
		  ('wa', 'a', 'connected', 'h1', ?1, ?1), ('ord', 'b', 'connected', 'h2', ?1, ?1), ('plain', 'c', 'connected', 'h3', ?1, ?1)`,
		`INSERT INTO menu_categories (id, instance_id, name, created_at, updated_at) VALUES ('c1', 'plain', 'Pizzas', ?1, ?1)`,
		`INSERT INTO menu_items (id, instance_id, category_id, name, source_ref, created_at, updated_at) VALUES
		  ('i1', 'wa', '', 'X', 'wa:123', ?1, ?1), ('i2', 'plain', 'c1', 'Y', '', ?1, ?1)`,
		`INSERT INTO menu_options (id, instance_id, item_id, name) VALUES ('o1', 'plain', 'i2', 'Tamanho')`,
		`INSERT INTO menu_item_images (id, instance_id, item_id, created_at) VALUES ('im1', 'plain', 'i2', ?1)`,
		`INSERT INTO menu_collections (id, instance_id, name, created_at, updated_at) VALUES ('col1', 'plain', 'Promo', ?1, ?1)`,
		`INSERT INTO menu_collection_items (collection_id, item_id, instance_id) VALUES ('col1', 'i2', 'plain')`,
		`INSERT INTO order_drafts (id, instance_id, created_at, updated_at) VALUES ('d1', 'plain', ?1, ?1)`,
		`INSERT INTO orders (instance_id, order_id, received_at) VALUES ('ord', 'o1', ?1)`,
		`INSERT INTO attendant_agents (id, instance_id, name, profile_json, created_at, updated_at) VALUES
		  ('a1', 'wa', 'A', '{"knowledge_source":"menu","tone":"x"}', ?1, ?1),
		  ('a2', 'ord', 'B', '{"knowledge_source":"menu"}', ?1, ?1),
		  ('a3', 'plain', 'C', '{"knowledge_source":" Menu "}', ?1, ?1),
		  ('a4', 'plain', 'D', '{"knowledge_source":"catalog"}', ?1, ?1),
		  ('a5', 'plain', 'E', '{}', ?1, ?1)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q, now); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, tb := range menuTables {
		if tableExists(t, st.db, tb) {
			t.Errorf("table %s still exists", tb)
		}
	}
	want := map[string]string{"a1": "catalog", "a2": "catalog", "a3": "manual", "a4": "catalog", "a5": ""}
	for id, w := range want {
		var src sql.NullString
		if err := st.db.QueryRow(`SELECT json_extract(profile_json, '$.knowledge_source') FROM attendant_agents WHERE id = ?`, id).Scan(&src); err != nil {
			t.Fatal(err)
		}
		if src.String != w {
			t.Errorf("%s knowledge_source = %q, want %q", id, src.String, w)
		}
	}
	var tone string
	if err := st.db.QueryRow(`SELECT json_extract(profile_json, '$.tone') FROM attendant_agents WHERE id = 'a1'`).Scan(&tone); err != nil || tone != "x" {
		t.Fatalf("other profile fields changed: %q, %v", tone, err)
	}
	if !tableExists(t, st.db, "orders") {
		t.Fatal("orders (native catalog) was dropped")
	}
}
