# El esquema PostgreSQL de `wapp-cloud-platform`

> Fichero hermano de [`contratos.md`](contratos.md): el esquema no cabía allí.
> Los invariantes que gobiernan las migraciones están en
> [`constitucion.md`](constitucion.md) (I-CP-7 full-replay, I-CP-10 censo de rotación).
>
> **Este documento es el dueño de tres cifras**, medidas el 2026-10-11 contra el código:
> **45 tablas** en las migraciones (§4), esquema **`0.48.0`** (§1) y **84 migraciones** (§3).
> Cada una lleva su comando y su regla; quien las necesite las enlaza, no las copia.

---

## 1 · Versión y cómo se versiona

```
const SchemaVersion = "0.48.0"
```
— `internal/platform/storage/postgres/migrations/version.go:435`
(`grep -n 'const SchemaVersion' internal/platform/storage/postgres/migrations/version.go`).
La preceden más de cuatrocientas líneas de comentario (desde `version.go:10`) que narran plan
por plan cada bump desde `0.25.0`. Ese comentario es la historia del esquema: léelo antes de
inventar por qué existe una columna.

El estado aplicado vive en `public.schema_version`, que **no la crea ninguna migración**: la
crea el propio runner con `createSchemaVersionTable` (`schema.go:14`). Una fila por aplicación,
con `version`, `content_hash`, `execution_id` (un UUID nuevo por `INSERT`: prueba que la
migración se ejecutó de verdad), `applied_at`, `applied_by` y `description`. El runner lee
siempre **la última** (`readSchemaVersion`, `schema.go:51`).

Para saber qué esquema corre en una base sin escribir nada: `make migrate-status`
(`cmd/migrate -status`), que llama a `Status` (`migrate.go:116`) y dice si la versión y el hash
registrados coinciden con los del binario.

🔴 **Un plan no se publica sin su bump de `SchemaVersion`.** El runner también dispara por
cambio de hash del contenido, así que una ola intermedia puede añadir migraciones sin mover la
constante; pero la versión es lo único que un operador puede comparar contra
`public.schema_version`. La regla está escrita en el comentario de `version.go:12`: **un bump
por plan**, no uno por migración.

---

## 2 · El runner: FULL-REPLAY

`internal/platform/storage/postgres/migrations/migrate.go` (154 líneas), con `schema.go`
(la tabla de seguimiento), `version.go` (la versión y el hash) y `embed.go` (los `.sql`
embebidos). Lo llama el arranque del servidor y, suelto, `make migrate` (`cmd/migrate`).

Qué hace `Migrate` (`migrate.go:46`), en orden:

1. Reserva **una conexión dedicada** del pool y toma sobre ella `pg_advisory_lock` con una clave
   constante (`migrate.go:61`). Lock, migración y unlock van por **esa misma conexión**: así dos
   arranques simultáneos sobre una base fresca se serializan, el primero crea el esquema y el
   segundo espera y lo encuentra hecho.
2. Crea `public.schema_version` si no existe (`ensureSchemaVersionTable`, `schema.go:42`).
3. Calcula el hash del contenido (`ComputeFilesHash`, `version.go:443`): SHA-256 sobre el
   **nombre y el contenido** de cada fichero embebido, en orden de nombre, truncado a 16
   caracteres hexadecimales. Cambia si un fichero se añade, se borra o se toca.
4. Lee la última fila y decide con `isUpToDate` (`schema.go:81`), que exige **versión Y hash**
   iguales. Si coinciden, no toca la base y devuelve `Skipped=true` (`migrate.go:87`).
5. Si algo difiere —o no hay fila—, `applyStructure` (`migrate.go:130`) **reejecuta TODOS** los
   `.sql`, no solo los nuevos, ordenados **por nombre** (`migrate.go:142`), cada fichero en una
   sola sentencia `Exec`. Al primer error se detiene y lo devuelve con el nombre del fichero.
6. Inserta la fila nueva en `public.schema_version` (`writeSchemaVersion`, `schema.go:67`) y
   libera el lock.

No hay tabla de «migraciones ya aplicadas» ni marcha atrás: el único estado es esa última fila.

⇒ **Todo el DDL tiene que ser idempotente.** `CREATE TABLE IF NOT EXISTS`,
`ALTER TABLE … ADD COLUMN IF NOT EXISTS`, `INSERT … ON CONFLICT DO NOTHING`, los `CHECK` con
nombre recreados con `DROP CONSTRAINT IF EXISTS` + `ADD CONSTRAINT`, y los `COMMENT ON` al
final. El replay sobre una base **con datos** no pierde filas justamente por eso.

