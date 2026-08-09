-- ============================================================
-- 0051: El EVENTO conversacional y su historial (Plan 043 · Ola 1 · T1.1,
-- design.md §2; ADR-0029 + su «Enmienda 2 (2026-08-05, noche)» E-6; ADR-0034).
--
-- Dos tablas NUEVAS. Ninguna tabla existente se toca aquí: las costuras
-- (flow_state.event_id, los dos TTL de tenant_settings, flow_triggers.event_kind)
-- van en la 0052, para que este archivo se pueda leer entero como «lo que nace».
--
--   * public.conversation_events         — la instancia VIVA de una capacidad
--     (carrito, encuesta, menú, media) para UNA conversación (tenant, sesión,
--     contacto). Nace TARDE: el saludo no crea evento (E-6).
--   * public.conversation_event_messages — el historial DEL EVENTO. NO es una
--     transcripción del chat: guarda DECISIONES, los RESÚMENES que emitimos
--     nosotros y —solo con el pipeline LLM— el texto literal, cifrado.
--
-- ------------------------------------------------------------
-- LO QUE «ACTIVO», «SUSPENDIDO» Y «VIVO» SIGNIFICAN AQUÍ
-- ------------------------------------------------------------
-- No son sinónimos y solo uno es una columna:
--   vivo       -> status='open'. Lo dice ESTA tabla.
--   activo     -> flow_state.event_id apunta a él (D-043.4). Lo dice la 0052,
--                 no un cuarto valor de status: un evento vivo puede no ser el
--                 que el motor está atendiendo ahora mismo.
--   suspendido -> DERIVADO (E-6, D-043.7): status='open' AND
--                 now() - last_activity_at > tenant_settings.event_inactivity_ttl_seconds.
--                 No se persiste, no hay cron y no hay columna awaiting_until.
--
-- ------------------------------------------------------------
-- LOS TRES NIVELES DEL ADR-0034 (por qué hay DOS caminos de contenido)
-- ------------------------------------------------------------
-- El historial no trata todo el contenido igual, y esa es la decisión central de
-- la tabla de mensajes:
--   nivel 1 · decision | summary -> `payload` JSONB EN CLARO. Es dato de negocio
--     CUANTIFICABLE («cuántos piden sin sal»); cifrarlo destruiría su valor sin
--     proteger a nadie. Consultable y agregable por SQL A PROPÓSITO.
--   nivel 2 · message           -> body_enc/body_dek/body_kek_id CIFRADO con el
--     patrón EXISTENTE de public.contacts (0006/0007, crypto/field_cipher.go):
--     envelope AES-256-GCM con DEK FRESCA POR VALOR envuelta por la KEK del
--     KeyProvider (Planes 011/012). Sin índice ciego: por el cuerpo no se busca
--     ni se deduplica. Solo con llm_intake activo.
--   nivel 3 · la charla que no decide nada («hola Herminia, ¿cómo estás?»)
--     -> NO SE GUARDA. No hay fila.
-- El CHECK de grado impide mezclar el nivel 1 y el 2 en la misma fila; lo que NO
-- puede impedir es que alguien meta PII dentro del JSON del nivel 1: eso lo
-- sostienen el código y la revisión (ADR-0034).
--
-- ------------------------------------------------------------
-- IDEMPOTENCIA
-- ------------------------------------------------------------
-- ADITIVA e IDEMPOTENTE: el runner es hash-based FULL-REPLAY (re-aplica TODOS los
-- structure/*.sql al cambiar el hash de cualquiera); CREATE TABLE/INDEX IF NOT
-- EXISTS + ADD COLUMN IF NOT EXISTS garantizan re-aplicación N veces sin daño ni
-- pérdida de filas. NO clean-slate.
--
-- ------------------------------------------------------------
-- SIN GRANTS
-- ------------------------------------------------------------
-- Estas tablas NO tienen endpoint propio en la Ola 1: el único que las expone
-- (POST /api/v1/conversation-events/{id}/cancel) es de la Ola 4 y es ahí donde se
-- fija el nombre de su scope. Los grants de este repo (0030/0040/0042 en fichero
-- aparte; 0033/0036 en bloque al final de la migración de su tabla) SIEMPRE
-- acompañan a un endpoint con scope ya definido: sembrar aquí un `events.read`
-- inventado dejaría un grant muerto con UUID fijo que después habría que
-- corregir. Cuando la Ola 4 fije el scope, el grant va en un fichero aparte al
-- estilo de 0042_intakes_grants.sql.
--
-- ------------------------------------------------------------
-- LO QUE ESTA MIGRACIÓN NO HACE, A PROPÓSITO
-- ------------------------------------------------------------
-- 1. NO crea `event_no`. El correlativo por conversación lo DEROGÓ E-2: si solo
--    puede haber un evento vivo por tipo, no hay nada que desambiguar y un
--    número que el cliente teclearía por WhatsApp sería una vía de error. Lo que
--    se le enseña a un humano es `history_id`.
-- 2. NO crea `awaiting_until`. El reloj de E-6 es de INACTIVIDAD y se ancla en
--    `last_activity_at`, que ya hay que tocar en cada interacción; un vencimiento
--    absoluto de la espera sobra cuando el reloj se refresca solo. La columna
--    `event_wait_ttl_seconds` que la acompañaba quedó derogada ANTES de existir.
-- 3. NO añade un cuarto valor a `status` para «pausado»/«vencido»: son estados
--    derivados (arriba), y persistirlos obligaría a un barrido que el ADR-0003
--    no admite.
-- 4. NO toca tablas existentes ni siembra datos: sin seeds, sin backfill. Un
--    evento nace cuando la conversación lo pare, nunca por migración.
-- 5. Los CHECK inline (`status`, `role`, `entry_kind`, `origin`) son aceptables
--    porque estas dos tablas NACEN aquí: no existe ninguna base con la tabla ya
--    creada y una lista más corta. Pero AMPLIARLOS después editando este archivo
--    NO FUNCIONARÍA: `CREATE TABLE IF NOT EXISTS` no toca una tabla que ya
--    existe, así que el CHECK quedaría congelado en la lista del día que se creó
--    (documentado en 0045:99-106 y 0046:69-72). Quien añada un `entry_kind` o un
--    `origin` nuevo lo hace con `ALTER TABLE ... DROP CONSTRAINT IF EXISTS` +
--    `ADD CONSTRAINT`, como la 0045 hizo con intake_revisions_kind_check.
-- ============================================================

-- ------------------------------------------------------------
-- 1. El evento conversacional (D-043.1, D-043.5, E-2, E-6)
-- ------------------------------------------------------------

-- El evento conversacional: una instancia viva de una capacidad (carrito, encuesta, menú)
-- para UNA conversación (tenant, sesión, contacto). ADR-0029 + enmienda 2026-08-05.
-- NO lleva correlativo: E-2 lo derogó (uno vivo por tipo ⇒ nada que desambiguar).
-- "Activo" NO es un estado: lo dice flow_state.event_id (D-043.4).
-- "Suspendido" TAMPOCO es un estado: es DERIVADO (E-6, D-043.7) —
--   status='open' AND now() - last_activity_at > event_inactivity_ttl_seconds.
-- Por eso NO existe columna awaiting_until: el reloj es de INACTIVIDAD y se ancla
-- en last_activity_at, que ya hay que tocar en cada interacción.
CREATE TABLE IF NOT EXISTS public.conversation_events (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID        NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    session_id       TEXT        NOT NULL,
    contact_id       UUID        NOT NULL,          -- OPACO: contacts.contact_id (Plan 010/ADR-0017)
    kind             TEXT        NOT NULL,          -- 'menu'|'cart'|'survey'|'media' (de fábrica, INV-07)
    history_id       TEXT        NOT NULL,          -- '<tipo>-YYYY-MM-DD-HHMM' (E-3); NUNCA se teclea por WhatsApp
    status           TEXT        NOT NULL DEFAULT 'open'
                     CHECK (status IN ('open','closed','cancelled')),   -- D-043.5; sin 'paused'/'expired'
    flow_id          TEXT        NOT NULL,
    flow_version     INTEGER     NOT NULL,
    intake_id        UUID,                          -- nullable; liga intakes.id si es un carrito (ADR-0031)
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),  -- nacimiento TARDÍO: no al saludar (E-6)
    last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now(),  -- ← EL RELOJ (E-6): se refresca en CADA interacción
    closed_at        TIMESTAMPTZ                    -- instante de la muerte explícita (closed|cancelled)
);

-- E-2, como RESTRICCIÓN y no como validación olvidable: un solo evento VIVO por tipo
-- en la misma conversación. Los terminales no ocupan el tipo ⇒ nada se borra (INV-09).
CREATE UNIQUE INDEX IF NOT EXISTS conversation_events_one_alive_per_kind_idx
    ON public.conversation_events (tenant_id, session_id, contact_id, kind)
    WHERE status = 'open';

-- Listado del despachador y del AUTOMENSAJE de rescate (D-043.14): los eventos vivos de
-- la conversación, ordenados por actividad. Sirve también la condición DERIVADA de
-- "suspendido" (E-6): no hace falta índice aparte porque se evalúa sobre estas mismas filas.
CREATE INDEX IF NOT EXISTS conversation_events_alive_idx
    ON public.conversation_events (tenant_id, session_id, contact_id, last_activity_at DESC)
    WHERE status = 'open';

-- Historial y soporte (E-3): buscar por el ID que se le dio al dueño / al CRM.
CREATE INDEX IF NOT EXISTS conversation_events_history_idx
    ON public.conversation_events (tenant_id, history_id);

-- ------------------------------------------------------------
-- 2. El historial DEL EVENTO, graduado (D-043.13, D-043.19, ADR-0034)
-- ------------------------------------------------------------

-- Historial DEL EVENTO. NO es una transcripción del chat (D-043.13, reescrito 2026-08-05 tarde):
-- guarda DECISIONES TOMADAS ('decision'), los RESÚMENES que emitimos NOSOTROS ('summary', E-4)
-- y —SOLO si el tenant tiene el pipeline LLM— el texto literal ('message'). La charla que no
-- decide nada (el "hola Herminia, ¿cómo estás?") NO SE GUARDA: nivel 3 del ADR-0034.
-- Tratamiento GRADUADO (ADR-0034 §Decisión 1, tres niveles) — la versión anterior de este plan
-- decía "todo el cuerpo cifrado" y quedó MATIZADA:
--   · decision/summary → payload JSONB EN CLARO (nivel 1): dato de negocio CUANTIFICABLE
--     ("cuántos piden sin sal"); cifrarlo destruye su valor sin proteger a nadie.
--   · message         → body_enc/body_dek/body_kek_id CIFRADO (nivel 2): texto libre que puede
--     arrastrar identidad ("deposítamelo a la cuenta XYZ").
CREATE TABLE IF NOT EXISTS public.conversation_event_messages (
    id           BIGSERIAL   PRIMARY KEY,
    event_id     UUID        NOT NULL REFERENCES public.conversation_events(id) ON DELETE CASCADE,
    seq          INTEGER     NOT NULL,              -- orden dentro del evento
    role         TEXT        NOT NULL CHECK (role IN ('client','business','system')),
    entry_kind   TEXT        NOT NULL DEFAULT 'decision'
                 CHECK (entry_kind IN ('decision','summary','message')),  -- ← LA MARCA (E-4)
    -- PROCEDENCIA (D-043.19, cierre de MD-044.2, 2026-08-06 noche). `role` dice DE QUIÉN ES la voz
    -- (y por eso el texto que el dueño PEGA entra con role='client': el pipeline lo trata igual que
    -- todo lo demás). `origin` dice POR DÓNDE ENTRÓ, que es una pregunta distinta y que `role` no
    -- puede responder sin mentirle al pipeline. El LLM NO lo lee; lo leen la auditoría y la app.
    -- Mismo papel que intake_revisions.created_by ('system'|'owner'|'crm'): sin PII, en claro.
    origin       TEXT        NOT NULL DEFAULT 'whatsapp'
                 CHECK (origin IN ('whatsapp','owner_pasted')),
    -- NIVEL 1 — EN CLARO y estructurado: la decisión tomada o el resumen que emitimos nosotros.
    -- Estructura, no prosa: {"lines":[{sku,label,qty,customization}]}, {"answers":[…]}.
    -- Es consultable y agregable por SQL a propósito (estadística de negocio). PII aquí NO va:
    -- si un valor identifica a una persona, su sitio es el camino cifrado (intake_buyer_data,
    -- contacts). NULL = podado por TTL.
    payload      JSONB,
    -- NIVEL 2 — CIFRADO con el patrón EXISTENTE de contacts (crypto/field_cipher.go; migraciones
    -- 0006_contacts_cifrado.sql / 0007_contacts_value_kek_id.sql): envelope AES-256-GCM con DEK
    -- FRESCA POR VALOR, envuelta por la KEK del KeyProvider (Planes 011/012). Sin índice ciego:
    -- por el cuerpo no se busca ni se deduplica (a diferencia de contacts.value_bidx).
    -- SOLO se puebla en filas entry_kind='message' y SOLO con llm_intake activo.
    -- NULL = no aplica (nivel 1) o podado por TTL.
    body_enc     BYTEA,                             -- nonce||ciphertext||tag del cuerpo
    body_dek     BYTEA,                             -- DEK por-valor envuelta por la KEK
    body_kek_id  TEXT,                              -- key_id de la KEK que envolvió body_dek (rotación)
    purged_at    TIMESTAMPTZ,                       -- instante de la poda por TTL (auditoría); NULL = vigente
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (event_id, seq),
    -- El grado NO es una convención olvidable: una decisión o un resumen JAMÁS traen cuerpo
    -- cifrado (no son texto libre), y un texto literal JAMÁS se cuela en el payload en claro.
    CONSTRAINT conversation_event_messages_grade_chk CHECK (
        (entry_kind IN ('decision','summary') AND body_enc IS NULL AND body_dek IS NULL)
     OR (entry_kind = 'message' AND payload IS NULL)
    )
);

-- Poda perezosa por TTL de retención: barrido por antigüedad dentro del evento.
CREATE INDEX IF NOT EXISTS conversation_event_messages_retention_idx
    ON public.conversation_event_messages (event_id, created_at)
    WHERE purged_at IS NULL;

-- ------------------------------------------------------------
-- 3. Registro de columnas
-- ------------------------------------------------------------

COMMENT ON TABLE public.conversation_events IS
  'EVENTO conversacional (ADR-0029 + Enmienda 2 del 2026-08-05 noche): una instancia VIVA de una capacidad (carrito, encuesta, menú, media) para UNA conversación (tenant, sesión, contacto). Nace TARDE —el saludo no crea evento (E-6)— por una de tres puertas: un trigger event_start, una intención LLM mapeada a event_kind o una elección en el despachador. Dato de NEGOCIO EN CLARO (ADR-0009): aquí NUNCA hay PII (el contacto viaja OPACO, ADR-0010) ni material criptográfico. El contenido de la conversación NO vive en esta tabla: vive graduado en conversation_event_messages (ADR-0034).';
COMMENT ON COLUMN public.conversation_events.id IS
  'Identidad técnica del evento (UUID). Es lo que referencian flow_state.event_id, conversation_event_messages.event_id y el endpoint de cancelación. NO es lo que se le enseña a un humano: eso es history_id.';
COMMENT ON COLUMN public.conversation_events.tenant_id IS
  'Tenant dueño del evento (FK a tenants, CASCADE). Aislamiento INV-8: sale del token, jamás del cuerpo, y toda consulta filtra por aquí.';
COMMENT ON COLUMN public.conversation_events.session_id IS
  'Sesión CloudLink en la que vive la conversación (id opaco de fleet, lo provee el Edge). Forma parte de la identidad de la conversación: el MISMO contacto en otra sesión es otra conversación y NO colisiona con el único parcial de E-2.';
COMMENT ON COLUMN public.conversation_events.contact_id IS
  'Identidad OPACA del contacto (contacts.contact_id, Plan 010 / ADR-0017). NUNCA un número ni un JID: el motor no conoce el identificador crudo de WhatsApp.';
COMMENT ON COLUMN public.conversation_events.kind IS
  'Capacidad que instancia el evento: menu | cart | survey | media (las de fábrica, INV-07). SIN CHECK a propósito: el vocabulario lo fija el Registry de módulos del motor (internal/flujos), y enchufar un módulo nuevo no debe costar una migración. Es la dimensión del único parcial: solo puede haber UN evento vivo POR TIPO en la misma conversación (E-2).';
COMMENT ON COLUMN public.conversation_events.history_id IS
  'ID de HISTORIAL legible, formato <tipo>-YYYY-MM-DD-HHMM en UTC (E-3): lo que se le enseña al dueño y viaja al CRM para hablar de "ese pedido". Se persiste (no se recalcula) porque un identificador que se recalcula deja de ser el mismo cuando cambia la fórmula. NUNCA se teclea por WhatsApp: el cliente elige por el despachador, no escribiendo un código.';
COMMENT ON COLUMN public.conversation_events.status IS
  'Ciclo de vida del evento: open (vivo) | closed (terminado bien) | cancelled (abandonado o cancelado). EXACTAMENTE tres (D-043.5). No hay paused ni expired: "suspendido" es DERIVADO de last_activity_at (E-6) y "activo" lo dice flow_state.event_id (D-043.4), no esta columna. Los terminales NO ocupan el tipo en el único parcial ⇒ el rastro se conserva y nada se borra (INV-09).';
COMMENT ON COLUMN public.conversation_events.flow_id IS
  'Flujo que ejecutaba la conversación cuando nació el evento. Se congela aquí para que el historial siga siendo legible aunque el flujo se edite o se retire después.';
COMMENT ON COLUMN public.conversation_events.flow_version IS
  'Versión del flujo con la que arrancó el evento. Misma razón que flow_state.flow_version: un evento no salta de versión a mitad de camino.';
COMMENT ON COLUMN public.conversation_events.intake_id IS
  'Solicitud (intakes.id) que este evento produjo, si es un carrito (ADR-0031). NULL = el evento todavía no ha parido solicitud o es de un tipo que no la produce (menú, encuesta). Sin FK a propósito: el evento y la solicitud tienen ciclos de vida distintos y el evento sobrevive al descarte de la solicitud.';
COMMENT ON COLUMN public.conversation_events.last_activity_at IS
  'EL reloj de conversación (ADR-0029 E-6): instante de la última interacción del evento. Se refresca en CADA interacción (cliente o negocio), de modo que una conversación activa NUNCA vence — el chat infinito. Un evento esta SUSPENDIDO cuando status=''open'' AND now() - last_activity_at > tenant_settings.event_inactivity_ttl_seconds: condición DERIVADA, evaluada perezosamente al llegar el entrante, SIN cron y SIN cuarto valor en status. No existe columna awaiting_until: un vencimiento absoluto de la espera sobra cuando el reloj se refresca solo.';
COMMENT ON COLUMN public.conversation_events.created_at IS
  'Nacimiento del evento, que es TARDÍO (ADR-0029 E-6): el saludo no crea evento. La fila nace cuando un event_start, una intención LLM mapeada a event_kind o una elección en el despachador lo paren — y el reloj arranca ahí, no al empezar la conversación.';
COMMENT ON COLUMN public.conversation_events.closed_at IS
  'Instante de la muerte EXPLÍCITA del evento (paso a closed o cancelled). NULL mientras siga open. No es un vencimiento: nada mata un evento por tiempo (E-6) — un evento inactivo sigue open y sigue siendo rescatable.';

COMMENT ON TABLE public.conversation_event_messages IS
  'Historial DEL EVENTO, GRADUADO (D-043.13, ADR-0034 §Decisión 1). NO es una transcripción del chat: la charla que no decide nada no se guarda (nivel 3). Lo que se guarda son las DECISIONES y los RESÚMENES que emitimos nosotros —estructurados y EN CLARO en payload, porque son negocio cuantificable— y, solo con el pipeline LLM activo, el texto LITERAL en body_enc, CIFRADO (envelope AES-256-GCM, patrón de public.contacts). El texto literal NUNCA está en claro en esta tabla, y lo cifrado no sale del borde de la app: no va a flow_events, ni a logs, ni al Edge, ni al proto de CloudLink (INV-13).';
COMMENT ON COLUMN public.conversation_event_messages.id IS
  'Identidad técnica de la fila (BIGSERIAL, append-only). El orden con significado dentro del evento lo da seq, no este contador global.';
COMMENT ON COLUMN public.conversation_event_messages.event_id IS
  'Evento (conversation_events.id) al que pertenece la entrada. CASCADE: borrado el evento, su historial no significa nada.';
COMMENT ON COLUMN public.conversation_event_messages.seq IS
  'Orden DENTRO del evento, 1..N y sin huecos. Se calcula MAX+1 en la transacción de escritura; el UNIQUE (event_id, seq) es lo que impide que dos escritores concurrentes numeren dos veces la misma posición y que el hilo se lea desordenado.';
COMMENT ON COLUMN public.conversation_event_messages.role IS
  'De QUIÉN es la voz: client (el cliente) | business (la empresa) | system (lo que emite la plataforma, p. ej. el resumen de E-4). Es un ROL, no una persona: aquí NUNCA va un identificador de usuario, un número ni un nombre. Por dónde ENTRÓ la fila es otra pregunta y la responde origin.';
COMMENT ON COLUMN public.conversation_event_messages.entry_kind IS
  'decision = decisión TOMADA por el cliente, estructurada y en claro (lo que se rescata sin LLM: "en tu último evento esto es lo que ya habías decidido"); summary = resumen determinista que emitimos NOSOTROS al cambiar de evento (ADR-0029 E-4, role=system); message = texto literal, SOLO con llm_intake y SOLO cifrado. Quien analice el hilo (Plan 044 o un humano) DEBE tratar summary como contexto, nunca como mensaje original: la decisión ya está en la tabla y ADEMAS aparece en el resumen — contarlo dos veces es creer que el usuario pidió 2 hamburguesas cuando pidió 1.';
COMMENT ON COLUMN public.conversation_event_messages.origin IS
  'POR DONDE entro la fila, que NO es lo mismo que de quien es la voz (eso lo dice role). whatsapp = llego por el canal; owner_pasted = lo tecleo/pego el dueno en la app (transcripcion externa, Plan 045 D-045.5). El texto pegado entra con role=''client'' A PROPOSITO —para que el pipeline lo trate igual que todo lo demas, sin una rama nueva— y esta columna es la que impide que se pierda el rastro de quien lo escribio. El LLM NO la lee (no entra en el prompt); la leen la auditoria, la comparacion original-vs-interpretado y la app del dueno. Sin PII. Analoga a intake_revisions.created_by.';
COMMENT ON COLUMN public.conversation_event_messages.payload IS
  'NIVEL 1 del ADR-0034 (EN CLARO): decisión tomada o resumen, ESTRUCTURADO. Va en claro a propósito porque es dato de negocio CUANTIFICABLE (estadística de "cuántos piden sin sal") y cifrarlo destruiría su valor sin proteger a nadie. No es un cajón de texto libre: lo que identifique a una persona va al camino cifrado. NULL = podado por TTL de retención.';
COMMENT ON COLUMN public.conversation_event_messages.body_enc IS
  'NIVEL 2 del ADR-0034: cuerpo LITERAL de la interacción CIFRADO (envelope AES-256-GCM, DEK por-valor envuelta por la KEK; patrón de contacts, internal/platform/crypto/field_cipher.go). Solo en filas entry_kind=message y solo con el pipeline LLM activo (sin LLM no se persiste la charla). El texto NUNCA está en claro en la fila. NULL = no aplica o podado por TTL de retención.';
COMMENT ON COLUMN public.conversation_event_messages.body_dek IS
  'DEK por-valor (32B) envuelta por la KEK del KeyProvider (Planes 011/012). Se desenvuelve en el borde de la app para descifrar body_enc.';
COMMENT ON COLUMN public.conversation_event_messages.body_kek_id IS
  'key_id de la KEK que envolvió body_dek (mismo discriminador de rotación que contacts.value_kek_id, migración 0007).';
COMMENT ON COLUMN public.conversation_event_messages.purged_at IS
  'Instante en que la poda perezosa por TTL vació el contenido de la fila: body_enc/body_dek (nivel 2) Y payload (nivel 1 — en claro tampoco es para siempre). La fila (seq, role, entry_kind, created_at) sobrevive: el rastro del evento no se pierde, el contenido sí (ADR-0034).';
COMMENT ON COLUMN public.conversation_event_messages.created_at IS
  'Momento en que se registró la entrada. Usa el DEFAULT now(). Es la antigüedad que mira la poda perezosa por TTL de retención (junto con purged_at, en el índice parcial de retención).';
