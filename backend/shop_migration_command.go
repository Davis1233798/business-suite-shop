package main

import (
	"bytes"

	"encoding/json"
	"errors"
	"io"
	"strings"
)

func runShopMigration(args []string, input io.Reader, output io.Writer, dsn string) error {
	if len(args) != 2 || args[0] != "--apply" || strings.TrimSpace(args[1]) == "" {
		return errors.New("usage: --migrate-shop-sqlite --apply SNAPSHOT")
	}
	const manifestLimit = 4 << 20
	data, err := io.ReadAll(io.LimitReader(input, manifestLimit+1))
	if err != nil || len(data) > manifestLimit {
		return errors.New("could not read migration manifest within 4 MiB")
	}
	var manifest shopDataMigrationManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || decoder.Decode(new(any)) != io.EOF || len(manifest.TenantMappings) == 0 {
		return errors.New("invalid migration manifest")
	}
	if strings.TrimSpace(dsn) == "" {
		return errors.New("SUITE_DATABASE_URL is required")
	}
	target, err := connectShopPostgres(dsn)
	if err != nil {
		return errors.New("could not initialize PostgreSQL connection")
	}
	defer target.Close()
	result, err := migrateShopSQLiteData(args[1], target, manifest)
	if err != nil {
		return errors.New("Shop migration failed; verify the snapshot, mappings and destination")
	}
	return json.NewEncoder(output).Encode(result)
}