⚠️ **Trampa medida**: `CREATE TABLE IF NOT EXISTS` **no repone** una columna que una migración
posterior borró. Si el `COMMENT ON COLUMN` de esa columna sigue debajo, el **segundo** arranque
muere. Es el fallo clásico de este mecanismo. Por el mismo motivo, un `CHECK` escrito dentro del
`CREATE TABLE` queda congelado para siempre: el que tenga que crecer va fuera, con nombre.

⚠️ **Contra un pooler en modo transacción el advisory lock se puede repartir entre sesiones**
(el lock en una, el unlock en otra). Apunta siempre al **host directo**. Llevan el aviso el
`Makefile:368` y `cmd/migrate/main.go:22`.

**Candado.** `version_test.go` (junto al runner) fija que el hash es determinista
(`TestComputeFilesHash_Deterministic`) y la tabla de verdad de `isUpToDate` (`TestIsUpToDate`).
La conducta contra un Postgres real la ejercen los procesos de `test/procesos/`, que migran
una base de testcontainers en cada corrida (`make test-procesos`).

---

## 3 · Las migraciones

**84 ficheros** en `internal/platform/storage/postgres/migrations/structure/`
(`ls internal/platform/storage/postgres/migrations/structure/*.sql | wc -l`; regla: un fichero
`.sql` es una migración), embebidos con `go:embed`
(`internal/platform/storage/postgres/migrations/embed.go:15`), numerados `0001`…`0086`. La
primera es `0001_tenants.sql` y la última `0086_user_active_tenant.sql`.

🔴 **Faltan los números `0020` y `0021`**: la serie salta de `0019` a `0022`, por eso son 84
ficheros y no 86. Ningún fichero del repo lo explica. No los reutilices ni los inventes.

🔴 **Tres tablas se crean y se borran en cada replay.** `0014_iam_users.sql` y
`0018_iam_api_keys.sql` siguen creando `iam_users`, `iam_refresh_tokens` e `iam_api_keys`, y
`0038_retiro_iam_propio.sql:59` las dropea después. Es coherente con append-only +
full-replay, pero cada replay recrea y destruye tres tablas muertas. **No es un bug: es la
consecuencia de que las migraciones no se editan hacia atrás.**

Otros dos `DROP TABLE` son **condicionales** y no retiran nada en una base al día:
`0005_contacts.sql:98` (tira `flow_state` solo si aún tiene la columna `contact` del diseño
anterior) y `0006_contacts_cifrado.sql:33` (tira `contacts` solo si aún tiene `value` en claro).
En los dos casos el mismo fichero la vuelve a crear con la forma nueva.

---

## 4 · Las 45 tablas, la tabla del runner y la vista

**La cifra y su regla.** Tablas que existen tras aplicar todas las migraciones: nombres
distintos con `CREATE TABLE` en `structure/*.sql`, menos los que un `DROP TABLE` incondicional
retira. Medido el 2026-10-11:

- **48** nombres distintos creados (`CREATE TABLE [IF NOT EXISTS] [public.]<nombre>`, sin contar
  las líneas de comentario `--`; `flow_state` y `contacts` aparecen en dos ficheros cada una y
  cuentan una vez);
- **− 3** retiradas por `0038_retiro_iam_propio.sql:59` (`iam_users`, `iam_refresh_tokens`,
  `iam_api_keys`);
- **= 45 tablas.** Dos de las 48 se crean con otro nombre: `0041_rename_orders_intakes.sql:39`
  renombra `orders` → `intakes` y `order_items` → `intake_items` (un renombre no cambia la
  cuenta).

```bash
D=internal/platform/storage/postgres/migrations/structure
grep -hivE '^\s*--' $D/*.sql | grep -oiE 'CREATE TABLE (IF NOT EXISTS )?(public\.)?[a-z_]+' \
  | sed -E 's/.* (public\.)?//' | sort -u | wc -l        # 48
grep -niE '^\s*DROP TABLE' $D/*.sql                       # 5: 3 incondicionales + 2 condicionales (§3)
```

A esas 45 se suman, **fuera de la regla**: la tabla `schema_version`, que crea el runner y no
una migración (§1) —una base migrada tiene, pues, **46 tablas** en `public`—, y **una vista**,
`event_content` (`0054_evento_contenido_invertido.sql:165`).

