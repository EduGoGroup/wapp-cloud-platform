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
// 0.38.0 -- Plan 046 · Ola 1 (0064): RETIRO de fleet_sessions.role. La 0063 lo
// conservaba «un ciclo como alias deprecado» para no romper a clientes que no se
// despliegan con la plataforma; al comprobarlo contra los seis repos, ese cliente
// NO EXISTE (el BFF llama a /profile y no conserva la ruta vieja; el proto de
// CloudLink no transporta role; nadie más lo menciona). Un ciclo de deprecación que
// no protege a nadie es coste sin contrapartida, así que D-046.1 se revisó y la
// columna, su tipo Go, sus dos rutas /role y la micro-duda MD-046.2 mueren juntos.
// 🔴 Bajo FULL-REPLAY la 0025 RECREA la columna en cada arranque y esta la vuelve a
// borrar: es correcto, converge y cuesta catálogo, no datos — pero esta migración
// tiene que ir SIEMPRE por encima de la 0063, que lee `role` en su backfill.
// 🔴 A partir de aquí el rollback al binario anterior NO es una opción: leía
// COALESCE(role,'bot').
//
// 0.39.0 -- Plan 046 · Ola 2 · T2.1 (0065): fleet_sessions gana
// profile_updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), el RELOJ del eje
// `profile`. La `version` del kind:"filters" que se empuja al Edge pasa a ser
// max(profile_updated_at) del tenant y deja de ser max(updated_at) — que lo mueve
// CUALQUIER escritura de la fila (MarkOnline, el SaveHealth de cada heartbeat,
// SetSelfPn) y hacía que una sola reconexión de un Edge con N sesiones publicara N
// versiones nuevas con el mapa IDÉNTICO, que el Edge re-aplica, re-persiste y —si
// llegan desordenadas— reporta con su WARN «versión anterior o igual» en operación
// normal, enterrando la única línea que delataría una anomalía real.
//
// 🔴 Este bump NO es de los «gratuitos» que la regla acota: la 0.38.0 ya salió a
// main con el commit del retiro de `role` (7ab3a7e), así que reusarla dejaría a un
// operador comparando public.schema_version contra un esquema que ya cambió — el
// mismo caso que 0.27.0/0.28.0/0.30.0/0.31.0 más arriba. Y aquí importa más de lo
// habitual: el binario nuevo LEE profile_updated_at, así que la fila de
// schema_version es lo único que le dice a un operador si esa columna existe en
// la base que tiene delante.
// 0.40.0 -- Plan 046 · Ola 3 · T3.2 (b) (0066): fleet_sessions gana greeted_at
// TIMESTAMPTZ NULL, la marca de «a esta sesión ya se le entregó el aviso
// AVISO_SESION_PASIVA_V1». NULLABLE y SIN default, al revés que la 0065: aquí NULL
// («nunca se le avisó») es el estado CORRECTO de toda fila preexistente, y un
// default la dejaría afirmando un saludo que nadie mandó. Sin backfill, por lo
// mismo. La escribe SOLO MarkGreeted, con centinela WHERE greeted_at IS NULL y solo
// cuando el Ack del Edge vuelve ok=true — si el envío cae en la ventana del lease
// (el Validator del Edge nace cerrado, 0,5-1,1 s medidos en campo) la marca no se
// pone y el siguiente latido reintenta solo.
//
// 🔴 Este bump TAMPOCO es de los «gratuitos» que la regla acota. La 0.39.0 ya se
// publicó y ya se desplegó en UAT el 2026-08-21 con las Olas 1 y 2 de este mismo
// plan (0063-0065), así que su número ya está escrito en la fila de
// public.schema_version de Neon. Reusarla dejaría a un operador comparando esa fila
// contra un esquema que ya cambió — el mismo caso que 0.27.0/0.28.0/0.30.0 más
// arriba. Que las tres olas sean del Plan 046 no lo convierte en una excepción: la
// regla «un bump por plan» acota los bumps dentro de un plan que AÚN NO SALIÓ, y de
// este ya salieron dos tercios.
//
// 0.41.0 -- Plan 046 · Ola 4 · T4.1 (0068): fleet_sessions gana el SOBRE DE CUATRO
// PIEZAS de self_pn — self_pn_bidx TEXT (hex del HMAC sobre el número NORMALIZADO),
// self_pn_enc BYTEA, self_pn_dek BYTEA y self_pn_kek_id TEXT, el mismo patrón que
// public.contacts lleva desde la 0006 (ADR-0017). Cierra la mitad DDL de REQ-16: el
// censo del MP-06 señala self_pn como el ÚNICO teléfono en claro de toda la base.
// Las cuatro nacen NULLABLES y SIN default, y a diferencia de la 0063 NO reciben
// jamás un SET NOT NULL: una sesión sin emparejar legítimamente no tiene número, así
// que NULL es un estado CORRECTO y no un hueco a rellenar. La columna en claro
// self_pn SE CONSERVÓ y quedó VACÍA — 🔧 y ya no existe: su DROP fue la 0070 (T5.4).
// La migración NO cifraba: la KEK no vive en esta BD (ADR-0007/0009), así que el
// backfill era un paso de Go y no podía ser un runbook SQL como el del Plan 053. Ese
// paso se retiró con la columna.
// Además del índice del lookup ciego (tenant_id, self_pn_bidx) trae el índice de
// rotación por self_pn_kek_id, que las otras tres tablas del censo de rekeyTargets
// ya tenían.
//
// El hueco de la 0067 (reservada a T4.4, que no se escribe en esta sesión) es
// INOCUO y deliberado: el runner lista el directorio embebido, lo ordena
// lexicográficamente y ejecuta lo que hay (migrate.go), sin tabla de aplicadas ni
// exigencia de continuidad — hoy ya falta la 0021 y el sistema arranca.
//
// 🔴 Este bump NO es opcional ni de los «gratuitos» que la regla acota. La 0.40.0 ya
// se publicó y ya se desplegó en UAT el 2026-08-21, así que su número ya está escrito
// en la fila de public.schema_version de Neon: reusarla dejaría a un operador
// comparando esa fila contra un esquema que ya cambió — el mismo caso que
// 0.27.0/0.28.0/0.30.0/0.39.0/0.40.0 más arriba. Que sea la cuarta ola del mismo Plan
// 046 no lo convierte en excepción: la regla «un bump por plan» acota los bumps
// dentro de un plan que AÚN NO SALIÓ, y de este ya salieron tres olas. Y aquí pesa
// más de lo habitual, porque el binario nuevo LEE self_pn_bidx en el camino del
// anti-self-loop: si esas columnas no existen en la base que el operador tiene
// delante, esta fila es lo único que se lo dice antes de arrancar.
//
// 0.42.0 -- Plan 046 · Ola 4 · T4.2 (0069): contacts.push_name gana el SOBRE DE TRES
// PIEZAS -- push_name_enc BYTEA, push_name_dek BYTEA y push_name_kek_id TEXT. Cierra
// REQ-17 y la asignación literal del ADR-0034 §Decisión 3, que ya rechazaba con ❌ el
// statu quo de dejarlo en claro: por su regla de admisión, un nombre propio identifica
// a una persona y es nivel 2.
//
// 🔴 SON TRES PIEZAS Y NO CUATRO. El sobre de value (0006) y el de self_pn (0068)
// llevan un _bidx porque hay consultas que buscan por ese valor; del push_name no
// busca NADIE — no aparece en un solo WHERE, ni en una PK, ni en un índice. Un índice
// ciego aquí se calcularía en cada escritura, ocuparía en cada fila y no respondería
// a ninguna pregunta.
//
// Las tres nacen NULLABLES y SIN default, y NO reciben jamás un SET NOT NULL: un
// contacto legítimamente puede no tener nombre. Ojo al contraste con la 0007, que en
// esta MISMA tabla hizo lo contrario (value_kek_id NOT NULL DEFAULT '1'): allí toda
// fila tenía ya una DEK que envolver, y aquí no.
//
// La columna en claro push_name SE CONSERVÓ y quedó VACÍA. 🔧 Ya no existe: la 0070
// (T5.4) la retiró junto con fleet_sessions.self_pn, una vez las dos estuvieron
// verificadas en campo. Mientras existió, el conteo en claro fue la PRUEBA de que el
// backfill funcionó. La migración NO cifraba: la KEK no vive en esta BD
// (ADR-0007/0009), así que el relleno era un paso de Go colgado del arranque
// (contact.PostgresResolver.BackfillPushName) y no podía ser un runbook SQL. Ese paso
// se retiró con la columna: sin claro que migrar, su SELECT no tenía dónde mirar.
//
// Trae además el índice parcial de rotación idx_contacts_push_name_kek, porque T4.2
// mete una SEGUNDA entrada de public.contacts en el censo de rekeyTargets: son dos
// sobres independientes en la misma fila y el barrido de uno no ve al otro.
//
// 🔴 Este bump NO es opcional ni de los «gratuitos» que la regla acota. La 0.41.0 ya
// se publicó y ya se desplegó en UAT el 2026-08-21 con T4.1, así que su número ya
// está escrito en la fila de public.schema_version de Neon: reusarla dejaría a un
// operador comparando esa fila contra un esquema que ya cambió -- el mismo caso que
// 0.27.0/0.28.0/0.30.0/0.39.0/0.40.0/0.41.0 más arriba. Que sea la segunda tarea de
// la misma ola del mismo Plan 046 no lo convierte en excepción: la regla «un bump por
// plan» acota los bumps dentro de un plan que AÚN NO SALIÓ, y de este ya salieron
// cuatro. Y aquí pesa, porque T4.1 y T4.2 son los dos backfills cifrados de la ola y
// NO van en el mismo despliegue: sin bump, las dos bases -- la que solo tiene el sobre
// del self_pn y la que ya tiene los dos -- se declararían idénticas.
// 0.43.0 -- Plan 046 · Ola 4 · T4.4 (0067): conversation_ttl_seconds deja de nacer
// en 0. El DEFAULT pasa a 7200 (2 h), igualado al reloj único
// event_inactivity_ttl_seconds. Cierra REQ-19 y el hallazgo de privacidad de la ola:
// con 0 --que la 0034 puso queriendo decir «sin vencimiento»-- el flow_state y sus
// vars, que llevan el TEXTO LITERAL del cliente, no caducaban NUNCA. No era config:
// era retención infinita por defecto, lo que el ADR-0034 prohíbe.
//
// 🔴 EL BUMP LO PIDE EL ESPEJO EN GO, NO EL SQL. El ALTER de la 0067 solo gobierna a
// los tenants CON fila en tenant_settings; el tenant SIN fila lee
// store.DefaultTenantSettings, que hasta esta tarea omitía ConversationTTL y por
// tanto devolvía el cero de Go. Las dos mitades tienen que viajar juntas, y este
// número es lo que le dice al operador si la base que tiene delante lleva ya las dos.
//
// 🔴 LA 0067 NO TRAE UN UPDATE DE LAS FILAS EXISTENTES, y no es un olvido: el runner
// es FULL-REPLAY, así que un UPDATE incondicional no correría una vez sino en cada
// arranque que recalcule el hash, y pisaría al tenant que eligiera 0 a propósito. La
// columna es NOT NULL y no hay centinela que sirva de guard (el truco de la 0063,
// profile IS NULL, aquí no existe). El backfill vive en
// docs/runbooks/backfill-046-conversation-ttl.sql y se corre UNA vez -- mismo
// precedente que el del Plan 053, citado más arriba en este fichero.
//
// 🔴 NO COLAPSA LOS DOS RELOJES (ADR-0029 §E-9.2). Que ahora compartan el número 7200
// es una coincidencia deliberada de valor, NO una unificación de claves: este es el
// SUBORDINADO y solo se evalúa con flow_state.event_id IS NULL. Colapsarlos está
// prohibido desde el 2026-08-06.
//
// El bump no es opcional, por lo de siempre: la 0.42.0 ya se publicó y ya se desplegó
// en UAT el 2026-08-21 con T4.2, así que su número ya está escrito en la fila de
// public.schema_version de Neon.
//
// 0.44.0 -- Plan 046 · Ola 5 · T5.4 (0070): MUEREN LAS DOS COLUMNAS DE PII EN CLARO
// que quedaron vivas y vacías tras los backfills cifrados -- fleet_sessions.self_pn
// (la trajo la 0028, la vació T4.1) y contacts.push_name (la trajo la 0005, la vació
// T4.2). Cierra el saneo de PII del plan: a partir de aquí no hay en toda la base UNA
// SOLA columna donde quepa un teléfono o un nombre sin cifrar. D-046.17.
//
// 🔴 LA MIGRACIÓN ES LA MITAD PEQUEÑA. La otra mitad, en el MISMO commit, es retirar
// el código Go que nombra esas dos columnas en su SQL: los dos backfills de arranque
// (fleet.BackfillSelfPn y contact.BackfillPushName), el `self_pn = NULL` de SetSelfPn
// con su guarda `OR self_pn IS NOT NULL`, y el `push_name = NULL` de resolveExisting.
// Los backfills BLOQUEAN el arranque y abortan el proceso si su consulta falla, así
// que desplegar el DROP sin ellos deja la plataforma SIN ARRANCAR con «column does
// not exist». No es limpieza posterior: es parte de la migración.
//
// 🔴 Y ES EL PUNTO DE NO RETORNO DE LA PRUEBA. Mientras esas columnas existieron,
// `count(*) WHERE self_pn IS NOT NULL` y su gemela de push_name eran LA evidencia de
// que los backfills funcionaron -- el criterio (a) de T4.1 y T4.2. Después de la 0070
// esa consulta ya no se puede hacer. Por eso T5.4 exige correrla contra UAT y anotarla
// en el journal justo antes de aplicar: es la última vez que la prueba es posible.
//
// El bump no es opcional: la 0.43.0 ya se publicó y ya se desplegó en UAT el
// 2026-08-21 con T4.4, así que su número ya está escrito en la fila de
// public.schema_version de Neon. Y aquí el número importa más que de costumbre --
// es lo único que distingue, ANTES de arrancar, una base donde el binario nuevo puede
// correr de una donde su primer SELECT encontraría columnas que ya no espera.
//
// 0.45.0 — Plan 044 · Ola 4 (0080 y 0081): el RE-ANÁLISIS A DEMANDA del dueño (T4.6)
// y la MARCA DEL RECORDATORIO del plazo (T4.5). Dos migraciones aditivas de la misma
// ola y UN solo bump, que es el del Plan 044 entero.
//
// 0080 — `intake_jobs` estrena CUATRO columnas de contexto: `requested_by` (el ROL
// que pidió el job, nunca una persona), `reanalysis_via` (local | api),
// `reanalysis_source` (event_thread | pasted_text | both) y `reanalyzed_from` (el
// `revision_no` vigente cuando se pidió, o sea la revisión que este job SUCEDE).
// Hasta hoy todo job nacía igual —lo abría el agregador con el mensaje del cliente—;
// `POST /api/v1/intakes/{id}/reanalyze` estrena un SEGUNDO productor, el dueño por
// HTTP, y su fila es indistinguible columna a columna de la del pipeline normal. La
// etapa que escribe la revisión (`draft`) no habla con HTTP: recibe un `ClaimedJob` y
// nada más, así que **o el job transporta el dato o el dato no llega**.
//
// Es lo que hace que la revisión resultante nazca con `created_by='owner'` en vez de
// `system`, lo que GATEA el empuje al puente CRM (sin esa marca, empujar en toda
// revisión convertiría el pipeline normal en un productor de `intake.push`), y lo que
// pone VÍA en `payload.analysis.provider` **cuando el resto de interpretaciones no la
// tienen**: en el pipeline normal la vía la elige el selector POR LLAMADA y no la
// publica ningún puerto, así que ese campo sale vacío. Solo el re-análisis la conoce,
// porque la resolvió el endpoint (cuerpo → `tenant_llm.via` → `local`, D-044.48 §4).
//
// 🔴 CUATRO COLUMNAS Y NINGUNA CON CHECK, y las dos cosas son decisión. No van en
// `artifacts` porque ese JSONB está indexado POR ETAPA y esto no es una etapa; no van
// con CHECK porque el molde exacto es `intake_revisions.created_by` (0045), que
// declara su vocabulario en el COMMENT y no en una constraint —un CHECK obligaría a
// migrar la base el día que aparezca un tercer productor de jobs—. Nacen NULL y sin
// default: rellenar el pasado con `system` afirmaría de cada job de la historia algo
// que nadie comprobó. Sin índice, porque ninguna consulta filtra por ellas.
//
// 0081 — `intakes.expiry_reminded_at`: la constancia de que al dueño YA se le recordó
// UNA vez que este presupuesto lleva más del plazo (24 h) esperando su decisión.
//
// 🔴 NO NACE NINGUNA COLUMNA DE VENCIMIENTO, Y ESO ES LA TAREA. La marca «vencido» de
// la bandeja es DERIVADA: se calcula AL LEER, desde `status` y `updated_at` contra una
// constante de plataforma que vive en Go (`intakes.QuoteDeadline`). No se persiste, así
// que no puede desincronizarse de la verdad, no hay backfill que correr, y el día que
// el plazo cambie cambia también el pasado —que es lo correcto: el plazo es una regla
// de la plataforma, no un hecho de la solicitud—. Y NO hay transición a `expired`:
// nada muere por tiempo (ADR-0029 Enmienda 2, D-041.16), la solicitud sigue en
// `pending_approval` con sus mismos destinos. Lo único que sí necesita constancia es
// el RECORDATORIO, porque no puede repetirse: de ahí esta columna, escrita con
// compare-and-swap contra NULL —de N toques simultáneos gana uno y avisa uno—.
// Nullable y sin default, molde exacto de `deposit_reminded_at` (0045). Sin índice, y
// aquí sí se separa de su gemela: el de la seña existe porque aquel recordatorio lo
// dispara también el mensaje entrante del cliente, que tiene que BUSCAR candidatas por
// (tenant, contacto) en el camino caliente; éste no busca nada —solo lo disparan las
// lecturas del dueño, que ya tienen la fila delante, y su único acceso es la PK—.
//
// 🔴 AVISO OPERATIVO, PARA QUE NO SORPRENDA A QUIEN LEA EL LOG DESPUÉS DE DESPLEGAR:
// desde el momento en que esto entre, UAT empezará a escribir `expiry_reminded_at` y a
// dejar traza de recordatorios QUE NO RECIBE NADIE. El canal real del aviso al dueño es
// el push del Plan 045 y NO EXISTE; el emisor de hoy solo loguea. Está decidido a
// sabiendas (D-044.50 §2): lo difícil de este recordatorio no es el canal sino la
// IDEMPOTENCIA —el orden «mirar → marcar → emitir» y el CAS contra NULL—, y eso se
// construye barato hoy, con el gemelo de la seña delante, y caro dentro del Plan 045,
// cuando ya nadie esté mirando este fichero. Un log lleno de recordatorios sin
// destinatario es lo ESPERADO, no un fallo, y nadie puede afirmar que el dueño los
// recibe: no los recibe.
//
// El bump no es opcional, por lo de siempre: la 0.44.0 ya se publicó y ya se desplegó
// (Plan 046 · Ola 5 · T5.4, 2026-08-21), así que su número ya está escrito en la fila
// de `public.schema_version` de esa base. Y aquí pesa más de lo habitual, porque el
// Plan 044 lleva NUEVE migraciones más —`0071`–`0079`— que ya salieron a `main` bajo
// la PRIMERA mitad de la regla, sin bump, por ser olas intermedias: hoy toda base
// donde ese trabajo ya corrió tiene la fila diciendo `0.44.0` sobre un esquema que
// lleva nueve migraciones del 044 dentro. Este número es lo único que, a partir de la
// publicación de la Ola 4, distingue esa base de una que solo tenga el esquema del
// Plan 046 — que es exactamente para lo que esta constante existe.
//
// (Las entradas de arriba dicen «de Neon»: desde la migración del VPS esa base es un
// Postgres 17 en Docker, `wapp-postgres`. La fila y el argumento son los mismos.)
// 0.46.0 — Plan 047 · Ola 1.0 · T1.0-3 (0084): los cuatro scopes del PLANO DE ROLES
// del tenant (`roles.read`/`roles.write`, `members.read`/`members.write`) y la cerca
// INV-10 en la dirección que faltaba.
//
// ⚠️ ESTA ENTRADA DIJO «es el bump ÚNICO del Plan 047: sus olas posteriores pueden
// añadir migraciones sin volver a moverlo», y la 0.47.0 de abajo la desmiente. No
// era un error de razonamiento sino una PREDICCIÓN sobre el futuro escrita como
// hecho: cuando se redactó, la 0.46.0 aún no había salido de `dev`, y bajo la
// primera mitad de la regla la predicción se habría cumplido. Lo que la rompió es
// que la 0.46.0 se desplegó en UAT ANTES de que llegara la ola siguiente. La
// lección, para la próxima entrada: el bump de un plan se puede llamar «el único»
// en pasado, nunca en futuro — quien lo escribe no controla cuándo se despliega.
//
// La migración no siembra ni un `allow` y eso es la decisión, no el olvido:
// tenant_admin alcanza los cuatro por su '*' (0015) y viewer alcanza los dos de
// lectura por '*.read', así que el criterio de T1.0-3 —"un tenant_admin los tiene
// por su glob; un viewer no puede escribir"— se cumple con el seed que ya existía.
// Lo que 0084 AÑADE son cuatro `deny` sobre platform_admin: hasta hoy, que el plano
// de plataforma no entrara en el de tenant se sostenía solo sobre el default-DENY,
// es decir, sobre que a nadie le diera por ampliar la consola con un glob. El lado
// simétrico de esa cerca lleva puesto desde 0059 (el deny '*.any' sobre
// tenant_admin) precisamente porque allí el glob YA existía.
//
// El bump toca aquí y no en el cierre del plan por la segunda mitad de la regla: la
// 0.45.0 ya se publicó y ya corre en UAT (Plan 044 · Ola 4), así que esta migración
// es la PRIMERA que cambia el esquema por encima de una versión ya escrita en la fila
// de public.schema_version. Dejarla sin bump haría que esa fila afirmara 0.45.0 sobre
// un esquema que ya no es el suyo.
//
// 0.47.0 — Plan 047 · Ola A · T-A1/T-A6 (0085): nace `public.tenant_invitations`, la
// invitación de UN SOLO USO con la que una dueña incorpora a alguien a su empresa
// (D-047.11). Guarda el SHA-256 del token y NUNCA el token en claro, y no tiene ni
// una columna de texto: CERO PII por forma, no por disciplina.
//
// Es el SEGUNDO bump del Plan 047, y eso NO rompe la regla de «un bump por plan»:
// esa regla dice que las olas INTERMEDIAS no necesitan uno, no que un plan largo
// solo pueda tener uno. Manda la segunda mitad —«publicar un plan sin su bump, no»—
// y aquí se cumple de forma verificable: la 0.46.0 ya está escrita en la fila de
// public.schema_version de UAT (journal 2026-08-28, `version=0.46.0
// content_hash=195f1659f1371310 skipped=false`), así que la 0085 es la primera
// migración que cambia el esquema por encima de una versión YA PUBLICADA. Dejarla
// sin bump haría que esa fila afirmara 0.46.0 sobre un esquema que ya no es el suyo
// — exactamente el argumento con el que la 0.46.0 se separó de la 0.45.0.
//
// El corolario que sí se sostiene: las olas que le queden al Plan 047 (T-A2…T-A8,
// las pantallas) pueden añadir migraciones bajo ESTE número mientras la 0.47.0 no
// se despliegue. En cuanto se despliegue, vuelve a aplicar lo de arriba.
const SchemaVersion = "0.47.0"

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
