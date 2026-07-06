-- ============================================================
-- 0028: self_pn (número propio) en fleet_sessions (Plan 020 · T2).
-- Cada sesión de fleet reporta en su Heartbeat el número propio E164 (sin '+')
-- con el que opera el Edge. El Cloud lo PERSISTE aquí para poder detectar el
-- anti-self-loop: si un entrante llega con un remitente (from_pn) que es un
-- self_pn de OTRA sesión del MISMO tenant, el motor NO auto-responde (rompe el
-- bucle sesión↔sesión destapado en el e2e del Plan 019).
--
-- Extiende 0003_lease_fleet.sql. El número es un dato de negocio NORMALIZADO
-- (solo dígitos, E.164 sin '+'); NO es credencial ni llave (ADR-0007/0009). Aun
-- así, el runtime NUNCA lo loguea (PII): solo IDs opacos y el hecho "self-loop
-- evitado". El valor lo escribe el Gateway al procesar cada Heartbeat; un self_pn
-- vacío (sesión sin emparejar) NO sobrescribe un valor previo bueno.
--
-- ADITIVA e IDEMPOTENTE (runner hash-based FULL-REPLAY): ADD COLUMN IF NOT EXISTS
-- => re-aplicable N veces sin daño. NULL por defecto garantiza que, sin poblar,
-- el comportamiento es idéntico al actual (INV no-regresión: la guarda no bloquea
-- nada si no hay self_pn).
-- ============================================================

ALTER TABLE public.fleet_sessions
    ADD COLUMN IF NOT EXISTS self_pn TEXT NULL;

COMMENT ON COLUMN public.fleet_sessions.self_pn IS 'Número propio (E.164 sin +, normalizado) que la sesión reporta en su Heartbeat. Lo usa el anti-self-loop (Plan 020 · T2): un entrante cuyo from_pn casa un self_pn de otra sesión del mismo tenant NO auto-responde. Dato de negocio (ADR-0009), NUNCA credencial/llave ni se loguea (PII).';
