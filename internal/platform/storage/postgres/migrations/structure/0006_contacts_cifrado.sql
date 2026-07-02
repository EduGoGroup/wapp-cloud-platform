-- ============================================================
-- 0006: Cifrado en reposo del identificador de contacto (Plan 011 · T1, ADR-0017).
-- SUPERSEDE el esquema en claro de contacts que 0005 creó: el `value` plano se
-- parte en TRES columnas (design.md §4, §10.G):
--   * value_bidx — HMAC(indexKey, tenant||value_norm): índice ciego, busca/deduplica.
--   * value_enc  — envelope AES-256-GCM del value normalizado (nonce fresco).
--   * value_dek  — DEK por-valor, envuelta por la KEK maestra.
-- El value EN CLARO desaparece de la fila: solo vive en memoria en el borde de la
-- app (repo). push_name sigue en claro (dato de negocio, R-d del ADR).
--
-- El runner es hash-based FULL-REPLAY (re-aplica todos los structure/*.sql en
-- orden alfabético al cambiar el hash); este script corre DESPUÉS de 0005 y debe
-- poder correr N veces sin daño. Guarda idempotente (patrón del re-claveo del
-- Plan 010, 0005):
--   * si contacts todavía tiene la columna `value` (plano, la creó 0005) se
--     DESCARTA para recrearla con el esquema cifrado (clean-slate);
--   * si ya está el esquema cifrado (existe value_bidx) NO se toca.
-- PRE-PRODUCCIÓN sin datos reales -> clean-slate, SIN backfill (design.md §10.G).
-- NO toca flow_state ni flow_definitions.
-- ADR-0009: aquí SOLO vive contenido de NEGOCIO cifrado; la KEK NO vive en esta BD
-- (env/secret store, §10.A). NUNCA la DEK del Edge ni el store de whatsmeow (ADR-0007).
-- ============================================================

-- Descarta el contacts en claro (con columna `value`) para recrearlo cifrado.
-- Si ya está el esquema cifrado (value_bidx) NO hace nada -> N veces inocuo.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'contacts'
          AND column_name = 'value'
    ) THEN
        DROP TABLE public.contacts;
    END IF;
END $$;

-- ------------------------------------------------------------
-- contacts cifrado: una fila por (tenant_id, kind, value_bidx). Un mismo
-- contact_id puede tener VARIAS filas (una por kind), todas con el mismo
-- contact_id (fusión, design.md §5). La PK (tenant_id, kind, value_bidx)
-- DEDUPLICA por índice ciego (antes: por value plano); el índice
-- (tenant_id, contact_id) resuelve la inversa contact_id -> refs (design.md §4).
-- ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.contacts (
    contact_id UUID        NOT NULL DEFAULT gen_random_uuid(),
    tenant_id  UUID        NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    kind       TEXT        NOT NULL,
    value_bidx TEXT        NOT NULL,          -- HMAC(indexKey, tenant||value_norm): busca/deduplica
    value_enc  BYTEA       NOT NULL,          -- envelope AES-256-GCM del value normalizado
    value_dek  BYTEA       NOT NULL,          -- DEK por-valor, envuelta por la KEK
    push_name  TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, kind, value_bidx) -- dedup por índice ciego
);

CREATE INDEX IF NOT EXISTS idx_contacts_id
    ON public.contacts (tenant_id, contact_id);

COMMENT ON TABLE  public.contacts IS 'Identidad flexible de contacto CIFRADA (Plan 011): resuelve (tenant_id, kind, value_bidx) -> contact_id opaco. Solo negocio (ADR-0009); nunca DEK ni store. La KEK vive fuera de esta BD (§10.A).';
COMMENT ON COLUMN public.contacts.contact_id IS 'Identidad OPACA y estable del contacto (UUID); con ella opera el motor. Varias filas (kinds) comparten contact_id (fusión, design.md §5).';
COMMENT ON COLUMN public.contacts.tenant_id  IS 'Tenant dueño del contacto (FK a tenants).';
COMMENT ON COLUMN public.contacts.kind       IS 'Tipo de referencia: phone_e164 | wa_lid | wa_username (extensible; design.md §10.B).';
COMMENT ON COLUMN public.contacts.value_bidx IS 'Índice ciego: hex(HMAC-SHA256(indexKey, tenant_id||0x00||value_norm)). Determinista (dedup/lookup), no invertible sin la indexKey (design.md §10.C).';
COMMENT ON COLUMN public.contacts.value_enc  IS 'Value normalizado cifrado con envelope AES-256-GCM (nonce fresco por fila). El value NUNCA está en claro en la fila (design.md §4).';
COMMENT ON COLUMN public.contacts.value_dek  IS 'DEK por-valor (32B) envuelta por la KEK maestra (§10.B). Se desenvuelve para descifrar value_enc en el borde de la app.';
COMMENT ON COLUMN public.contacts.push_name  IS 'Último push_name visto (dato de negocio, EN CLARO, opcional; R-d del ADR-0017).';
COMMENT ON COLUMN public.contacts.created_at IS 'Alta de la referencia.';
COMMENT ON COLUMN public.contacts.updated_at IS 'Última actualización de la referencia.';
