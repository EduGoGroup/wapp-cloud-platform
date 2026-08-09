-- ============================================================
-- 0050: El payload de una entrega ENTREGADA se vacía (Plan 042 · Ola 5,
-- saneamiento de PII; decisión de Jhoan del 2026-08-08).
--
-- ------------------------------------------------------------
-- QUÉ ESTABA MAL
-- ------------------------------------------------------------
-- `public.webhook_outbox` NO SE PODA NUNCA. No hay un solo DELETE contra esta
-- tabla en el repo (verificado). Una fila entregada se queda en `delivered` con su
-- `payload` intacto para siempre, y ese payload es una COPIA EN CLARO de lo que se
-- mandó al puente: líneas del pedido, personalizaciones, total y —hasta la Ola 5—
-- la indicación del cliente («dejarlo en portería, calle Mayor 14»).
--
-- Es la tercera puerta de la misma fuga. El defecto A2 del Plan 041 sacó esa
-- indicación de `public.flow_events` podándola por clave (Effect.PrivateKeys), y al
-- hacerlo destapó que seguía congelada aquí. La Ola 5 la sacó de la plantilla —hoy
-- la completa el worker justo antes del POST, como buyer_data y variables{}— pero
-- eso solo arregla las filas NUEVAS y solo ese campo. Esta migración cierra el
-- resto: la copia entera deja de sobrevivir a su propio uso.
--
-- ------------------------------------------------------------
-- LA POLÍTICA: VACIAR, NO BORRAR
-- ------------------------------------------------------------
-- Al pasar a `delivered`, el payload se sobrescribe con `'{}'::jsonb`. La FILA
-- SOBREVIVE —id, tenant_id, kind, status, attempts, created_at, last_error—, así
-- que la trazabilidad («esta solicitud se entregó, a la primera, tal día») queda
-- intacta. Lo que desaparece es el duplicado del contenido, que nadie vuelve a
-- leer: el puente ya lo tiene, y por eso mismo entregar es el instante exacto en
-- que la copia deja de tener uso.
--
-- El vaciado en vivo lo hace la propia transición (integrations/postgres.go,
-- MarkWebhookDelivered), en la MISMA sentencia que cierra el claim: así no existe
-- ninguna ventana en la que una fila esté `delivered` con payload. Este fichero es
-- la otra mitad — las filas que YA están entregadas en las bases que corren desde
-- las Olas 1-4 (hay datos reales en Neon).
--
-- `'{}'` y no NULL: la columna es NOT NULL (0046). Relajarla obligaría a todo lector
-- a distinguir tres casos (contenido / vacío / nulo) donde solo hay dos.
--
-- ------------------------------------------------------------
-- QUÉ **NO** HACE ESTA MIGRACIÓN, Y POR QUÉ
-- ------------------------------------------------------------
-- 1. NO borra filas ni pone TTL. La retención por antigüedad es una política con
--    su propia discusión (¿cuánto guarda un recibo?) y está RESERVADA al Plan 046.
--    Vaciar el contenido y decidir cuánto vive el recibo son dos preguntas
--    distintas; esta migración solo responde la primera.
--
-- 2. NO toca las filas en `dead`. Es deliberado y discutible, así que va escrito:
--    una fila `dead` es la que el puente NUNCA recibió. Su payload es el único
--    sitio donde queda constancia de qué se iba a entregar, y es lo que un operador
--    necesita para entender el fallo o re-empujar la solicitud a mano. En
--    `delivered` la copia es redundante; en `dead` es el original. Y tras la Ola 5
--    lo que queda en ese payload es dato de NEGOCIO EN CLARO (líneas, total,
--    personalizaciones) que ya vive igual en public.intakes/intake_items por
--    ADR-0009: no es una exposición nueva, es la misma con otro nombre. Si el Plan
--    046 decide que un fracaso tampoco debe conservarse indefinidamente, ahí es
--    donde toca — junto con la antigüedad.
--
-- 3. NO toca `pending` ni `delivering`: ahí el payload es lo que hay que entregar.
--
-- ------------------------------------------------------------
-- IDEMPOTENCIA
-- ------------------------------------------------------------
-- El runner es hash-based FULL-REPLAY (reejecuta TODOS los structure/*.sql al
-- cambiar el hash de cualquiera), así que esto se ejecuta N veces. Es idempotente
-- por construcción: tras la primera pasada ninguna fila `delivered` tiene payload
-- distinto de `'{}'`, y el WHERE deja de encontrar nada.
--
-- El guard `to_regclass IS NOT NULL` no es ceremonia: un UPDATE no admite
-- `IF EXISTS`, y la regla del repo es que lo destructivo no puede reventar el
-- ARRANQUE del servidor si la tabla no estuviera (ver 0037/0038). En el replay
-- normal la 0046 corre antes y la tabla existe siempre.
-- ============================================================

DO $$
BEGIN
    IF to_regclass('public.webhook_outbox') IS NOT NULL THEN
        UPDATE public.webhook_outbox
        SET payload = '{}'::jsonb
        WHERE status = 'delivered'
          AND payload <> '{}'::jsonb;
    END IF;
END
$$;

COMMENT ON COLUMN public.webhook_outbox.payload IS
    'Plantilla congelada en el encolado (snapshot, no referencia) con los campos baratos y estables. buyer_data, variables{} y customer_note NO viajan aquí: el worker los completa justo antes del POST (INV-02, nunca en línea con el mensaje; customer_note se sumó en la Ola 5 por ser PII). Lleva el contact OPACO, jamás un número/JID. SE VACÍA A ''{}'' AL ENTREGAR (migración 0050): una vez el puente lo tiene, la copia no se vuelve a leer y no se conserva. La FILA sobrevive como recibo; la retención por antigüedad es del Plan 046.';
