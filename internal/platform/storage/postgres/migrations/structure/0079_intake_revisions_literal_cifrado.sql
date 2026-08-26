-- ============================================================
-- 0079: intake_revisions — EL LITERAL DEL CLIENTE SALE DEL PAYLOAD Y SE CIFRA,
--       CON SU TTL DE RETENCIÓN POR TENANT
-- (Plan 044 · Ola 3 · T3.5 · D-044.13 — doc 14 D-13, ADR-0034 §Decisión 2)
--
-- 🔴 EL NÚMERO SE VERIFICÓ CON `ls`, NO SE HEREDÓ DE LA DOC. El 2026-08-26, con el
-- directorio delante, la última migración REAL era la `0078_intake_jobs_backoff.sql`
-- (la Ola 3 no había gastado ninguna: las `0071`–`0078` son de las olas 0, 1, 1.5,
-- 1.8 y 2), así que la `0079` estaba LIBRE. «Conocida» no es «verificada»: quien
-- añada la siguiente repite el `ls`.
--
-- ⚠️ **NO sube `SchemaVersion`**: el Plan 044 hace UN SOLO bump al cierre, en T6.2.
-- El runner reaplica por HASH de contenido, así que este fichero se ejecuta igual
-- sin tocar la constante.
--
-- ------------------------------------------------------------
-- QUÉ CAMBIA EN EL COMPORTAMIENTO, DICHO ANTES QUE EL DDL
-- ------------------------------------------------------------
-- Hasta hoy `intake_revisions.payload` guardaba el texto ORIGINAL del cliente
-- (`source_text`) y las frases literales que sostienen cada línea (`evidence`) EN
-- CLARO, dentro del JSONB. La cabecera de `internal/intake/stages/draft.go` lo
-- decía sin disimulo: «NO CIFRA NADA … hasta que T3.5 cierre». Esta migración es la
-- mitad SQL de ese cierre.
--
-- El ADR-0034 §Decisión 2 clasifica esos dos campos —y SOLO esos dos— como
-- **nivel 2**: texto libre del cliente que puede arrastrar identidad. El resto del
-- payload es **nivel 1** y se queda EN CLARO a propósito: skus, cantidades, rangos,
-- precios, fechas, variantes, avisos y —explícitamente— las **personalizaciones**
-- («sin sal», «sin cebolla»), que son dato de negocio cuantificable y cifrarlas
-- destruiría su valor sin proteger a nadie.
--
-- ------------------------------------------------------------
-- POR QUÉ TRES COLUMNAS Y NO UN SOBRE DENTRO DEL PROPIO JSONB
-- ------------------------------------------------------------
-- El `design.md` §6.3 dibujaba el cifrado «dentro del payload» (un `source_text`
-- cifrado en el mismo JSON). Se descarta, y por dos razones que se comprueban en el
-- código de este repo, no por gusto:
--
--   1. **LA ROTACIÓN DE KEK NO VE DENTRO DE UN JSONB.** `crypto.rekeyTargets`
--      (`internal/platform/crypto/rekey.go:49`) es un censo de (tabla, PK, columna
--      DEK, columna key_id) y su barrido interpola nombres de COLUMNA en el SQL. Un
--      sobre escondido en el JSON quedaría fuera del censo, y la regla que ese censo
--      se escribió a sí mismo —«ENTRA AQUÍ EN EL MISMO COMMIT QUE EMPIEZA A CIFRAR»,
--      rekey.go:82-87 y :212-215— dejaría de poder cumplirse. El precio de saltársela
--      no se paga hoy: se paga el día de la rotación, con las filas envueltas por una
--      KEK que ya nadie tiene. Con columnas, entrar en el censo es UNA entrada más.
--   2. **LA PODA TIENE QUE PODER JURAR QUE NO TOCÓ LA INTERPRETACIÓN.** Con el sobre
--      dentro del JSON, podar es REESCRIBIR el payload, y «se conservó la
--      interpretación» pasa a ser una afirmación sobre un `jsonb - 'clave'` que hay
--      que creerse. Con columnas aparte, podar es `SET literal_enc = NULL` y la
--      columna `payload` NO SE MENCIONA en el UPDATE: la interpretación queda intacta
--      POR CONSTRUCCIÓN, no por cuidado.
--
-- Efecto lateral que conviene decir en voz alta: con esta forma el literal no está
-- «cifrado dentro del payload», es que NO ESTÁ EN EL PAYLOAD. El barrido por
-- substring de T3.5 sobre la columna `payload` no encuentra el texto porque no hay
-- texto que encontrar — y quien lea eso como una tautología tiene razón a medias: lo
-- que el barrido demuestra de verdad es que el literal **sí se persistió** (en el
-- trío, y se puede descifrar y comparar) y aun así **no aparece** en claro en ninguna
-- de las dos columnas. Sin la primera mitad, el barrido mediría cero.
--
-- ------------------------------------------------------------
-- EL TRÍO ES NULLable, Y AQUÍ HAY TRES CAUSAS DISTINTAS
-- ------------------------------------------------------------
-- A diferencia de `intake_buyer_data` (0045), donde las tres son NOT NULL porque la
-- fila no existe si no hay dato, aquí una revisión SIN sobre es normal y frecuente:
--
--   * `kind='cart'`, `'corrected'`, `'discarded'`, `'revalidated'`, `'crm'` no llevan
--     literal del cliente: su payload son líneas y totales. Nacen con el trío NULL y
--     así se quedan.
--   * `kind='interpreted'` cuyo texto se compuso vacío (no debería, pero el store no
--     inventa un sobre para una cadena vacía).
--   * Una revisión PODADA: tenía sobre y se le vació al vencer el TTL. Esa se
--     distingue de las anteriores por `literal_pruned_at`, que es justo la razón de
--     que esa cuarta columna exista en vez de dejar el NULL mudo.
--
-- Como en `tenant_integrations`, `fleet_sessions`, `tenant_llm` e `intake_jobs`, las
-- filas sin sobre se caen SOLAS del barrido de rotación y de su conteo, porque
-- `NULL <> 'x'` no es TRUE en SQL.
-- ============================================================

