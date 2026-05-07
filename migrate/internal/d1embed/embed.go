package d1embed

import _ "embed"

// SnapshotData es el JSON embebido al compilar. Por defecto está vacío: torneos y catálogo viven en Postgres/Redis;
// solo rellena data/snapshot.json con `go run ./cmd/d1-export` si quieres un volcado D1 embebido en el build.
//
//go:embed data/snapshot.json
var SnapshotData []byte
