package sqlmigrations

import "embed"

// Postgres contiene SQL versionados (0001_*.sql, 0002_*.sql, …).
// Para extender el esquema, añade archivos nuevos aquí sin modificar números ya aplicados.
//
//go:embed postgres/*.sql
var Postgres embed.FS
