-- ============================================================
-- 0036: Diagnóstico remoto lado Cloud (Plan 031 · T5, ADR-0023, capa 3).
-- El incidente del 2026-07-11 exigió `lsof`/`kill -QUIT` EN la máquina del cliente.
-- A ~1000 Edges sin SSH eso es inviable; esta migración habilita la versión a
-- distancia: el Cloud pide un bundle (DiagnosticsRequest por el stream CloudLink) y
-- el Edge responde (DiagnosticsBundle) con ring buffer de logs + dump de goroutines
-- + snapshot de subsistemas.
--
-- DOS objetos:
--   1) tenant_diagnostics_consent — gobernanza del bundle (ADR-0023, DECIDIDA por el
--      usuario 2026-07-11): flag de consentimiento POR TENANT con default ON (opt-out).
--      El default ON se expresa por AUSENCIA de fila (⇒ consentido) o fila enabled=TRUE;
--      una fila enabled=FALSE = el tenant SE EXCLUYÓ. El Cloud gatea cada solicitud por
--      este flag (403 si está apagado).
--   2) diagnostics_bundles — la solicitud (pending) y su bundle (ready), correlacionados
--      por command_id. Retención con TTL: expires_at = requested_at + WAPP_DIAGNOSTICS_
--      BUNDLE_TTL; la limpieza es PEREZOSA (al crear una solicitud se borran las vencidas;
--      al descargar una vencida se borra y se responde 410), sin jobs nuevos (estilo del
--      TTL conversacional del 029). Un command_id es un UUIDv4 inadivinable: la
--      correlación por él es segura, y el SaveBundle además acota por (tenant_id,
--      session_id) de la identidad mTLS (defensa en profundidad).
--
-- FRONTERA ZERO-KNOWLEDGE (ADR-0007): el bundle es SOLO material operativo de
-- diagnóstico. El Edge lo sanea en origen (gate ZK verificable, Plan 031 · T8): JAMÁS
-- llaves, DEK, credenciales, tokens ni contenido de mensajes. Aquí el Cloud solo
-- almacena lo que llega; log_tail/goroutine_dump/subsystems_json son TEXT OPACO (no se
-- consultan por dentro; TEXT y no JSONB para admitir un snapshot vacío sin romper).
--
-- GRANTS (T5): la administración exige el scope diagnostics.request (POST del request
-- y GET de descarga usan el MISMO grant):
--   * tenant_admin ('*')   ya lo cubre por glob        -> NO se siembra nada.
--   * viewer       ('*.read') NO cubre diagnostics.request (no es una .read) -> NO lo tiene.
--   * operator NO tiene glob amplio -> se le añade el grant EXPLÍCITO (hermano de
--     0033_intent_configs.sql). Extiende el seed de 0015_iam_roles.sql (roles =
--     PLANTILLAS globales, tenant_id NULL).
--
-- ADITIVA e IDEMPOTENTE (runner hash-based FULL-REPLAY): CREATE ... IF NOT EXISTS +
-- INSERT ... ON CONFLICT DO NOTHING ⇒ re-aplicable N veces sin daño. NO clean-slate.
-- SchemaVersion NO sube: 0035 (T3) ya la subió a 0.21.0 en esta MISMA Ola 1; la 0036
-- rueda bajo esa versión (precedente 029: 0032-0034 compartieron 0.20.0). El full-replay
-- por hash re-aplica igual (isUpToDate compara versión Y hash).
-- ============================================================

-- 1) Consentimiento por tenant (default ON = opt-out). Ausencia de fila = consentido.
CREATE TABLE IF NOT EXISTS public.tenant_diagnostics_consent (
    tenant_id  UUID        PRIMARY KEY REFERENCES public.tenants(id) ON DELETE CASCADE,
    enabled    BOOLEAN     NOT NULL DEFAULT TRUE,   -- FALSE = el tenant se excluyó del diagnóstico remoto
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE  public.tenant_diagnostics_consent         IS 'Consentimiento POR TENANT del diagnóstico remoto (ADR-0023, decidido por el usuario 2026-07-11): default ON (opt-out). Ausencia de fila ⇒ consentido; enabled=FALSE ⇒ el tenant se excluyó. CERO PII/llaves.';
COMMENT ON COLUMN public.tenant_diagnostics_consent.enabled IS 'FALSE = opt-out (el Cloud rechaza toda solicitud de bundle con 403). Ausencia de fila o TRUE = consentido (default ON).';

-- 2) Solicitudes de diagnóstico y sus bundles (correlación por command_id, TTL perezoso).
CREATE TABLE IF NOT EXISTS public.diagnostics_bundles (
    command_id      TEXT        PRIMARY KEY,                     -- UUIDv4 que correlaciona request↔bundle (inadivinable)
    tenant_id       UUID        NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,  -- dueño (aislamiento INV-8)
    session_id      TEXT        NOT NULL,                        -- sesión objetivo del diagnóstico
    requested_by    TEXT        NOT NULL,                        -- subject del JWT que lo pidió (auditoría; ID opaco, no PII)
    requested_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,                        -- requested_at + TTL de retención (limpieza perezosa)
    status          TEXT        NOT NULL DEFAULT 'pending',      -- pending (solicitado) | ready (bundle recibido)
    received_at     TIMESTAMPTZ,                                 -- cuándo llegó el bundle (NULL mientras pending)
    log_tail        TEXT,                                        -- ring buffer de logs saneado en origen (TEXT opaco)
    goroutine_dump  TEXT,                                        -- runtime.Stack(all=true) truncado en origen
    subsystems_json TEXT                                         -- snapshot JSON de subsistemas (opaco; TEXT admite vacío)
);

COMMENT ON TABLE  public.diagnostics_bundles                 IS 'Diagnóstico remoto bajo demanda (Plan 031 · T5, ADR-0023 capa 3). Solicitud (pending) + bundle (ready) por command_id. Retención con TTL perezoso. Bundle = material OPERATIVO saneado por el Edge (gate ZK, T8): CERO llaves/DEK/credenciales/PII.';
COMMENT ON COLUMN public.diagnostics_bundles.status          IS 'pending = DiagnosticsRequest emitido, sin respuesta; ready = DiagnosticsBundle recibido y almacenado.';
COMMENT ON COLUMN public.diagnostics_bundles.expires_at      IS 'requested_at + WAPP_DIAGNOSTICS_BUNDLE_TTL. Vencido ⇒ 410 en la descarga + borrado perezoso; también se purgan al crear una nueva solicitud.';
COMMENT ON COLUMN public.diagnostics_bundles.requested_by    IS 'Subject (sub) del JWT que solicitó el diagnóstico. Rastro de auditoría (quién). ID opaco: CERO PII.';

-- Índice para el barrido perezoso de vencidas (DELETE WHERE expires_at < now()).
CREATE INDEX IF NOT EXISTS diagnostics_bundles_expires_idx
    ON public.diagnostics_bundles (expires_at);
-- Índice para la descarga acotada al tenant (INV-8): GET por (tenant_id, command_id).
CREATE INDEX IF NOT EXISTS diagnostics_bundles_tenant_idx
    ON public.diagnostics_bundles (tenant_id, command_id);

-- 3) Grant de administración del diagnóstico remoto para el rol canónico `operator`.
INSERT INTO public.iam_role_grants (id, role_id, pattern, effect) VALUES
    ('20000000-0000-0000-0000-00000000000f', '10000000-0000-0000-0000-000000000002', 'diagnostics.request', 'allow')
ON CONFLICT (id) DO NOTHING;
