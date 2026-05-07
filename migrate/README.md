# redis-pg-migrate

Binario que, en **PostgreSQL**, ejecuta en orden:

1. **Tú** exportas el estado actual del **D1 local** (SQLite de Wrangler) a un JSON y lo **vuelves a compilar** dentro del binario (ver abajo).  
2. El binario ejecuta **migraciones embebidas** en Postgres.  
3. El binario **vuelca ese JSON** (`organizers`, `tournaments`, `tournament_phases`, `tournament_standings`, `pokemon` si aplica) a Postgres.  
4. El binario **lee Redis** (detalles / standings por torneo).  
5. Opcional: escribe **datos crudos** en `tournament_redis_raw` y snapshot de `--input` (salvo `--skip-raw-backup`).  
6. **Import tabular** desde Redis + lista `--input` (enriquece/sobrescribe torneos coincidentes por `id`).

**Importante:** el binario **no** ejecuta Wrangler, **no** contacta D1 en la nube y **no** abre ficheros `.sqlite` en el NAS. Solo lleva **datos congelados en el build**.

---

## 1) Extraer datos del D1 (solo en tu máquina de desarrollo)

Con la base D1 local poblada (por ejemplo `pnpm seedLocalD1` / imports y/o `wrangler dev`):

```bash
# encuentra el .sqlite de Wrangler (la ruta exacta cambia entre versiones / IDs de BD)
find .wrangler -name '*.sqlite' 2>/dev/null | head -5

cd migrate
go run ./cmd/d1-export \
  -sqlite /ruta/absoluta/al/archivo.sqlite \
  -out ./internal/d1embed/data/snapshot.json
```

**Variable de entorno:** si defines `D1_LOCAL_SQLITE_PATH` (por ejemplo en `.env` del repo raíz), puedes omitir `-sqlite`; `d1-export` intenta cargar `.env` en el cwd y en `..` (sirve cuando corres desde `migrate/`).

Desde la raíz del repo:

```bash
pnpm migrate:export:d1 -- --sqlite /ruta/al.sqlite -out migrate/internal/d1embed/data/snapshot.json
```

(o ajusta rutas relativos si `cd migrate`).

## 2) Compilar el binario (incluye el snapshot en el ejecutable)

```bash
pnpm binario
# ó para NAS:
pnpm binario:nas-arm64
```

El fichero `migrate/internal/d1embed/data/snapshot.json` queda **embebido** con `go:embed`.

## Qué copiar al NAS

Un solo ejecutable (`redis-pg-migrate` o `redis-pg-migrate-linux-*`). **No** hace falta llevar `.sqlite` ni el repo.

## Flags útiles

| Flag | Efecto |
|------|--------|
| `--skip-schema-migrations` | No aplicar SQL en `migrate/sqlmigrations/postgres/` |
| `--skip-embedded-d1` | Saltar el paso (3): solo Redis/lista en tablas ya existentes |
| `--skip-raw-backup` | No escribir tablas de respaldo JSON crudo |
| `--dry-run` | No toca Postgres; muestra resumen Redis + snapshot embebido |

Variables: `REDIS_URL`, `DATABASE_URL`, `REDIS_KEY_PREFIX`; opcional `--input` como `docs/tournaments.json`.

## Snapshot por defecto

Si no has ejecutado `d1-export`, el `snapshot.json` incluido tiene arrays vacíos: el binario puede seguir funcionando **solo con Redis** (`--input` opcional). Para “llevar lo que ya está en D1”, **debes** regenerar el JSON antes de cada release que quieras publicar al NAS.

## Evolucionar el esquema Postgres

Añade `migrate/sqlmigrations/postgres/0004_*.sql`, etc. No modifiques ficheros ya aplicados en destino (tabla `tool_redis_pg_migrations`).

Tablas de respaldo crudo: migración `0002_raw_backup_tables.sql`. Pokémon en Postgres: `0003_pokemon.sql`.

## Variables de entorno (.env)

Ver comentarios en `.env` del repo; el export D1 es solo comando local, no variable obligatoria para el binario en NAS.

## NAS sin Go en el host

Compila en tu PC (`pnpm binario:nas-arm64`, etc.) y copia el ejecutable. Ver también `Makefile` en `migrate/`.
