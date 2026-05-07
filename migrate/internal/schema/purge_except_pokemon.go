package schema

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PurgeApplicationDataExceptPokemon borra filas de torneos y respaldos en una transacción.
// No toca la tabla pokemon ni tool_redis_pg_migrations.
//
// Si dryRun es true, no ejecuta SQL (el caller debe mostrar el resumen previo).
func PurgeApplicationDataExceptPokemon(ctx context.Context, pool *pgxpool.Pool, dryRun bool) error {
	if dryRun {
		return nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Una sola sentencia + CASCADE respeta FKs extra (p. ej. tournament_redis_raw → tournaments) y tablas aplicadas fuera del embed.
	const q = `
TRUNCATE TABLE
  tournament_standings,
  tournament_phases,
  tournament_redis_raw,
  tournaments_list_json_snapshots,
  tournaments,
  organizers
RESTART IDENTITY CASCADE;`
	if _, err := tx.Exec(ctx, q); err != nil {
		return fmt.Errorf("truncate datos aplicación: %w", err)
	}
	return tx.Commit(ctx)
}
