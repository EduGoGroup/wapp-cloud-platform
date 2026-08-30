# El esquema PostgreSQL de `wapp-cloud-platform`

> Fichero hermano de [`contratos.md`](contratos.md): el esquema no cabía allí.
> Los invariantes que gobiernan las migraciones están en
> [`constitucion.md`](constitucion.md) (I-CP-7 full-replay, I-CP-10 censo de rotación).

---

## 1 · Versión y cómo se versiona

```
const SchemaVersion = "0.48.0"
```
— `internal/platform/storage/postgres/migrations/version.go:435`, precedido de **410 líneas
de comentario** que narran plan por plan cada bump desde `0.25.0`. Ese comentario es la
historia del esquema: léelo antes de inventar por qué existe una columna.

El estado aplicado vive en `public.schema_version` (`schema.go:15`), una fila por aplicación,
con `version`, `content_hash`, `applied_at` y `applied_by`. Verificado en UAT el 2026-08-29:
**base `0.48.0` == código `0.48.0`, sin deriva**, 25 filas.

🔴 **Sube `SchemaVersion` cuando añadas una migración.** El runner también dispara por cambio
de hash del contenido, pero la versión es lo que un operador lee.

---

## 2 · El runner: FULL-REPLAY

`internal/platform/storage/postgres/migrations/migrate.go`.

1. Toma `pg_advisory_lock` sobre **una conexión dedicada** (`migrate.go:61`) — serializa dos
   arranques simultáneos.
2. Ordena los ficheros **por nombre** (`:142`).
3. `isUpToDate` (`schema.go:81`) compara versión **y** hash; si algo cambió, **reejecuta
   TODOS** los `.sql`, no solo los nuevos.

⇒ **Todo el DDL tiene que ser idempotente.** `CREATE TABLE IF NOT EXISTS`,
`ALTER TABLE … ADD COLUMN IF NOT EXISTS`, `INSERT … ON CONFLICT DO NOTHING`, y los
`COMMENT ON` al final.

⚠️ **Trampa medida**: `CREATE TABLE IF NOT EXISTS` **no repone** una columna que una migración
posterior borró. Si el `COMMENT ON COLUMN` de esa columna sigue debajo, el **segundo** arranque
muere. Es el fallo clásico de este mecanismo.

⚠️ **Contra un pooler en modo transacción el advisory lock se puede repartir entre sesiones**
(el lock en una, el unlock en otra). Apunta siempre al **host directo**. El `Makefile:63` y
`cmd/migrate/main.go:21` llevan el aviso.

---

## 3 · Las migraciones

**84 ficheros** en `internal/platform/storage/postgres/migrations/structure/`, embebidos con
`go:embed`, numerados `0001`…`0086`. La última es `0086_user_active_tenant.sql`.

🔴 **Faltan los números `0020` y `0021`**: nunca existieron (no aparecen borrados en el
historial de git) y **ningún documento lo explica**. La única mención de pasada está en
`0060_platform_console_grants.sql:64`. No los reutilices ni los inventes.

🔴 **Tres tablas se crean y se borran en cada replay.** `0014_iam_users.sql` y
`0018_iam_api_keys.sql` siguen creando `iam_users`, `iam_refresh_tokens` e `iam_api_keys`, y
`0038_retiro_iam_propio.sql:59` las dropea después. Es coherente con append-only +
full-replay, pero cada arranque recrea y destruye tres tablas muertas. **No es un bug: es la
consecuencia de que las migraciones no se editan hacia atrás.**

---

## 4 · Las 47 tablas y vistas que toca el código

Medidas con
`grep -rhoiE '(FROM|INTO|UPDATE|JOIN)[[:space:]]+public\.[a-z_]+' --include='*.go' internal/`
→ 47 nombres distintos.

### Tenancy e IAM (14)
`tenants` · `tenant_members` · `tenant_settings` · `tenant_features` · `plans` ·
`plan_features` · `tenant_invitations` · `user_active_tenant` · `access_requests` ·
`audit_events` · `iam_roles` · `iam_role_grants` · `iam_user_roles` · `iam_user_grants`

⚠️ Aquí **no hay padrón de personas**: las cuatro `iam_*` son **RBAC de negocio**, no
identidad. Ver I-CP-9.

### Flota y CloudLink (7)
`fleet_sessions` · `edge_certs` · `enrollment_codes` · `leases` · `message_receipts` ·
`ingest_dedupe` · `diagnostics_bundles` · `tenant_diagnostics_consent`

### Motor de flujos (12)
`flow_definitions` · `flow_state` · `flow_events` · `flow_triggers` · `contacts` ·
`conversation_events` · `conversation_event_messages` · `conversation_welcomes` ·
`survey_results` · `tenant_content` · `tenant_content_versions` · vista `event_content`

### Negocio · intakes (6)
`intakes` · `intake_items` · `intake_revisions` · `intake_buyer_data` · `intake_jobs` ·
`intake_case_bank`

🔴 **`orders` / `order_items` ya no existen**: `0041_rename_orders_intakes.sql:39` los renombró
a `intakes` / `intake_items` — pedido y presupuesto son el mismo objeto de dominio.

### LLM e integraciones (6)
`tenant_llm` · `intent_configs` · `owner_degradation_notices` · `tenant_integrations` ·
`webhook_outbox` · `tenant_variables`

### Infraestructura (1)
`schema_version`

---

## 5 · Vocabularios cerrados

