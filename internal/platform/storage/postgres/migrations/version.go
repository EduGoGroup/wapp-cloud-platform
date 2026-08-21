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
// 0.30.0 — Plan 043 · Ola 4.5 (0054): la relación evento↔contenido se INVIERTE
// (D-043.21/22) — intakes.event_id y survey_results.event_id (el hijo declara a su
// padre), DROP de conversation_events.intake_id y la vista public.event_content.
// Un solo bump para la ola, sobre la 0.29.0 ya publicada.
//
// 0.31.0 — Plan 043 (0055): decisión del dueño (Jhoan, 2026-08-10) sobre el legado
// que la 0054 dejaba tolerado con CHECK NOT VALID — se BORRA (0 filas reparables
// por backfill, medido contra Neon: conversation_events.intake_id nunca se
// escribió) y intakes.event_id pasa a NOT NULL real, retirando el CHECK que hacía
// de sustituto.
//
// 0.32.0 — Plan 043 · Ola 6 · T6.5 (0056): cierra MD-043.17 — GET
// /api/v1/events/telemetry gana su índice PARCIAL
// (tenant_id, created_at, id) WHERE name LIKE 'event\_%' sobre flow_events,
// enmienda #2 de las cuatro que exigió la refutación con medición del diseño
// original (2026-08-10/11: flow_events_scan_idx, 0009, no sirve a esta ruta —
// ver la cabecera de la 0056). Un solo bump para toda la ola, sobre la 0.31.0
// ya publicada (mismo criterio que 0.27.0/0.28.0/0.30.0/0.31.0 arriba: instrucción
// explícita del dueño de bumpear al cerrar esta tarea, no una migración suelta).
//
// 0.33.0 — Plan 055 · Ola 3 · T3.1 (0058): segundo sujeto de corte del
// kill-switch (D-055.2) — public.tenants gana revoked_at TIMESTAMPTZ (NULL =
// activo, NOT NULL = revocación COMERCIAL, distinta de leases.revoked que
// corta UNA instalación). Un solo bump para la ola, sobre la 0.32.0 ya
// publicada.
//
// La 0059 (plano de plataforma: el tenant operador de wApp, el rol
// platform_admin con 'tenants.revoke.any'/'tenants.restore.any' y el deny
// '*.any' que impide que el '*' de tenant_admin los alcance — ADR-0039) NO
// mueve esta constante: es del MISMO Plan 055 que la 0058 y la 0.33.0 aún no se
// ha publicado. Es exactamente el caso que la primera mitad de la regla
// contempla: una migración más dentro de un plan que todavía no salió.
//
// 0.34.0 -- Plan 056 (0060): la consola de plataforma gana su esquema -- el
// scope multi-empresa de iam_user_roles (tenant_id, D-056.11), la bandeja
// public.access_requests (T3.1) y los cinco grants nuevos de platform_admin
// (tenants.read.any, tenants.create.any, fleet.read.any, users.provision.any,
// enrollment.issue.any). Un solo bump para T1.1/T3.1, sobre la 0.33.0 ya
// publicada (la 0058/0059 del Plan 055, aplicadas contra Neon de UAT).
//
// 0.35.0 -- Plan 051 · Ola 4 · T4.3 (0061): fleet_sessions gana la salud del
// WORKER del cajero de intents (campos 9-15 del SessionHealth de cloudlink):
// worker_taskset, intent_p50_ms, intent_omitted_by_reason (JSONB, motivo->conteo)
// y los cuatro contadores del despachador de T3.12 (stuck_heads,
// stuck_head_polls, failed_seal_dispatch, failed_seal_budget). Todas NULLABLE y
// SIN default: NULL = «este Edge no lo sabe», jamás «está bien». Un solo bump
// para la ola, sobre la 0.34.0 ya publicada (Plan 056, desplegada en UAT).
//
// 0.36.0 -- Plan 053 · Ola 1 · T1.3 (0062): flow_state gana owner_event_id UUID
// NULL REFERENCES conversation_events(id) — el evento DUEÑO del flujo que corre
// en la fila, la relación que `event_id` (el evento ACTIVO, D-043.4) nunca pudo
// expresar y que divergen cuando el `menu` se monta sobre un `cart` vivo
// (D-053.1). NULLABLE y sin CHECK a propósito: NULL = «ningún módulo en curso»
// (el menú puro de D-043.3) es el estado CORRECTO, no un hueco a rellenar
// (REQ-053.5). Sin índice: se decide en la Ola 3 con la medición delante
// (MD-053.2). El backfill NO viaja en la migración —el runner es full-replay y
// pisaría las resoluciones manuales—: vive en
// docs/runbooks/backfill-053-owner-event-id.sql. Un solo bump para la ola, sobre
// la 0.35.0 ya publicada (Plan 051 · Ola 4).
//
// 0.37.0 -- Plan 046 · Ola 1 · T1.1 (0063): fleet_sessions gana profile
// (active|passive), el EJE DE NEGOCIO que sustituye a role (bot|passive, 0025).
// Columna NUEVA y no rename (D-046.1): role se conserva un ciclo como alias
// deprecado —la escritura mantiene las dos sincronizadas, la lectura de negocio
// pasa a profile y solo a profile— y su DROP es de un plan futuro. La columna
// nace SIN default y se backfillea con guard `WHERE profile IS NULL` antes de
// recibir `DEFAULT 'passive'` + NOT NULL: bajo un runner FULL-REPLAY el orden
// inverso volcaría a pasiva las sesiones vivas del cliente (REQ-15). El default
// alcanza SOLO a las filas nuevas y es un cambio de comportamiento deliberado
// (D-07: una sesión recién emparejada nace pasiva). Un solo bump para el plan,
// sobre la 0.36.0 ya publicada (Plan 053 · Ola 1, desplegada en UAT): las
// migraciones que el resto de olas del 046 añadan NO vuelven a bumpear mientras
// la 0.37.0 no se publique.
const SchemaVersion = "0.37.0"

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
