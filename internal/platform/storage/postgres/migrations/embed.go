// Package migrations aplica el esquema de la Plataforma Cloud sobre PostgreSQL
// mediante un runner propio con archivos SQL embebidos (embed.FS).
//
// Copia-adaptación simplificada del runner de edugo-infrastructure: conserva la
// idempotencia (no re-aplica si la BD ya está al día) y el registro de versión
// con hash de contenido en public.schema_version, pero elimina seeds, datos de
// desarrollo y el modo Force/recreación de schemas, innecesarios aquí.
package migrations

import "embed"

// structureFS contiene los scripts de estructura embebidos. Se aplican en orden
// alfabético de nombre de archivo; usa el prefijo numérico (0001_, 0002_, …).
//
//go:embed structure/*.sql
var structureFS embed.FS

// structureDir es el directorio embebido con los scripts de estructura.
const structureDir = "structure"
