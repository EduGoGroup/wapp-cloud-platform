-- ============================================================
-- 0083: intake_jobs — LA RETENCIÓN, DECIDIDA Y ESCRITA
-- (Plan 044 · Ola 6 · T6.3, criterio (d) — D-044.53, decisión de Jhoan del
--  2026-08-28. Doctrina: ADR-0043 §Enmienda E-1, que nombra esta tabla.)
--
-- ESTA MIGRACIÓN NO CAMBIA NI UNA COLUMNA. Solo `COMMENT ON`.
-- ------------------------------------------------------------
-- Hace tres cosas, y las tres son de la misma clase: poner por escrito, donde se
-- lee el esquema, lo que hasta hoy había que deducir o —peor— lo que el esquema
-- afirmaba y ya no era verdad.
--
--   1. DECLARA LA RETENCIÓN de `intake_jobs` (T6.3, criterio (d)). `INV-050.3`
--      exige de una tabla-cola con payload de negocio tres cosas: cifrado KEK,
--      migración y TTL declarado. Esta tabla cumplía dos. La tercera se decide
--      ahora: RETENCIÓN INDEFINIDA DECLARADA, sin poda y sin TTL.
--   2. CORRIGE DOS COMENTARIOS DE LA 0072 QUE HOY SON FALSOS (ver abajo). No se
--      edita la 0072: cambiar su contenido cambia su `content_hash` y el motor
--      intentaría reejecutarla. Un `COMMENT ON` es idempotente y SOBREESCRIBE, así
--      que una migración nueva basta y el histórico queda intacto.
--   3. REGISTRA UN DESVÍO QUE NO RESUELVE, apuntando a su dueño (MP-13).
--
-- 🔴 SIN BUMP DE `SchemaVersion`, y es deliberado: INV-8 da UN bump por plan y el
-- Plan 044 lo gastó en la Ola 4. La 0082 entró igual. Lo que sí cambia es el
-- `content_hash` de la carpeta, que es precisamente el mecanismo que detecta un
-- fichero nuevo sin bump.
--
-- ------------------------------------------------------------
-- 1) LA DECISIÓN — D-044.53: RETENCIÓN INDEFINIDA DECLARADA
-- ------------------------------------------------------------
-- Se toma la segunda de las dos salidas que T6.3 admitía. El motivo NO es que
-- podar cueste: es que esta tabla es la COLA DE TRABAJO del pipeline y su valor
-- es forense. Lo que se consulta de un job es exactamente lo que pasa DESPUÉS de
-- que muera —por qué murió, qué devolvió cada etapa, cuántos intentos consumió—,
-- y podarlo por reloj borra la única copia de esa respuesta.
--
-- El precedente es del Plan 046: allí TRES tareas de poda (T4.6/T4.7/T4.8) se
-- descartaron —no se difirieron— por el ADR-0043, que dejó la retención indefinida
-- para `intakes`, `flow_events` y `webhook_outbox`. `intake_jobs` NO estaba en esa
-- lista por una razón simple: no existía cuando se decidió. Heredar la decisión en
-- silencio sería justo lo que el ADR-0043 se escribió para impedir —que la
-- retención sea un olvido en vez de una decisión—, y por eso T6.3 existe y por eso
-- la enmienda E-1 del ADR-0043 nombra esta tabla con todas sus letras.
--
-- ⚠️ INDEFINIDA NO ES «GUARDARLO TODO PARA SIEMPRE», y la diferencia ya estaba
-- construida: INV-13 vacía el trío `source_text_*` al entrar el job en `done` o
-- `failed`. Lo que se retiene indefinidamente es la FILA y su rastro técnico; el
-- literal del cliente que la fila llevaba se recorta POR ESTADO, no por reloj —y
-- la copia canónica de ese literal vive en `conversation_event_messages`
-- (D-043.13), cifrada y con su propio tratamiento.
--
-- 🔴 CON UNA EXCEPCIÓN QUE HOY NO SE CIERRA: `artifacts`. Ver el punto 3.
--
-- ------------------------------------------------------------
-- 2) LAS DOS FRASES DE LA 0072 QUE HAY QUE RETIRAR
-- ------------------------------------------------------------
-- (a) `0072:739`, en `source_text_kek_id`: «cablear esta tabla en el censo del
--     Rekey (rekeyTargets) es CODIGO GO QUE LA OLA 1 NO ESCRIBE y esa tarea SIGUE
--     SIN DUENO». FALSO desde que se escribió el censo: la entrada existe en
--     `internal/platform/crypto/rekey.go:230-235` (`table: "public.intake_jobs"`,
--     `dekCol: "source_text_dek"`, `kekCol: "source_text_kek_id"`), la barre el
--     bucle de `rekey.go:407` y el llamador de producción está cableado en
--     `internal/bootstrap/bootstrap.go:972-976`. El propio enunciado de T6.3 ya lo
--     daba por hecho; el comentario del esquema se quedó atrás.
--
-- (b) `0072:741`, en `artifacts`: «esto NO es literal del cliente sino la decision
--     estructurada que tomo el pipeline». FALSO, y no por un detalle: el artefacto
--     de P2 lleva una `evidence` POR IDEA, y una `evidence` es —por contrato— una
--     SUBCADENA DEL MENSAJE DEL CLIENTE. No es una paráfrasis y no puede serlo: la
--     etapa ANCLA cada idea comprobando en Go que su evidencia aparece en el
--     literal (`internal/intake/stages/p2.go:131-133` lo declara, y
--     `p2.go:229-246` lo ejecuta con `evidence.Contains`), y la idea cuya evidencia
--     no aparece se DESCARTA. Es decir: lo que sobrevive en `artifacts` es
--     exactamente lo que sí estaba en el texto del cliente.
--
-- ------------------------------------------------------------
-- 3) EL DESVÍO QUE ESTA MIGRACIÓN REGISTRA Y **NO** RESUELVE — MP-13
-- ------------------------------------------------------------
-- Junta las cuatro mitades y sale una situación que nadie decidió:
--
--   * `artifacts` guarda literal del cliente (punto 2b), EN CLARO;
--   * NO se vacía en estado terminal (a diferencia del trío, INV-13);
--   * está FUERA del censo del Rekey — el censo interpola nombres de COLUMNA, y
--     esto vive dentro de un JSONB;
--   * y NO figura en el inventario de texto conversacional del ADR-0034
--     (`0034:113` solo nombra `intake_jobs.source_text`, y para decir que ya no
--     cuenta), que clasifica «las `evidence` citadas» como NIVEL 2 —cifrado KEK +
--     TTL— en `0034:64`.
--
-- 🔴 LA IRONÍA, ESCRITA PARA QUE NO SE PIERDA: la migración
-- `0079_intake_revisions_literal_cifrado.sql:39-46` rechazó meter el sobre DENTRO
-- de un JSONB, y su primera razón fue literalmente «LA ROTACIÓN DE KEK NO VE
-- DENTRO DE UN JSONB». Es, palabra por palabra, la situación en la que `artifacts`
-- ya estaba mientras se escribía esa frase.
--
-- Por qué no se cierra aquí: las tres salidas —vaciar `artifacts` en terminal como
-- INV-13 hace con el sobre, cifrarlo, o declararlo Nivel 1 enmendando el ADR-0034—
-- son DECISIONES DE PRODUCTO con costes distintos, y ninguna es un efecto colateral
-- legítimo de una tarea de retención. Se registra con dueño: **MP-13**.
-- ============================================================

