package main

import (
	"context"
	"log"

	"github.com/decimodan/vgchampions-api/migrate/internal/config"
	"github.com/decimodan/vgchampions-api/migrate/internal/schema"

	"github.com/jackc/pgx/v5/pgxpool"
)

func runPurgePostgresExceptPokemon(ctx context.Context, cfg *config.Config) {
	msg := `dry-run: se ejecutaría TRUNCATE ... CASCADE sobre tournament_standings, tournament_phases, tournament_redis_raw, tournaments_list_json_snapshots, tournaments, organizers; no se borra pokemon ni tool_redis_pg_migrations`
	if cfg.DryRun {
		log.Println(msg)
		return
	}

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("postgres: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("postgres ping: %v", err)
	}

	if !cfg.SkipMigrations {
		if err := schema.ApplyPending(ctx, pool); err != nil {
			log.Fatalf("migraciones: %v", err)
		}
	}

	if err := schema.PurgeApplicationDataExceptPokemon(ctx, pool, false); err != nil {
		log.Fatalf("purge Postgres: %v", err)
	}
	log.Println("Postgres truncado (catálogo pokemon intacto, historial migraciones intacto).")
}
