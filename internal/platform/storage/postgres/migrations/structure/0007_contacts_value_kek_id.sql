-- ============================================================
-- 0007: Discriminador de KEK por fila en contacts (Plan 012 · T1, ADR-0017 §rotable).
-- Añade value_kek_id: el key_id de la KEK que envolvió value_dek (design.md §5, §10.A).
-- Permite ROTAR la KEK re-envolviendo las DEK (re-wrap incremental por key_id) SIN
-- re-cifrar value_enc ni tocar value_bidx/PK. El discriminador va en COLUMNA (no en
-- el blob del envelope de wapp-shared) para poder consultar/filtrar pendientes por
-- KEK en SQL (design.md §10.A).
--
-- ADITIVA e IDEMPOTENTE — NO clean-slate: ya hay datos del Plan 011 en pre-prod
-- (design.md §5). A diferencia de 0006, aquí NO se descarta la tabla: se AÑADE
-- columna con DEFAULT + backfill al key_id inicial. El runner es hash-based
-- FULL-REPLAY (re-aplica todos los structure/*.sql al cambiar el hash); los
-- IF NOT EXISTS y el WHERE del backfill garantizan re-aplicación N veces sin daño.
--
-- El DEFAULT '1' DEBE coincidir con crypto.compatKeyID (keyprovider.go): las filas
-- del Plan 011 fueron envueltas por la KEK maestra única, cargada por el camino
-- compat como key_id "1" (idéntico comportamiento al 011).
--
-- INVARIANTES: value_enc/value_dek/value_bidx, la PK (tenant_id, kind, value_bidx)
-- y el índice inverso NO cambian. La KEK NO vive en esta BD (env/secret store).
-- ============================================================

-- Columna del discriminador de KEK. DEFAULT '1' cubre además cualquier INSERT
-- legacy en vuelo durante el despliegue; los INSERT nuevos escriben el key_id
-- explícito devuelto por Encrypt (design.md §6).
ALTER TABLE public.contacts
    ADD COLUMN IF NOT EXISTS value_kek_id TEXT NOT NULL DEFAULT '1';  -- key_id que envolvió value_dek

-- Backfill idempotente: las filas del Plan 011 quedan con el key_id inicial ('1').
-- El WHERE hace la re-aplicación inocua (no toca filas ya con key_id no vacío).
UPDATE public.contacts SET value_kek_id = '1' WHERE value_kek_id IS NULL OR value_kek_id = '';

-- Índice para localizar rápido las filas pendientes de rotación (por KEK vieja).
CREATE INDEX IF NOT EXISTS idx_contacts_kek
    ON public.contacts (tenant_id, value_kek_id);

COMMENT ON COLUMN public.contacts.value_kek_id IS 'key_id de la KEK del keyring que envolvió value_dek (Plan 012 §10.A). Discriminador para el re-wrap incremental (rotación) y para consultar pendientes por KEK. NO cambia value_enc/value_bidx. Backfill del 011 = key_id inicial (compatKeyID "1").';