COMMENT ON TABLE public.intake_jobs IS
    'Cola del pipeline de captacion por LLM (Plan 044 - Ola 1, T1.0; design 6.2). Una fila = UNA VENTANA DE AGREGACION: los mensajes seguidos de un contacto sobre un mismo evento se juntan aqui y disparan UN solo pipeline, no uno por mensaje. Desempena para el Plan 044 el mismo papel que webhook_outbox (0046) para el 042: es la fila que existe para que el trabajo caro lo haga otro, despues. El AggregatorSink escribe UNA sentencia por entrante y NINGUNA lectura (D-044.26, INV-02 del Plan 050). SIN BROKER (ADR-0003): la durabilidad es esta tabla, y por eso un reinicio no pierde el job. Maquina de estados: aggregating -> pending -> processing(p2,p3,p4,match,draft) -> done | failed; guards UPDATE ... WHERE status=...; terminales absorbentes. RETENCION: INDEFINIDA Y DECLARADA (D-044.53, decision de Jhoan del 2026-08-28; T6.3 de la Ola 6; ADR-0043 Enmienda E-1, que nombra esta tabla explicitamente). NO hay poda, NO hay TTL y NO es un olvido: es la cola de trabajo del pipeline y su valor es FORENSE -- lo que se le pregunta a un job es por que murio y que devolvio cada etapa, y eso se pregunta DESPUES de que muera. Lo que si se recorta es el CONTENIDO, y por ESTADO y no por reloj: el trio source_text_* se pone a NULL al entrar en done/failed (INV-13, cierre de MD-044.1), y la copia canonica de ese literal vive en conversation_event_messages (D-043.13). PII: CERO en las columnas de identidad (contact_id es OPACO, ADR-0017; los wa_message_id son opacos). 🔴 PERO NO ES CIERTO que el trio del sobre sea la unica columna con contenido de persona, y la 0072 lo afirmaba: artifacts guarda las evidence de P2, que son SUBCADENAS DEL MENSAJE DEL CLIENTE por contrato -- en claro, fuera del censo del Rekey y sin vaciado en terminal. Desvio REGISTRADO y con dueno: MP-13. Ver el encabezado de la 0083.';

