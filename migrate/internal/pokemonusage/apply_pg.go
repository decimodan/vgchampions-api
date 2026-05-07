package pokemonusage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ApplyUsageCountsToPokemon pone usage_count en 0 para todos los registros y luego asigna
// los totales por slug encontrados en Redis (solo filas que existan en pokemon).
func ApplyUsageCountsToPokemon(ctx context.Context, pool *pgxpool.Pool, counts map[string]int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `UPDATE pokemon SET usage_count = 0`); err != nil {
		return fmt.Errorf("reset usage_count: %w", err)
	}
	for slug, n := range counts {
		if n <= 0 {
			continue
		}
		tag, err := tx.Exec(ctx, `UPDATE pokemon SET usage_count = $1 WHERE slug = $2`, n, slug)
		if err != nil {
			return fmt.Errorf("update %s: %w", slug, err)
		}
		if tag.RowsAffected() == 0 {
			// slug en Redis pero sin fila en catálogo: se ignora (no INSERT parcial sin tipos/etc.)
			continue
		}
	}

	return tx.Commit(ctx)
}
