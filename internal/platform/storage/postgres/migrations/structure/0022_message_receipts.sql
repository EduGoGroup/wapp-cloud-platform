-- ============================================================
-- 0022: Acuses de entrega/lectura persistidos (Plan 018 · T10, R11).
-- Tabla NUEVA message_receipts: materializa los MessageReceipt (Plan 013) que el
-- Edge emite por CloudLink (delivered/read) para consulta e2e. Hasta el Plan 018
-- el acuse era log-only (LogReceiptSink, sin tabla); aquí gana persistencia
-- idempotente sin tocar el ruteo del stream.
--
-- NUMERACIÓN: 0020/0021 quedan RESERVADOS a T8 (webhook_outbox/tenant_integrations,
-- aún no ejecutado); este tramo usa el siguiente número REALMENTE libre (0022).
--
-- CERO PII (regla dura, INV-5): session_id, command_id y message_id son
-- IDENTIDADES OPACAS del transporte (metadatos de whatsmeow/CloudLink), NUNCA el
-- número/JID del contacto ni el contenido del mensaje. No hay tenant_id: el acuse
-- viaja del Edge sin tenant; el aislamiento operativo es por session_id (la sesión
-- ya pertenece a un tenant en fleet_sessions). NUNCA la DEK/lease del Edge.
--
-- IDEMPOTENCIA: el mismo acuse (misma sesión + message_id + status) puede llegar
-- repetido (reintentos del stream); el índice único evita duplicados y el INSERT
-- ... ON CONFLICT DO UPDATE refresca el timestamp. Un mensaje transita
-- delivered→read: son DOS filas distintas (status forma parte de la clave).
--
-- ADITIVA e IDEMPOTENTE: runner hash-based FULL-REPLAY; CREATE ... IF NOT EXISTS
-- garantiza re-aplicación N veces sin daño. NO clean-slate.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.message_receipts (
    id          BIGSERIAL   PRIMARY KEY,
    session_id  TEXT        NOT NULL,              -- sesión del Edge (metadato opaco)
    command_id  TEXT        NOT NULL DEFAULT '',   -- correlación con el SendText original (opaco)
    message_id  TEXT        NOT NULL,              -- MessageID acusado (metadato, NO PII)
    status      TEXT        NOT NULL,              -- "delivered" | "read"
    receipt_at  TIMESTAMPTZ,                       -- epoch del acuse (types.Receipt.Timestamp); NULL si 0
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now() -- momento de persistencia en la nube
);

-- Dedupe idempotente: un acuse único por (sesión, mensaje, status). delivered y
-- read del MISMO mensaje son filas distintas (status en la clave).
CREATE UNIQUE INDEX IF NOT EXISTS message_receipts_dedupe_uidx
    ON public.message_receipts (session_id, message_id, status);
-- Escaneo por sesión y orden temporal (consulta de acuses).
CREATE INDEX IF NOT EXISTS message_receipts_scan_idx
    ON public.message_receipts (session_id, recorded_at);
-- Correlación por command_id (acuses de un envío concreto).
CREATE INDEX IF NOT EXISTS message_receipts_command_idx
    ON public.message_receipts (command_id);

COMMENT ON TABLE  public.message_receipts IS 'Acuses delivered/read persistidos (Plan 018 §R11, del Plan 013). CERO PII (INV-5): session_id/command_id/message_id son metadatos OPACOS del transporte, NUNCA número/JID ni contenido. Sin tenant_id (el acuse no lo porta); aislamiento por session_id. NUNCA DEK/lease del Edge.';
COMMENT ON COLUMN public.message_receipts.id          IS 'Identidad técnica de la fila (append-only).';
COMMENT ON COLUMN public.message_receipts.session_id  IS 'Sesión del Edge que emitió el acuse (metadato opaco).';
COMMENT ON COLUMN public.message_receipts.command_id  IS 'Correlación con el SendText original (opaco); "" si no viene.';
COMMENT ON COLUMN public.message_receipts.message_id  IS 'MessageID acusado (metadato de whatsmeow). NUNCA número/JID ni contenido.';
COMMENT ON COLUMN public.message_receipts.status      IS 'Desenlace del acuse: "delivered" | "read".';
COMMENT ON COLUMN public.message_receipts.receipt_at  IS 'Instante del acuse reportado por el Edge (epoch); NULL si no se informó.';
COMMENT ON COLUMN public.message_receipts.recorded_at IS 'Momento de persistencia en la nube. Usa el DEFAULT now().';