**Quién las toca.** La columna «Paquetes» lista los paquetes de producción cuyo SQL nombra la
tabla tras `FROM`, `INTO`, `UPDATE` o `JOIN` (con o sin `public.`), sin contar `_test.go` ni los
paquetes `…helpertest`. Las rutas son relativas a `internal/modulos/` salvo las que empiezan por
`nucleo/`, `apipublica` o `platform/`, que cuelgan de `internal/`. **Van primero los que la
escriben** (`INSERT`, `UPDATE` o `DELETE`); donde nadie la escribe desde Go, se dice.

### Tenancy, planes y RBAC (14)

| Tabla | Paquetes |
|---|---|
| `tenants` | `acceso/platformadmin` (el alta) · `edge/lease` (lee y escribe `revoked_at`: el kill-switch) · leen: `acceso/iam/infra/postgres`, `acceso/entitlements` (`plan_id`) |
| `tenant_members` | `acceso/iam/infra/postgres` · lee: `acceso/platformadmin` |
| `tenant_invitations` | `acceso/iam/infra/postgres` |
| `user_active_tenant` | `acceso/iam/infra/postgres` |
| `access_requests` | `acceso/iam/infra/postgres` · `acceso/platformadmin` (los dos escriben) |
| `audit_events` | `acceso/iam/infra/postgres` |
| `iam_roles` | `acceso/iam/infra/postgres` · lee: `acceso/platformadmin` |
| `iam_role_grants` | `acceso/iam/infra/postgres` |
| `iam_user_roles` | `acceso/iam/infra/postgres` · lee: `acceso/platformadmin` |
| `iam_user_grants` | `acceso/iam/infra/postgres` |
| `plans` | — ningún paquete de producción la nombra: es catálogo sembrado (§7) y destino de la clave foránea de `plan_features` |
| `plan_features` | solo lectura desde Go: `acceso/entitlements` · `acceso/platformadmin` (la escriben las semillas, §7) |
| `tenant_features` | solo lectura desde Go: `acceso/entitlements` · `acceso/platformadmin` |
| `tenant_settings` | solo lectura desde Go: `conversacion/store` · `conversacion/events` · `solicitudes/intakes` |

🔴 **Cuatro tablas no las escribe ningún `.go` de producción**: `plans`, `plan_features`,
`tenant_features` y `tenant_settings` (medido el 2026-10-11: ningún `INSERT INTO`, `UPDATE` ni
`DELETE FROM` sobre ellas fuera de los `_test.go`). Las dos primeras las llenan las migraciones;
un override de feature o un ajuste de `tenant_settings` se hace hoy **con SQL a mano**, no por
API.

⚠️ Aquí **no hay padrón de personas**: las cuatro `iam_*` son **RBAC de negocio**, no
identidad. Ver I-CP-9 en [`constitucion.md`](constitucion.md).

### Flota y CloudLink (8)

| Tabla | Paquetes |
|---|---|
| `fleet_sessions` | `edge/fleet` · leen: `acceso/platformadmin`, `conversacion/runtime` (resuelve el tenant y los números propios) |
| `edge_certs` | `edge/enroll` |
| `enrollment_codes` | `edge/enroll` |
| `leases` | `edge/lease` · lee: `acceso/platformadmin` |
| `message_receipts` | `edge/receipts` |
| `ingest_dedupe` | `edge/ingest` |
| `diagnostics_bundles` | `edge/diagnostics` |
| `tenant_diagnostics_consent` | `edge/diagnostics` |

### Motor de flujos (11, más la vista)

| Tabla | Paquetes |
|---|---|
| `flow_definitions` | `conversacion/store` |
| `flow_state` | `conversacion/store` · `nucleo/contact` (reapunta `contact_id` al fusionar contactos) |
| `flow_events` | `conversacion/store` · leen: `apipublica` (telemetría de eventos), `platform/metrics/flowlifecycle` |
| `flow_triggers` | `conversacion/trigger` |
| `contacts` | `nucleo/contact` |
| `conversation_events` | `conversacion/events` · `solicitudes/intakes` (cierra el evento al descartar) |
| `conversation_event_messages` | `conversacion/events` |
| `conversation_welcomes` | `conversacion/store` |
| `survey_results` | `conversacion/store` |
| `tenant_content` | `conversacion/store` |
| `tenant_content_versions` | `conversacion/store` |
| vista `event_content` | `conversacion/events` |

### Negocio · solicitudes y captación (6)

| Tabla | Paquetes |
|---|---|
| `intakes` | `solicitudes/intakes` · `conversacion/store` (el carrito la abre; los dos escriben) |
| `intake_items` | `solicitudes/intakes` · `conversacion/store` (los dos escriben) |
| `intake_revisions` | `solicitudes/intakes` |
| `intake_buyer_data` | `solicitudes/intakes` |
| `intake_jobs` | `captacion/intake` |
| `intake_case_bank` | `captacion/casebank` |

