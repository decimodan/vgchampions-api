package schema

import (
	"context"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/decimodan/vgchampions-api/migrate/sqlmigrations"
)

const metaTable = `CREATE TABLE IF NOT EXISTS tool_redis_pg_migrations (
    version TEXT PRIMARY KEY NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`

// ApplyPending runs embedded postgres/*.sql files in lexical order, once each.
func ApplyPending(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, metaTable); err != nil {
		return fmt.Errorf("bootstrap migrations table: %w", err)
	}

	dir := "postgres"
	entries, err := sqlmigrations.Postgres.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read embedded postgres migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		var exists int64
		q := `SELECT COUNT(*) FROM tool_redis_pg_migrations WHERE version = $1`
		if err := pool.QueryRow(ctx, q, name).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if exists > 0 {
			continue
		}

		bytes, err := sqlmigrations.Postgres.ReadFile(path.Join(dir, name))
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", name, err)
		}
		sql := strings.TrimSpace(string(bytes))
		if sql == "" {
			continue
		}

		if _, err := pool.Exec(ctx, sql); err != nil {
			return fmt.Errorf("apply migration %s: %w\n---\n%s\n---", name, err, summarize(sql))
		}
		tq := `INSERT INTO tool_redis_pg_migrations (version) VALUES ($1)`
		if _, err := pool.Exec(ctx, tq, name); err != nil {
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		fmt.Fprintf(os.Stderr, "[redis-pg-migrate] aplicada migración embebida: %s\n", name)
	}
	return nil
}

func summarize(s string) string {
	const max = 400
	rs := strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(rs) <= max {
		return rs
	}
	return rs[:max] + "..."
}
