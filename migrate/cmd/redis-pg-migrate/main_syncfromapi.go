package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/decimodan/vgchampions-api/migrate/internal/championsfetch"
	"github.com/decimodan/vgchampions-api/migrate/internal/config"
	"github.com/decimodan/vgchampions-api/migrate/internal/importer"
	"github.com/decimodan/vgchampions-api/migrate/internal/schema"
	"github.com/decimodan/vgchampions-api/migrate/internal/syncfromlist"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// runSyncFromAPI: solo ids de la lista JSON.
// 1) Carga ids existentes en Postgres (tournaments).
// 2) Si Redis ya tiene details+standings (salvo --fetch-force) → omitir.
// 3) Si falta Redis y el id está en PG → tournament_redis_raw o JSON sintético desde tablas.
// 4) Si el id no está en PG → GET API (requiere CHAMPIONS_*).
// 5) Upsert filas tournaments desde la lista.
func runSyncFromAPI(ctx context.Context, cfg *config.Config) error {
	if cfg.SyncTournamentsFromAPI && cfg.UpdatePokemonDecklistUsage {
		return fmt.Errorf("no uses --sync-tournaments-from-api junto con --update-pokemon-usage-from-decklists")
	}

	inputPath, err := championsfetch.ResolveInputPath(wdOrDot(), cfg.InputJSON)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(inputPath)
	if err != nil {
		return fmt.Errorf("leer lista torneos: %w", err)
	}
	listRows, err := importer.ParseTournamentsList(raw)
	if err != nil {
		return fmt.Errorf("parse lista: %w", err)
	}
	listRows = importer.DedupeTournamentListByID(listRows)
	if len(listRows) == 0 {
		log.Println("sync: lista vacía")
		return nil
	}

	wanted := make([]string, 0, len(listRows))
	for _, r := range listRows {
		if strings.TrimSpace(r.ID) != "" {
			wanted = append(wanted, strings.TrimSpace(r.ID))
		}
	}

	detailsTpl := strings.TrimSpace(os.Getenv("CHAMPIONS_DETAILS_URL_TEMPLATE"))
	standingsTpl := strings.TrimSpace(os.Getenv("CHAMPIONS_STANDINGS_URL_TEMPLATE"))

	rOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("REDIS_URL: %w", err)
	}
	rdb := redis.NewClient(rOpts)
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping: %w", err)
	}

	dbURL := strings.TrimSpace(cfg.DatabaseURL)
	var pool *pgxpool.Pool
	if cfg.DryRun && dbURL == "" {
		log.Println("sync dry-run: DATABASE_URL ausente → se asume PostgreSQL sin ids (solo API clasificarán todos los que falten en Redis)")
	}
	if dbURL != "" {
		pool, err = pgxpool.New(ctx, dbURL)
		if err != nil {
			return fmt.Errorf("postgres: %w", err)
		}
		defer pool.Close()
		if err := pool.Ping(ctx); err != nil {
			return fmt.Errorf("postgres ping: %w", err)
		}
	}
	if dbURL != "" && !cfg.DryRun {
		if !cfg.SkipMigrations {
			if err := schema.ApplyPending(ctx, pool); err != nil {
				return fmt.Errorf("migraciones: %w", err)
			}
		}
	}

	pgIDs := map[string]struct{}{}
	if pool != nil {
		pgIDs, err = syncfromlist.LoadTournamentIDSet(ctx, pool)
		if err != nil {
			return fmt.Errorf("leer ids PostgreSQL tournaments: %w", err)
		}
	}
	inListAndPG := 0
	for _, id := range wanted {
		if _, ok := pgIDs[id]; ok {
			inListAndPG++
		}
	}
	if !cfg.DryRun && pool == nil {
		return fmt.Errorf("DATABASE_URL requerido para --sync-tournaments-from-api (salvo sólo dry-run sin PG)")
	}

	ttlSec := getenvTTLSeconds()

	var skipRedisOK, hydrateRaw, hydrateSyn int
	var needAPI []string

	for _, id := range wanted {
		redisOK, err := syncfromlist.RedisHasDetailsAndStandings(ctx, rdb, cfg.RedisPrefix, id)
		if err != nil {
			return fmt.Errorf("redis %s: %w", id, err)
		}
		if redisOK && !cfg.FetchForce {
			skipRedisOK++
			continue
		}

		if _, inPG := pgIDs[id]; inPG && pool != nil {
			det, st, okRB, er := syncfromlist.RawBackupTexts(ctx, pool, id)
			if er != nil {
				return fmt.Errorf("tournament_redis_raw %s: %w", id, er)
			}
			if okRB {
				if !cfg.DryRun {
					if er := syncfromlist.WriteRedisTournamentKV(ctx, rdb, cfg.RedisPrefix, id, det, st, ttlSec); er != nil {
						return er
					}
				}
				hydrateRaw++
				continue
			}
			dj, sj, er := syncfromlist.SyntheticDetailsStandings(ctx, pool, id)
			if er == nil {
				if !cfg.DryRun {
					if er := syncfromlist.WriteRedisTournamentKV(ctx, rdb, cfg.RedisPrefix, id, dj, sj, ttlSec); er != nil {
						return er
					}
				}
				hydrateSyn++
				continue
			}
			log.Printf("sync: %s existe en PG pero sin backup usable (%v) → se intentará API", id, er)
		}

		needAPI = append(needAPI, id)
	}

	if cfg.DryRun {
		log.Printf("sync dry-run archivo=%s", inputPath)
		log.Printf("  ids_lista=%d en_PG_y_en_lista=%d | redis_ok_omitidos=%d hidratar_raw=%d hidratar_sintético=%d pendientes_API=%d",
			len(wanted), inListAndPG, skipRedisOK, hydrateRaw, hydrateSyn, len(needAPI))
		if len(needAPI) > 0 && detailsTpl != "" && standingsTpl != "" {
			fr, err := championsfetch.EnvFetch(detailsTpl, standingsTpl, 0, true)
			if err != nil {
				return err
			}
			fr.Client = rdb
			fr.Prefix = cfg.RedisPrefix
			fr.DryURLsOnly = true
			if _, err := fr.Run(ctx, needAPI); err != nil {
				return err
			}
		} else if len(needAPI) > 0 {
			log.Println("define CHAMPIONS_* para ver URLs de los ids pendientes de API.")
		}
		log.Println("sync dry-run hecho.")
		return nil
	}

	if len(needAPI) > 0 {
		if detailsTpl == "" || standingsTpl == "" {
			return fmt.Errorf("%d ids requieren descarga HTTP: define CHAMPIONS_DETAILS_URL_TEMPLATE y CHAMPIONS_STANDINGS_URL_TEMPLATE", len(needAPI))
		}
		fr, err := championsfetch.EnvFetch(detailsTpl, standingsTpl, ttlSec, false)
		if err != nil {
			return err
		}
		fr.Client = rdb
		fr.Prefix = cfg.RedisPrefix
		fr.SkipExisting = false
		fr.Force = cfg.FetchForce
		fr.ErrListKey = championsfetch.RedisErrorsKey(cfg.RedisPrefix)
		showProg := shouldShowProg(cfg.NoProgress)
		fr.Prog = func(cur, total int, label string) {
			if showProg && total > 0 {
				reportProg(true, cur, total, label)
			}
		}
		stats, err := fr.Run(ctx, needAPI)
		if showProg {
			clearProg(true)
			finishProg(true)
		}
		if err != nil {
			return err
		}
		log.Printf("fetch Champions→Redis: sets_details≈%d sets_standings≈%d omitidos=%d fallos_HTTP=%d (errores RPUSH → %s si aplica)",
			stats.FetchedDetailSets, stats.FetchedStandingSets, stats.SkippedWhole, stats.Failed, fr.ErrListKey)
	} else {
		log.Println("ningún id de la lista requería llamada HTTP.")
	}

	log.Printf("upsert tabla tournaments desde lista (%d filas)…", len(listRows))
	if err := importer.UpsertOrganizersAndTournamentsFromList(ctx, pool, listRows); err != nil {
		return fmt.Errorf("upsert lista: %w", err)
	}
	log.Printf("terminado (--sync-tournaments-from-api). redis_ok=%d raw_PG→Redis=%d synth_PG→Redis=%d http=%d",
		skipRedisOK, hydrateRaw, hydrateSyn, len(needAPI))
	return nil
}

func getenvTTLSeconds() int {
	v := strings.TrimSpace(os.Getenv("REDIS_TTL_SECONDS"))
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
