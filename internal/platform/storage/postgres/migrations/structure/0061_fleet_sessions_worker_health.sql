-- ============================================================
-- 0061: Salud del WORKER del cajero de intents en fleet_sessions
-- (Plan 051 · Ola 4 · T4.3 — cierra REQ-051.17 y la mitad diferida de REQ-051.14).
--
-- La 0035 (Plan 031 · T3) trajo la salud del SOCKET. Esta trae la salud del
-- PROCESO QUE CLASIFICA: el reparto de CPU entre el cajero y Ollama, la latencia
-- real de la inferencia, el desglose de despachos que salieron sin intent y los
-- cuatro contadores del despachador que nacieron en el barrido de la Ola 3
-- (T3.12). Son los campos 9-15 del `SessionHealth` de cloudlink (>= v0.13.0).
--
-- 🔴 REGLA DE LECTURA — NULL = «ESTE EDGE NO LO SABE», JAMÁS «ESTÁ BIEN».
-- Por eso TODAS las columnas son NULLABLE y SIN DEFAULT, igual que la 0035. Un
-- `BIGINT NOT NULL DEFAULT 0` en `intent_p50_ms` destruiría la distinción PARA
-- SIEMPRE: un p50 de 0 ms se leería como «instantáneo» cuando lo que dice el
-- contrato es «no medible». El Edge, además, aplica una regla de RANCIDEZ: si el
-- parte del worker lleva más de 90 s sin refrescarse, manda `worker_taskset`,
-- `intent_p50_ms` e `intent_omitted_by_reason` a su cero A PROPÓSITO, para no
-- publicar una señal de salud inventada. El ingestor traduce ese cero a NULL.
--
-- 🔴 `intent_omitted_by_reason` NO SE AGREGA EN UN TOTAL. `fastlane` es el camino
-- SANO (no hacía falta clasificar); `presupuesto` y `breaker` son FALLOS. Sumarlos
-- borra exactamente la información por la que existe el desglose (INV-051.3). Por
-- eso es un JSONB de motivo→conteo y no columnas fijas ni una suma: un motivo nuevo
-- en el Edge (la lista se recorre con `app.MotivosOmitido()`, ya se quedó corta dos
-- veces) no debe exigir una migración aquí. JSONB y no TEXT porque es exactamente
-- lo que el repo ya hace con todo lo semiestructurado (flow_state.vars,
-- flow_events.payload, tenant_content.content, audit_events.meta): mismo tipo,
-- misma clase de dato. Un Edge viejo lo manda nil y un Edge nuevo SOLO envía las
-- claves con valor distinto de cero ⇒ ausencia de clave NO es «cero medido».
--
-- 🔴 `failed_seal_dispatch` y `failed_seal_budget` van SEPARADOS a propósito: solo
-- el PRIMERO implica mensajes DUPLICADOS (el mensaje queda sin marca de salida y
-- puede reenviarse); el segundo solo descuadra la contabilidad del gasto. Volver a
-- agregarlos en un contador único deshace T3.12.
--
-- Nota honesta sobre los cuatro contadores acumulados (12-15): proto3 no transporta
-- presencia para un `int64` sin `optional`, así que un Edge viejo y un Edge nuevo
-- SIN incidencias llegan indistinguibles (ambos 0). Se persiste ese 0 tal cual —
-- es la semántica que el propio contrato declara («0 = no ocurrió, o Edge que no
-- los mide»)— y NULL queda reservado a «esta fila nunca recibió un snapshot con el
-- bloque del worker». Para las tres señales del worker (9-11) sí hay distinción
-- real y se respeta.
--
-- ADITIVA e IDEMPOTENTE (runner hash-based FULL-REPLAY): ADD COLUMN IF NOT EXISTS ⇒
-- re-aplicable N veces sin daño. SchemaVersion sube a 0.35.0.
-- ============================================================

ALTER TABLE public.fleet_sessions
    ADD COLUMN IF NOT EXISTS worker_taskset           TEXT,
    ADD COLUMN IF NOT EXISTS intent_p50_ms            BIGINT,
    ADD COLUMN IF NOT EXISTS intent_omitted_by_reason JSONB,
    ADD COLUMN IF NOT EXISTS stuck_heads              BIGINT,
    ADD COLUMN IF NOT EXISTS stuck_head_polls         BIGINT,
    ADD COLUMN IF NOT EXISTS failed_seal_dispatch     BIGINT,
    ADD COLUMN IF NOT EXISTS failed_seal_budget       BIGINT;

COMMENT ON COLUMN public.fleet_sessions.worker_taskset IS 'Veredicto del reparto de CPU entre el proceso del cajero de intents y Ollama (Plan 051 · T2.8): disjunta|solapada|cajero_sin_confinar. NULL = ESTE EDGE NO LO SABE (no es Linux, o el parte del worker esta rancio) — NUNCA se lee como disjunta. Mismo patron que intent_circuit. Plan 051 · T4.3.';
COMMENT ON COLUMN public.fleet_sessions.intent_p50_ms IS 'p50 en ms de la INFERENCIA del clasificador local (Plan 029), medido por el worker sobre sus propias muestras. NULL = NO MEDIBLE (sin muestras o parte rancio); NUNCA se lee como 0 ms / instantaneo. NO es el p50 del handler de whatsmeow (otra poblacion y otro proceso). Plan 051 · T4.3.';
COMMENT ON COLUMN public.fleet_sessions.intent_omitted_by_reason IS 'Despachos que salieron SIN intent, desglosados por motivo (motivo->conteo, JSONB). Claves segun app.MotivosOmitido() del Edge; hoy ocho: fastlane, sin_texto, no_elegible, presupuesto, breaker, desconocido, apagado, fallo_repetido. NUNCA se suman entre si: fastlane es el camino SANO y presupuesto/breaker son FALLOS (INV-051.3). Solo llegan las claves con valor <> 0: clave ausente NO es cero medido. NULL = no reportado. Plan 051 · T4.3.';
COMMENT ON COLUMN public.fleet_sessions.stuck_heads IS 'Cabezas de cola detectadas ATASCADAS (Plan 051 · T3.12). Existe porque antes de T3.12 ese caso no tenia contador y la telemetria publicaba una sesion MUERTA como si estuviera ociosa. 0 = no ocurrio (o Edge que no lo mide); NULL = sin snapshot del bloque worker. Plan 051 · T4.3.';
COMMENT ON COLUMN public.fleet_sessions.stuck_head_polls IS 'Veces que se sondeo una cabeza de cola atascada (Plan 051 · T3.12). 0 = no ocurrio (o Edge que no lo mide); NULL = sin snapshot del bloque worker. Plan 051 · T4.3.';
COMMENT ON COLUMN public.fleet_sessions.failed_seal_dispatch IS 'Fallos al SELLAR EL DESPACHO (Plan 051 · T3.12). SEPARADO de failed_seal_budget a proposito: solo ESTE implica mensajes DUPLICADOS (el mensaje queda sin marca de salida y puede reenviarse). 0 = no ocurrio; NULL = sin snapshot del bloque worker. Plan 051 · T4.3.';
COMMENT ON COLUMN public.fleet_sessions.failed_seal_budget IS 'Fallos al SELLAR EL PRESUPUESTO (Plan 051 · T3.12). NO implica duplicados: solo descuadra la contabilidad del gasto. Agregarlo con failed_seal_dispatch deshace T3.12. 0 = no ocurrio; NULL = sin snapshot del bloque worker. Plan 051 · T4.3.';
