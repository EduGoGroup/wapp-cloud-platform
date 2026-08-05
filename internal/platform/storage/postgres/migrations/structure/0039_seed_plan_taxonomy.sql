-- ============================================================
-- 0039: Taxonomía de paquetes comerciales — SEED de plans/plan_features
-- (Plan 040 · T1.1, design.md §D-040.1/§D-040.2, ADR-0022 · ADR-0033).
--
-- QUÉ SIEMBRA: los tres paquetes comerciales que faltaban (`commerce`,
-- `advisor_ai`, `advisor_ai_pro`) y la composición en features de los CINCO
-- planes. La 0032 dejó el MODELO montado pero el catálogo casi vacío (solo
-- `basic` sin features y `pro` con `llm_intent`): sin taxonomía, el Resolver
-- responde false a todo lo que no sea `llm_intent` y no hay nada que gatear.
--
-- `basic` y `pro` NO se re-crean ni se renombran: ya existen desde la 0032 y se
-- REUTILIZAN (design §D-040.2). `basic` pasa a ser el paquete Básico —sus
-- tenants GANAN features, nunca pierden (no-regresión)— y `pro` es el plan
-- INTERNO DE LABORATORIO, al que se le completan las 12 claves; su
-- `('pro','llm_intent')` de la 0032 lo absorbe el ON CONFLICT.
--
-- COMPOSICIÓN DENORMALIZADA: cada plan lista TODAS sus features. NO hay herencia
-- en BD — la notación «Comercio + …» del design es solo documental; el lookup
-- del Resolver es un JOIN plano contra plan_features (postgres.go), y una
-- herencia implícita lo obligaría a recursión.
--
--   basic          : menu, cart_basic, intakes_export
--   commerce       : basic + catalog_import, crm_bridge
--   advisor_ai     : commerce + llm_intent
--   advisor_ai_pro : advisor_ai + llm_intake, api_llm, stt_audio, owner_app
--   pro            : las 12 (advisor_ai_pro + passive_profiles, multi_empresa)
--
-- `passive_profiles` y `multi_empresa` NO entran en ningún paquete comercial:
-- son ADD-ON y se activan tenant a tenant por override en `tenant_features`
-- (mecánica existente de la 0032, que GANA sobre el plan), hasta que exista
-- decisión de empaque. Por eso solo aparecen en `pro` (laboratorio).
--
-- CERO PII / CERO llaves: catálogo de derechos comerciales, nada más. No toca
-- `tenants.plan_id` de ningún tenant (el tenant e2e conserva su plan `pro` y con
-- él `llm_intent`): un seed de catálogo no reasigna planes.
--
-- ADITIVA e IDEMPOTENTE: runner hash-based FULL-REPLAY; INSERT ... ON CONFLICT
-- DO NOTHING contra las PKs reales (`plans(id)`, `plan_features(plan_id,
-- feature)`) => re-aplicable N veces sin duplicar ni fallar. NO clean-slate.
-- ============================================================

-- Paquetes comerciales nuevos. `basic` y `pro` los sembró la 0032 y NO se tocan.
INSERT INTO public.plans (id, name) VALUES
    ('commerce',       'Comercio'),
    ('advisor_ai',     'Asesor IA'),
    ('advisor_ai_pro', 'Asesor IA Pro')
ON CONFLICT (id) DO NOTHING;

-- Composición por plan (denormalizada: cada plan repite las heredadas).
INSERT INTO public.plan_features (plan_id, feature) VALUES
    -- basic — paquete Básico
    ('basic',          'menu'),
    ('basic',          'cart_basic'),
    ('basic',          'intakes_export'),
    -- commerce — Básico + comercio
    ('commerce',       'menu'),
    ('commerce',       'cart_basic'),
    ('commerce',       'intakes_export'),
    ('commerce',       'catalog_import'),
    ('commerce',       'crm_bridge'),
    -- advisor_ai — Comercio + clasificador LLM (ADR-0020)
    ('advisor_ai',     'menu'),
    ('advisor_ai',     'cart_basic'),
    ('advisor_ai',     'intakes_export'),
    ('advisor_ai',     'catalog_import'),
    ('advisor_ai',     'crm_bridge'),
    ('advisor_ai',     'llm_intent'),
    -- advisor_ai_pro — Asesor IA + captación/LLM avanzado
    ('advisor_ai_pro', 'menu'),
    ('advisor_ai_pro', 'cart_basic'),
    ('advisor_ai_pro', 'intakes_export'),
    ('advisor_ai_pro', 'catalog_import'),
    ('advisor_ai_pro', 'crm_bridge'),
    ('advisor_ai_pro', 'llm_intent'),
    ('advisor_ai_pro', 'llm_intake'),
    ('advisor_ai_pro', 'api_llm'),
    ('advisor_ai_pro', 'stt_audio'),
    ('advisor_ai_pro', 'owner_app'),
    -- pro — plan interno de laboratorio: las 12 claves, add-ons incluidos
    ('pro',            'menu'),
    ('pro',            'cart_basic'),
    ('pro',            'intakes_export'),
    ('pro',            'catalog_import'),
    ('pro',            'crm_bridge'),
    ('pro',            'llm_intent'),
    ('pro',            'llm_intake'),
    ('pro',            'api_llm'),
    ('pro',            'stt_audio'),
    ('pro',            'owner_app'),
    ('pro',            'passive_profiles'),
    ('pro',            'multi_empresa')
ON CONFLICT (plan_id, feature) DO NOTHING;
