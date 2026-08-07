-- ============================================================
-- 0045: Ciclo de vida extendido de la SOLICITUD (Plan 041 · O4 · T4.1 + T4.1b +
-- T4.1c, ADR-0031, design.md §3). Seis cosas, todas ADITIVAS sobre tablas vivas:
--
--   1. public.intakes gana las dos marcas de la SEÑA (deposit_due_at,
--      deposit_reminded_at). Son marcas de tiempo, NO un reloj: nadie las barre con
--      un cron (ADR-0003 prohíbe el broker y D-041.12 el barrido); el recordatorio
--      es PEREZOSO — se evalúa cuando el contacto vuelve a hablar.
--   2. public.tenant_settings gana la config comercial del tenant (zonas de envío,
--      plantilla de seña, plazo de la seña y checklist de datos del comprador).
--   3. Nace public.intake_revisions: la NEGOCIACIÓN auditada. Cada revisión es una
--      foto de la solicitud en un momento (lo que armó el carrito, lo que
--      interpretó el LLM, lo que corrigió el dueño, lo que se aprobó). NO sustituye
--      a intake_items, que sigue siendo la verdad de LO VENDIDO.
--   4. Nace public.intake_buyer_data: los datos mínimos del comprador, CIFRADOS.
--   5. public.intake_items gana la PERSONALIZACIÓN de la línea (customization):
--      el «sin cebolla» que quien prepara tiene que leer (T4.1b, D-041.17).
--   6. public.intakes gana la INDICACIÓN del cliente para todo el pedido
--      (customer_note): el «dejarlo en portería» (T4.1c, D-041.19).
--   7. public.intake_items gana el índice ÚNICO PARCIAL que hace estructural la
--      regla «una solicitud tiene como mucho UNA línea de envío» (T4.3, D-041.11).
--   8. public.intakes gana el índice PARCIAL que hace barato el toque del
--      recordatorio de la seña (T4.4): sin él, cada mensaje entrante pagaría un
--      recorrido de la tabla para preguntar por algo que casi nunca hay.
--
-- LO QUE ESTA MIGRACIÓN NO HACE, a propósito:
--   * NO añade un CHECK de status a public.intakes. La 0041 lo dejó fuera
--     deliberadamente (ver su COMMENT sobre intakes.status): el ciclo extendido
--     añade estados y la validación vive en la máquina de estados del código
--     (internal/intakes/status.go), no en la tabla. Meterlo aquí obligaría a una
--     migración por cada estado nuevo y rompería las filas históricas.
--   * NO toca expires_at ni order_ttl_seconds. Su derogación como causa de muerte
--     (D-041.16) la ejecutó T4.7, que reescribió sus COMMENT en el archivo que las
--     CREÓ —0013 para tenant_settings.order_ttl_seconds, 0041 para
--     public.intakes.expires_at— y no aquí: un COMMENT es last-writer-wins en el
--     replay, así que las dos opciones dan la misma base, pero dejarlo aquí habría
--     dejado mintiendo al archivo donde cualquiera busca la columna. Es el mismo
--     criterio por el que el CHECK de `kind` de abajo va en DROP+ADD: cada archivo
--     manda sobre lo suyo en CADA replay.
--
-- CERO PII EN CLARO: intake_revisions guarda estado de NEGOCIO (skus, cantidades,
-- precios, el texto que se le renderizó al cliente) — jamás un número, un JID ni
-- material criptográfico. Lo que identifica a una persona va a intake_buyer_data y
-- va CIFRADO (ADR-0017): esa es toda la razón de que sean dos tablas y no una.
--
-- ADITIVA e IDEMPOTENTE: el runner es hash-based FULL-REPLAY (re-aplica TODOS los
-- structure/*.sql al cambiar el hash de cualquiera); ADD COLUMN IF NOT EXISTS /
-- CREATE TABLE IF NOT EXISTS / CREATE INDEX IF NOT EXISTS garantizan re-aplicación
-- N veces sin daño ni pérdida de filas. NO clean-slate.
-- ============================================================

-- ------------------------------------------------------------
-- 1. Las dos marcas de la seña en la solicitud (D-041.12)
-- ------------------------------------------------------------
-- Ambas NULLables: la inmensa mayoría de las solicitudes no llega nunca a pedir
-- seña, y un DEFAULT las obligaría a mentir con una fecha.
ALTER TABLE public.intakes ADD COLUMN IF NOT EXISTS deposit_due_at      TIMESTAMPTZ;
ALTER TABLE public.intakes ADD COLUMN IF NOT EXISTS deposit_reminded_at TIMESTAMPTZ;

COMMENT ON COLUMN public.intakes.deposit_due_at IS
    'Fecha límite de la SEÑA, fijada al pasar a deposit_requested (now + tenant_settings.deposit_due_days). NULL mientras no se haya pedido seña. NO es un TTL: pasarse de la fecha NO mata la solicitud (D-041.16, nada vence por tiempo); solo habilita el recordatorio.';
COMMENT ON COLUMN public.intakes.deposit_reminded_at IS
    'Último recordatorio de seña enviado al cliente. El recordatorio es PEREZOSO (D-041.12): se evalúa cuando el contacto vuelve a hablar, JAMÁS con un cron ni un barrido (ADR-0003). Esta marca es lo que impide recordarle dos veces en la misma conversación.';

-- El índice que hace barato el TOQUE del recordatorio (T4.4). Es PARCIAL sobre lo
-- que se busca —seña pedida y todavía sin recordar—, así que solo contiene las
-- poquísimas filas que pueden producir un mensaje; las demás no entran ni pesan.
--
-- Sirve a los dos caminos que preguntan: el del mensaje entrante, que busca por
-- (tenant, contacto) y corre en el camino CALIENTE de cada entrante del motor de
-- flujos, y el compare-and-swap por id. Sin él, cada mensaje de cualquier cliente
-- costaría un recorrido de public.intakes.
--
-- El predicado usa literales (no now()): un índice parcial exige expresiones
-- inmutables, y por eso el vencimiento se filtra fuera del índice, sobre el puñado
-- de filas que ya quedan.
CREATE INDEX IF NOT EXISTS idx_intakes_deposit_pending
    ON public.intakes (tenant_id, contact_id)
    WHERE status = 'deposit_requested' AND deposit_reminded_at IS NULL;

-- ------------------------------------------------------------
-- 2. Config comercial por tenant (D-041.11, D-041.12, D-041.13)
-- ------------------------------------------------------------
-- Los DEFAULT hacen que un tenant que nunca configuró nada siga funcionando: sin
-- zonas de envío la línea _shipping sale "por confirmar" (D-041.11), sin plantilla
-- de seña no se puede pedir seña, y sin buyer_fields no se pregunta nada antes de
-- cerrar. Ninguno de los tres es un error: son el estado de arranque.
ALTER TABLE public.tenant_settings ADD COLUMN IF NOT EXISTS shipping_zones   JSONB   NOT NULL DEFAULT '[]';
ALTER TABLE public.tenant_settings ADD COLUMN IF NOT EXISTS deposit_template TEXT    NOT NULL DEFAULT '';
ALTER TABLE public.tenant_settings ADD COLUMN IF NOT EXISTS deposit_due_days INTEGER NOT NULL DEFAULT 3;
ALTER TABLE public.tenant_settings ADD COLUMN IF NOT EXISTS buyer_fields     JSONB   NOT NULL DEFAULT '[]';

COMMENT ON COLUMN public.tenant_settings.shipping_zones IS
    'Zonas de envío del tenant como JSON (lista de {nombre, precio}). Alimenta la línea estándar _shipping (D-041.11), que está SIEMPRE presente en un presupuesto: vacío ⇒ la línea sale con precio 0 y la etiqueta "por confirmar", que es la verdad, no un error.';
COMMENT ON COLUMN public.tenant_settings.deposit_template IS
    'Plantilla de texto de la SEÑA del tenant (datos de la cuenta + instrucciones), con marcadores que la plataforma rellena. Vacía ⇒ el tenant no puede pedir seña. wApp NUNCA cobra ni concilia: esto es texto que se le manda al cliente (ADR-0031, fronteras duras).';
COMMENT ON COLUMN public.tenant_settings.deposit_due_days IS
    'Días de plazo de la seña (default 3): fija intakes.deposit_due_at al pasar a deposit_requested. Vencerlo NO mata la solicitud (D-041.16); solo habilita el recordatorio perezoso.';
COMMENT ON COLUMN public.tenant_settings.buyer_fields IS
    'Checklist de datos mínimos del comprador que se piden antes de cerrar, como JSON (p. ej. ["rut","direccion"]). wApp los recolecta SIN validación semántica (ADR-0031 §5) y los guarda CIFRADOS en intake_buyer_data. Vacío ⇒ no se pregunta nada.';

-- ------------------------------------------------------------
-- 3. Revisiones: la negociación auditada (ADR-0031 §3, D-041.25, D-041.18)
-- ------------------------------------------------------------
-- ESTE ES EL ÚNICO ESQUEMA DE LA TABLA. El Plan 044 la consume tal cual (escribe
-- kind interpreted/corrected/approved) y el 042 lee revision_no de aquí: ninguno la
-- redefine.
--
-- revision_no es un correlativo POR SOLICITUD (1..N), no un id global: quien mira
-- una solicitud quiere "la revisión 3 de este pedido", no "la 91847 del sistema".
-- El UNIQUE (intake_id, revision_no) es lo que lo garantiza — sin él, dos
-- escritores concurrentes numerarían dos veces la misma revisión y la auditoría
-- dejaría de ser un relato ordenado.
--
-- ON DELETE CASCADE: borrada la solicitud, su negociación no significa nada.
CREATE TABLE IF NOT EXISTS public.intake_revisions (
    id            BIGSERIAL   PRIMARY KEY,
    intake_id     UUID        NOT NULL REFERENCES public.intakes(id) ON DELETE CASCADE,
    revision_no   INTEGER     NOT NULL,
    kind          TEXT        NOT NULL,   -- el conjunto cerrado lo fija el CHECK de abajo
    payload       JSONB       NOT NULL,   -- versionado: {"version":1, ...}
    rendered_text TEXT,                   -- lo que se le mandó al cliente, si aplica
    created_by    TEXT,                   -- 'system' | 'owner' | 'crm' (sin PII)
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (intake_id, revision_no)
);

-- El CHECK de `kind` vive AQUÍ y no dentro del CREATE TABLE por una razón del
-- runner: `CREATE TABLE IF NOT EXISTS` no toca una tabla que ya existe, así que un
-- CHECK inline quedaría CONGELADO en la lista del día que se creó la tabla y
-- ampliarlo (T4.8 añade 'discarded', T4.9 'revalidated') no tendría efecto sobre
-- ninguna BD ya migrada. Con DROP + ADD, este archivo es la fuente autoritativa en
-- CADA replay: quien amplíe la lista edita ESTA línea y basta.
-- El nombre es el que Postgres habría generado para un CHECK inline sobre `kind`.
ALTER TABLE public.intake_revisions DROP CONSTRAINT IF EXISTS intake_revisions_kind_check;
ALTER TABLE public.intake_revisions ADD  CONSTRAINT intake_revisions_kind_check
    CHECK (kind IN ('cart', 'interpreted', 'corrected', 'approved', 'crm', 'discarded', 'revalidated'));

COMMENT ON TABLE public.intake_revisions IS
    'NEGOCIACIÓN auditada de una solicitud (ADR-0031 §3): una fila por versión (lo que armó el carrito, lo que interpretó el LLM, lo que corrigió el dueño, lo aprobado, lo descartado). NO sustituye a intake_items, que sigue siendo la verdad de lo VENDIDO: aquí vive el borrador y el rastro de cómo se llegó a él. Dato de NEGOCIO EN CLARO (ADR-0009). CERO PII: lo que identifique a una persona va cifrado a intake_buyer_data.';
COMMENT ON COLUMN public.intake_revisions.id IS
    'Identidad técnica de la fila (append-only, sin significado de negocio). Lo que se le enseña a un humano es revision_no.';
COMMENT ON COLUMN public.intake_revisions.intake_id IS
    'Solicitud (intakes.id) cuya negociación registra esta revisión. CASCADE: borrada la solicitud, su negociación no significa nada.';
COMMENT ON COLUMN public.intake_revisions.revision_no IS
    'Correlativo POR SOLICITUD, 1..N, en orden cronológico. Se calcula MAX+1 dentro de la transacción de escritura; el UNIQUE (intake_id, revision_no) es lo que impide que dos escritores concurrentes numeren dos veces lo mismo.';
COMMENT ON COLUMN public.intake_revisions.kind IS
    'Qué acto originó la revisión: cart (cierre del carrito numérico, Plan 016/041), interpreted | corrected | approved (pipeline LLM del Plan 044), crm (vuelta del puente, ADR-0032), discarded (descarte manual del pedido huérfano, D-041.18 — es lo que distingue en el embudo un abandoned por descarte de uno por cancelación de evento, sin un segundo valor de status), revalidated (gancho de revalidación del rescate, D-041.25: qué precios cambiaron y qué líneas se retiraron, más la prueba de que se le AVISÓ al cliente). Conjunto cerrado por CHECK: añadir un origen exige migración, que es la fricción que se quiere.';
COMMENT ON COLUMN public.intake_revisions.payload IS
    'Estado completo de la revisión, VERSIONADO ({"version":1, ...}). La versión va DENTRO del blob para que un lector viejo sepa que no entiende lo que lee en vez de interpretarlo mal. Para kind=cart: {"version":1,"total":…,"items":[{sku,label,qty,unit_price}]}.';
COMMENT ON COLUMN public.intake_revisions.rendered_text IS
    'Texto EXACTO que se le renderizó al cliente en esta revisión (la voz de la dueña), si lo hubo. Es la prueba de qué se le dijo; NULL cuando la revisión no produjo mensaje.';
COMMENT ON COLUMN public.intake_revisions.created_by IS
    'Quién produjo la revisión: system | owner | crm. Es un ROL, no una persona: aquí NUNCA va un identificador de usuario, un número ni un nombre.';
COMMENT ON COLUMN public.intake_revisions.created_at IS
    'Momento de la revisión. Usa el DEFAULT now().';

-- ------------------------------------------------------------
-- 4. Datos del comprador, cifrados (ADR-0031 §5, ADR-0017, D-041.13)
-- ------------------------------------------------------------
-- ⚠️ Esta tabla NO sigue el sketch del design §3 (`data_encrypted TEXT`), sino el
-- patrón REAL de PII cifrada de este repo (public.contacts, migraciones 0006/0007):
-- envelope encryption de tres piezas — dato cifrado + DEK envuelta + key_id de la
-- KEK que la envolvió. Un solo TEXT opaco no puede guardar lo que devuelve
-- crypto.FieldCipher.Encrypt (valueEnc, valueDEK, keyID) y, sobre todo, dejaría
-- estas filas FUERA de la rotación de KEK del Plan 012: sin data_kek_id no se sabe
-- con qué KEK desenvolver y el re-wrap incremental no puede localizarlas.
--
-- Sin índice ciego (a diferencia de contacts.value_bidx): aquí NUNCA se busca por
-- el contenido — se lee por intake_id y se entrega al puente. Un HMAC de búsqueda
-- que nadie consulta solo sería superficie de correlación.
--
-- QUÉ HAY DENTRO: el JSON del checklist de tenant_settings.buyer_fields tal como lo
-- dictó el cliente, SIN validación semántica (ADR-0031 §5: si el RUT viene mal, lo
-- corrige la gente del CRM; no es culpa de wApp).
CREATE TABLE IF NOT EXISTS public.intake_buyer_data (
    intake_id   UUID        PRIMARY KEY REFERENCES public.intakes(id) ON DELETE CASCADE,
    data_enc    BYTEA       NOT NULL,   -- envelope AES-256-GCM del JSON del comprador
    data_dek    BYTEA       NOT NULL,   -- DEK por-fila, envuelta por la KEK
    data_kek_id TEXT        NOT NULL,   -- key_id de la KEK que envolvió data_dek
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Localiza rápido las filas pendientes de rotación (por KEK vieja), igual que
-- idx_contacts_kek de la 0007.
CREATE INDEX IF NOT EXISTS idx_intake_buyer_data_kek
    ON public.intake_buyer_data (data_kek_id);

COMMENT ON TABLE public.intake_buyer_data IS
    'Datos mínimos del COMPRADOR de una solicitud (RUT, dirección…), CIFRADOS en reposo (ADR-0017 / D-041.13). Es la única tabla del ciclo de la solicitud que contiene PII: por eso está separada de intake_revisions y de intakes, que van en claro. JAMÁS se copian a flow_events ni al summary. Envelope de tres piezas, igual que public.contacts.';
COMMENT ON COLUMN public.intake_buyer_data.intake_id IS
    'Solicitud (intakes.id) a la que pertenecen los datos. PK: como mucho una fila por solicitud. CASCADE: borrada la solicitud, sus datos personales se van con ella.';
COMMENT ON COLUMN public.intake_buyer_data.data_enc IS
    'JSON del checklist del comprador cifrado con envelope AES-256-GCM (nonce fresco por fila). NUNCA está en claro en la fila.';
COMMENT ON COLUMN public.intake_buyer_data.data_dek IS
    'DEK por-fila (32B) envuelta por la KEK del keyring. Se desenvuelve para descifrar data_enc en el borde de la app; la DEK solo vive en memoria.';
COMMENT ON COLUMN public.intake_buyer_data.data_kek_id IS
    'key_id de la KEK que envolvió data_dek (Plan 012 §10.A). Sin esta columna la fila quedaría fuera de la rotación de KEK: es el discriminador del re-wrap incremental y lo que permite consultar pendientes por KEK en SQL.';
COMMENT ON COLUMN public.intake_buyer_data.created_at IS
    'Momento de la primera captura. Usa el DEFAULT now().';
COMMENT ON COLUMN public.intake_buyer_data.updated_at IS
    'Última escritura de la fila. La refresca tanto una corrección del dato como el re-wrap de la rotación de KEK (que NO descifra nada).';

-- ------------------------------------------------------------
-- 5. Personalización por línea (D-041.17, REQ-31, T4.1b)
-- ------------------------------------------------------------
-- «Sin cebolla» no es un ítem del catálogo ni un añadido facturable: es una
-- característica del producto de ESA línea que quien prepara tiene que leer. Va
-- aquí y no en una tabla hija porque una personalización no tiene vida propia —no
-- existe sin su línea y muere con ella—, y va EN CLARO (nivel 1 del ADR-0034)
-- porque es dato de negocio cuantificable: cifrarla destruiría su valor sin
-- proteger a nadie.
--
-- NOT NULL DEFAULT '' y no NULLable a propósito: "sin personalización" es la
-- cadena vacía, no un desconocido. Ninguna lectura tiene que distinguir dos formas
-- del mismo hecho, y las líneas ya escritas quedan válidas sin tocar una sola fila
-- (cero regresión: el blob del carrito viejo sigue cerrando igual).
--
-- INV-13: esta columna JAMÁS entra en el cálculo de unit_price, line_total ni
-- total. El perro caliente no es más barato por quitarle la cebolla.
ALTER TABLE public.intake_items ADD COLUMN IF NOT EXISTS customization TEXT NOT NULL DEFAULT '';

COMMENT ON COLUMN public.intake_items.customization IS
    'Personalización de la línea: instrucción de PRODUCCIÓN, no facturable ("sin sal", "sin cebolla"). En claro a propósito (ADR-0034 nivel 1). Quien recibe el pedido y quien lo prepara son personas distintas: si esto no viaja con la línea hasta quien prepara, se pierde y el cliente recibe el producto mal hecho. NO es un cajón de texto libre: lo que identifique a una persona va a intake_buyer_data, cifrado.';

-- ------------------------------------------------------------
-- 6. Indicación del cliente para TODO el pedido (D-041.19, REQ-33, T4.1c)
-- ------------------------------------------------------------
-- La HERMANA de customization, y son dos columnas y no una porque son dos cosas:
-- «sin cebolla» es RECETA y viaja pegada a SU línea (si estuviera aquí, nadie
-- sabría a cuál de las dos hamburguesas se refería); «dejarlo en portería» es
-- ENTREGA y no es de ninguna línea en particular (si estuviera allí, quedaría
-- escondida bajo un artículo cualquiera).
--
-- La escribe el cart numérico por la tecla 3 desde el resumen (nivel order_note) —
-- su primer productor SIN LLM, que es justo el tenant que la necesita— y mañana
-- también el pipeline del Plan 044. Las dos puertas comparten UNA regla de saneo:
-- cart.SanitizeNote (280 runas, sin controles, sin invisibles, una sola línea).
--
-- EN CLARO, pero POR COSTE y no por doctrina (D-041.23): es donde de verdad se
-- cuela la PII —«dejarlo en portería, calle Mayor 14»— y cifrarla exigiría tres
-- columnas más, generalizar el Rekey del Plan 012 (hoy clavado en public.contacts)
-- y descifrar en seis lectores. Queda como deuda anotada con disparador; la
-- contención de hoy es de DISEÑO: ranura corta, propósito declarado en pantalla,
-- advertencia explícita de no escribir datos personales (ADR-0034 §Decisión 1) y
-- la poda por retención del Plan 046.
--
-- NOT NULL DEFAULT '' por la misma razón que customization: "sin indicación" es la
-- cadena vacía, no un desconocido, y las solicitudes ya escritas quedan válidas
-- sin tocar una sola fila. INV-13: JAMÁS entra en el cálculo de total.
ALTER TABLE public.intakes ADD COLUMN IF NOT EXISTS customer_note TEXT NOT NULL DEFAULT '';

COMMENT ON COLUMN public.intakes.customer_note IS
    'Indicacion del CLIENTE para TODO el pedido ("sin gluten", "dejar en porteria"). Instruccion de PRODUCCION/ENTREGA, no facturable: jamas entra en total. En claro a proposito (ADR-0034 nivel 1). Max 280 runas, saneado (sin controles, sin saltos de linea). NO es un cajon de texto libre: direcciones y datos del comprador van a intake_buyer_data, cifrados.';

-- ------------------------------------------------------------
-- 7. Una sola línea de envío por solicitud (D-041.11, T4.3)
-- ------------------------------------------------------------
-- La línea de envío es un intake_items normal con sku '_shipping'. Que esté
-- EXACTAMENTE UNA VEZ es el criterio entero de la tarea, y el código lo sostiene
-- serializando por la cabecera de la solicitud (SELECT ... FOR UPDATE) antes de
-- decidir si inserta. Este índice es la otra mitad: convierte «dos envíos en el
-- mismo pedido» —el cliente pagando el reparto dos veces— en un error de escritura
-- en vez de en una fila de más que nadie mira.
--
-- Es PARCIAL a propósito: solo mira las líneas del sistema. Un pedido puede tener
-- dos líneas del mismo artículo de catálogo (dos empanadas pedidas por separado, o
-- la misma con dos personalizaciones distintas — D-041.20 parte líneas justo para
-- eso), así que un UNIQUE sobre (intake_id, sku) a secas rompería el carrito.
--
-- El prefijo '_' está reservado (el validador del import lo rechaza, T3.1): ningún
-- catálogo de tenant puede producir este sku, así que el índice no puede morder a
-- una línea de cliente. Y no puede fallar al crearse sobre una base viva: antes de
-- esta tarea NADIE escribía '_shipping', luego no hay duplicados históricos que
-- reventaran el replay.
CREATE UNIQUE INDEX IF NOT EXISTS intake_items_shipping_uniq
    ON public.intake_items (intake_id) WHERE sku = '_shipping';
