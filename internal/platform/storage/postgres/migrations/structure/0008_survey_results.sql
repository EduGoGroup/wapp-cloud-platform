-- ============================================================
-- 0008: Resultados de encuesta (Plan 014 · T2, ADR-0009 / ADR-0010).
-- Tabla NUEVA survey_results: guarda las respuestas de encuesta como DATOS DE
-- NEGOCIO EN CLARO en Postgres cloud (ADR-0009: la nube aloja contenido de
-- negocio; la DEK y el store cifrado NUNCA salen del Edge, ADR-0007).
--
-- Decisión de diseño (design.md §10.D): answer_code va EN CLARO — es un CÓDIGO
-- de opción cerrada (p.ej. "1", "si", "malo"), NO PII. Cifrarlo rompería el
-- GROUP BY que es el valor mismo de la tabla (agregación de resultados). La
-- identidad ya la protege el contact_id OPACO (Plan 010 / ADR-0010: UUID sin
-- número) + el teléfono cifrado que vive en public.contacts. Aquí NUNCA se
-- guarda número/JID en claro ni ninguna llave.
--
-- ADITIVA e IDEMPOTENTE: el runner es hash-based FULL-REPLAY (re-aplica todos
-- los structure/*.sql al cambiar el hash); CREATE TABLE/INDEX IF NOT EXISTS
-- garantizan re-aplicación N veces sin daño. NO clean-slate.
-- ============================================================

CREATE TABLE IF NOT EXISTS survey_results (
    id            BIGSERIAL   PRIMARY KEY,
    tenant_id     TEXT        NOT NULL,
    contact_id    TEXT        NOT NULL,   -- OPACO (Plan 010); NUNCA número/JID en claro
    flow_id       TEXT        NOT NULL,
    flow_version  INTEGER     NOT NULL,
    question_id   TEXT        NOT NULL,
    answer_code   TEXT        NOT NULL,   -- EN CLARO (§10.D): código de opción de negocio, NO PII
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Índice de AGREGACIÓN: sirve el GROUP BY de resultados por
-- (tenant, flujo, versión, pregunta, opción) — el valor de negocio de la tabla.
CREATE INDEX IF NOT EXISTS survey_results_agg_idx
    ON survey_results (tenant_id, flow_id, flow_version, question_id, answer_code);

COMMENT ON TABLE  survey_results IS 'Resultados de encuesta como datos de NEGOCIO EN CLARO (ADR-0009). answer_code sin cifrar para permitir agregación (GROUP BY, design.md §10.D); la identidad la protege el contact_id opaco (ADR-0010). NUNCA DEK, store cifrado ni número/JID en claro.';
COMMENT ON COLUMN survey_results.id           IS 'Identidad técnica de la fila (append-only; no tiene significado de negocio).';
COMMENT ON COLUMN survey_results.tenant_id    IS 'Tenant dueño de la encuesta.';
COMMENT ON COLUMN survey_results.contact_id   IS 'Identidad OPACA del contacto (contacts.contact_id, Plan 010 / ADR-0010). NUNCA el número/JID en claro.';
COMMENT ON COLUMN survey_results.flow_id      IS 'Flujo (encuesta) que produjo la respuesta.';
COMMENT ON COLUMN survey_results.flow_version IS 'Versión del flujo con la que se respondió (permite agregar por versión).';
COMMENT ON COLUMN survey_results.question_id  IS 'Identificador de la pregunta dentro de la encuesta.';
COMMENT ON COLUMN survey_results.answer_code  IS 'Código de la opción elegida, EN CLARO (design.md §10.D): dato de negocio agregable, NO PII. NO se cifra para no romper el GROUP BY.';
COMMENT ON COLUMN survey_results.created_at   IS 'Momento de la escritura (flush del runtime, T3). Usa el DEFAULT now().';
