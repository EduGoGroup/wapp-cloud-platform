-- ============================================================
-- 0002: Enrolamiento por tenant (Plan 005 · T3).
-- Dos tablas que cuelgan de public.tenants:
--   * enrollment_codes — códigos de activación de un solo uso (cloud→edge).
--   * edge_certs        — metadatos de los certificados de Edge EMITIDOS.
-- ADR-0009 (zero-knowledge): aquí SOLO viven materiales PÚBLICOS y metadatos.
-- NUNCA la DEK, ni la clave privada del Edge, ni el store cifrado.
-- DDL idempotente (IF NOT EXISTS); el runner lo (re)aplica en orden alfabético.
-- ============================================================

-- ------------------------------------------------------------
-- enrollment_codes: el Edge presenta uno de estos códigos junto a su CSR.
-- El consumo es de un solo uso (used_at pasa de NULL a now() de forma atómica).
-- ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.enrollment_codes (
    code       TEXT        PRIMARY KEY,
    tenant_id  UUID        NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_enrollment_codes_tenant
    ON public.enrollment_codes (tenant_id);

COMMENT ON TABLE  public.enrollment_codes IS 'Códigos de activación de un solo uso para enrolar un Edge en un tenant.';
COMMENT ON COLUMN public.enrollment_codes.code       IS 'Código de activación; clave natural única, se busca por aquí.';
COMMENT ON COLUMN public.enrollment_codes.tenant_id  IS 'Tenant que se enrola con este código (FK a tenants).';
COMMENT ON COLUMN public.enrollment_codes.expires_at IS 'Vencimiento del código (TTL); el consumo lo rechaza si ya pasó.';
COMMENT ON COLUMN public.enrollment_codes.used_at    IS 'NULL = sin usar; al consumir pasa a now() (un solo uso, atómico).';

-- ------------------------------------------------------------
-- edge_certs: metadatos de cada certificado hoja de Edge emitido por la CA.
-- Se guarda el cert (PEM, material PÚBLICO) para auditoría/revocación futura;
-- la clave privada del Edge NUNCA sale del Edge ni llega aquí (zero-knowledge).
-- ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.edge_certs (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID        NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    subject_cn    TEXT        NOT NULL,
    serial_number TEXT        NOT NULL,
    fingerprint   TEXT        NOT NULL UNIQUE,
    not_before    TIMESTAMPTZ NOT NULL,
    not_after     TIMESTAMPTZ NOT NULL,
    cert_pem      TEXT        NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_edge_certs_tenant
    ON public.edge_certs (tenant_id);

COMMENT ON TABLE  public.edge_certs IS 'Metadatos de certificados de Edge emitidos. Solo material público (ADR-0009).';
COMMENT ON COLUMN public.edge_certs.tenant_id     IS 'Tenant dueño del cert (Organization del Subject; FK a tenants).';
COMMENT ON COLUMN public.edge_certs.subject_cn    IS 'CommonName del CSR: identidad del Edge.';
COMMENT ON COLUMN public.edge_certs.serial_number IS 'Serial del certificado (hex); para auditoría/revocación.';
COMMENT ON COLUMN public.edge_certs.fingerprint   IS 'SHA-256 del DER (hex), único: identidad estable del cert.';
COMMENT ON COLUMN public.edge_certs.not_before    IS 'Inicio de validez del cert.';
COMMENT ON COLUMN public.edge_certs.not_after     IS 'Fin de validez del cert (monitoreo de expiración / mTLS en T4).';
COMMENT ON COLUMN public.edge_certs.cert_pem      IS 'Certificado hoja emitido en PEM (PÚBLICO). Nunca la clave privada.';
