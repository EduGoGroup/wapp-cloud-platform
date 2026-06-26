-- ============================================================
-- 0003: Lease + Fleet (Plan 005 · T4).
-- Dos tablas que cuelgan de public.tenants:
--   * leases         — metadatos de autorización del kill-switch (ADR-0007).
--   * fleet_sessions — estado online/offline de cada sesión CloudLink de un Edge.
-- ADR-0007 (doble llave) / ADR-0009 (zero-knowledge): aquí SOLO viven
-- metadatos de AUTORIZACIÓN. El lease NUNCA contiene la DEK ni ninguna llave
-- privada; el blob firmado del lease (Ed25519) viaja por el stream, no se
-- persiste aquí: estas columnas solo reflejan su estado para auditoría/admin.
-- DDL idempotente (IF NOT EXISTS); el runner lo (re)aplica en orden alfabético.
-- ============================================================

-- ------------------------------------------------------------
-- leases: estado vigente del lease por Edge (granularidad POR-EDGE, ADR-0007).
-- Clave por (tenant_id, edge_id): un Edge tiene a lo sumo un lease vigente. El
-- counter es monótono creciente (anti-replay); lo ancla el Heartbeat del Edge.
-- ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.leases (
    tenant_id  UUID        NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    edge_id    TEXT        NOT NULL,
    counter    BIGINT      NOT NULL DEFAULT 0,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked    BOOLEAN     NOT NULL DEFAULT false,
    issued_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, edge_id)
);

COMMENT ON TABLE  public.leases IS 'Estado de autorización (kill-switch, ADR-0007) por Edge. Solo metadatos; nunca la DEK ni el blob firmado.';
COMMENT ON COLUMN public.leases.tenant_id  IS 'Tenant dueño del Edge (FK a tenants).';
COMMENT ON COLUMN public.leases.edge_id    IS 'Identidad del Edge (CommonName del cert mTLS).';
COMMENT ON COLUMN public.leases.counter    IS 'Counter monótono del último lease emitido (anti-replay); lo ancla el Heartbeat.';
COMMENT ON COLUMN public.leases.expires_at IS 'Vencimiento del lease vigente (TTL, p.ej. 5 min).';
COMMENT ON COLUMN public.leases.revoked    IS 'true = kill-switch disparado (pegajoso); el Edge deja de operar.';
COMMENT ON COLUMN public.leases.issued_at  IS 'Primera emisión del lease para este Edge.';
COMMENT ON COLUMN public.leases.updated_at IS 'Última emisión/renovación/revocación.';

-- ------------------------------------------------------------
-- fleet_sessions: estado de cada sesión CloudLink (un stream gRPC vivo) de un
-- Edge. El estado es DERIVADO del stream (online al conectar, offline al caer);
-- la fuente viva está en memoria (session.Registry), esta tabla la durabiliza.
-- ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.fleet_sessions (
    tenant_id         UUID        NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    edge_id           TEXT        NOT NULL,
    session_id        TEXT        NOT NULL,
    state             TEXT        NOT NULL DEFAULT 'offline',
    capabilities      JSONB,
    last_connected_at TIMESTAMPTZ,
    last_seen_at      TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, edge_id, session_id),
    CONSTRAINT fleet_sessions_state_chk CHECK (state IN ('online', 'offline'))
);

CREATE INDEX IF NOT EXISTS idx_fleet_sessions_tenant
    ON public.fleet_sessions (tenant_id);

COMMENT ON TABLE  public.fleet_sessions IS 'Estado online/offline de las sesiones CloudLink de cada Edge. Estado derivado del stream vivo.';
COMMENT ON COLUMN public.fleet_sessions.tenant_id         IS 'Tenant dueño del Edge (FK a tenants).';
COMMENT ON COLUMN public.fleet_sessions.edge_id           IS 'Identidad del Edge (CommonName del cert mTLS).';
COMMENT ON COLUMN public.fleet_sessions.session_id        IS 'Identificador de la sesión (stream CloudLink); lo provee el Edge.';
COMMENT ON COLUMN public.fleet_sessions.state             IS 'online = stream vivo; offline = stream caído. CHECK acota los valores.';
COMMENT ON COLUMN public.fleet_sessions.capabilities      IS 'Capacidades del Edge (design.md §3). El contrato CloudLink v0.1.0 aún NO transporta capacidades: queda NULL hasta que el contrato las exponga (no se inventa formato).';
COMMENT ON COLUMN public.fleet_sessions.last_connected_at IS 'Marca de la última vez que la sesión pasó a online.';
COMMENT ON COLUMN public.fleet_sessions.last_seen_at      IS 'Marca de la última señal de la sesión (conexión/heartbeat/desconexión).';
COMMENT ON COLUMN public.fleet_sessions.created_at        IS 'Alta de la fila.';
COMMENT ON COLUMN public.fleet_sessions.updated_at        IS 'Última actualización de estado.';