-- ------------------------------------------------------------
-- A) EL SOBRE DEL LITERAL, EN public.intake_revisions
-- ------------------------------------------------------------
-- `ADD COLUMN IF NOT EXISTS` sin NOT NULL y sin DEFAULT: es NO-OP exacto del segundo
-- arranque en adelante y no reescribe una sola fila. Las revisiones que ya existen
-- —las del carrito numérico, que no tienen literal— se quedan como están, que es lo
-- correcto: no hay backfill que hacer porque no hay nada que mover.
--
-- 🔴 LO QUE ESTA MIGRACIÓN NO HACE, Y NO ES UN OLVIDO: no toca los payloads YA
-- escritos. Si alguna base tuviera revisiones `interpreted` de antes de T3.5, su
-- `source_text` seguiría en claro dentro del JSONB y ninguna sentencia de aquí lo
-- sacaría. No se construye ese backfill porque **no existe tal base**: la etapa
-- `draft` (T3.4) se publicó en esta misma ola y aún no ha corrido en ningún entorno
-- desplegado, así que `intake_revisions` no tiene hoy ni una fila `interpreted`. El
-- día que la tenga, esta afirmación deja de ser cierta y hay que escribir el
-- backfill: se comprueba con
-- `SELECT count(*) FROM public.intake_revisions WHERE kind = 'interpreted'`.
ALTER TABLE public.intake_revisions ADD COLUMN IF NOT EXISTS literal_enc       BYTEA;
ALTER TABLE public.intake_revisions ADD COLUMN IF NOT EXISTS literal_dek       BYTEA;
ALTER TABLE public.intake_revisions ADD COLUMN IF NOT EXISTS literal_kek_id    TEXT;
ALTER TABLE public.intake_revisions ADD COLUMN IF NOT EXISTS literal_pruned_at TIMESTAMPTZ;

