package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/decimodan/vgchampions-api/migrate/internal/dotenv"
)

// Herramienta de DESARROLLO: lee el .sqlite que usa Wrangler con D1 local y escribe JSON embebido
// que luego debe compilarse dentro de redis-pg-migrate. El binario de migración no abre SQLite en producción.

// Si no pasas -sqlite, se usa D1_LOCAL_SQLITE_PATH (p. ej. en .env en la raíz del repo).
const envSQLitePath = "D1_LOCAL_SQLITE_PATH"

func main() {
	sqliteFlag := flag.String("sqlite", "", "ruta al archivo .sqlite de D1 local (Wrangler); si falta: env "+envSQLitePath)
	outPath := flag.String("out", "internal/d1embed/data/snapshot.json", "JSON de salida (relativo normalmente desde migrate/)")
	flag.Parse()

	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	_ = dotenv.LoadFromFile(filepath.Join(wd, ".env"))
	_ = dotenv.LoadFromFile(filepath.Join(wd, "..", ".env"))

	sqliteResolved := filepath.Clean(strings.TrimSpace(*sqliteFlag))
	if sqliteResolved == "" || sqliteResolved == "." {
		sqliteResolved = filepath.Clean(strings.TrimSpace(os.Getenv(envSQLitePath)))
	}
	if sqliteResolved == "" || sqliteResolved == "." {
		log.Fatalf("sin ruta SQLite: pasa -sqlite /ruta/al/archivo.sqlite o define %s en el entorno o en .env", envSQLitePath)
	}

	snap, err := PullFromSQLite(sqliteResolved)
	if err != nil {
		log.Fatalf("SQLite: %v", err)
	}

	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		log.Fatalf("json: %v", err)
	}
	raw = append(bytes.TrimSpace(raw), '\n')

	absOut, err := filepath.Abs(*outPath)
	if err != nil {
		log.Fatalf("out: %v", err)
	}
	if err := os.WriteFile(absOut, raw, 0o644); err != nil {
		log.Fatalf("escribir %s: %v", absOut, err)
	}
	log.Printf("snapshot escrito: %s (%s)", absOut, snap.CountsLine())
}
