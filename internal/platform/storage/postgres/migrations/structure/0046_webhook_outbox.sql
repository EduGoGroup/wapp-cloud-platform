-- ============================================================
-- 0046: Cola durable de entregas salientes (Plan 042 · T1.1, design.md §2).
-- Materializa el T8 pendiente del Plan 018 («webhook durable»): una sola tabla que
-- guarda lo que hay que entregar al puente del CRM y sobrevive a caídas, reinicios y
-- a que el endpoint del cliente esté muerto. NO es un broker: es una cola en
-- PostgreSQL con reclamo por (status, next_attempt_at) — coherente con ADR-0003
-- (sin RabbitMQ/Redis) y con el fan-out por concurrencia Go de la ficha 03.
--
-- CICLO DE VIDA (CHECK explícito, a diferencia de intakes.status):
--   pending    -> hay algo que entregar y su momento aún no llegó o ya llegó
--   delivering -> un worker la reclamó (claim) y está en vuelo
--   delivered  -> el puente respondió 2xx; fin del camino
--   dead       -> se agotaron los reintentos; queda para que el dueño la vea
-- Aquí SÍ hay CHECK porque este vocabulario es INTERNO y cerrado (lo fija el
-- despachador de este mismo plan), al revés que el ciclo de vida de negocio de
-- intakes, cuya máquina de estados vive en Go (internal/intakes/status.go) y no
-- admite un CHECK que obligue a migrar por cada estado nuevo.
--
-- ENTREGA AT-LEAST-ONCE: `payload` guarda una PLANTILLA con los campos baratos y
-- estables, congelados en el momento del encolado (snapshot, no referencia) — si
-- la solicitud cambia después, la entrega en vuelo sigue reflejando lo que se
-- prometió en ESOS campos. `buyer_data` y `variables{}` NO viajan congelados
-- aquí: el descifrado de buyer_data y la lectura de tenant_variables ocurren
-- DENTRO DEL WORKER de la Ola 3, justo antes del POST — nunca en línea con el
-- mensaje de WhatsApp (INV-02; decisión 2026-08-07, ver design.md D-042.9/11).
-- La idempotencia de negocio (intake_id + revision_no) es responsabilidad del
-- puente receptor (contrato wapp-crm-v1, §3).
--
-- tenant_id es TEXT SIN FK, convención de la ficha 3: el tenant sale del token
-- (INV-8), nunca de un JOIN.
--
-- ÍNDICES: webhook_outbox_claim_idx sirve al reclamo del worker
-- (WHERE status='pending' AND next_attempt_at <= now() ... FOR UPDATE SKIP LOCKED);
-- webhook_outbox_tenant_idx sirve a la vista por tenant (bandeja de entregas).
--
-- CERO PII / CERO llaves: el payload lleva el `contact` OPACO del contrato
-- (ADR-0010), jamás un número/JID en claro, y nunca material criptográfico. El
-- secreto de firma HMAC NO vive aquí: vive cifrado en tenant_integrations (0047).
--
-- ADITIVA e IDEMPOTENTE: el runner es hash-based FULL-REPLAY (re-aplica TODOS los
-- structure/*.sql al cambiar el hash de cualquiera); CREATE TABLE/INDEX IF NOT
-- EXISTS + ADD COLUMN IF NOT EXISTS garantizan re-aplicación N veces sin daño ni
-- pérdida de filas. NO clean-slate.
--
-- SIN GRANTS: esta tabla NO tiene endpoint propio en la Ola 1 y el Plan 042 no
-- nombra ningún scope para ella (verificado en design.md/tasks.md). Los grants de
-- este repo (0030/0040/0042 en fichero aparte; 0033/0036 en bloque al final de la
-- migración de su tabla) SIEMPRE acompañan a un endpoint con scope ya definido:
-- sembrar aquí un `integrations.read` inventado dejaría un grant muerto con UUID
-- fijo que después habría que corregir. Cuando la ola que exponga la API de
-- integraciones fije el nombre del scope, el grant va en un fichero aparte al
-- estilo de 0042_intakes_grants.sql (patrón dominante para grants sin tabla nueva).
-- ============================================================

CREATE TABLE IF NOT EXISTS public.webhook_outbox (
    id              BIGSERIAL   PRIMARY KEY,
    tenant_id       TEXT        NOT NULL,
    kind            TEXT        NOT NULL,              -- verbo del contrato: 'intake.push'
    payload         JSONB       NOT NULL,              -- plantilla estable; buyer_data/variables{} los completa el worker (INV-02)
    status          TEXT        NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending','delivering','delivered','dead')),
    attempts        INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Visibilidad del fracaso (T3.4): sin esto, una fila en 'dead' no dice POR QUÉ
-- murió y el dueño se queda mirando un contador. Se añade con ADD COLUMN IF NOT
-- EXISTS (y no dentro del CREATE TABLE) para que una base que ya tenga la tabla
-- creada por una versión anterior de este mismo fichero la gane igual en el replay.
-- NULLable a propósito: una fila que nunca falló no tiene error que contar.
ALTER TABLE public.webhook_outbox ADD COLUMN IF NOT EXISTS last_error TEXT;

CREATE INDEX IF NOT EXISTS webhook_outbox_claim_idx
    ON public.webhook_outbox (status, next_attempt_at);
CREATE INDEX IF NOT EXISTS webhook_outbox_tenant_idx
    ON public.webhook_outbox (tenant_id, created_at);

COMMENT ON TABLE public.webhook_outbox IS
    'Cola DURABLE de entregas salientes hacia el puente del CRM (Plan 042 §2, materializa el T8 del Plan 018). Cola en PostgreSQL, NO un broker (ADR-0003): el worker reclama por (status, next_attempt_at) con FOR UPDATE SKIP LOCKED. Entrega at-least-once; la idempotencia es del receptor (contrato wapp-crm-v1). Dato de NEGOCIO EN CLARO (ADR-0009). CERO PII: el payload correlaciona por contact OPACO (ADR-0010). NUNCA llaves ni secretos: el secreto HMAC vive cifrado en tenant_integrations.';
COMMENT ON COLUMN public.webhook_outbox.id              IS 'Identidad de la entrega (BIGSERIAL). El orden de id es el orden de encolado dentro de un tenant.';
COMMENT ON COLUMN public.webhook_outbox.tenant_id       IS 'Tenant dueño de la entrega (TEXT sin FK, convención de la ficha 3). Sale del token (INV-8), nunca del cuerpo.';
COMMENT ON COLUMN public.webhook_outbox.kind            IS 'Verbo del contrato wapp-crm-v1 que se entrega (hoy ''intake.push''). Sin CHECK: el vocabulario de verbos lo fija el contrato versionado, no la tabla; añadir un verbo no debe costar una migración.';
COMMENT ON COLUMN public.webhook_outbox.payload         IS 'Plantilla congelada en el encolado (snapshot, no referencia) con los campos baratos y estables. buyer_data y variables{} NO viajan aquí: el worker de la Ola 3 los completa justo antes del POST (INV-02, nunca en línea con el mensaje). Lleva el contact OPACO, jamás un número/JID.';
COMMENT ON COLUMN public.webhook_outbox.status          IS 'Ciclo de vida INTERNO de la entrega: pending (por entregar) | delivering (reclamada, en vuelo) | delivered (2xx del puente, terminal) | dead (reintentos agotados, terminal y visible). Con CHECK porque el vocabulario es cerrado y lo fija este plan — al contrario que intakes.status, cuya máquina de estados vive en Go.';
COMMENT ON COLUMN public.webhook_outbox.attempts        IS 'Número de intentos de entrega ya realizados. Alimenta el backoff y el corte a ''dead''.';
COMMENT ON COLUMN public.webhook_outbox.next_attempt_at IS 'Momento a partir del cual la fila es reclamable. Es la mitad temporal del claim (con status): el backoff se implementa empujando esta marca, no durmiendo el worker.';
COMMENT ON COLUMN public.webhook_outbox.created_at      IS 'Momento del encolado. Usa el DEFAULT now().';
COMMENT ON COLUMN public.webhook_outbox.last_error      IS 'Último error de entrega en texto plano para diagnóstico (T3.4): sin esto una fila ''dead'' no dice por qué murió. NULL = nunca falló. NUNCA se escribe aquí el secreto, la cabecera de firma ni el cuerpo de la respuesta con datos del cliente: solo el motivo (código HTTP, timeout, DNS).';