COMMENT ON COLUMN public.intake_jobs.source_text_kek_id IS
    'key_id de la KEK que envolvio source_text_dek. Discriminador de la rotacion: distinto del current => fila pendiente de re-envolver (crypto.PendingByKeyID / Rekey). NULLable, y su indice idx_intake_jobs_kek es PARCIAL por eso -- al contrario que el de la 0071, donde la columna es NOT NULL y el predicado seria decorativo. 🔧 CORREGIDO POR LA 0083 (T6.3): la 0072 avisaba de que cablear esta tabla en el censo del Rekey era codigo pendiente y que la tarea SEGUIA SIN DUENO. Ya NO es cierto y hay que dejar de leerlo asi -- la entrada existe (crypto.rekeyTargets, table public.intake_jobs / dekCol source_text_dek / kekCol source_text_kek_id), el barrido de Rekey la recorre y el llamador de produccion esta cableado en el bootstrap. Una rotacion que diga COMPLETA hoy incluye estos sobres. Mitigante que ya no hace falta invocar, pero que sigue siendo verdad: el sobre es efimero (se llena al flush, se vacia en terminal por INV-13).';

COMMENT ON COLUMN public.intake_jobs.artifacts IS
    'Salidas VERSIONADAS de cada paso del pipeline: {"p2":{...},"p3":{...},"p4":{...}}. Es lo que permite que el redelivery SALTE etapas ya resueltas en vez de re-pagar el LLM. DEFAULT {} porque un job recien abierto no ha producido nada todavia. NO se vacia en estado terminal: sigue teniendo lector (auditoria, depuracion de un presupuesto raro). 🔴 CORREGIDO POR LA 0083 (T6.3): la 0072 decia que esto NO es literal del cliente sino solo la decision estructurada del pipeline, y es FALSO. El artefacto de P2 lleva una evidence POR IDEA, y una evidence es POR CONTRATO una subcadena del mensaje del cliente -- la etapa ancla cada idea comprobando en Go que su evidencia APARECE en el literal, y descarta la que no aparece; lo que sobrevive aqui es, por construccion, texto que el cliente escribio. Consecuencia que NO se resuelve en esta migracion: hay literal de cliente EN CLARO, FUERA del censo del Rekey (el censo interpola nombres de COLUMNA y esto vive dentro de un JSONB -- la misma razon por la que la 0079 rechazo meter su sobre en un JSONB), SIN vaciado en terminal y SIN entrada en el inventario del ADR-0034, que clasifica las evidence citadas como NIVEL 2 (cifrado KEK + TTL). Desvio REGISTRADO con dueno en MP-13; las tres salidas (vaciar en terminal, cifrar, o declararlo Nivel 1 enmendando el ADR-0034) son decision de producto y no un efecto colateral de T6.3.';

COMMENT ON COLUMN public.intake_jobs.updated_at IS
    'Momento del ultimo cambio. Usa el DEFAULT now() en el alta y lo pone a now() cada transicion de estado y cada anadido a source_refs. 🔧 PRECISADO POR LA 0083 (T6.3): la 0072 ya decia que esta tabla NO tiene poda por tiempo y que eso era una DECISION y no un olvido, pero esa decision NO existia escrita en ninguna parte -- se estaba heredando de la de intakes, flow_events y webhook_outbox (ADR-0043), que no nombraba a esta tabla porque no existia cuando se tomo. Ahora SI existe con numero propio: D-044.53, retencion INDEFINIDA DECLARADA, y el ADR-0043 la nombra en su Enmienda E-1. Lo que si se recorta sigue siendo el CONTENIDO, por INV-13 y no por reloj.';