🔴 **`orders` / `order_items` no existen**: `0041_rename_orders_intakes.sql:39` los renombró
a `intakes` / `intake_items` — pedido y presupuesto son el mismo objeto de dominio.

### LLM e integraciones (6)

| Tabla | Paquetes |
|---|---|
| `tenant_llm` | `inferencia/tenantllm` |
| `intent_configs` | `captacion/intentcfg` |
| `owner_degradation_notices` | `inferencia/degradation` |
| `tenant_integrations` | `solicitudes/integrations` |
| `webhook_outbox` | `solicitudes/integrations` |
| `tenant_variables` | `solicitudes/tenantvars` |

14 + 8 + 11 + 6 + 6 = **45**.

### Infraestructura (fuera de la cuenta)

`schema_version` — la crea y la lee el runner
(`internal/platform/storage/postgres/migrations/schema.go:14`).

---

## 5 · Vocabularios cerrados

| Dónde | Valores |
|---|---|
| `intake_jobs.status` (CHECK, `0072_intake_jobs_y_ventana_agregacion.sql:479`) | `aggregating` · `pending` · `processing` · `done` · `failed` |
| `intake_jobs.stage` (CHECK, `0072_intake_jobs_y_ventana_agregacion.sql:493`) | `p2` · `p3` · `p4` · `match` · `draft` (o NULL) |
| `conversation_event_messages.entry_kind` (CHECK, `0072_intake_jobs_y_ventana_agregacion.sql:705`) | `decision` · `summary` · `message` · `message_out_of_turn` |
| `intakes.status` (Go, `internal/modulos/solicitudes/intakes/status.go:27`) | `open` · `pending_approval` · `confirmed` · `deposit_requested` · `deposit_paid` · `settled` · `cancelled` · `expired` · `abandoned` · `rejected` · `needs_info`, más `closed` como **alias legado** |
| `fleet_sessions.state` (Go, `internal/modulos/edge/fleet/repository_postgres.go:106`, `:125` y `:142`) | `online` · `offline` · `loggedout` |

Sobre `intakes.status`: los terminales (`settled`, `cancelled`, `rejected`, `abandoned` y el
legado `expired`) no transicionan a ninguna parte (`transitions`, `internal/modulos/solicitudes/intakes/status.go:76`); `expired`
**ya no se entra nunca** (nada vence por tiempo) y solo se conserva por las filas que quedaron
dentro, que el dueño puede descartar a mano (`discardable`, `internal/modulos/solicitudes/intakes/status.go:106`). El alias
`closed → confirmed` se resuelve **en un único punto** (`aliases`, `internal/modulos/solicitudes/intakes/status.go:64`, que consulta
`NormalizeStatus`, `internal/modulos/solicitudes/intakes/status.go:136`): si algún día se migran las filas, se vacía esa tabla y no
se toca nada más.

---

## 6 · Cifrado de campo y el censo de rotación

Mecanismo único: `FieldCipher` (`internal/platform/crypto/field_cipher.go:18`) — envelope
AES-256-GCM con **DEK fresca por valor**, envuelta por la KEK del keyring, más el `kek_id`.
Tres columnas por sobre: `*_enc`, `*_dek`, `*_kek_id`.

🔴 **Recuerda el homónimo**: esta «DEK» es la del **contenido de negocio**, no la DEK del
Edge de la doble llave. Ver §3.1 de [`constitucion.md`](constitucion.md).

**Censo de rotación** — `rekeyTargets` en `internal/platform/crypto/rekey.go:56`: **8 sobres
en 7 tablas** (regla: una entrada del slice es un sobre;
`grep -c 'dekCol:' internal/platform/crypto/rekey.go` → 8).

| Tabla | Sobre | Quién lo escribe |
|---|---|---|
| `contacts` | `value_dek` y `push_name_dek` (dos) | `internal/nucleo/contact/` |
| `intake_buyer_data` | `data_dek` | `internal/modulos/solicitudes/intakes/buyerdata_postgres.go` |
| `tenant_integrations` | `secret_dek` | `internal/modulos/solicitudes/integrations/` |
| `fleet_sessions` | `self_pn_dek` | `internal/modulos/edge/fleet/` |
| `tenant_llm` | `api_key_dek` | `internal/modulos/inferencia/tenantllm/` |
| `intake_jobs` | `source_text_dek` | `internal/modulos/captacion/intake/` |
| `intake_revisions` | `literal_dek` | `internal/modulos/solicitudes/intakes/` |

