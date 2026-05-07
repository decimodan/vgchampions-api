package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/decimodan/vgchampions-api/migrate/internal/config"
	"github.com/decimodan/vgchampions-api/migrate/internal/d1embed"
	"github.com/decimodan/vgchampions-api/migrate/internal/dotenv"
	"github.com/decimodan/vgchampions-api/migrate/internal/importer"
	"github.com/decimodan/vgchampions-api/migrate/internal/pokemonusage"
	"github.com/decimodan/vgchampions-api/migrate/internal/redisx"
	"github.com/decimodan/vgchampions-api/migrate/internal/schema"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// Version sustituible en release: go build -ldflags "-X main.Version=v1.0.0"
var Version = "dev"

func main() {
	log.SetPrefix("[redis-pg-migrate] ")
	log.SetFlags(0)

	if err := dotenv.LoadFromFile(filepath.Join(wdOrDot(), ".env")); err != nil {
		log.Fatalf("cargar .env: %v", err)
	}

	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		log.Fatal(err)
	}
	if cfg.ShowVersion {
		fmt.Println(Version)
		return
	}

	ctx := context.Background()

	if cfg.PurgePostgresExceptPokemon {
		runPurgePostgresExceptPokemon(ctx, cfg)
		return
	}

	if cfg.SyncTournamentsFromAPI {
		if err := runSyncFromAPI(ctx, cfg); err != nil {
			log.Fatal(err)
		}
		return
	}

	embedLine, err := d1embed.DryRunCounts(d1embed.SnapshotData)
	if err != nil {
		log.Fatalf("snapshot embebido: %v", err)
	}

	rOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		log.Fatalf("REDIS_URL: %v", err)
	}
	rdb := redis.NewClient(rOpts)
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis ping: %v", err)
	}

	ids, err := redisx.CollectIDs(ctx, rdb, cfg.RedisPrefix)
	if err != nil {
		log.Fatalf("redis scan: %v", err)
	}
	log.Printf("%d torneos con clave :details en Redis (prefix=%q)", len(ids), cfg.RedisPrefix)

	payloads := make([]*importer.RedisPayload, 0, len(ids))
	showProg := shouldShowProg(cfg.NoProgress)
	for idx, id := range ids {
		reportProg(showProg, idx+1, len(ids), "Leyendo Redis")
		p, err := redisx.FetchPayload(ctx, rdb, cfg.RedisPrefix, id)
		if err != nil {
			clearProg(showProg)
			log.Fatalf("%s get: %v", id, err)
		}
		if p == nil {
			continue
		}
		rp, err := importer.ParseRedisPayload(p.DetailsRaw, p.ListRaw)
		if err != nil {
			clearProg(showProg)
			log.Printf("omitido %s: %v", id, err)
			continue
		}
		payloads = append(payloads, rp)
	}
	reportProg(showProg, len(ids), len(ids), "Leyendo Redis")
	finishProg(showProg)

	var listRows []importer.TournamentListJSONRow
	var listRaw []byte
	if cfg.InputJSON != "" {
		var err error
		listRaw, err = os.ReadFile(cfg.InputJSON)
		if err != nil {
			log.Fatalf("leer --input: %v", err)
		}
		listRows, err = importer.ParseTournamentsList(listRaw)
		if err != nil {
			log.Fatalf("parse --input: %v", err)
		}
	}

	snap, err := d1embed.ParseSnapshot(d1embed.SnapshotData)
	if err != nil {
		log.Fatalf("parse snapshot interno: %v", err)
	}
	hasEmbed := snap != nil && !snap.IsEmpty()

	if cfg.DryRun {
		log.Printf("(1) Snapshot D1 embebido en este binario: %s", embedLine)
		log.Printf("(2→3) Omitido en dry-run: migraciones Postgres y volcado embebido")
		log.Printf("(4) Redis: payloads válidos=%d", len(payloads))
		log.Printf("(5→6) Omitido en dry-run: tablas de respaldo crudo")
		log.Printf("(7) Omitido en dry-run: import tabular final (lista=%d filas)", len(listRows))
		if cfg.UpdatePokemonDecklistUsage {
			counts, summary := pokemonusage.AggregateDecklistCounts(payloads, cfg.UsageFilterGame, cfg.UsageFilterFormat, cfg.UsageMinPlayersExclusive)
			log.Printf("(--update-pokemon-usage-from-decklists dry-run) game=%q format=%q players>%d",
				cfg.UsageFilterGame, cfg.UsageFilterFormat, cfg.UsageMinPlayersExclusive)
			log.Printf("  torneos_elegibles=%d filas_standings=%d apariciones_en_decklists=%d slugs_distintos=%d",
				summary.TournamentsEligible, summary.StandingsScanned, summary.PokemonAppearances, summary.UniqueSlugs)
			logPokemonUsageLines("decklist Redis (dry-run)", pokemonusage.SortedUsageDescending(counts))
		}
		return
	}

	if !hasEmbed && len(payloads) == 0 && len(listRows) == 0 && !cfg.UpdatePokemonDecklistUsage {
		log.Println("snapshot embebido vacío y sin datos Redis ni --input: nada que hacer. Exporta D1 con `go run ./cmd/d1-export -sqlite ...` y recompila, o rellena Redis/--input.")
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

	// (2) Migraciones Postgres
	if !cfg.SkipMigrations {
		if err := schema.ApplyPending(ctx, pool); err != nil {
			log.Fatalf("migraciones embebidas: %v", err)
		}
	}

	if cfg.UpdatePokemonDecklistUsage {
		counts, summary := pokemonusage.AggregateDecklistCounts(payloads, cfg.UsageFilterGame, cfg.UsageFilterFormat, cfg.UsageMinPlayersExclusive)
		log.Printf("actualizando pokemon.usage_count desde decklists Redis (game=%q format=%q players>%d)",
			cfg.UsageFilterGame, cfg.UsageFilterFormat, cfg.UsageMinPlayersExclusive)
		log.Printf("  torneos_elegibles=%d filas_standings=%d apariciones_en_decklists=%d slugs_distintos=%d",
			summary.TournamentsEligible, summary.StandingsScanned, summary.PokemonAppearances, summary.UniqueSlugs)
		if err := pokemonusage.ApplyUsageCountsToPokemon(ctx, pool, counts); err != nil {
			log.Fatalf("usage_count decklists→Postgres: %v", err)
		}
		logPokemonUsageLines("decklist Redis (persistido en pokemon.usage_count)", pokemonusage.SortedUsageDescending(counts))
		log.Println("terminado (--update-pokemon-usage-from-decklists).")
		return
	}

	// (3) Volcar snapshot D1 embebido al compilar (no ejecuta Wrangler ni abre SQLite aquí)
	if !cfg.SkipEmbeddedD1Snapshot && hasEmbed {
		if err := d1embed.ApplyEmbedded(ctx, pool); err != nil {
			log.Fatalf("volcado snapshot D1 embebido: %v", err)
		}
	} else if cfg.SkipEmbeddedD1Snapshot {
		log.Println("omitido volcado snapshot embebido (--skip-embedded-d1)")
	}

	// (5) Respaldos JSON crudos desde Redis (y lista --input)
	if !cfg.SkipRawBackup {
		if len(payloads) > 0 {
			log.Printf("guardando JSON crudo en tournament_redis_raw (%d torneos)…", len(payloads))
			if err := importer.UpsertRedisRawSnapshots(ctx, pool, payloads); err != nil {
				log.Fatalf("respaldo crudo Redis: %v", err)
			}
		}
		if len(listRaw) > 0 {
			if err := importer.AppendListJSONSnapshot(ctx, pool, listRaw); err != nil {
				log.Fatalf("respaldo lista --input: %v", err)
			}
			log.Printf("lista --input guardada en tournaments_list_json_snapshots")
		}
	}

	// (6–7) Import tabular Redis + lista (upsert: actualiza filas; la lista no borra platform/flags ya en PG)
	if len(payloads) > 0 || len(listRows) > 0 {
		log.Printf("import tabular final: %d payloads Redis, %d filas lista…", len(payloads), len(listRows))
		if err := importer.Import(ctx, pool, listRows, payloads); err != nil {
			log.Fatalf("import: %v", err)
		}
	} else {
		log.Printf("sin import tabular Redis (--input vacío y Redis sin payloads válidos); Postgres quedó con solo el snapshot D1 embebido.")
	}

	log.Println("terminado.")
}