-- Localiza las filas pendientes de rotación sin barrer la tabla entera. PARCIAL —al
-- contrario que `idx_intake_buyer_data_kek` de la 0045 y por la misma razón que
-- `idx_intake_jobs_kek` de la 0072— porque la MAYORÍA de las revisiones no tiene
-- sobre: las del carrito numérico son el caso corriente de esta tabla.
CREATE INDEX IF NOT EXISTS idx_intake_revisions_kek
    ON public.intake_revisions (literal_kek_id)
    WHERE literal_kek_id IS NOT NULL;

COMMENT ON COLUMN public.intake_revisions.literal_enc IS
'Plan 044 T3.5 (D-044.13, ADR-0034 §Decisión 2, nivel 2): envelope AES-256-GCM (nonce fresco por escritura) del LITERAL DEL CLIENTE de esta revisión — el JSON {"source_text":…,"evidence":{"<pos de línea>":…}} que se SACÓ del payload antes de persistirlo. NO es el payload cifrado: la interpretación estructurada (skus, cantidades, precios, fechas, variantes y las personalizaciones «sin sal») sigue EN CLARO en payload, es nivel 1 y cifrarla destruiría su valor sin proteger a nadie. NULLable y ese NULL tiene TRES causas distintas: la revisión no lleva literal (kind cart/corrected/approved/crm/discarded/revalidated), el literal venía vacío, o la revisión fue PODADA al vencer el TTL — esta última se distingue por literal_pruned_at. Se vacía junto con literal_dek y literal_kek_id, nunca por separado.';
COMMENT ON COLUMN public.intake_revisions.literal_dek IS
'DEK por-fila (32B) que cifra literal_enc, envuelta por la KEK maestra (design §10.B). La KEK NO vive en esta BD. NADA que ver con la DEK del Edge (el store de whatsmeow, ADR-0007), que la nube jamás ve. NULLable por lo mismo que el resto del trío, y se vacía con él: media escritura deja una fila INDESCIFRABLE y eso no se puede deshacer leyendo, porque no hay copia de la DEK en ningún otro sitio.';
COMMENT ON COLUMN public.intake_revisions.literal_kek_id IS
'key_id de la KEK que envolvió literal_dek. Discriminador de la rotación: distinto del current => fila pendiente de re-envolver (crypto.PendingByKeyID / Rekey). Su índice idx_intake_revisions_kek es PARCIAL porque la mayoría de las revisiones no tiene sobre. ✅ ESTA TABLA SÍ ESTÁ EN EL CENSO DEL REKEY (crypto.rekeyTargets), y entró en el MISMO commit que empezó a cifrar — la regla que ese censo se escribió a sí mismo, y que la 0072 tuvo que dejar como AVISO VIVO en su día.';
COMMENT ON COLUMN public.intake_revisions.literal_pruned_at IS
'Momento en que la PODA PEREZOSA vació el sobre de esta revisión por haber vencido tenant_settings.intake_literal_ttl_seconds. NULL = no se ha podado (que NO significa que tuviera literal: ver literal_enc). Existe para que un trío en NULL no sea mudo: sin esta columna, «esta revisión nunca tuvo texto» y «el texto se retuvo el plazo pactado y se destruyó» serían el mismo estado en la BD, y son dos hechos distintos que la auditoría tiene que poder separar. Es la ÚNICA marca que queda: el literal no se archiva en ninguna parte, se destruye.';

