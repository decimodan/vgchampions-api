package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds CLI and env-derived settings after Parse.
type Config struct {
	ShowVersion    bool
	SkipMigrations bool
	DryRun         bool
	NoProgress     bool

	RedisURL    string
	RedisPrefix string
	DatabaseURL string
	InputJSON   string

	// SkipEmbeddedD1Snapshot no vuelca el JSON embebido (solo Redis / lista).
	SkipEmbeddedD1Snapshot bool

	SkipRawBackup bool

	// UpdatePokemonDecklistUsage: solo recalcula pokemon.usage_count desde decklists Redis; no ejecuta embed/import tabular completo.
	UpdatePokemonDecklistUsage bool
	UsageFilterGame            string
	UsageFilterFormat          string // p.ej. "M-A"; en champions suele estar en details.format
	// UsageMinPlayersExclusive: solo torneos con details.Players mayor que este valor (p.ej. 10 → jugadores ≥ 11).
	UsageMinPlayersExclusive int

	// SyncTournamentsFromAPI: lista tournaments.json → fetch HTTP igual que fetch-tournament-data.mjs → Redis + upsert filas tournaments en Postgres.
	SyncTournamentsFromAPI bool
	FetchSkipExisting      bool // no volver a pedir si Redis ya tiene ambas claves (salvo fetch-force).
	FetchForce             bool // repide GET aunque existan claves

	// PurgePostgresExceptPokemon: TRUNCATE tablas de torneos/respaldo; no borra pokemon ni tool_redis_pg_migrations.
	PurgePostgresExceptPokemon bool
	PurgeConfirm               bool
}

