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
// no uno por migración. (El Plan 041 subió este valor a 0.25.0 añadiendo las
// migraciones 0041-0045 a lo largo de cuatro olas con un solo incremento; el
// Plan 042 lo subió a 0.26.0 añadiendo las 0046-0048 —webhook_outbox,
// tenant_integrations y el reflejo del CRM sobre intakes— también con un ÚNICO
// incremento para todo el plan.)
//
// 0.27.0 fue la EXCEPCIÓN que confirma la segunda mitad de la regla, no una
// violación de la primera: la 0.26.0 YA SE PUBLICÓ y ya está escrita en la fila de
// public.schema_version de Neon. La 0049 (lease del claim de webhook_outbox, Ola
// 3.1 del mismo Plan 042) llega DESPUÉS de esa publicación, así que reusar 0.26.0
// dejaría a un operador comparando una versión que afirma un esquema que ya
// cambió — exactamente lo que esta constante existe para impedir. La regla «un
// bump por plan» acota los bumps GRATUITOS dentro de un plan que aún no salió; no
// obliga a mentir sobre un esquema ya publicado.
//
// 0.28.0 es ese MISMO caso otra vez, y por eso no hay contradicción en que el Plan
// 042 lleve tres incrementos: la 0.27.0 también se publicó (Olas 3.1 y 4, main
// 2026-08-08) antes de que existiera la 0050 (vaciado del payload entregado en
// webhook_outbox, saneamiento de PII de la Ola 5). Cada bump de este plan
// corresponde a un esquema que salió a Neon, no a una migración suelta: las 0046-
// 0048 fueron a la 0.26.0 en un solo incremento, y ese es el patrón que la regla
// pide.
const SchemaVersion = "0.29.0"

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
