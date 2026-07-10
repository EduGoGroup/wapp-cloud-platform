-- ============================================================
-- 0031: Dedupe PERSISTENTE de ingesta de entrantes (Plan 028 · T6, ADR-0003).
-- Tabla NUEVA ingest_dedupe: registra la clave de idempotencia de cada mensaje
-- ENTRANTE (Edge→Cloud) para cortar los reenvíos del OUTBOX DURABLE del Edge
-- (Plan 027 Ola 3, ADR-0003), que da semántica at-least-once: el mismo mensaje de
-- WhatsApp puede llegar dos veces tras una reconexión.
--
-- POR QUÉ NO BASTA lo que ya había: la idempotencia previa vivía en
-- flow_state.last_wa_message_id (runtime), pero es CONSECUTIVA (solo compara con el
-- ÚLTIMO mensaje procesado); un duplicado INTERCALADO (A, B, A) o el reenvío de un
-- entrante que dispara/escapa un flujo (caminos que NO tocan last_wa_message_id) se
-- colaban y re-ejecutaban efectos o auto-respondían. Esta tabla es la memoria
-- persistente e independiente del estado del flujo.
--
-- CLAVE: (session_id, wa_message_id). wa_message_id es el MessageID de whatsmeow,
-- ESTABLE entre reenvíos (el outbox del Edge reenvía los MISMOS bytes) y viaja
-- SIEMPRE en claro (nunca dentro del enc_payload sellado, cloudlink.proto §
-- IncomingMessage). session_id lo acota a la sesión del Edge.
--
-- CERO PII (regla dura, INV-5, zero-knowledge): session_id y wa_message_id son
-- IDENTIDADES OPACAS del transporte (whatsmeow/CloudLink), NUNCA el número/JID del
-- contacto ni el contenido del mensaje. Sin tenant_id: el aislamiento operativo es
-- por session_id (la sesión ya pertenece a un tenant en fleet_sessions). NUNCA la
-- DEK/lease del Edge.
--
-- LIMPIEZA (crecimiento acotado): first_seen_at permite una poda PEREZOSA por
-- ventana de retención (el deduper barre en lote, throttled, fuera del camino
-- caliente). La retención solo debe cubrir el horizonte de reenvío del outbox del
-- Edge; pasada esa ventana un "duplicado" ya no puede llegar.
--
-- ADITIVA e IDEMPOTENTE: runner hash-based FULL-REPLAY; CREATE ... IF NOT EXISTS
-- garantiza re-aplicación N veces sin daño. NO clean-slate.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.ingest_dedupe (
    session_id    TEXT        NOT NULL,              -- sesión del Edge (metadato opaco)
    wa_message_id TEXT        NOT NULL,              -- MessageID de whatsmeow (metadato, NO PII)
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),-- primer avistamiento en la nube (para poda por retención)
    PRIMARY KEY (session_id, wa_message_id)
);

-- Poda por retención: barrido perezoso de filas más viejas que la ventana de
-- reenvío del outbox del Edge (DELETE ... WHERE first_seen_at < cutoff).
CREATE INDEX IF NOT EXISTS ingest_dedupe_first_seen_idx
    ON public.ingest_dedupe (first_seen_at);

COMMENT ON TABLE  public.ingest_dedupe IS 'Dedupe persistente de entrantes ante reenvíos del outbox durable del Edge (Plan 028 §T6, ADR-0003). Clave (session_id, wa_message_id). CERO PII (INV-5): metadatos OPACOS del transporte, NUNCA número/JID ni contenido. Sin tenant_id; aislamiento por session_id. NUNCA DEK/lease del Edge.';
COMMENT ON COLUMN public.ingest_dedupe.session_id    IS 'Sesión del Edge que recibió el entrante (metadato opaco).';
COMMENT ON COLUMN public.ingest_dedupe.wa_message_id IS 'MessageID de whatsmeow, estable entre reenvíos. NUNCA número/JID ni contenido.';
COMMENT ON COLUMN public.ingest_dedupe.first_seen_at IS 'Primer avistamiento en la nube; base de la poda perezosa por retención.';
