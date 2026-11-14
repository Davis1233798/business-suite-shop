package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestShopSQLiteImportRegistersDriverInFreshProcess(t *testing.T) {
	if os.Getenv("SHOP_TEST_MIGRATION_CHILD") == "1" {
		err := runShopMigration([]string{"--apply", filepath.Join(t.TempDir(), "missing.sqlite")}, strings.NewReader(`{"TenantMappings":{"legacy":"target"}}`), os.Stdout, "postgres://localhost/shop")
		if err == nil || !strings.HasPrefix(err.Error(), "Shop migration failed") {
			t.Fatalf("expected source validation failure, got %v", err)
		}
		return
	}
	child := exec.Command(os.Args[0], "-test.run=^TestShopSQLiteImportRegistersDriverInFreshProcess$")
	child.Env = append(os.Environ(), "SHOP_TEST_MIGRATION_CHILD=1")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("fresh migration process: %v\n%s", err, output)
	}
}
