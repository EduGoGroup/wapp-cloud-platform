-- ============================================================
-- 0078: LA SEDE DEL BACKOFF DE intake_jobs — dónde se escribe «vuelve luego»
-- (Plan 044 · Ola 2 · T2.1; lo EXIGEN por escrito T2.5 y D-044.43).
--
-- QUÉ AÑADE, Y POR QUÉ NO ESTABA
-- ------------------------------------------------------------
-- La 0072 creó `intake_jobs` con su máquina de estados entera —aggregating →
-- pending → processing → done|failed— y con TODO lo que hace falta para saber
-- QUÉ hay que hacer... y nada para decir CUÁNDO. Un job que vuelve a `pending`
-- porque el provider está caído es hoy indistinguible de uno recién cerrado: el
-- claim se lo lleva otra vez en el acto, vuelve a fallar, y el bucle gira a la
-- velocidad del error. Eso NO es un reintento: es una tormenta.
--
-- Y no es una carencia que se pueda tapar en memoria. D-044.43 lo deja escrito:
-- `intake_jobs` es «la ÚNICA sede donde el registro pendiente necesita Postgres
-- (no memoria): aquí sí hay trabajo de minutos que un reinicio del Cloud no debe
-- tirar». Un backoff en un `map[string]time.Time` del worker se evapora en el
-- siguiente despliegue y todos los jobs castigados vuelven a la vez.
--
--   * attempts        — cuántas veces se ha intentado ya. Alimenta la política.
--   * next_attempt_at — a partir de cuándo la fila es reclamable.
--
-- EL GEMELO CANÓNICO ES public.webhook_outbox (0046), Y ESTO ES UN CALCO
-- ------------------------------------------------------------
-- La 0046 resolvió EXACTAMENTE este problema para las entregas del CRM, y lleva
-- meses medido en campo: mismas dos columnas, mismos tipos, mismos defaults,
-- mismo índice `(status, next_attempt_at)` y el mismo claim
-- (`WHERE status='pending' AND next_attempt_at <= now() … FOR UPDATE SKIP
-- LOCKED`, integrations/postgres.go). La 0072 ya declara en su cabecera que
-- `intake_jobs` «desempeña para el Plan 044 el mismo papel que webhook_outbox
-- (0046) para el 042»; esta migración termina de hacer verdad esa frase. NO se
-- inventa aquí un diseño nuevo —ni una tabla de reintentos aparte, ni un
-- `retry_after` en `artifacts`, ni un job especial— porque la casa ya tiene una
-- respuesta a esta pregunta y dos respuestas distintas al mismo problema son la
-- deuda que después nadie sabe cuál manda.
--
-- 🔴 LA DOCTRINA, LITERAL DE LA 0046:87 — «el backoff se implementa EMPUJANDO
-- ESTA MARCA, NO DURMIENDO EL WORKER». Un `time.Sleep` en el worker retiene la
-- goroutine, no sobrevive al reinicio, no lo ve nadie desde fuera y castiga a los
-- jobs que SÍ podían correr. Empujar `next_attempt_at` deja el worker libre, deja
-- el castigo escrito en la base y lo hace consultable por un operador.
--
-- LO QUE ESTA MIGRACIÓN NO TRAE, A PROPÓSITO
-- ------------------------------------------------------------
-- NO trae la POLÍTICA de reintentos (cuánto se empuja la marca, con qué curva,
-- cuántos intentos antes de `failed`) ni el worker que la aplica: eso es T2.5.
-- Aquí queda la SEDE y el claim que la respeta, y ni una decisión más. Tampoco
-- hay CHECK sobre `attempts`: el techo de intentos es política del worker y
-- fijarlo en el catálogo obligaría a migrar la base para cambiar un número.
--
-- 🔴 EL DEFAULT DE next_attempt_at ES `now()` Y ESO GOBIERNA TAMBIÉN A LAS FILAS
-- VIEJAS. `ADD COLUMN … NOT NULL DEFAULT` rellena las filas existentes (Postgres
-- ≥ 11), así que los jobs que ya estén en `pending` cuando esto se aplique
-- quedan con la marca puesta en el instante de la migración — es decir,
-- RECLAMABLES DE INMEDIATO, que es justo lo que se quiere: nadie se queda
-- esperando por haber entrado antes que la columna. La alternativa (NULLable +
-- backfill) exigiría además un `COALESCE` en el claim para siempre. Verificado
-- con filas fabricadas ANTES de la migración, no sobre una tabla vacía:
-- TestIntegration_Backoff_FilasPreexistentesQuedanReclamables.
--
-- ADITIVA e IDEMPOTENTE: el runner es hash-based FULL-REPLAY (re-aplica TODOS los
-- structure/*.sql al cambiar el hash de cualquiera). `ADD COLUMN IF NOT EXISTS`
-- va ANTES de los `COMMENT ON COLUMN` y no dentro de un `CREATE TABLE`: la 0072
-- ya creó la tabla, así que un `CREATE TABLE IF NOT EXISTS` no repondría nada, y
-- un `COMMENT` sobre una columna que no existe ABORTA EL ARRANQUE. Re-aplicarla
-- N veces no toca un solo valor: sobre la columna ya creada el ALTER es un no-op
-- y el DEFAULT no vuelve a evaluarse.
--
-- SIN BUMP DE SchemaVersion: el Plan 044 lo lleva al CIERRE, en T6.2 (lo dice la
-- propia 0072). El runner reejecuta por HASH, así que esta migración se aplica
-- igual sin tocar la constante.
-- ============================================================

ALTER TABLE public.intake_jobs
    ADD COLUMN IF NOT EXISTS attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE public.intake_jobs
    ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- El índice del CLAIM, gemelo de webhook_outbox_claim_idx (0046): sirve al
-- `WHERE status='pending' AND next_attempt_at <= now() ORDER BY next_attempt_at`
-- del worker. El `intake_jobs_window_idx` de la 0072 NO sirve a esta pregunta:
-- empieza por la tupla de ventana (tenant, session, contact, event) y el claim no
-- filtra por ninguna de ellas — la cola es global a propósito.
CREATE INDEX IF NOT EXISTS intake_jobs_claim_idx
    ON public.intake_jobs (status, next_attempt_at);

COMMENT ON COLUMN public.intake_jobs.attempts        IS 'Número de intentos de pipeline ya realizados sobre este job (Plan 044 · T2.1, exigido por D-044.43 y T2.5). Nace en 0 y lo mueve el worker, no la máquina de estados: reclamar no es intentar. Alimenta el backoff y el corte a ''failed''; el techo de intentos NO vive aquí ni en un CHECK, es política del worker (T2.5) y cambiarlo no debe costar una migración. Gemelo exacto de webhook_outbox.attempts (0046).';
COMMENT ON COLUMN public.intake_jobs.next_attempt_at IS 'Momento a partir del cual el job es reclamable. Es la mitad temporal del claim (con status): el backoff se implementa EMPUJANDO ESTA MARCA, NO DURMIENDO EL WORKER — misma doctrina que webhook_outbox.next_attempt_at (0046), y por el mismo motivo: un sleep retiene la goroutine, no sobrevive al reinicio y no lo ve nadie desde fuera. DEFAULT now() = «reclamable ya», que es lo correcto tanto para un job recién cerrado como para los que YA estaban en pending cuando se aplicó esta migración (el DEFAULT los pobló). NUNCA es NULL: un NULL obligaría a un COALESCE en el claim para siempre y una fila sin marca sería invisible al worker. NO lo pone el Edge ni el cliente: es reloj de PostgreSQL, el mismo que evalúa el <= now() del claim, así que aquí no se comparan dos relojes.';