-- ------------------------------------------------------------
-- B) EL TTL DE RETENCIÓN DEL LITERAL, EN public.tenant_settings
-- ------------------------------------------------------------
-- 🔴 POR QUÉ ESTA CLAVE NACE SIENDO OTRA DISTINTA DE `intake_retention_ttl_seconds`,
-- QUE EL ADR-0034 §Enmienda E-1 DECLARÓ QUE «NO NACE NUNCA».
--
-- No es la misma clave con otro nombre, y confundirlas sería resucitar algo que se
-- descartó con motivo. Las diferencias son tres y cada una importa:
--
--   * QUÉ MIDE. Aquélla era la retención de la SOLICITUD ENTERA (`intakes`, su
--     `customer_note` incluido). Ésta mide SOLO el literal de nivel 2 dentro de una
--     revisión: el texto que el cliente escribió y las frases que lo citan.
--   * CÓMO SE EJECUTA. Aquélla exigía un BARRIDO AUTOMÁTICO por antigüedad, y eso es
--     exactamente lo que el ADR-0043 rechazó («la limpieza, cuando haga falta, será
--     ON-DEMAND»). Ésta es PODA PEREZOSA: no hay cron, no hay barrido, no hay proceso
--     de fondo. Si nadie abre la revisión, no pasa nada; el trabajo se hace en el
--     acceso, que es el patrón que este repo ya usa en `ingest_dedupe`.
--   * DE DÓNDE SALE SU NÚMERO. El techo de 5 años de D-046.15 se justificaba con un
--     argumento FISCAL POR ANALOGÍA, y el ADR-0043 §Decisión corolario 4 lo prohíbe:
--     «la retención de datos en wApp se dimensiona por utilidad de negocio, nunca por
--     una obligación legal de terceros». Los 12 meses de aquí son utilidad de negocio
--     y se pueden decir en una frase: es el propósito escrito del literal (ADR-0034
--     §Decisión 2 / D-14) —que el dueño pueda auditar al LLM y RE-ANALIZAR desde el
--     origen— y ese propósito se agota cuando el pedido lleva un año cerrado.
--
-- Y sobre todo: la fila del inventario del ADR-0034 §Decisión 2 que gobierna
-- `intake_revisions.payload → source_text y evidence` dice literalmente «Cifrado con
-- KEK + TTL de retención con poda perezosa», y la Enmienda E-1 **no la tachó** — tachó
-- las de `intakes.customer_note` y `conversation_event_messages`. Lo que E-1 dejó
-- escrito sobre este plan es que D-044.13 «se revisa contra el ADR-0043 cuando el Plan
-- 044 arranque»: esta sección ES esa revisión, y su resultado es que el mandato sigue
-- en pie porque no depende del mecanismo que se descartó.
ALTER TABLE public.tenant_settings
    ADD COLUMN IF NOT EXISTS intake_literal_ttl_seconds INTEGER NOT NULL DEFAULT 31536000;

-- CHECK con nombre propio y FUERA del ALTER: misma regla full-replay que los de
-- `intake_jobs` (0072) y `aggregation_max_seconds` (0076). Un CHECK inline sobre una
-- columna añadida por `ADD COLUMN IF NOT EXISTS` quedaría congelado el día que la
-- columna nació y ampliarlo no tendría efecto sobre ninguna base ya migrada.
ALTER TABLE public.tenant_settings DROP CONSTRAINT IF EXISTS tenant_settings_intake_literal_ttl_check;
ALTER TABLE public.tenant_settings ADD  CONSTRAINT tenant_settings_intake_literal_ttl_check
    CHECK (intake_literal_ttl_seconds >= 0);

COMMENT ON COLUMN public.tenant_settings.intake_literal_ttl_seconds IS
'Plan 044 T3.5 (D-044.13, ADR-0034): RETENCIÓN del literal del cliente dentro de una revisión (intake_revisions.literal_enc), en segundos desde intake_revisions.created_at. Default de plataforma 31536000 = 365 días = los 12 meses de D-044.13. 0 = SIN PODA (retención indefinida), igual que el 0 de event_history_ttl_seconds y al contrario que el 0 de aggregation_window_seconds — «para siempre» es una decisión legítima del tenant y tiene que poder expresarse. Vencido el plazo, el SIGUIENTE ACCESO a la revisión vacía el trío del sobre, sella literal_pruned_at y DEJA EL PAYLOAD INTACTO: la interpretación estructurada (nivel 1) no se poda nunca. NO HAY BARRIDO NI CRON: es poda PEREZOSA (ADR-0043 §Consecuencias 3 — la limpieza es on-demand, no un barrido de fondo), así que una revisión que nadie abra conserva su sobre indefinidamente y eso es correcto y consciente. NO es intake_retention_ttl_seconds, la clave que el ADR-0034 §Enmienda E-1 declaró que no nace nunca: aquélla podaba la solicitud entera con un barrido automático justificado por analogía fiscal; ésta poda un campo de nivel 2 al leerlo y su plazo sale de la utilidad de negocio (auditar y re-analizar, D-14).';
