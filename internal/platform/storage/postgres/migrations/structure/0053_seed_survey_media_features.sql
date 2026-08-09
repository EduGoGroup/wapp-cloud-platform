-- ============================================================
-- 0053: `survey` y `media` entran en la taxonomía de planes
-- (Plan 043 · Ola 2 · T2.3, reparto de producto de Jhoan 2026-08-09).
--
-- QUÉ SIEMBRA: las dos claves de tipo de evento que la 0039 dejó fuera.
-- `survey` y `media` son tipos de FÁBRICA del motor de flujos (ya
-- implementados, ver `internal/flujos/modules/{survey,media}`), pero nunca
-- tuvieron feature asignada — sin ella, el despachador de nivel superior de
-- T2.3 (menú numérico filtrado por features del tenant) no tiene con qué
-- filtrarlos.
--
-- REPARTO (decisión de producto, no técnica):
--   - `survey` desde `basic`   — es solo lógica conversacional, no cuesta
--     servirla; no hay coste de infraestructura por tenant que la use.
--   - `media`  desde `commerce` — consume almacenamiento R2 y ancho de banda
--     al entregar el PDF/imagen (Plan 017); tiene coste real por uso, así
--     que arranca en el primer paquete de pago.
--
-- HERENCIA HACIA ARRIBA (mismo criterio que la 0039, composición
-- denormalizada — no hay herencia en BD, cada plan repite las heredadas):
--   basic          : + survey
--   commerce       : + survey, media
--   advisor_ai     : + survey, media
--   advisor_ai_pro : + survey, media
--   pro            : + survey, media
--
-- `survey` entra en los CINCO planes (nace en basic, el más bajo). `media`
-- entra en CUATRO (nace en commerce): `basic` NO la recibe.
--
-- CERO PII / CERO llaves: catálogo de derechos comerciales, nada más. No
-- toca `tenants.plan_id` de ningún tenant.
--
-- ADITIVA e IDEMPOTENTE: runner hash-based FULL-REPLAY; INSERT ... ON
-- CONFLICT DO NOTHING contra la PK real (`plan_features(plan_id, feature)`)
-- => re-aplicable N veces sin duplicar ni fallar. NO clean-slate. Mismo
-- mecanismo que 0039_seed_plan_taxonomy.sql:50-92.
-- ============================================================

INSERT INTO public.plan_features (plan_id, feature) VALUES
    -- survey — nace en basic, hereda hacia arriba a los cinco planes
    ('basic',          'survey'),
    ('commerce',       'survey'),
    ('advisor_ai',     'survey'),
    ('advisor_ai_pro', 'survey'),
    ('pro',            'survey'),
    -- media — nace en commerce, hereda hacia arriba (basic NO la recibe)
    ('commerce',       'media'),
    ('advisor_ai',     'media'),
    ('advisor_ai_pro', 'media'),
    ('pro',            'media')
ON CONFLICT (plan_id, feature) DO NOTHING;
