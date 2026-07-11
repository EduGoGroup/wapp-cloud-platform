-- ============================================================
-- 0033: Config de intenciones por tenant + grants de administración (Plan 029 · T5).
-- Almacena el BLOB de configuración del clasificador LLM por tenant (nombres de
-- intents, ejemplos few-shot, params permitidos, umbral), editado en el Cloud vía
-- PUT /api/v1/intents y empujado al Edge por ConfigUpdate (ADR-0021). El contrato
-- del blob lo valida wapp-shared/intents antes de persistir.
--
-- intent_configs(tenant_id PK): UN blob de intents por tenant.
--   * version — hash corto (sha256, 12 hex) del blob NORMALIZADO; lo fija el
--     servidor en cada PUT. Es la versión de ENTIDAD (idempotencia del push del
--     ADR-0021: el Edge ignora una version ya aplicada), distinta del campo
--     `version` INTERNO del blob (que autora el administrador).
--   * config — JSONB validado por wapp-shared/intents (ParseAndValidate).
--
-- DATO DE NEGOCIO EN CLARO (ADR-0009): configuración autorada en el Cloud, NUNCA
-- PII, número/JID ni llaves. tenant_id TEXT (mismo criterio que tenant_content):
-- aislamiento por (tenant_id) en toda query (INV-8).
--
-- GRANTS (T5): la administración exige los scopes intents.read / intents.write:
--   * tenant_admin ('*')   ya los cubre por glob        -> NO se siembra nada.
--   * viewer       ('*.read') ya cubre intents.read      -> idem.
--   * operator NO tiene glob amplio -> se le añaden los DOS grants EXPLÍCITOS
--     (hermanos de 0024_iam_triggers_grants.sql). Extiende el seed de
--     0015_iam_roles.sql (roles = PLANTILLAS globales, tenant_id NULL).
--
-- ADITIVA e IDEMPOTENTE: runner hash-based FULL-REPLAY; CREATE ... IF NOT EXISTS +
-- ON CONFLICT DO NOTHING => re-aplicable N veces sin duplicar. NO clean-slate.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.intent_configs (
    tenant_id  TEXT        PRIMARY KEY,       -- tenant dueño del blob (aislamiento INV-8)
    version    TEXT        NOT NULL,          -- hash corto del blob normalizado (lo fija el servidor)
    config     JSONB       NOT NULL,          -- blob validado por wapp-shared/intents
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE  public.intent_configs         IS 'Config del clasificador LLM por tenant (Plan 029, ADR-0020/0021). Dato de NEGOCIO EN CLARO (ADR-0009). CERO PII/llaves. Aislamiento por tenant_id (INV-8).';
COMMENT ON COLUMN public.intent_configs.version IS 'Hash corto (sha256 12 hex) del blob normalizado; versión de ENTIDAD para la idempotencia del push (ADR-0021), distinta del version interno del blob.';

-- Grants de administración de intents para el rol canónico `operator`.
INSERT INTO public.iam_role_grants (id, role_id, pattern, effect) VALUES
    ('20000000-0000-0000-0000-00000000000d', '10000000-0000-0000-0000-000000000002', 'intents.read',  'allow'),
    ('20000000-0000-0000-0000-00000000000e', '10000000-0000-0000-0000-000000000002', 'intents.write', 'allow')
ON CONFLICT (id) DO NOTHING;