// Parse reads flags; env defaults se rellenan si el flag no se pasó (flag default desde getenv).
func Parse(args []string) (*Config, error) {
	fs := flag.NewFlagSet("redis-pg-migrate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Uso: redis-pg-migrate [opciones]\n\n")
		fmt.Fprintf(fs.Output(), "Migraciones Postgres → opcionalmente snapshot D1 embebido → Redis (crudo + tabular).\n")
		fmt.Fprintf(fs.Output(), "Por defecto el snapshot embebido está vacío; Postgres/Redis son la fuente habitual. Solo `go run ./cmd/d1-export` antes de build si quieres volcar D1 dentro del binario.\n")
		fmt.Fprintf(fs.Output(), "Lista Champions: `--sync-tournaments-from-api` (URLs en CHAMPIONS_* env, igual que fetch-tournament-data.mjs).\n")
		fmt.Fprintf(fs.Output(), "Vacíar Postgres (excepto pokemon): `--purge-postgres-except-pokemon` + `--purge-confirm` (o `--dry-run` para ver alcance).\n\n")
		fs.PrintDefaults()
	}

	showV := fs.Bool("version", false, "imprime versión y sale")
	skipMigrate := fs.Bool("skip-schema-migrations", false, "no aplicar SQL embebido en migrate/sqlmigrations/postgres/")
	dry := fs.Bool("dry-run", false, "solo resumen (snapshot embebido + Redis/Heredado; sin escribir en Postgres)")
	noProg := fs.Bool("no-progress", false, "sin línea de progreso")
	skipRaw := fs.Bool("skip-raw-backup", false, "no guardar JSON crudo en tournament_redis_raw ni snapshot de --input")
	skipEmbed := fs.Bool("skip-embedded-d1", false, "no volcar snapshot D1 embebido (solo Postgres vía Redis/--input si aplica)")

	redisURL := fs.String("redis-url", getenv("REDIS_URL", ""), "URL Redis (también $REDIS_URL)")
	prefix := fs.String("redis-prefix", getenv("REDIS_KEY_PREFIX", "vgchampions"), "prefijo claves Redis")
	dbURL := fs.String("database-url", getenv("DATABASE_URL", ""), "connection string Postgres (salvo --dry-run)")
	input := fs.String("input", "", "JSON opcional: array como docs/tournaments.json")
	updPokemonUsage := fs.Bool("update-pokemon-usage-from-decklists", false, "modo sólo uso: cuenta apariciones en decklist[] (campo id) desde Redis y escribe pokemon.usage_count en Postgres; no ejecuta embed ni import tabular")
	usageGame := fs.String("usage-filter-game", getenv("USAGE_FILTER_GAME", "VGC"), `con --update-pokemon-usage-from-decklists: filtra por details.game; "-" = sin filtro de game`)
	usageFormat := fs.String("usage-filter-format", getenv("USAGE_FILTER_FORMAT", "M-A"), `con --update-pokemon-usage-from-decklists: filtra por details.format (M-A masters); "-" = sin filtro`)
	usageMinPl := fs.Int("usage-min-players", getenvInt("USAGE_MIN_PLAYERS_EXCLUSIVE", 10), "con --update-pokemon-usage-from-decklists: solo torneos donde details.players > este valor")

	syncAPI := fs.Bool("sync-tournaments-from-api", false, "lista JSON (--input o docs/tournaments.json): GET API Champions → Redis; upsert Postgres tournaments (CHAMPIONS_* URLs; con --dry-run solo resumen/PG opcional)")
	fetchSkip := fs.Bool("fetch-skip-existing", true, "con --sync-tournaments-from-api: si ya existen :details y :standings en Redis, no llamar HTTP")
	fetchForce := fs.Bool("fetch-force", false, "con --sync-tournaments-from-api: fuerza nueva descarga ignorando Redis")

	purgeExc := fs.Bool("purge-postgres-except-pokemon", false, "trunca standings/fases/torneos/organizers + tournament_redis_raw + tournaments_list_json_snapshots; conserva pokemon y migraciones (--purge-confirm obligatorio si no hay --dry-run)")
	purgeYes := fs.Bool("purge-confirm", false, "confirma --purge-postgres-except-pokemon (irreversible)")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	if err := validatePurgeAndModes(*purgeExc, *purgeYes, *dry, *syncAPI, *updPokemonUsage); err != nil {
		return nil, err
	}

	if *showV {
		return &Config{ShowVersion: true}, nil
	}

	if *dry {
		if !*purgeExc && strings.TrimSpace(*redisURL) == "" {
			return nil, fmt.Errorf("--dry-run requiere --redis-url / REDIS_URL (salvo --purge-postgres-except-pokemon sin conexión, solo texto)")
		}
		return &Config{
			DryRun:                     true,
			NoProgress:                 *noProg,
			RedisURL:                   *redisURL,
			RedisPrefix:                *prefix,
			InputJSON:                  *input,
			SkipEmbeddedD1Snapshot:     *skipEmbed,
			SkipMigrations:             true,
			SkipRawBackup:              true,
			UpdatePokemonDecklistUsage: *updPokemonUsage,
			UsageFilterGame:            normalizeUsageFilter(*usageGame),
			UsageFilterFormat:          normalizeUsageFilter(*usageFormat),
			UsageMinPlayersExclusive:   *usageMinPl,
			DatabaseURL:                strings.TrimSpace(*dbURL),
			SyncTournamentsFromAPI:     *syncAPI,
			FetchSkipExisting:          *fetchSkip,
			FetchForce:                 *fetchForce,
			PurgePostgresExceptPokemon: *purgeExc,
			PurgeConfirm:               *purgeYes,
		}, nil
	}

	dbTrim := strings.TrimSpace(*dbURL)
	if dbTrim == "" {
		return nil, fmt.Errorf("--database-url o DATABASE_URL requerido")
	}

	purgeOnly := *purgeExc && !*syncAPI && !*updPokemonUsage
	if *redisURL == "" && !purgeOnly {
		return nil, fmt.Errorf("--redis-url o REDIS_URL requerido")
	}

	return &Config{
		RedisURL:                   *redisURL,
		RedisPrefix:                *prefix,
		DatabaseURL:                dbTrim,
		InputJSON:                  *input,
		NoProgress:                 *noProg,
		SkipEmbeddedD1Snapshot:     *skipEmbed,
		SkipMigrations:             *skipMigrate,
		SkipRawBackup:              *skipRaw,
		DryRun:                     false,
		UpdatePokemonDecklistUsage: *updPokemonUsage,
		UsageFilterGame:            normalizeUsageFilter(*usageGame),
		UsageFilterFormat:          normalizeUsageFilter(*usageFormat),
		UsageMinPlayersExclusive:   *usageMinPl,
		SyncTournamentsFromAPI:     *syncAPI,
		FetchSkipExisting:          *fetchSkip,
		FetchForce:                 *fetchForce,
		PurgePostgresExceptPokemon: *purgeExc,
		PurgeConfirm:               *purgeYes,
	}, nil
}

func validatePurgeAndModes(purge, purgeConfirm, dry, syncAPI, updPokemon bool) error {
	if !purge {
		return nil
	}
	if syncAPI {
		return fmt.Errorf("--purge-postgres-except-pokemon no es compatible con --sync-tournaments-from-api")
	}
	if updPokemon {
		return fmt.Errorf("--purge-postgres-except-pokemon no es compatible con --update-pokemon-usage-from-decklists")
	}
	if !dry && !purgeConfirm {
		return fmt.Errorf("--purge-postgres-except-pokemon requiere --purge-confirm en ejecución real (o usa --dry-run para simular)")
	}
	return nil
}

func getenv(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func getenvInt(k string, def int) int {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// normalizeUsageFilter convierte "-" en filtro omitido (vacío).
func normalizeUsageFilter(s string) string {
	s = strings.TrimSpace(s)
	if s == "-" {
		return ""
	}
	return s
}
