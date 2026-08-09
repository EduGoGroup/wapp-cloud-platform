-- ============================================================
-- 0052: Las COSTURAS del evento en las tablas que ya existen (Plan 043 · Ola 1 ·
-- T1.3, design.md §2; ADR-0029 «Enmienda 2 (2026-08-05, noche)» E-6; D-043.2,
-- D-043.4, D-043.7, D-043.13).
--
-- Cuatro ALTER aditivos, ni una tabla nueva (esas nacieron en la 0051). Son las
-- cuatro preguntas que el modelo de evento le hace al esquema viejo:
--
--   1. flow_state.event_id                       -> ¿cuál es el evento ACTIVO de
--      esta conversación? (D-043.4) «Activo» NO es un estado del evento: es un
--      puntero de la conversación, y por eso vive aquí y no en un cuarto valor
--      de conversation_events.status.
--   2. tenant_settings.event_inactivity_ttl_seconds -> ¿cuánto silencio tolero
--      antes de dar por SUSPENDIDA la conversación? (E-6) EL reloj, en singular.
--   3. flow_triggers.event_kind                  -> ¿qué tipo de evento pare este
--      disparo? (D-043.2)
--   4. tenant_settings.event_history_ttl_seconds -> ¿cuánto tiempo tengo derecho
--      a GUARDAR lo que se dijo? (D-043.13) Retención de datos, no conversación.
--
-- ------------------------------------------------------------
-- HAY UN SOLO RELOJ DE CONVERSACIÓN, Y ES EL 2
-- ------------------------------------------------------------
-- Esta migración añade dos columnas que acaban en `_ttl_seconds` y NO son la
-- misma clase de cosa. Se deja escrito aquí porque confundirlas es el error caro:
--   · event_inactivity_ttl_seconds -> RELOJ DE CONVERSACIÓN. Pregunta «¿sigues
--     ahí?». Se mide desde conversation_events.last_activity_at y se REFRESCA en
--     cada interacción: una conversación activa nunca vence (chat infinito).
--   · event_history_ttl_seconds    -> RETENCIÓN DE DATOS. Pregunta «¿cuánto
--     tiempo tengo derecho a guardar tu texto?». Existiría aunque no hubiera
--     conversación, y su vencimiento no interrumpe nada: solo vacía contenido.
-- Y los dos vecinos que ya vivían en tenant_settings tampoco son relojes de
-- conversación: order_ttl_seconds quedó DEROGADO como causa de muerte (D-041.16,
-- noche del 2026-08-05: ya no vence NINGUNA solicitud) y conversation_ttl_seconds
-- fue RECLASIFICADO —ni derogado ni colapsado— a RECOLECCIÓN DE BASURA del
-- flow_state (ADR-0029 Enmienda 4 · E-9.2 y D-043.16, que CIERRAN MD-043.7): sale
-- de la tabla de relojes de conversación de E-6 porque al vencer lo único que hace
-- es soltar el puntero de nodo del motor (rt.store.Delete) — no toca intakes, no
-- toca conversation_events y no emite ningún efecto de negocio. Se sigue leyendo y
-- se sigue obedeciendo, ahora con una guarda: SOLO se evalúa cuando
-- flow_state.event_id IS NULL; con evento vivo manda event_inactivity_ttl_seconds
-- y nadie más.
--
-- ------------------------------------------------------------
-- IDEMPOTENCIA
-- ------------------------------------------------------------
-- ADITIVA e IDEMPOTENTE: el runner es hash-based FULL-REPLAY (re-aplica TODOS los
-- structure/*.sql al cambiar el hash de cualquiera); ADD COLUMN IF NOT EXISTS
-- garantiza re-aplicación N veces sin daño ni pérdida de filas. NO clean-slate.
--
-- Los dos INTEGER entran NOT NULL DEFAULT sobre tablas VIVAS: es seguro porque el
-- default no es una decisión de negocio disfrazada sino el comportamiento de
-- arranque (7200 = las 2 h de plataforma; 0 = sin poda, retrocompatible), así que
-- ninguna fila existente queda mintiendo. Las dos columnas NULLables (event_id,
-- event_kind) lo son a propósito: «esta conversación no tiene evento activo» y
-- «este disparo no pare ningún evento» son el caso NORMAL, no un desconocido.
--
-- ------------------------------------------------------------
-- SIN GRANTS
-- ------------------------------------------------------------
-- Igual que la 0051: ninguna de estas cuatro columnas estrena endpoint. El único
-- de la materia (POST /api/v1/conversation-events/{id}/cancel) es de la Ola 4 y
-- es ahí donde se fija el nombre de su scope; sembrar aquí un grant inventado
-- dejaría un grant muerto con UUID fijo. Cuando la Ola 4 fije el scope, el grant
-- va en un fichero aparte al estilo de 0042_intakes_grants.sql.
--
-- ------------------------------------------------------------
-- LO QUE ESTA MIGRACIÓN NO HACE, A PROPÓSITO
-- ------------------------------------------------------------
-- 1. NO añade ni amplía ningún CHECK en flow_triggers.kind. VERIFICADO: no
--    existe — la 0023 lo declara `TEXT NOT NULL` con la lista solo en un
--    comentario, y ni la 0024 ni la 0027 lo añadieron. Por eso `kind='event_start'`
--    (D-043.2) entra sin tocar el esquema. Con esto queda SALDADA la verificación
--    que el plan arrastraba pendiente.
-- 2. NO crea `event_wait_ttl_seconds`. Esa columna —TTL de ESPERA con vencimiento
--    absoluto— quedó DEROGADA la noche del 2026-08-05, ANTES de existir: E-6 la
--    sustituyó por el reloj de INACTIVIDAD de arriba. No hay nada que migrar ni
--    que renombrar; la cadena no debe aparecer en el repo.
-- 3. NO pone FK en flow_state.event_id hacia conversation_events. Es deliberado:
--    una FK obligaría a decidir aquí qué le pasa al puntero cuando el evento se
--    borra, y la respuesta correcta («el puntero se apaga, la conversación
--    sigue») la escribe el código al cerrar/cancelar el evento. Un ON DELETE SET
--    NULL parecería lo mismo y no lo es: convertiría un cambio de estado de
--    negocio en un efecto colateral del borrado físico, que aquí no ocurre nunca
--    (INV-09: nada se borra).
-- 4. NO toca ninguna fila: sin backfill y sin seeds. Las conversaciones vivas
--    arrancan con event_id NULL, que es exactamente lo que son hasta que un
--    evento nazca (E-6, nacimiento tardío).
-- 5. NO reescribe los COMMENT de order_ttl_seconds ni de conversation_ttl_seconds.
--    Cada archivo manda sobre lo suyo en CADA replay (criterio de 0045): tocarlos
--    desde aquí dejaría mintiendo al archivo donde cualquiera busca la columna.
--    Lo que sí se hace es NOMBRARLOS en el COMMENT del reloj nuevo, para que
--    quien lea uno encuentre la distinción sin tener que adivinarla.
-- 6. NO toca la migración 0034 y NO colapsa conversation_ttl_seconds con el reloj
--    nuevo. Lo prohíbe expresamente ADR-0029 E-9.2: miden cosas distintas sobre
--    filas distintas —la vida útil de un puntero del motor (flow_state.updated_at)
--    frente a la ventana de un evento de negocio (last_activity_at)— y unificarlas
--    costaría tocar una migración con consumidores y tests vivos para ahorrar una
--    fila de configuración que el dueño nunca ve. La guarda `event_id IS NULL` que
--    esa reclasificación exige es CÓDIGO (T3.7), no esquema: aquí no cabe.
-- ============================================================

-- ------------------------------------------------------------
-- 1-4. Las cuatro costuras (design.md §2, migración N+1)
-- ------------------------------------------------------------
ALTER TABLE public.flow_state      ADD COLUMN IF NOT EXISTS event_id UUID;           -- D-043.4 (evento ACTIVO)
ALTER TABLE public.tenant_settings ADD COLUMN IF NOT EXISTS event_inactivity_ttl_seconds INTEGER
                                   NOT NULL DEFAULT 7200;                            -- D-043.7 / E-6 (2 h; 0 = sin vencimiento)
ALTER TABLE public.flow_triggers   ADD COLUMN IF NOT EXISTS event_kind TEXT;         -- D-043.2
ALTER TABLE public.tenant_settings ADD COLUMN IF NOT EXISTS event_history_ttl_seconds INTEGER
                                   NOT NULL DEFAULT 0;                               -- D-043.13 (retención del hilo)

-- ------------------------------------------------------------
-- 5. Registro de columnas
-- ------------------------------------------------------------
COMMENT ON COLUMN public.flow_state.event_id IS
    'Evento ACTIVO de esta conversación (conversation_events.id): el que el motor está atendiendo ahora mismo. "Activo" NO es un estado del evento (D-043.4) —por eso el puntero vive aquí y no como un cuarto valor de conversation_events.status—: un evento puede estar VIVO (status=''open'') sin ser el activo, y eso es justo lo que permite tener un carrito y una encuesta abiertos a la vez y saltar entre ellos por el despachador. NULL = la conversación no tiene evento activo, que es el estado NORMAL mientras nadie ha parido uno (E-6: el saludo no crea evento). SIN FK a propósito: apagar el puntero es una decisión del código al cerrar o cancelar, no un efecto colateral de un borrado que nunca ocurre (INV-09).';

COMMENT ON COLUMN public.flow_triggers.event_kind IS
    'Tipo de evento que pare este disparo (D-043.2): menu | cart | survey | media, el mismo vocabulario de conversation_events.kind. Lo usa kind=''event_start'', el disparo que en vez de limitarse a arrancar un flujo CREA el evento conversacional que lo va a contener. NULL = este disparo no pare ningún evento (el caso de keyword/fallback/escape de siempre, que siguen funcionando sin tocar nada). SIN CHECK, igual que flow_triggers.kind: el vocabulario lo fija el Registry de módulos del motor, y enchufar un módulo nuevo no debe costar una migración.';

COMMENT ON COLUMN public.tenant_settings.event_history_ttl_seconds IS
  'TTL de RETENCIÓN DE DATOS del historial del evento (conversation_event_messages), en segundos. Vencido, la poda perezosa vacía el contenido de la fila —body_enc/body_dek (nivel 2) Y payload (nivel 1: en claro tampoco es para siempre)— y sella purged_at; la fila y su marca entry_kind sobreviven (ADR-0034 §Decisión 2). Default TÉCNICO 0 = sin poda, retrocompatible y NO es una decisión de retención: el valor lo fija el Plan 046 (micro-duda MD-043.6). NO es un reloj de CONVERSACIÓN (ADR-0029 E-6): no pregunta "¿sigues ahí?" sino "¿cuánto tiempo tengo derecho a guardar tu texto?", y existiría aunque no hubiera conversación. El único reloj de conversación es event_inactivity_ttl_seconds.';

COMMENT ON COLUMN public.tenant_settings.event_inactivity_ttl_seconds IS
  'EL reloj de conversación (ADR-0029 E-6), en segundos: periodo de INACTIVIDAD tolerado entre interacciones de un evento (default de plataforma 7200 = 2 h; 0 = sin vencimiento, override explicito por empresa). Se mide desde conversation_events.last_activity_at y se REFRESCA en cada interacción: una conversación activa nunca vence (chat infinito). Al vencer NO muere el evento — el siguiente mensaje NO se ata al evento anterior: arranca conversación nueva con el automensaje de rescate, y el evento sigue open y rescatable. SUSTITUYE a event_wait_ttl_seconds (TTL de espera con vencimiento absoluto, derogado la noche del 2026-08-05). Distinto de order_ttl_seconds (DEROGADO como causa de muerte el 2026-08-05 noche: ya no vence NINGUNA solicitud, D-041.16), de event_history_ttl_seconds (RETENCIÓN de datos, no conversación) y de conversation_ttl_seconds, que NO es un reloj de conversación sino RECOLECCIÓN DE BASURA del flow_state (ADR-0029 Enmienda 4 · E-9.2 y D-043.16): al vencer solo suelta el puntero de nodo del motor —no toca intakes, ni conversation_events, ni emite efecto de negocio— y se evalúa SOLO cuando flow_state.event_id IS NULL; con evento vivo manda ESTE reloj y nadie más. Las dos claves NO se colapsan: miden cosas distintas sobre filas distintas (la vida útil de un puntero del motor frente a la ventana de un evento de negocio).';
