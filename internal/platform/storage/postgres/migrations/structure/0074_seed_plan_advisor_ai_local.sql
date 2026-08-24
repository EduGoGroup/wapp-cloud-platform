-- ============================================================
-- 0074: `advisor_ai_local` — el paquete que vende la CAPACIDAD sin la VÍA
-- (Plan 044 · Ola 1.5 · T1.5-2, cierra el tercio abierto de T1.5-1;
--  ADR-0044, D-044.28, REQ-33. Amplía la taxonomía de la 0039/0053).
--
-- QUÉ SIEMBRA: UN plan nuevo, `advisor_ai_local` = lo mismo que `advisor_ai`
-- **más `llm_intake`** y **sin `api_llm`**. Nada más. No borra, no renombra y no
-- reasigna el plan de ningún tenant.
--
-- ------------------------------------------------------------
-- POR QUÉ EXISTE: ERA EL SÍNTOMA, NO EL PROBLEMA
-- ------------------------------------------------------------
-- El barrido de T1.5-1 encontró que NINGÚN gate de capacidad consultaba
-- `api_llm`: el agregador y el productor del hilo miran `llm_intake` y solo
-- `llm_intake`, que es lo correcto (ADR-0044). Lo que fallaba estaba AQUÍ, en el
-- catálogo: **no existía ningún plan que vendiera `llm_intake` sin `api_llm`**
-- —en la 0039 las dos claves entran juntas, en `advisor_ai_pro` y en `pro`—, así
-- que la combinación «capacidad sí, vía externa no» JAMÁS se había ejercitado en
-- campo, y una doctrina equivocada (D-044.6: «llm_intake exige api_llm») pudo
-- vivir meses en los comentarios sin que nadie tropezara con ella.
--
-- Un plan es la forma de que esa combinación EXISTA y se pueda vender:
--
--   advisor_ai_local : advisor_ai + llm_intake   (y NO api_llm)
--
-- Es el paquete del tenant que capta pedidos con IA **en su propio fierro**
-- (`tenant_llm.via='local'`, ADR-0045): abre ventanas, archiva el hilo cifrado y
-- crea jobs — sin mandar una sola letra a un proveedor externo y sin pagar la
-- cuenta de nadie. `api_llm` no gatea la capacidad; gatea **configurar y usar
-- credenciales de un tercero**, que es un derecho distinto y con precio distinto.
--
-- ------------------------------------------------------------
-- 🔴 LO QUE ESTA MIGRACIÓN **NO** HACE, Y ES DELIBERADO (decisión de Jhoan,
--     2026-08-23): NO se retira `api_llm` de `advisor_ai_pro` NI de `pro`.
-- ------------------------------------------------------------
-- Los dos siguen vendiendo `llm_intake` y `api_llm` JUNTAS, y eso es correcto
-- para lo que son: `advisor_ai_pro` es el paquete de quien sí quiere la vía API,
-- y `pro` es el plan interno de laboratorio, que las lleva todas por definición.
-- Aquí no sobraba una feature: **faltaba un plan**.
--
-- ⚠️ **DEUDA DECLARADA, con dueño pendiente**: queda sin decidir si el catálogo
-- comercial debe además ofrecer más combinaciones (p. ej. `commerce` +
-- `llm_intake` sin el resto del asesor), y si algún día `advisor_ai_pro` debería
-- partirse en dos. **Esa decisión NO se toma sin datos de tenants reales**: hoy
-- no hay tenants suficientes en cada paquete como para saber qué se compra
-- junto, y empaquetar a ciegas se paga migrando tenants vivos después. Retirar
-- un derecho a un tenant que ya lo tiene NO es aditivo: es una regresión con
-- nombre, y el criterio de no-regresión de la 0039 («sus tenants GANAN features,
-- nunca pierden») manda mientras nadie decida lo contrario con datos delante.
--
-- ------------------------------------------------------------
-- COMPOSICIÓN DENORMALIZADA, como toda la taxonomía: cada plan lista TODAS sus
-- features. NO hay herencia en BD — la notación «advisor_ai + …» es documental;
-- el lookup del Resolver es un JOIN plano contra plan_features (postgres.go), y
-- una herencia implícita lo obligaría a recursión (0039).
--
--   advisor_ai_local : menu, cart_basic, intakes_export, catalog_import,
--                      crm_bridge, llm_intent, survey, media  ← todo advisor_ai
--                      + llm_intake                            ← lo que añade
--                      (y NINGUNA de: api_llm, stt_audio, owner_app,
--                       passive_profiles, multi_empresa)
--
-- ⚠️ `survey` y `media` entran porque la **0053** se las dio a `advisor_ai`
-- DESPUÉS de la 0039, y este plan se define como «lo mismo que `advisor_ai` más
-- `llm_intake`»: leer solo la 0039 daría una composición incompleta y este plan
-- nacería siendo MENOS que el que dice ampliar. Se dice aquí porque es el error
-- fácil de cometer al copiar el molde.
--
-- `passive_profiles` y `multi_empresa` NO entran: son ADD-ON y se activan tenant
-- a tenant por override en `tenant_features` (mecánica de la 0032), hasta que
-- exista decisión de empaque. Solo aparecen en `pro` (laboratorio).
--
-- CERO PII / CERO llaves: catálogo de derechos comerciales, nada más. NO toca
-- `tenants.plan_id` de ningún tenant — un seed de catálogo no reasigna planes, y
-- mover un tenant a este paquete es un acto comercial, no una migración.
--
-- ADITIVA e IDEMPOTENTE: runner hash-based FULL-REPLAY; `INSERT … ON CONFLICT DO
-- NOTHING` contra las PKs reales (`plans(id)`, `plan_features(plan_id, feature)`)
-- ⇒ re-aplicable N veces sin duplicar ni fallar. NO clean-slate. ⚠️ **NO sube
-- `SchemaVersion`** (INV-8: un solo bump, en T6.2).
--
-- ------------------------------------------------------------
-- VERIFICACIÓN — ⏳ ESCRITA SIN BASE DELANTE. La ejecuta el barrido del CLI.
-- ------------------------------------------------------------
--
-- (V1) El plan existe y su composición es EXACTAMENTE la de arriba:
--
--   SELECT feature FROM public.plan_features
--    WHERE plan_id='advisor_ai_local' ORDER BY feature;
--
--   Esperado, NUEVE filas: cart_basic · catalog_import · crm_bridge ·
--   intakes_export · llm_intake · llm_intent · media · menu · survey.
--   🔴 `api_llm` NO puede salir: si sale, este plan no sirve para lo que existe.
--
-- (V2) La diferencia con `advisor_ai` es UNA sola feature, y es `llm_intake`:
--
--   SELECT feature FROM public.plan_features WHERE plan_id='advisor_ai_local'
--   EXCEPT
--   SELECT feature FROM public.plan_features WHERE plan_id='advisor_ai';
--                                                  -- esperado: llm_intake, y nada más
--   SELECT feature FROM public.plan_features WHERE plan_id='advisor_ai'
--   EXCEPT
--   SELECT feature FROM public.plan_features WHERE plan_id='advisor_ai_local';
--                                                  -- esperado: CERO filas
--
--   🔴 Las DOS mitades: la primera prueba que añade lo que dice añadir, la
--   segunda que no PERDIÓ nada por el camino (el fallo real al copiar el molde
--   de la 0039 sin leer la 0053).
--
-- (V3) No se le quitó nada a nadie — el criterio de no-regresión:
--
--   SELECT plan_id FROM public.plan_features WHERE feature='api_llm' ORDER BY plan_id;
--                                        -- esperado: advisor_ai_pro, pro (los dos de siempre)
--
-- (V4) El catálogo tiene ahora SEIS paquetes:
--
--   SELECT id, name FROM public.plans ORDER BY id;
--                    -- esperado: advisor_ai · advisor_ai_local · advisor_ai_pro ·
--                    --           basic · commerce · pro
-- ============================================================

