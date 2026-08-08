-- ============================================================
-- 0047: Bifurcación de adaptadores por tenant (Plan 042 · T1.2, design.md §2).
-- Una fila por tenant que dice DE DÓNDE sale su catálogo y A DÓNDE van sus eventos.
-- El default 'local'/'local' ES el adaptador sin-CRM de facto: un tenant SIN fila
-- aquí tiene la experiencia completa igual (blob + intakes + export). Esta tabla no
-- añade capacidades, solo desvía las que ya existen hacia un puente externo.
--
-- catalog_adapter='http' es admisible en el CHECK desde ya —el CHECK nombra el
-- futuro— pero seleccionarlo sin implementación lo rechaza la API con error claro
-- («catalog.pull diferido», design.md §2). Se acota con CHECK porque este
-- vocabulario es CERRADO y de wApp: no es un estado de negocio del cliente, es la
-- lista de adaptadores que este código sabe construir.
--
-- tenant_id es TEXT y PRIMARY KEY, sin FK (convención de la ficha 3): una
-- integración por tenant en v1; el tenant sale del token (INV-8).
--
-- ------------------------------------------------------------
-- ⚠️ DIVERGENCIA DELIBERADA CONTRA EL SKETCH DEL DESIGN §2
-- ------------------------------------------------------------
-- El design §2 dibuja UNA sola columna `secret_ciphertext BYTEA` (y §D-042.7 dice
-- «se cifra/descifra con el KeyProvider de los planes 011/012»). Las dos frases no
-- caben juntas: el KeyProvider REAL de este repo cifra con
-- crypto.FieldCipher.Encrypt, que devuelve TRES piezas
-- (valueEnc []byte, valueDEK []byte, keyID string) — envelope encryption. Un solo
-- BYTEA no puede guardar la DEK envuelta ni el key_id de la KEK que la envolvió.
--
-- Peor que incómodo, sería INSEGURO de operar: sin `secret_kek_id` la fila queda
-- FUERA de la rotación de KEK del Plan 012 — no se sabe con qué KEK desenvolver y
-- el re-wrap incremental no puede localizarla. Una KEK comprometida no se podría
-- retirar mientras este secreto siga envuelto por ella.
--
-- Por eso esta tabla sigue el patrón REAL de dato cifrado del repo (public.contacts
-- de 0006/0007, public.intake_buyer_data de 0045, que ya resolvió esta MISMA
-- divergencia): tres columnas + índice por kek_id. Es el design el que va corto,
-- no el código. Decisión tomada en EJECUCIÓN de la Ola 1 (2026-08-07); anotada aquí
-- para que el design §2/§D-042.7 se corrija al cerrar el plan.
--
-- Las tres columnas son NULLables porque el secreto es OPCIONAL: un tenant puede
-- tener la fila con adaptadores 'local' y sin puente, y por tanto sin secreto que
-- firmar. Van juntas o no van: o las tres NULL, o las tres pobladas. Esa invariante
-- vive en el código (no hay CHECK) para no bloquear la escritura parcial durante la
-- rotación.
--
-- ⚠️ PENDIENTE que esto abre (no es de esta migración, se anota): la rotación de
-- KEK (internal/platform/crypto/rekey.go) hoy barre SOLO public.contacts. Ni
-- intake_buyer_data (0045) ni esta tabla están en su bucle. Mientras no se añadan,
-- sus filas no se re-envuelven aunque tengan el kek_id que lo permite.
--
-- CERO PII: aquí no vive ningún dato de persona. Lo único sensible es el secreto de
-- firma HMAC, y va CIFRADO — nunca en claro, nunca en logs, nunca en el JSON de la
-- API (que devuelve solo un booleano «hay secreto configurado»).
--
-- ADITIVA e IDEMPOTENTE: el runner es hash-based FULL-REPLAY; CREATE TABLE/INDEX IF
-- NOT EXISTS + ADD COLUMN IF NOT EXISTS => re-aplicable N veces sin daño ni pérdida
-- de filas. NO clean-slate.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.tenant_integrations (
    tenant_id         TEXT        PRIMARY KEY,         -- una integración por tenant en v1
    catalog_adapter   TEXT        NOT NULL DEFAULT 'local'
                      CHECK (catalog_adapter IN ('local','http','webhook')),
    events_adapter    TEXT        NOT NULL DEFAULT 'local'
                      CHECK (events_adapter IN ('local','webhook')),
    endpoint_url      TEXT,
    secret_enc        BYTEA,                           -- envelope AES-256-GCM del secreto HMAC
    secret_dek        BYTEA,                           -- DEK por-fila, envuelta por la KEK
    secret_kek_id     TEXT,                            -- key_id de la KEK que envolvió secret_dek
    enabled           BOOLEAN     NOT NULL DEFAULT false,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Por si una base ya tiene la tabla de una versión anterior de este fichero: el
-- replay le añade las tres columnas del envelope sin tocar las filas existentes.
ALTER TABLE public.tenant_integrations ADD COLUMN IF NOT EXISTS secret_enc    BYTEA;
ALTER TABLE public.tenant_integrations ADD COLUMN IF NOT EXISTS secret_dek    BYTEA;
ALTER TABLE public.tenant_integrations ADD COLUMN IF NOT EXISTS secret_kek_id TEXT;

-- Localiza rápido las filas pendientes de rotación (por KEK vieja), igual que
-- idx_contacts_kek de la 0007 e idx_intake_buyer_data_kek de la 0045. Sin este
-- índice el re-wrap incremental del Plan 012 no puede consultar pendientes por KEK.
CREATE INDEX IF NOT EXISTS tenant_integrations_kek_idx
    ON public.tenant_integrations (secret_kek_id);

COMMENT ON TABLE public.tenant_integrations IS
    'Bifurcación de adaptadores por tenant (Plan 042 §2): de dónde sale el catálogo y a dónde van los eventos. Sin fila = local/local = experiencia completa sin CRM. Dato de CONFIGURACIÓN. CERO PII. El único material sensible es el secreto de firma HMAC, guardado CIFRADO en tres columnas (envelope encryption, patrón de public.contacts / public.intake_buyer_data).';
COMMENT ON COLUMN public.tenant_integrations.tenant_id       IS 'Tenant dueño de la integración (TEXT sin FK, convención de la ficha 3; PK porque en v1 hay una integración por tenant). Sale del token (INV-8).';
COMMENT ON COLUMN public.tenant_integrations.catalog_adapter IS 'Origen del catálogo: local (blob de wApp, default) | http (verbo catalog.pull — DIFERIDO: el CHECK lo nombra pero la API lo rechaza con error claro) | webhook. El CHECK acota el vocabulario porque es CERRADO y de wApp, no del cliente.';
COMMENT ON COLUMN public.tenant_integrations.events_adapter  IS 'Destino de los eventos de solicitud: local (bandeja del 041, default) | webhook (entrega durable por webhook_outbox hacia el puente).';
COMMENT ON COLUMN public.tenant_integrations.endpoint_url    IS 'URL del puente del cliente a la que se entregan los eventos. NULL mientras no haya integración. Es un endpoint del TENANT, no de wApp.';
COMMENT ON COLUMN public.tenant_integrations.secret_enc      IS 'Envelope AES-256-GCM del secreto de firma HMAC (D-042.5). NULL si el tenant no tiene puente. NUNCA se devuelve por la API: el JSON solo dice si hay secreto configurado. ⚠️ El design §2 dibujaba una sola columna secret_ciphertext BYTEA; se divide en tres porque crypto.FieldCipher.Encrypt devuelve (valueEnc, valueDEK, keyID) — ver cabecera.';
COMMENT ON COLUMN public.tenant_integrations.secret_dek      IS 'DEK por-fila (32B) envuelta por la KEK del keyring. Se desenvuelve para descifrar secret_enc en el borde de la app; la DEK solo vive en memoria. NULL si no hay secreto.';
COMMENT ON COLUMN public.tenant_integrations.secret_kek_id   IS 'key_id de la KEK que envolvió secret_dek (Plan 012 §10.A). Sin esta columna la fila quedaría FUERA de la rotación de KEK: es el discriminador del re-wrap incremental y lo que permite consultar pendientes por KEK en SQL. NULL si no hay secreto.';
COMMENT ON COLUMN public.tenant_integrations.enabled         IS 'Interruptor de la integración. false (default) = configurada pero apagada: nada sale hacia el puente. Es lo que permite preparar una integración sin activarla, y apagarla sin borrar la configuración.';
COMMENT ON COLUMN public.tenant_integrations.created_at      IS 'Momento del alta de la integración. Usa el DEFAULT now().';
COMMENT ON COLUMN public.tenant_integrations.updated_at      IS 'Momento del último cambio de configuración. Usa el DEFAULT now() en el alta.';
