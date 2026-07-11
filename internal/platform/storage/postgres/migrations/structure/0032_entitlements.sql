-- ============================================================
-- 0032: Planes y features por tenant — ENTITLEMENTS (Plan 029 · T4, ADR-0022).
-- wApp gana la noción de PLAN COMERCIAL y de FEATURES habilitables por tenant: el
-- IAM (Plan 018) autoriza OPERACIONES por RBAC, pero nada responde "¿este tenant
-- tiene derecho a la capacidad X?". El clasificador LLM (ADR-0020) es la primera
-- feature gateada (`llm_intent`).
--
-- MODELO (ADR-0022):
--   * plans(id, name)                 — catálogo de planes (basic, pro, …).
--   * plan_features(plan_id, feature) — features que trae cada plan.
--   * tenants.plan_id (FK, NULL)      — plan del tenant; NULL ⇒ se trata como basic.
--   * tenant_features(tenant_id, feature, enabled) — OVERRIDE quirúrgico por tenant
--     (activa o desactiva una feature con independencia del plan).
--
-- RESOLUCIÓN (puerto Entitlements.Has): el override de tenant_features GANA; si no
-- hay override, mandan las features del plan (COALESCE(plan_id, 'basic')). Un tenant
-- SIN override y con plan NULL/basic no tiene `llm_intent` ⇒ COMPORTAMIENTO ACTUAL
-- INTACTO (los tenants existentes quedan NULL/basic tras esta migración).
--
-- SEED: plan `basic` (SIN features) y `pro` (feature `llm_intent`). El plan por
-- tenant es dato administrativo (seed/SQL/endpoint admin); no hay facturación en
-- este corte, solo el modelo que la hará posible. CERO PII / CERO llaves: solo
-- catálogo de derechos comerciales.
--
-- ADITIVA e IDEMPOTENTE: runner hash-based FULL-REPLAY; CREATE ... IF NOT EXISTS +
-- ADD COLUMN IF NOT EXISTS + ON CONFLICT DO NOTHING => re-aplicable N veces sin
-- daño. NO clean-slate.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.plans (
    id   TEXT PRIMARY KEY,   -- 'basic', 'pro', … (identificador estable del plan)
    name TEXT NOT NULL        -- nombre legible
);

CREATE TABLE IF NOT EXISTS public.plan_features (
    plan_id TEXT NOT NULL REFERENCES public.plans(id) ON DELETE CASCADE,
    feature TEXT NOT NULL,    -- 'llm_intent', … (espacio de nombres de features)
    PRIMARY KEY (plan_id, feature)
);

-- plan_id del tenant: NULL ⇒ tratado como 'basic' por la resolución (tenants
-- existentes intactos). FK laxa (sin cascada) para no acoplar el ciclo de vida.
ALTER TABLE public.tenants
    ADD COLUMN IF NOT EXISTS plan_id TEXT REFERENCES public.plans(id);

CREATE TABLE IF NOT EXISTS public.tenant_features (
    tenant_id UUID    NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    feature   TEXT    NOT NULL,
    enabled   BOOLEAN NOT NULL,   -- override explícito: TRUE activa, FALSE desactiva
    PRIMARY KEY (tenant_id, feature)
);

-- Seed de planes base. IDs estables; el plan `pro` trae `llm_intent`.
INSERT INTO public.plans (id, name) VALUES
    ('basic', 'Basic'),
    ('pro',   'Pro')
ON CONFLICT (id) DO NOTHING;

INSERT INTO public.plan_features (plan_id, feature) VALUES
    ('pro', 'llm_intent')
ON CONFLICT (plan_id, feature) DO NOTHING;

COMMENT ON TABLE  public.plans          IS 'Catálogo de planes comerciales (ADR-0022). Solo derechos, CERO PII.';
COMMENT ON TABLE  public.plan_features  IS 'Features que trae cada plan (ADR-0022). p.ej. pro ⇒ llm_intent.';
COMMENT ON TABLE  public.tenant_features IS 'Override de feature por tenant (ADR-0022): gana sobre el plan. enabled=TRUE activa, FALSE desactiva.';
COMMENT ON COLUMN public.tenants.plan_id IS 'Plan del tenant (FK a plans); NULL ⇒ se resuelve como basic (tenants existentes intactos).';
