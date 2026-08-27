-- ============================================================
-- 0080: EL CONTEXTO DEL RE-ANÁLISIS en intake_jobs — quién pidió este job y
-- con qué material (Plan 044 · Ola 4 · T4.6, D-044.15 / REQ-24b-e, design §8.1).
--
-- QUÉ AÑADE, Y POR QUÉ NO ESTABA
-- ------------------------------------------------------------
-- Hasta hoy TODO job de `intake_jobs` nacía igual: lo abría el agregador con el
-- mensaje del cliente y lo cerraba el barrido. `POST /api/v1/intakes/{id}/reanalyze`
-- estrena un SEGUNDO productor —el DUEÑO, por HTTP, sobre un evento que ya tiene su
-- solicitud— y ese job tiene que producir una revisión distinta: `created_by='owner'`
-- en vez de `system`, y un `payload.analysis` con el rastro de D-044.15
-- (`provider`, `model`, `source`, `reanalyzed_from`).
--
-- Nada de eso se puede DEDUCIR mirando la fila: un job de re-análisis y uno normal
-- son idénticos columna a columna. Y la etapa que escribe la revisión (`draft`) no
-- habla con HTTP: recibe un `ClaimedJob` y nada más. O el job transporta el dato, o
-- el dato no llega.
--
--   * requested_by      — el ROL que pidió el job. NULL = el pipeline normal.
--   * reanalysis_via    — la VÍA con la que se pidió correr ('local' | 'api').
--   * reanalysis_source — de dónde salió el material ('event_thread' | 'pasted_text'
--                         | 'both').
--   * reanalyzed_from   — el `revision_no` vigente cuando se pidió el re-análisis.
--
-- 🔴 SON CUATRO COLUMNAS Y NO UN JSONB, Y ES UNA DECISIÓN. `artifacts` ya es el
-- JSONB de esta tabla y está indexado POR ETAPA (`{"p2":…,"p3":…}`, ver saveStageSQL):
-- meter aquí un quinto objeto que no es una etapa obligaría a todo lector de
-- artefactos a saber que una de las claves no lo es. `source_refs` tampoco vale: su
-- COMMENT lo fija como lista de `wa_message_id` opacos. Y un JSONB nuevo escondería
-- en un blob cuatro valores de vocabulario cerrado que un operador tiene que poder
-- filtrar con un `WHERE` — la doctrina de esta máquina es que «el rastro queda
-- consultable por SQL» (machine.go, INV-13).
--
-- 🔴 NINGUNA LLEVA CHECK, Y TAMPOCO ES UN OLVIDO. El molde exacto es
-- `intake_revisions.created_by` (0045:129), que declara su vocabulario —system |
-- owner | crm— en el COMMENT y no en una constraint. Se copia el molde entero: un
-- CHECK aquí obligaría a migrar la base el día que aparezca un tercer productor de
-- jobs, y el vocabulario de estas cuatro lo sostienen las constantes de Go
-- (`intake.RequestedByOwner`, `tenantllm.Via*`, `stages.Origen*`) más los tests que
-- las comparan. Lo que sí hace falta es que NULL sea legítimo, y lo es: es el estado
-- de los jobs que abre el agregador, que son la inmensa mayoría de la tabla.
--
-- 🔴 NULL Y NO UN DEFAULT, y aquí sí hay una diferencia con la 0078. Allí
-- `next_attempt_at NOT NULL DEFAULT now()` era lo correcto porque toda fila necesita
-- una marca temporal y «ya» era la respuesta buena también para las viejas. Aquí es
-- al revés: rellenar las filas existentes con `'system'` afirmaría de cada job de la
-- historia algo que nadie comprobó. NULL dice «esta fila no la pidió nadie por esta
-- puerta», que es exactamente la verdad.
--
-- LO QUE ESTA MIGRACIÓN NO TRAE, A PROPÓSITO
-- ------------------------------------------------------------
-- NO trae un índice. Ninguna consulta filtra por estas columnas: el claim ordena por
-- `(next_attempt_at, created_at)` y las lee por el `RETURNING`. Un índice sobre una
-- columna que solo se lee por id es coste de escritura a cambio de nada.
-- NO trae `reanalysis_model`: el modelo concreto lo elige el adaptador POR LLAMADA y
-- no lo publica ningún puerto (misma razón por la que `Analisis.Provider` sale vacío
-- en el pipeline normal, ver pipeline.go). `analysis.model` se queda vacío y eso está
-- dicho donde se arma, no escondido aquí.
--
-- ADITIVA e IDEMPOTENTE: el runner es hash-based FULL-REPLAY (re-aplica TODOS los
-- structure/*.sql al cambiar el hash de cualquiera). `ADD COLUMN IF NOT EXISTS` va
-- ANTES de los `COMMENT ON COLUMN` y fuera de cualquier `CREATE TABLE`: la 0072 ya
-- creó la tabla, y un COMMENT sobre una columna que no existe ABORTA EL ARRANQUE.
-- Re-aplicarla N veces no toca un solo valor.
--
-- SIN BUMP DE SchemaVersion: el Plan 044 lo lleva al CIERRE, en T6.2 (lo dice la
-- propia 0072). El runner reejecuta por HASH.
-- ============================================================

ALTER TABLE public.intake_jobs
    ADD COLUMN IF NOT EXISTS requested_by TEXT;
ALTER TABLE public.intake_jobs
    ADD COLUMN IF NOT EXISTS reanalysis_via TEXT;
ALTER TABLE public.intake_jobs
    ADD COLUMN IF NOT EXISTS reanalysis_source TEXT;
ALTER TABLE public.intake_jobs
    ADD COLUMN IF NOT EXISTS reanalyzed_from INTEGER;

COMMENT ON COLUMN public.intake_jobs.requested_by IS
'Plan 044 T4.6 (D-044.15): QUIÉN pidió este job, como ROL y nunca como persona — aquí NUNCA va un identificador de usuario, un número ni un nombre (CERO PII). Vocabulario: ''owner'' = lo pidió el dueño por POST /api/v1/intakes/{id}/reanalyze. NULL = el pipeline normal, o sea el job que abrió el agregador con el mensaje del cliente (D-044.26), que es la inmensa mayoría de la tabla. Es lo que hace que la etapa `draft` escriba la revisión con created_by=''owner'' en vez de ''system'' Y lo que GATEA el empuje al puente CRM (T4.10 mitad 2): sin esta marca, empujar en toda revisión convertiría el pipeline normal en un productor de intake.push, que es un cambio de conducta que nadie pidió. Molde exacto de intake_revisions.created_by (0045): mismo tipo, mismo criterio de rol, vocabulario en el COMMENT y no en un CHECK.';

COMMENT ON COLUMN public.intake_jobs.reanalysis_via IS
'Plan 044 T4.6 (design §8.1, ADR-0044 · D-044.28): la VÍA con la que se pidió correr este re-análisis — ''local'' (el fierro del propio tenant) o ''api'' (proveedor externo). Es el eje VÍA, NO el eje PROVEEDOR: el proveedor (anthropic|gemini) sale SIEMPRE de tenant_llm y no viaja en el cuerpo de /reanalyze. La resuelve el endpoint (cuerpo → tenant_llm.via → ''local'' si no hay fila, D-044.48 §4) y acaba en payload.analysis.provider de la revisión, que es el campo que permite comparar «lo que sacó el local» contra «lo que sacó la API». NULL para los jobs del pipeline normal, donde la vía la resuelve el selector POR LLAMADA y no la publica ningún puerto.';

COMMENT ON COLUMN public.intake_jobs.reanalysis_source IS
'Plan 044 T4.6 (design §7.4/§8.1, D-044.17): de dónde salió el material que se va a interpretar — ''event_thread'' (solo el hilo cifrado del evento), ''pasted_text'' (solo la transcripción que pegó el dueño en el cuerpo) o ''both''. Lo decide el endpoint ANTES de abrir el job, comparando lo que hay en conversation_event_messages con lo que trae el campo `text`. Acaba en payload.analysis.source. 🔴 NO lo puede deducir el pipeline: el texto pegado se PERSISTE en el hilo como una fila más (role=''client'', origin=''owner_pasted'') justamente para que el prompt no lo distinga (criterio de T4.6: «el prompt no contiene la palabra origin ni distingue esas filas»), así que aguas abajo las dos procedencias son indistinguibles a propósito. NULL para los jobs del pipeline normal, cuyo material es siempre el hilo.';

COMMENT ON COLUMN public.intake_jobs.reanalyzed_from IS
'Plan 044 T4.6 (design §7.4, D-044.15): el revision_no que estaba vigente en la solicitud cuando se pidió el re-análisis — o sea, la revisión que este job va a SUCEDER, no la que va a escribir. Acaba en payload.analysis.reanalyzed_from, que §7.4 escribe como null en la revisión 1 («esta es la primera lectura»). Lo lee el ENDPOINT y no la etapa `draft`, y la razón es que el número solo se conoce con certeza antes de abrir el job: pedirlo después obligaría a una lectura extra con una carrera contra cualquier otra escritura de revisiones. 0 no se escribe nunca: una solicitud sin revisiones deja esta columna en NULL, que es lo que el contrato llama null. NULL para los jobs del pipeline normal.';
