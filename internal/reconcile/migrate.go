package reconcile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrations embed.FS

func migrate(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS reconciler_schema_migrations(version TEXT PRIMARY KEY,checksum TEXT NOT NULL)"); err != nil {
		return err
	}
	files, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
	for _, file := range files {
		raw, err := migrations.ReadFile("migrations/" + file.Name())
		if err != nil {
			return err
		}
		raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
		hash := sha256.Sum256(raw)
		checksum := hex.EncodeToString(hash[:])
		var old string
		err = tx.QueryRow(ctx, "SELECT checksum FROM reconciler_schema_migrations WHERE version=$1", file.Name()).Scan(&old)
		if err == nil {
			if old != checksum {
				return fmt.Errorf("migration checksum changed")
			}
			continue
		}
		if err != pgx.ErrNoRows {
			return err
		}
		if _, err = tx.Exec(ctx, string(raw)); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO reconciler_schema_migrations(version,checksum) VALUES($1,$2)", file.Name(), checksum); err != nil {
			return err
		}
	}
	return nil
}
