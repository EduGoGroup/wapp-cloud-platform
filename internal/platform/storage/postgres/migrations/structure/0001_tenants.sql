-- ============================================================
-- 0001: Tenants — ancla de multi-tenancy de la Plataforma Cloud.
-- Tabla raíz a la que cuelgan el resto de entidades del Gateway
-- (enrollment_codes, edge_certs, fleet_sessions, leases) en T3/T4.
-- Esquema mínimo y defendible; las columnas finas llegan con sus tablas.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.tenants (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    slug         TEXT        NOT NULL UNIQUE,
    display_name TEXT        NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE  public.tenants IS 'Ancla de multi-tenancy; cada Edge y sus credenciales pertenecen a un tenant.';
COMMENT ON COLUMN public.tenants.slug IS 'Identificador legible y estable del tenant (único).';
