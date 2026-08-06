package migrations

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"sort"
)

// SchemaVersion es la versión actual de los scripts de migración.
//
// REGLA REAL (medida contra Postgres, no supuesta): el runner decide reaplicar con
// isUpToDate, que exige versión Y hash de contenido (schema.go). Tocar un
// structure/*.sql cambia el hash, así que las migraciones SE REEJECUTAN aunque esta
// constante no se mueva — y el full-replay sobre una BD CON DATOS no pierde filas,
// porque todo el DDL es idempotente. Consecuencia práctica, en dos mitades:
//
//   - Una ola INTERMEDIA de un plan puede añadir migraciones sin tocar esta
//     constante: es seguro y evita un rosario de versiones que no significan nada.
//   - Lo que NO puede ocurrir es PUBLICAR un plan sin su bump. Cuando el trabajo
//     sale a dev/main, esta constante tiene que reflejar el esquema nuevo: es lo
//     único que un operador puede comparar contra public.schema_version para saber
//     qué esquema corre en esa base. Sin bump, la fila registrada seguiría
//     afirmando una versión vieja sobre un esquema que ya cambió.
//
// En la práctica: UN bump por plan, en el commit del plan que decide dónde ponerlo,
// no uno por migración. (Este valor lo subió el Plan 041, que añadió las
// migraciones 0041-0045 a lo largo de cuatro olas con un solo incremento.)
const SchemaVersion = "0.25.0"

// hashLen es la longitud (en caracteres hex) a la que se trunca el content hash.
const hashLen = 16

// ComputeFilesHash calcula un SHA256 de todos los archivos SQL embebidos en
// structure/. El hash cambia si cualquier archivo se añade, borra o modifica,
// detectando cambios aunque no se haya subido SchemaVersion.
func ComputeFilesHash() string {
	h := sha256.New()

	entries, err := fs.ReadDir(structureFS, structureDir)
	if err != nil {
		return "error"
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		content, readErr := structureFS.ReadFile(structureDir + "/" + name)
		if readErr != nil {
			continue
		}
		h.Write([]byte(name))
		h.Write(content)
	}

	return fmt.Sprintf("%x", h.Sum(nil))[:hashLen]
}
