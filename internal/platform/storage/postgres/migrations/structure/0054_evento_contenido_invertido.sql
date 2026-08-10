-- ============================================================
-- 0054: La relación evento↔contenido se INVIERTE — el hijo declara a su padre
-- (Plan 043 · Ola 4.5 · T4.5.2; D-043.21, D-043.22; cierra el ANALISIS-043).
--
-- EL PORQUÉ, en una frase: el e2e vivo del 2026-08-10 demostró que
-- `conversation_events.intake_id` no lo escribía NADIE en producción (ni el INSERT
-- de birthEvent ni ningún UPDATE) — T4.3 estaba inerte, INV-17 roto y el filtro
-- `content` de REQ-28 mentía. El diagnóstico completo, con las cinco opciones y
-- sus costes, está en docs/ANALISIS-043-evento-vs-contenido.md.
--
-- LA DOCTRINA (D-043.21): el padre (`conversation_events`) NO tiene columnas de
-- ningún hijo. Es el CONTENIDO quien declara de qué conversación nació:
-- `intakes.event_id` y `survey_results.event_id` (FK a conversation_events.id).
-- Con la FK del lado del hijo exigida al nacer, el huérfano se vuelve IMPOSIBLE
-- POR CONSTRUCCIÓN: una solicitud no puede existir sin declarar su conversación —
-- el INSERT revienta; no es «acordarse de escribir». Y compone: un quinto tipo con
-- contenido (pagos, reservas) declara su event_id y punto; el padre no cambia ni
-- una columna ni una consulta. Es la dirección que el Plan 044 ya asumió
-- (intake_jobs.event_id NOT NULL).
--
-- LAS CUATRO PIEZAS:
--   1. intakes.event_id        — FK obligatoria para toda fila NUEVA, con índice
--                                ÚNICO parcial: un evento tiene A LO SUMO un
--                                contenido durable (E-8: evento y pedido son la
--                                misma cosa; el rescate REUSA el mismo intake,
--                                REQ-26b). Si un plan futuro necesita N contenidos
--                                por evento, que vuelva a D-043.21 con el caso en
--                                la mano.
--   2. survey_results.event_id — mismo patrón SIN único: es TRAZABILIDAD (hoy el
--                                resumen de encuesta correlaciona por timestamp,
--                                summary_sources.go — frágil), no «contenido que
--                                puede morir» (D-043.18: el contenido de la
--                                encuesta es el propio flujo).
--   3. DROP de conversation_events.intake_id — la columna que nacía muerta.
--                                No estamos en producción: se borra sin duelo y
--                                sin backfill (no hay dato del que rellenar).
--   4. La vista public.event_content (D-043.22) — el padre pregunta por su
--                                contenido SIN conocer a nadie: el paquete
--                                `events` deja de incrustar el literal
--                                `i.status = 'open'` (vocabulario de un dominio
--                                ajeno, por string, sin compilador que lo vigile)
--                                y pregunta por un vocabulario genérico de TRES
--                                estados: alive | settled | discarded.
--
-- POR QUÉ `CHECK … NOT VALID` y no NOT NULL: las filas anteriores a esta
-- migración no tienen ligadura de la que rellenar el dato, y NO se inventan
-- ligaduras retroactivas. NOT VALID obliga a TODA fila nueva (INSERT y UPDATE se
-- validan siempre; lo único que se salta es el escaneo del legado) y tolera lo
-- que ya estaba. El legado queda visible: event_id IS NULL = fila pre-0054.
--
-- ⚠️ MEDIDO, no supuesto (2026-08-10, contra PG16): «tolera el legado» significa
-- EN REPOSO. Postgres valida un CHECK NOT VALID también en el UPDATE de filas
-- viejas, así que una fila legada NO puede cambiar de status (descarte del dueño,
-- reflejo CRM…) sin declarar a su padre en el MISMO UPDATE — la reparación
-- (SET event_id=…, status=…) sí pasa. Consecuencia registrada como hallazgo de la
-- Ola 4.5 para decidir con orden; si resulta inaceptable, la alternativa es un
-- trigger BEFORE INSERT o validar solo en el dominio — no se cambia en caliente.
--
-- POR QUÉ la FK NO lleva ON DELETE CASCADE (ni SET NULL): aquí nada se borra
-- (INV-09 — nada muere por tiempo y nada se borra; el abandono es un status, no
-- un DELETE). La FK con NO ACTION convierte cualquier intento de borrar un evento
-- con contenido en un error de base — que es exactamente lo que INV-09 quiere.
--
-- POR QUÉ VISTA y no columna proyectada ni tabla puente (D-043.22): la columna
-- denormaliza — si el hijo no reporta, el padre miente, que es la clase EXACTA
-- del fallo que nos trajo aquí; la tabla física es lo mismo repartido. La vista
-- no se sincroniza: SE DERIVA. Cero estado nuevo que pueda quedarse viejo.
--
-- ------------------------------------------------------------
-- EL MAPEO DE ESTADOS, VERIFICADO CONTRA LA VERDAD (no de memoria)
-- ------------------------------------------------------------
-- `intakes.status` NO tiene CHECK en BD A PROPÓSITO (0041, COMMENT de la
-- columna): la máquina de estados vive en el código (internal/intakes/status.go).
-- Claves canónicas: open | pending_approval | needs_info | confirmed |
-- deposit_requested | deposit_paid | settled | cancelled | rejected | abandoned;
-- más DOS legadas que no se migran: closed (la escribe el módulo cart desde el
-- Plan 016; se lee como confirmed) y expired (del reloj derogado, D-041.16).
-- El CASE de la vista los proyecta al vocabulario genérico:
--   open              → alive     (lo único rescatable: nadie lo ha tocado)
--   abandoned         → discarded (dejó de estar vivo sin que nadie lo resolviera:
--                                  cancelación del evento o descarte del dueño)
--   TODO LO DEMÁS     → settled   (un humano ya se pronunció: pending_approval,
--                                  needs_info, confirmed, closed, deposit_requested,
--                                  deposit_paid, settled, cancelled, rejected,
--                                  expired — para el evento da igual CÓMO terminó;
--                                  esa granularidad es del dominio intakes)
-- El mapeo vive AQUÍ, del lado del contenido: si `intakes` cambia su vocabulario,
-- el sitio que hay que tocar es suyo y es uno.
--
-- ------------------------------------------------------------
-- IDEMPOTENCIA
-- ------------------------------------------------------------
-- ADITIVA (salvo el DROP, que es el punto) e IDEMPOTENTE: el runner es hash-based
-- FULL-REPLAY (re-aplica TODOS los structure/*.sql al cambiar el hash de
-- cualquiera). ADD COLUMN IF NOT EXISTS + DROP/ADD CONSTRAINT IF EXISTS (patrón
-- 0025/0029/0045) + CREATE UNIQUE INDEX IF NOT EXISTS + DROP COLUMN IF EXISTS +
-- CREATE OR REPLACE VIEW garantizan re-aplicación N veces sin daño ni pérdida de
-- filas. En una base FRESCA la 0051 crea conversation_events CON intake_id y esta
-- migración lo dropea en la misma corrida: el estado final es idéntico al de una
-- base migrada por pasos. NO clean-slate.
--
-- CERO PII / CERO llaves: identificadores técnicos (UUID) y una vista de
-- proyección de estados. Nada que cifrar, nada que filtrar.
-- ============================================================

-- ------------------------------------------------------------
-- 1. intakes declara a su padre (D-043.21)
-- ------------------------------------------------------------

ALTER TABLE public.intakes
    ADD COLUMN IF NOT EXISTS event_id UUID REFERENCES public.conversation_events(id);

-- Obligatoria para toda fila NUEVA, tolerante con el legado (NOT VALID: valida
-- INSERT/UPDATE siempre, se salta solo el escaneo de las filas que ya estaban).
ALTER TABLE public.intakes
    DROP CONSTRAINT IF EXISTS intakes_event_id_required_chk;
ALTER TABLE public.intakes
    ADD CONSTRAINT intakes_event_id_required_chk CHECK (event_id IS NOT NULL) NOT VALID;

-- E-8 como RESTRICCIÓN y no como validación olvidable: un evento tiene A LO SUMO
-- un contenido durable (el rescate reusa el MISMO intake, REQ-26b; las revisiones
-- del 044 cuelgan del intake, no del evento). Parcial: el legado sin ligadura no
-- ocupa sitio. Este índice sirve además el join de la vista event_content y el
-- UPDATE por event_id del abandono (T4.5.5) — no hace falta índice de apoyo aparte.
CREATE UNIQUE INDEX IF NOT EXISTS intakes_event_id_uidx
    ON public.intakes (event_id)
    WHERE event_id IS NOT NULL;

COMMENT ON COLUMN public.intakes.event_id IS
  'Evento conversacional (conversation_events.id) del que nació esta solicitud (D-043.21: el hijo declara a su padre; el padre NO tiene columnas de ningún hijo — la vieja conversation_events.intake_id murió en la 0054). La escribe el PROYECTOR del módulo cart al parir la fila (ensureOpenIntake, con el EventID que viaja en modules.EffectMeta). Obligatoria para toda fila nueva (CHECK NOT VALID); NULL = fila legada anterior a la 0054, sin ligadura conocida — no se inventan ligaduras retroactivas. ÚNICO parcial: un evento tiene a lo sumo un contenido durable (E-8); el rescate REUSA el mismo intake (REQ-26b). Sin ON DELETE a propósito: aquí nada se borra (INV-09).';

-- ------------------------------------------------------------
-- 2. survey_results declara a su padre (D-043.21) — trazabilidad, sin único
-- ------------------------------------------------------------

ALTER TABLE public.survey_results
    ADD COLUMN IF NOT EXISTS event_id UUID REFERENCES public.conversation_events(id);

ALTER TABLE public.survey_results
    DROP CONSTRAINT IF EXISTS survey_results_event_id_required_chk;
ALTER TABLE public.survey_results
    ADD CONSTRAINT survey_results_event_id_required_chk CHECK (event_id IS NOT NULL) NOT VALID;

COMMENT ON COLUMN public.survey_results.event_id IS
  'Evento conversacional (conversation_events.id) durante el que se respondió (D-043.21). Es TRAZABILIDAD, no «contenido que puede morir» (D-043.18: el contenido de la encuesta es el propio flujo, y por eso la encuesta NO entra en la vista event_content): sustituye la correlación frágil por timestamp del resumen de encuesta (summary_sources.go), que queda como fallback del legado. La escribe el proyector del módulo survey. Obligatoria para toda fila nueva (CHECK NOT VALID); NULL = fila legada anterior a la 0054. SIN índice único: una encuesta tiene N respuestas por evento.';

-- ------------------------------------------------------------
-- 3. El padre deja de conocer a UN hijo: adiós intake_id
-- ------------------------------------------------------------

-- No estamos en producción y la columna nacía muerta (nadie la escribía): se borra
-- sin duelo, sin backfill y sin ventana de convivencia. Su COMMENT muere con ella.
-- Quien la leía (events/store.go, runtime/events.go, event_lifecycle.go) se
-- recablea en T4.5.4/T4.5.5 sobre la vista y sobre AbandonByEvent.
ALTER TABLE public.conversation_events DROP COLUMN IF EXISTS intake_id;

-- ------------------------------------------------------------
-- 4. La vista event_content: el padre pregunta sin conocer a nadie (D-043.22)
-- ------------------------------------------------------------

-- Una sola rama hoy (intakes). Un módulo nuevo con contenido durable AÑADE su
-- rama con UNION ALL en SU migración — del lado del contenido, nunca del padre.
-- La sirve el índice parcial intakes_event_id_uidx (UUID en las dos puntas del
-- join, sin CAST).
CREATE OR REPLACE VIEW public.event_content (event_id, kind, ref, state) AS
SELECT event_id,
       'cart' AS kind,
       id     AS ref,
       CASE WHEN status = 'open'      THEN 'alive'
            WHEN status = 'abandoned' THEN 'discarded'
            ELSE 'settled' END AS state
  FROM public.intakes
 WHERE event_id IS NOT NULL;

COMMENT ON VIEW public.event_content IS
  'El contenido durable de cada evento conversacional, en vocabulario GENÉRICO (D-043.22): el padre (paquete events) pregunta «¿tiene contenido y en qué estado?» sin conocer tablas hijas ni sus vocabularios — antes incrustaba el literal i.status=''open'', el vocabulario de un dominio ajeno por string. DERIVADA, jamás sincronizada: cero estado nuevo que pueda quedarse viejo (se descartó columna proyectada y tabla puente porque denormalizan — si el hijo no reporta, el padre miente, la clase exacta del fallo del ANALISIS-043). El predicado de rescatables (REQ-26c) es LEFT JOIN event_content c ON c.event_id=e.id → (c.event_id IS NULL OR c.state=''alive''); el filtro content=none|alive|any de REQ-28 sale de aquí. Hoy UNA rama (intakes); la encuesta NO entra (D-043.18: su contenido es el propio flujo; survey_results.event_id es trazabilidad). El mapeo de estados vive en la migración de cada rama, del lado del contenido.';
COMMENT ON COLUMN public.event_content.event_id IS
  'El evento dueño del contenido (conversation_events.id). Una fila por evento como máximo hoy (índice único parcial de intakes.event_id); el legado sin ligadura (event_id NULL en la tabla hija) NO aparece en la vista.';
COMMENT ON COLUMN public.event_content.kind IS
  'Qué CLASE de contenido es, en el vocabulario del catálogo de módulos: hoy solo ''cart'' (la rama intakes). No confundir con conversation_events.kind: un evento kind=cart sin solicitud parida aún NO tiene fila aquí.';
COMMENT ON COLUMN public.event_content.ref IS
  'Identidad de la fila de contenido en SU tabla (hoy intakes.id, UUID): lo que el DTO del listado expone como content_ref DERIVADO del join —nunca almacenado— para que el dueño navegue evento→solicitud.';
COMMENT ON COLUMN public.event_content.state IS
  'Estado del contenido en vocabulario genérico de TRES valores: alive (nadie lo ha tocado; el evento sigue rescatable, D-043.18) | discarded (dejó de estar vivo sin resolverse: abandoned) | settled (un humano ya se pronunció: todo lo demás — pending_approval, needs_info, confirmed, closed, deposit_requested, deposit_paid, settled, cancelled, rejected, expired). La granularidad fina es del dominio del contenido; el padre solo necesita estas tres.';