// logPokemonUsageLines imprime cada slug detectado en decklists y su número de apariciones
// (suma sobre torneos / jugadores tras los filtros de game/format/players).
func logPokemonUsageLines(title string, rows []pokemonusage.SlugCount) {
	if len(rows) == 0 {
		log.Printf("%s: sin apariciones (revisa filtros o decklists vacías en standings).", title)
		return
	}
	log.Printf("%s: %d Pokémon — slug → apariciones", title, len(rows))
	for _, r := range rows {
		log.Printf("  %-34s %d", r.Slug, r.Count)
	}
}

func wdOrDot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}

func shouldShowProg(noProg bool) bool {
	if noProg {
		return false
	}
	stat, err := os.Stderr.Stat()
	return err == nil && stat.Mode()&os.ModeCharDevice != 0
}

func reportProg(on bool, cur, total int, label string) {
	if !on || total <= 0 {
		return
	}
	const w = 28
	done := min(w, (cur*w+total-1)/total)
	pct := 100
	if cur < total {
		pct = min(99, (100*cur)/total)
	}
	bar := strings.Repeat("█", done) + strings.Repeat("░", w-done)
	fmt.Fprintf(os.Stderr, "\x1b[2K\r%s: %s %d%% (%d/%d)", label, bar, pct, cur, total)
}

func clearProg(on bool) {
	if !on {
		return
	}
	fmt.Fprint(os.Stderr, "\x1b[2K\r")
}

func finishProg(on bool) {
	if !on {
		return
	}
	fmt.Fprintln(os.Stderr)
}