| Dónde | Valores |
|---|---|
| `intake_jobs.status` (CHECK, `0072:479`) | `aggregating` · `pending` · `processing` · `done` · `failed` |
| `intake_jobs.stage` (CHECK, `0072:493`) | `p2` · `p3` · `p4` · `match` · `draft` (o NULL) |
| `intakes.status` (Go, `internal/intakes/status.go:13`) | `open` · `pending_approval` · `confirmed` · `deposit_requested` · `deposit_paid` · `settled` · `cancelled` · `expired` · `abandoned` · `rejected` · `needs_info`, más `closed` como **alias legado** |
| `fleet_sessions.state` | `online` · `offline` · `loggedout` (`internal/gateway/fleet/repository_postgres.go:174-209`) |

Sobre `intakes.status`: los terminales (`settled`, `cancelled`, `rejected`, `abandoned` y el
legado `expired`) no transicionan a ninguna parte; `expired` **ya no se entra nunca** (nada
vence por tiempo) y solo se conserva por filas históricas. El alias `closed → confirmed` se
resuelve **en un único punto** (`status.go:47`): si algún día se migran las filas, se vacía esa
tabla y no se toca nada más.

---

## 6 · Cifrado de campo y el censo de rotación

Mecanismo único: `FieldCipher` (`internal/platform/crypto/field_cipher.go`) — envelope
AES-256-GCM con **DEK fresca por valor**, envuelta por la KEK del keyring, más el `kek_id`.
Tres columnas por sobre: `*_enc`, `*_dek`, `*_kek_id`.

🔴 **Recuerda el homónimo**: esta «DEK» es la del **contenido de negocio**, no la DEK del
Edge de la doble llave. Ver §3.1 de [`constitucion.md`](constitucion.md).

**Censo de rotación** — `rekeyTargets` en `internal/platform/crypto/rekey.go`: hoy **8 sobres
en 7 tablas**.

| Tabla | Sobre |
|---|---|
| `contacts` | `value_dek` y `push_name_dek` (dos) |
| `intake_buyer_data` | `data_dek` |
| `tenant_integrations` | `secret_dek` |
| `fleet_sessions` | `self_pn_dek` |
| `tenant_llm` | `api_key_dek` |
| `intake_jobs` | `source_text_dek` |
| `intake_revisions` | `literal_dek` |

🔴 **Falta `conversation_event_messages`**, que se escribe cifrada
(`internal/flujos/events/store.go:811` inserta `body_enc, body_dek, body_kek_id`) y **no está
en el censo**. Consecuencia: una rotación se declararía «completa» y al retirar la KEK vieja
esas filas quedarían **ilegibles para siempre** — y son la copia canónica del literal del
cliente. Está en [`deuda.md`](deuda.md).

**El nivel de cifrado lo impone la BASE, no el llamador.** En `conversation_event_messages` el
CHECK de `0051_conversation_events.sql:195` obliga a
`(entry_kind IN ('decision','summary') AND body_enc IS NULL …) OR (entry_kind='message' AND
payload IS NULL)`: no se puede escribir un literal en claro por descuido. Y el store lo repite
en `store.go:824`.

**La KEK**: proveedor conmutable `env` | `kms` (`WAPP_KEK_PROVIDER`). El adaptador de Google
KMS está **construido** (`internal/platform/crypto/kms_gcp.go`, `keyprovider_kms.go`) y
**apagado**: el default es `env` (`config.go:654`), por decisión de coste. Encenderlo es un
gate de negocio, no un cambio de fichero.

---

## 7 · Entitlements sembrados

Semillas en `0032_entitlements.sql:54,59`, `0039_seed_plan_taxonomy.sql:43,50`,
`0053_seed_survey_media_features.sql:39` y `0074_seed_plan_advisor_ai_local.sql:124,130`.

**6 planes**: `basic` · `commerce` · `advisor_ai` · `advisor_ai_pro` · **`advisor_ai_local`**
(lo añadió la `0074`: es `advisor_ai` + `llm_intake` **sin** `api_llm` — «la línea que no está,
y es el plan entero») · `pro` (interno de laboratorio).

**14 features sembradas**; **11 tienen constante en Go**
(`internal/entitlements/entitlements.go:45-176`): `llm_intent` · `cart_basic` ·
`intakes_export` · `catalog_import` · `crm_bridge` · `menu` · `survey` · `media` ·
`llm_intake` · `api_llm` · `multi_empresa`.

Las tres restantes (`stt_audio`, `owner_app`, `passive_profiles`) **no gatean nada**. Ver
[`deuda.md`](deuda.md).

**Resolución de una feature efectiva**: un override en `tenant_features` **gana**; sin
override manda el plan; sin `plan_id` se trata como `basic`
(`COALESCE(plan_id,'basic')`, `internal/entitlements/postgres.go:151`).

---

## 8 · Retención y minimización

- **INV-13** — `intake_jobs.source_text` (y su sobre) **se vacían en estado terminal**,
  declarado y documentado en `0083_intake_jobs_retencion.sql:102`. Ver `sello_poda_ast_test.go`
  y `retencion_test.go` en `internal/intakes/`.
- 🔴 **`intake_jobs.artifacts` guarda literal del cliente EN CLARO**, sin TTL y **fuera del
  censo de rotación**. Lo dice el `COMMENT ON TABLE` de la propia migración
  (`0083_intake_jobs_retencion.sql:80` y `:102`). Es un desvío **registrado con dueño** — está
  en [`deuda.md`](deuda.md) — y el `COMMENT` de `0072:741` que decía lo contrario **sigue en el
  árbol**.
- `conversation_ttl_seconds` tiene default > 0 desde `0067_conversation_ttl_default.sql`.
- `fleet_sessions.self_pn` y `contacts.push_name` están cifrados y **sus columnas en claro
  borradas** (`0068`, `0069`, `0070`).
