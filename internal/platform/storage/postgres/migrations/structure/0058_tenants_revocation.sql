-- ============================================================
-- 0058: Kill-switch COMERCIAL por TENANT (Plan 055 · Ola 3 · T3.1, D-055.2).
--
-- POR QUÉ EXISTE ESTA MIGRACIÓN. Hasta hoy la ÚNICA revocación posible es
-- por-instalación (public.leases.revoked, 0003_lease_fleet.sql): corta UN
-- Edge (anti-clon, ADR-0007). No existe ningún concepto de "esta EMPRESA
-- está cortada" -- verificado: public.tenants no tiene columna de estado;
-- la única ALTER TABLE previa (0032_entitlements.sql) añadió plan_id, sin
-- relación. Dos fallos legítimamente distintos (README del Plan 055 §3):
-- comercial (dejó de pagar -> TODAS sus instalaciones, presentes y
-- futuras) vs anti-clon (una máquina comprometida -> solo esa). Ninguno
-- sustituye al otro (D-055.2): "tenant" y "edge" son los DOS sujetos de
-- corte de este plan, ninguno más.
--
-- POR QUÉ UNA COLUMNA Y NO UNA TABLA SATÉLITE (alternativa (b) descartada
-- en design.md §1.2): el camino caliente (issueAndPersist, invocado en
-- cada Heartbeat, cada 30s por Edge conectado) ya hace una consulta contra
-- leases; una columna en tenants resuelve el chequeo del tenant con una
-- única consulta adicional por tenant_id sola, sin JOIN. El motivo/auditor
-- que una tabla satélite ofrecería gratis ya tiene hogar: audit_log, vía
-- el mismo adminHandler que audita todo /admin/tenants/revoke.
--
-- revoked_at TIMESTAMPTZ NULL (no un BOOLEAN): NULL = tenant activo,
-- NOT NULL = kill-switch comercial disparado, y de paso guarda CUÁNDO sin
-- columna extra. Aditiva, sin NOT NULL: todo tenant existente nace NULL
-- (activo), sin backfill.
--
-- DDL idempotente (IF NOT EXISTS); el runner es hash-based FULL-REPLAY
-- (re-aplica TODOS los structure/*.sql al cambiar el hash de cualquiera) y
-- se ejecuta en orden alfabético -- aplicable tanto sobre una BD vacía
-- (arranca desde 0001) como sobre una con datos preexistentes, sin perder
-- filas ni romper ninguna migración posterior.
--
-- Esta migración BUMPEA SchemaVersion (migrations/version.go): 0.32.0 ->
-- 0.33.0, en el commit que el Plan 055 designe para publicarse (regla de
-- version.go: UN bump por plan, no uno por ola intermedia).
--
-- CERO PII / CERO llaves: solo un timestamp de estado administrativo del
-- tenant. Zero-knowledge (ADR-0007/0009) intacto: esta columna no guarda
-- ni DEK ni ningún material criptográfico, solo AUTORIZACIÓN.
-- ============================================================

ALTER TABLE public.tenants ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ;

COMMENT ON COLUMN public.tenants.revoked_at IS
  'NULL = tenant activo. NOT NULL = kill-switch COMERCIAL disparado (Plan 055/D-055.2): ninguna instalación de este tenant puede operar, presente ni futura. Distinto de leases.revoked, que corta UNA instalación (anti-clon, ADR-0007).';
