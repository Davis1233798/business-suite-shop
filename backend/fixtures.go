package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func seedFixtures(tx *sql.Tx, org string) error {
	b, e := migrations.ReadFile("migrations/shop_fixtures.json")
	if e != nil {
		return e
	}
	var fixtures map[string][]map[string]any
	if e = json.Unmarshal(b, &fixtures); e != nil {
		return e
	}
	tables := []string{}
	for t := range fixtures {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	for _, table := range tables {
		for _, row := range fixtures[table] {
			keys := []string{}
			for k := range row {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			args := []any{}
			marks := []string{}
			for _, k := range keys {
				v := row[k]
				if s, yes := v.(string); yes {
					v = strings.ReplaceAll(s, "org_go_fixture", org)
				}
				args = append(args, v)
				marks = append(marks, "?")
			}
			q := fmt.Sprintf("INSERT OR IGNORE INTO %s (%s) VALUES (%s)", table, strings.Join(keys, ","), strings.Join(marks, ","))
			if _, e = tx.Exec(q, args...); e != nil {
				return fmt.Errorf("fixture %s: %w", table, e)
			}
		}
	}
	return nil
}