🔴 **Falta `conversation_event_messages`**, que se escribe cifrada
(`internal/modulos/conversacion/events/store_append.go:50` inserta
`body_enc, body_dek, body_kek_id`) y **no está en el censo**. Consecuencia: una rotación se
declararía «completa» y al retirar la KEK vieja esas filas quedarían **ilegibles para
siempre** — y son la copia canónica del literal del cliente. Está en [`deuda.md`](deuda.md).

**El nivel de cifrado lo impone la BASE, no el llamador.** En `conversation_event_messages` el
CHECK de grado (`0072_intake_jobs_y_ventana_agregacion.sql:717`, que recrea con nombre el que
nació en `0051_conversation_events.sql:195`) obliga a
`(entry_kind IN ('decision','summary') AND body_enc IS NULL AND body_dek IS NULL) OR
(entry_kind IN ('message','message_out_of_turn') AND payload IS NULL)`: no se puede escribir un
literal en claro por descuido. Y el almacén lo repite: el grado se decide dentro de `Store`,
no lo elige quien llama (`AppendMessage`, `internal/modulos/conversacion/events/store_append.go:156`).

**La KEK**: proveedor conmutable `env` | `kms` (`WAPP_KEK_PROVIDER`). El adaptador de Google
KMS está **construido** (`internal/platform/crypto/kms_gcp.go`, `keyprovider_kms.go`) y
**apagado**: el default es `env` (`internal/platform/config/config.go:654`), por decisión de
coste. Encenderlo es un gate de negocio, no un cambio de fichero.

---

## 7 · Entitlements sembrados

Semillas en `0032_entitlements.sql:54` y `:59`, `0039_seed_plan_taxonomy.sql:43` y `:50`,
`0053_seed_survey_media_features.sql:39` y `0074_seed_plan_advisor_ai_local.sql:124` y `:130`
(regla de las dos cifras de abajo: valores distintos en los `INSERT INTO public.plans` e
`INSERT INTO public.plan_features` de esos cuatro ficheros).

**6 planes**: `basic` · `commerce` · `advisor_ai` · `advisor_ai_pro` · **`advisor_ai_local`**
(lo añade la `0074`: es `advisor_ai` + `llm_intake` **sin** `api_llm` — «la línea que no está,
y es el plan entero») · `pro` (interno de laboratorio).

**14 features sembradas**; **11 tienen constante en Go**
(`internal/modulos/acceso/entitlements/entitlements.go:51-141`): `llm_intent` · `cart_basic` ·
`intakes_export` · `catalog_import` · `crm_bridge` · `menu` · `survey` · `media` ·
`llm_intake` · `api_llm` · `multi_empresa`.

Las tres restantes (`stt_audio`, `owner_app`, `passive_profiles`) **no gatean nada**: ningún
`.go` de producción las nombra. Ver [`deuda.md`](deuda.md).

**Resolución de una feature efectiva**: un override en `tenant_features` **gana**; sin
override manda el plan; sin `plan_id` se trata como `basic`
(`COALESCE(plan_id, 'basic')`, `internal/modulos/acceso/entitlements/postgres.go:196`).

---

## 8 · Retención y minimización

- **INV-13** — `intake_jobs.source_text` (el sobre de tres columnas) **se vacía en estado
  terminal**: lo hacen las dos sentencias de cierre de
  `internal/modulos/captacion/intake/machine_postgres.go` (`:360` al terminar en `done`, `:394`
  al fallar), y lo declara la `0083_intake_jobs_retencion.sql`. Sus tests,
  `machine_postgres_test.go`, al lado.
- 🔴 **`intake_jobs.artifacts` guarda literal del cliente EN CLARO**, sin TTL y **fuera del
  censo de rotación**. Lo dice la propia migración (`0083_intake_jobs_retencion.sql:80`), que
  por eso vuelve a declarar el `COMMENT` de la columna (`:107`) por encima del que escribió
  `0072_intake_jobs_y_ventana_agregacion.sql:741`, que afirma que no es literal del cliente y
  **sigue en el árbol**: en una base migrada manda el de la `0083`, porque corre después. Es un
  desvío **registrado con dueño** — está en [`deuda.md`](deuda.md).
- `conversation_ttl_seconds` tiene default 7200 (2 h) desde
  `0067_conversation_ttl_default.sql:50`.
- `fleet_sessions.self_pn` y `contacts.push_name` están cifrados
  (`0068_fleet_sessions_self_pn_cifrado.sql`, `0069_contacts_push_name_cifrado.sql`) y **sus
  columnas en claro borradas** (`0070_drop_self_pn_y_push_name_en_claro.sql:87`).