-- El paquete nuevo. `basic`/`pro` los sembró la 0032 y los tres comerciales la
-- 0039: ninguno se toca.
INSERT INTO public.plans (id, name) VALUES
    ('advisor_ai_local', 'Asesor IA Local')
ON CONFLICT (id) DO NOTHING;

-- Composición COMPLETA (denormalizada). Las ocho primeras son la foto de
-- `advisor_ai` tal como quedó tras la 0053; la novena es lo que este plan añade.
INSERT INTO public.plan_features (plan_id, feature) VALUES
    -- lo mismo que advisor_ai (0039 + 0053)
    ('advisor_ai_local', 'menu'),
    ('advisor_ai_local', 'cart_basic'),
    ('advisor_ai_local', 'intakes_export'),
    ('advisor_ai_local', 'catalog_import'),
    ('advisor_ai_local', 'crm_bridge'),
    ('advisor_ai_local', 'llm_intent'),
    ('advisor_ai_local', 'survey'),
    ('advisor_ai_local', 'media'),
    -- lo que este paquete añade, y la razón entera de que exista:
    -- la CAPACIDAD de captación con IA, SIN la vía externa (ADR-0044).
    ('advisor_ai_local', 'llm_intake')
    -- 🔴 NO se añade 'api_llm'. Es la línea que no está, y es el plan entero.
ON CONFLICT (plan_id, feature) DO NOTHING;
