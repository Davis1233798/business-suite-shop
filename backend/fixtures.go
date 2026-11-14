package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
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
	fixtureOrder := map[string]int{
		"shop_categories":  0,
		"shop_products":    1,
		"shop_variants":    2,
		"shop_promotions":  3,
		"shop_orders":      4,
		"shop_order_items": 5,
	}
	sort.Slice(tables, func(i, j int) bool {
		left, leftOK := fixtureOrder[tables[i]]
		right, rightOK := fixtureOrder[tables[j]]
		if leftOK && rightOK {
			return left < right
		}
		if leftOK != rightOK {
			return leftOK
		}
		return tables[i] < tables[j]
	})
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
				if n, yes := v.(float64); yes && n >= math.MinInt64 && n <= math.MaxInt64 && math.Trunc(n) == n {
					v = int64(n)
				}
				args = append(args, v)
				marks = append(marks, "?")
			}
			q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT DO NOTHING", table, strings.Join(keys, ","), strings.Join(marks, ","))
			if _, e = tx.Exec(q, args...); e != nil {
				return fmt.Errorf("fixture %s: %w", table, e)
			}
		}
	}
	return nil
}
