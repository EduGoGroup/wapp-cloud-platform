# Constitución de `wapp-cloud-platform`

> Este documento es autosuficiente **a propósito**. Si has clonado solo este repo, aquí está
> todo lo que necesitas para no romper el ecosistema desde dentro. Cuando cito algo que vive
> fuera —un ADR, un plan— lo cito **en texto**, no como enlace: esos enlaces no resolverían.

---

## 0 · Qué es esta pieza, en tres frases

wApp es un ecosistema de mensajería sobre WhatsApp cuyo **núcleo corre 24/7 en el equipo del
cliente** (el *Edge*), gobernado por una **plataforma cloud modular**. Esta pieza **es** esa
plataforma: un monolito modular Go, un proceso, cuatro listeners.

El reparto del ecosistema, que rige todo lo demás: **el Edge despacha y custodia llaves; la
nube piensa y guarda el negocio.**

---

## 1 · Invariantes DEL ECOSISTEMA que aplican aquí

Los repito porque un agente que trabaje solo en este repo puede violarlos sin darse cuenta.

### I-ECO-1 · Zero-knowledge: la nube nunca accede a credenciales ni llaves privadas

El zero-knowledge protege **credenciales y llaves**, **NO el contenido de negocio** — ese sí
sube a la nube a propósito, y por eso esta pieza tiene un PostgreSQL lleno de pedidos.

Lo que este repo **jamás** debe hacer: persistir el store cifrado de `whatsmeow`, recibir la
llave que lo descifra, o pedir al Edge que se la mande.

**Cómo se comprueba.** `grep -c whatsmeow go.mod` → **cero**: el driver de WhatsApp no es
dependencia de este repo. ⚠️ **El grep sobre el código NO sale a cero**, así que no lo uses de
receta: `grep -rn "whatsmeow" --include='*.go' cmd internal test` devuelve **19 aciertos**
(medido el 2026-10-11) y **todos son comentarios** que nombran la pieza — la mayoría, justo para
avisar del homónimo de §3.1 (p. ej. `internal/modulos/solicitudes/intakes/buyerdata.go:15`,
`internal/modulos/edge/fleet/fleet.go:14`, `internal/nucleo/contact/repository_postgres.go:45`).
La regla: el invariante se verifica sobre `go.mod`, no sobre los comentarios. **Sin candado**:
ningún `depguard` lo vigila (`.golangci.yml` no lo declara); es una regla sostenida por revisión.

### I-ECO-2 · Doble llave: la DEK es del cliente, el lease es del servidor

Dos llaves independientes, y esta pieza **solo** tiene la segunda:

- **DEK** — descifra el almacén de `whatsmeow` en el Edge. **La custodia el cliente y jamás
  cruza el contrato.** No hay campo en el proto que la transporte, ni columna donde guardarla.
- **Lease** — autoriza a operar. **Lo emite y lo revoca este repo**, y es el kill-switch
  anti-clon: revocado, el Edge deja de despachar aunque tenga la DEK.

Dónde vive el lado servidor: `internal/modulos/edge/lease/`. La firma es Ed25519, la clave
privada se lee de fichero (`WAPP_LEASE_PRIVATE_KEY_FILE`) o de base64
(`WAPP_LEASE_PRIVATE_KEY_B64`), y la pública se imprime al arrancar para configurar el Edge
(`internal/arranque/lease.go:45`). Revocación por `/admin/leases/revoke`, permiso
`leases.revoke` (`internal/arranque/rutas_admin.go:71`).

🔴 **Homónimo peligroso: ver §3.1.** «DEK» en el código de este repo nombra **otra cosa**.

**Cómo se comprueba.** La revocación es *pegajosa* y la vigilan los tests del paquete
(`internal/modulos/edge/lease/lease_revocation_test.go`). El invariante «la DEK del ADR-0007 no entra aquí» **no tiene
candado**: se sostiene en que no existe el campo.

### I-ECO-3 · Sin Redis, sin broker

En el Edge está prohibido por ADR (0003). **Aquí tampoco hay**, y es deliberado: el fan-out de
campañas y el trabajo concurrente se resuelven **con goroutines y canales de Go**, no con
RabbitMQ ni con `edugo-worker`.

**Cómo se comprueba.** `go.mod` no tiene un solo import de Redis, AMQP ni Mongo. Verificado.
La durabilidad que en otro diseño daría un broker aquí la da **la base**: `webhook_outbox`
(outbox durable del puente CRM) e `intake_jobs` (la cola del pipeline LLM), ambas reclamadas
con SQL.

### I-ECO-4 · Copia-adaptación, nunca dependencia — y la EXCEPCIÓN que sí existe

El código de otro producto del grupo (EduGo) se **copió y adaptó** al espacio de nombres wApp.
**Está prohibido importar un repo `edugo-*`.**

**Cómo se comprueba.** `grep -n "edugo-" go.mod` → **cero**. ✅

⚠️ **Pero hay una excepción real y sin documentar en la regla original**: `go.mod:7` declara
`github.com/EduGoGroup/identity-shared/auth v0.3.1`, y se usa **en producción** en
`internal/arranque/auth_stack.go:10`, `internal/platform/httpapi/authmw.go:9`,
`internal/modulos/acceso/iam/usecase/grants.go:10` e
`internal/modulos/acceso/iam/usecase/exchange.go:9`. No es un
`edugo-*`: es el SDK del **SSO del grupo** (identity-core), cuyos tokens ES256 esta pieza
verifica. **No lo retires creyendo que es una violación** — y no lo uses como precedente para
importar nada más.

### I-ECO-5 · El código compartido interno vive en `wapp-shared`

`wapp-shared` es el **monorepo multi-módulo propio de wApp**, con releases **por módulo**
(tags `<modulo>/vX.Y.Z`). Este repo consume ocho de sus módulos (`auth`, `config`, `envelope`,
`health`, `intents`, `llm`, `logger`, `textmatch`). Si un símbolo tuyo va a servir a dos
repos, va allí; si sirve solo aquí, se queda aquí.

🔴 **Consecuencia operativa**: cambiar el texto compilado de un prompt, o la API del módulo
`llm`, **exige cortar un release de `wapp-shared` primero**. Por eso existe la palanca de
prompts por fichero (I-CP-1): para no pagar ese peaje al afinar texto.

---

## 2 · Invariantes PROPIOS de esta pieza

### I-CP-1 · 🔴 El texto de los prompts P2–P5 se ajusta POR FICHERO y SIN release

Es la regla de oro del pipeline y la que más se viola por desconocimiento. El procedimiento
completo, y no hay otro:

```bash
go run ./cmd/prompts -volcar /ruta/prompts     # saca los 4 .tmpl con el texto que corre HOY
# editas el fichero
WAPP_LLM_PROMPTS_DIR=/ruta/prompts ./server    # se enciende con la variable
# y se REINICIA el proceso
```

- Los cuatro ficheros son `p2-extraer-ideas.tmpl`, `p3-especificar-item.tmpl`,
  `p4-normalizar-cantidades.tmpl`, `p5-redactar-cotizacion.tmpl`
  (`NombreDeFichero`, `internal/modulos/inferencia/prompts/volcar.go:20`). **Solo el prefijo
  `pN-` es contrato**: el resto del nombre es tuyo (`stageFromName`,
  `internal/modulos/inferencia/prompts/prompts.go:257`). Un fichero sin extensión `.tmpl` se
  ignora en silencio — es la forma de dejar notas al lado.
- **NO hay recarga en caliente, y es a propósito.** Cambiar texto sin reiniciar significaría
  que dos peticiones simultáneas corren prompts distintos.
- `go run ./cmd/prompts -comprobar <dir>` valida un directorio **sin arrancar el servidor**.
- 🔴 **Un directorio inválido ABORTA EL ARRANQUE.** No hay degradación a «sigo con el texto
  compilado»: `ErrPromptsDir` lo declara (`internal/modulos/inferencia/prompts/prompts.go:128`) y
  `cargarPlantillasDePrompt` lo propaga (`internal/arranque/prompts.go:26`). El motivo está
  escrito allí mismo: *«un operador que editó un fichero y no ve el efecto es peor que un
  proceso que no arranca, porque el segundo se nota»*.
- El arranque **deja en el log de dónde salió cada plantilla** (`p4=/ruta/…` o
  `compilada`). Esa línea no es adorno: contesta sola «¿qué texto corrió?».

🔴 **La sub-regla que costó 14 jobs muertos: en el esquema NO puede haber un valor que su
propio validador rechace.** El modelo **copia el ejemplo**. P4 fue **0 de 14 en campo** por un
`"package_size": 0` impreso en su esquema, que `validarPaquete` rechazaba. Por eso
`llm.ValidarPlantilla` corre el `Parse*` de la etapa **sobre el ejemplo del esquema** y falla
si lo rechaza (la llamada está en `internal/modulos/inferencia/prompts/prompts.go:244`).

**Candado.** `TestValidarPlantilla…` en el módulo `wapp-shared/llm` muta
`"package_size": 30` → `0` y exige el error; aquí,
`internal/modulos/inferencia/prompts/prompts_test.go` (`TestCargar_StartupFailures` y
`TestCargar_ValidatorErrorStaysWrapped`).

### I-CP-2 · 🔴 La inferencia la ORQUESTA este repo; el Edge solo la SIRVE

Es **lo contrario** de lo que decía el diseño original. La doctrina vigente (ADR-0045, en el
repo de documentación del ecosistema):

- **El Cloud** construye el prompt, trocea, valida la salida y decide. Todo eso vive aquí:
  `internal/modulos/captacion/stages/`, `internal/modulos/inferencia/prompts/`,
  `internal/modulos/inferencia/llmvia/`.
- **El Edge** es un servidor de inferencia: *prompt entra → JSON sale*. **No interpreta nada.**
- La clasificación es **pull**, no push. El campo `intent` del proto está `reserved`: no
  existe el canal por el que el Edge empujaba una interpretación.

**Consecuencia medida que cambia lo que puedes construir:** `Signal.Intent` **es hoy siempre
`nil` en producción**, así que las reglas `flow_triggers kind='llm'` **NO PUEDEN DISPARAR**.
La rama sigue viva (`internal/modulos/conversacion/trigger/config_resolver.go:83`, y
`trigger.KindLLM` en `internal/modulos/conversacion/trigger/trigger.go:52`) y **sus tests están
verdes**, midiendo código que producción no alcanza. Lo declara el propio código: el runtime
arma la señal **sin** intención en `buildSignal`
(`internal/modulos/conversacion/runtime/incoming_trigger.go:424`). No lo «arregles» de paso: es
un frente de producto con dueño. Ver `deuda.md`.

### I-CP-3 · 🔴 El switch por vía (`local` | `api`) vive en `llmvia` y en NINGÚN otro sitio

El `switch via` de la selección está en `Selector.For`
(`internal/modulos/inferencia/llmvia/llmvia.go:308`). Sus únicos hermanos legítimos son tres
métodos del mismo `Selector`, en el mismo fichero y con el mismo vocabulario cerrado: `Warm`
(`:424`), `PlazaDe` (`:534`) y `turnRoute` (`:567`). Si te encuentras preguntando «¿qué vía usa
este tenant?» fuera de `internal/modulos/inferencia/llmvia/`, lo que necesitas es **otro método
en el puerto**.

**Candado.** `TestC2_TheRouteIsOnlyAskedInTheSelection`, en
`internal/modulos/inferencia/llmvia/c2_via_test.go` — es un test **AST**, no de conducta, y es
ancho a propósito: cuenta cualquier comparación o `switch` cuyo sujeto empiece o termine por
`via`. Renombrar la variable para esquivarlo está prohibido.

### I-CP-4 · La plaza única: W=1 worker de pipeline, K=1 por Edge

El pipeline de captación corre con **un** worker (`internal/arranque/fase9_fondo.go:105`). Duplicar esa línea
**no daría ningún error**: dos workers reclamarían sin pisarse en la base y se bloquearían
mutuamente en la única plaza de inferencia del Edge. El aforo por Edge lo toma el worker antes
de la cadena (`Slot` y `Capacity`, en `internal/modulos/captacion/pipeline/slot.go`).

**Candado.** `TestPipelineCaptacionCableado`, en
`internal/arranque/pipeline_captacion_cableado_test.go` — un test de cableado que cuenta las
apariciones de esa línea, porque el invariante vive en una línea repetible.

### I-CP-5 · 🔴 Los permisos de PLATAFORMA acaban en `.any`

El listener admin `:8100` mezcla dos planos: rutas de tenant y rutas de **operador de
plataforma** (nosotros, no el cliente). Lo que las distingue es el sufijo **`.any`** del
permiso: `tenants.read.any`, `fleet.read.any`, `users.provision.any`… Se le concede solo a
`platform_admin` (`0059_platform_admin.sql:78` y `0060_platform_console_grants.sql:145`) y se le
**niega** al glob `*` de `tenant_admin` con un deny `*.any`, que siembra la `0059`
(`0059_platform_admin.sql:81`) y cubre por forma cualquier permiso `.any` futuro: por eso la
`0060` no lo repite. Sin ese deny, cualquier admin de cliente alcanzaría el plano de plataforma.

**Candado.** `internal/arranque/platform_permissions_test.go` →
`TestINV056_1_PlatformPermissionsMustEndInDotAny`, con su caso negativo
(`TestINV056_1_DetectsUnguardedPlatformRoute`).

⚠️ **Trampa de estilo que impone el candado**: el detector lee **texto fuente** y reconoce una
ruta de plataforma buscando la cadena `"platformadmin."` en el argumento de `adminHandler(...)`
(el detector, en `internal/arranque/platform_permissions_test.go:49`; la advertencia, en
`internal/arranque/rutas_admin.go:25`). Por eso esos handlers **deben** construirse inline y no
pre-armarse en `adminRouteDeps`. Un refactor «de limpieza» que los mueva a campos deja el candado **ciego sin
ponerse rojo**.

### I-CP-6 · El gate de features es FAIL-CLOSED

Los tres modos de no-resolución de `RequireFeature` devuelven **403**, nunca 500 ni un pase
libre (`internal/modulos/acceso/entitlements/middleware.go:46`). La resolución (feature efectiva de un
tenant): un override en `tenant_features` **gana**; sin override manda el plan; sin `plan_id`
se trata como `basic` (`COALESCE(plan_id, 'basic')`, `internal/modulos/acceso/entitlements/postgres.go:194`).

Hay **11 constantes de feature en Go**
(`internal/modulos/acceso/entitlements/entitlements.go:49-137`) y **14 sembradas** en las
migraciones (el recuento y su regla, en [`esquema-postgres.md`](esquema-postgres.md) §7). Las tres de más (`stt_audio`, `owner_app` y
`passive_profiles`) **no gatean nada**. Ver `deuda.md`.

### I-CP-7 · El esquema es FULL-REPLAY: todo el DDL tiene que ser idempotente

El runner (`internal/platform/storage/postgres/migrations/migrate.go`) **reejecuta TODOS** los
`.sql` cuando cambia `SchemaVersion` **o** el hash del contenido. No hay «ya aplicada». El
mecanismo entero está en [`esquema-postgres.md`](esquema-postgres.md) §2.

⇒ **Toda migración que escribas debe poder correr N veces.** `CREATE TABLE IF NOT EXISTS`,
`ADD COLUMN IF NOT EXISTS`, `INSERT … ON CONFLICT DO NOTHING`.

⚠️ Trampa conocida del mecanismo: `CREATE TABLE IF NOT EXISTS` **no repone una columna que una
migración posterior borró** — el `COMMENT ON COLUMN` que va debajo mata el segundo arranque.

Se serializa con `pg_advisory_lock` sobre **una conexión dedicada** (`migrate.go:61`) y se
ordena **por nombre** (`migrate.go:142`). ⚠️ Contra un pooler en modo transacción el lock se puede
repartir entre sesiones: apunta siempre al **host directo**.

### I-CP-8 · Identificadores en inglés, nombre de negocio en la UI (INV-09)

Las claves que viajan por el wire y viven en la BD van en inglés (`pending_approval`,
`needs_info`); el nombre bonito vive en la UI y en la documentación, **nunca** en la base.
Ejemplo canónico: `internal/modulos/solicitudes/intakes/status.go:25`.

En el código, los **nombres** (ficheros, tipos, funciones, variables, tests) van en **inglés** y
los **comentarios** y la documentación en **español**
(E-11, [§5.1](#51--las-normas-que-dejó-la-reconstrucción)).
Los textos observables —mensajes de error, claves de log, literales que viajan al usuario— son
contrato y se conservan tal cual están, estén en el idioma que estén: no los «corrijas».

### I-CP-9 · ⚠️ wApp NO autentica personas: aquí no hay padrón ni contraseñas

Las credenciales las valida **identity-core**, el SSO del grupo. Esta pieza **canjea** el
Identity Token por un **Context Token** propio (ES256) con el tenant y los grants de negocio
(`POST /api/v1/auth/exchange`).

Si vas a proponer algo que valide una contraseña, resuelva un usuario o emita un refresh en
este repo, estás reintroduciendo lo que se eliminó a propósito. Verificado: la migración
`0038_retiro_iam_propio.sql:59` dropea `iam_users`, `iam_refresh_tokens` e `iam_api_keys`.

Lo que **sí** se queda aquí y es de negocio: `iam_roles`, `iam_role_grants`, `iam_user_roles`,
`iam_user_grants` — RBAC glob multi-tenant, no identidad.

🔴 **Trampa multi-tenant**: un rol asignado con `tenant_id` NULL vale en **todas** las
empresas. El rol transversal se distingue por su **ID**, no por su nombre: cualquier dueña
puede crear un rol llamado `platform_admin`.

### I-CP-10 · Un solo mecanismo de cifrado de campo, y un censo que nadie puede saltarse

`internal/platform/crypto/field_cipher.go` (`FieldCipher`: envelope AES-256-GCM con DEK fresca
por valor, envuelta por la KEK del keyring, más el `kek_id`) es **el único** mecanismo. Toda
tabla con sobre KEK **debe** estar en `rekeyTargets` (`internal/platform/crypto/rekey.go`), el
censo que recorre la rotación: omitir una es declarar «rotación completa» dejando esas filas
**ilegibles para siempre** cuando se retire la KEK vieja.

Hoy el censo tiene **8 sobres en 7 tablas**. **Y hay una que falta**:
`conversation_event_messages` se escribe cifrada
(`internal/modulos/conversacion/events/store_append.go:50`) y **no está en el censo**. Ver
`deuda.md`.

**Candado.** El proceso de rotación de `test/procesos/` (`p10_plataforma_rekey_test.go` y
`rekey_contrato_test.go`) rota sobre las entradas **declaradas** — es un test *de la lista*, no
*de la ausencia*. No atrapa una tabla omitida.

---

## 3 · Homónimos: las tres palabras que hacen concluir cosas falsas

### 3.1 · 🔴 «DEK» aquí NO es la DEK del ecosistema

| | DEK del ecosistema (doble llave) | `DEK` en el código de este repo |
|---|---|---|
| Qué descifra | el store de `whatsmeow` en el Edge | un campo de **PII de negocio** en PostgreSQL |
| Quién la custodia | **el cliente**, jamás cruza a la nube | **esta pieza**, envuelta por su KEK |
| Dónde se ve | no existe aquí | `internal/platform/crypto/field_cipher.go` (quien la genera), columnas `*_dek` |

Confundirlas lleva a concluir que **el zero-knowledge está roto cuando no lo está**. Que en
`intake_buyer_data` haya una columna `data_dek` es exactamente lo que el diseño quiere: el
contenido de negocio sube a la nube y se cifra allí.

El propio código lleva el aviso allí donde aparece la palabra: en los datos del comprador
(`internal/modulos/solicitudes/intakes/buyerdata.go:12`, que es donde lo declara con todas las
letras, y `buyerdata_postgres.go` a su lado), en el gateway
(`internal/modulos/edge/grpc/server.go:283`), en la flota
(`internal/modulos/edge/fleet/fleet.go:14`), en los contactos
(`internal/nucleo/contact/repository_postgres.go:45`), en el historial de eventos
(`internal/modulos/conversacion/events/store.go:38`) y en la clave de API del tenant
(`internal/modulos/inferencia/tenantllm/postgres.go:25`). Las tablas con sobre y el censo de
rotación, en [`esquema-postgres.md`](esquema-postgres.md) §6.

### 3.2 · «admin» nombra dos planos distintos

`/admin/*` en el listener `:8100` contiene **rutas de tenant** (enviar un mensaje, crear un
flujo) **y rutas de operador de plataforma** (crear tenants, aprobar altas). Lo único que las
separa es el sufijo `.any` del permiso (I-CP-5). «Es una ruta admin» no dice quién puede
llamarla.

### 3.3 · «P1» no vive aquí

El pipeline de este repo es **P2→P5**. `wapp-shared/llm` define `EtapaP2`…`EtapaP5` y **no
existe `EtapaP1`**. El prompt del clasificador de intenciones lo gobierna el **catálogo de
intenciones**, que se edita **por API** (`PUT /api/v1/intents`,
`internal/modulos/captacion/intentcfg/`). Quien venga a «tocar el prompt del clasificador» y
abra `internal/modulos/inferencia/prompts/` está en el sitio equivocado.

### 3.4 · `orders` / `order_items` ya no existen

La migración `0041_rename_orders_intakes.sql:39` los renombró a **`intakes` / `intake_items`**:
pedido y presupuesto son el **mismo objeto de dominio**. Un grep por `orders` no encuentra la
tabla y hace concluir que el dominio no está construido.

---

## 4 · Tecnología y versiones REALES (de `go.mod`)

- **Go 1.26.5** (`go.mod:3`). Módulo `github.com/EduGoGroup/wapp-cloud-platform`, público
  (sin `GOPRIVATE`).
- **PostgreSQL** vía `github.com/jackc/pgx/v5 v5.10.0` en modo *stdlib* sobre `database/sql`.
  **Sin ORM.** SQL crudo con `$N`. Un único `*sql.DB` compartido, pool configurable
  (default 25 abiertas / 5 ociosas / 1 h de vida / 10 min ociosa,
  `internal/platform/storage/postgres/connect.go:31`).
- **gRPC** `google.golang.org/grpc v1.82.1` + `protobuf v1.36.11`.
- **Contrato edge↔cloud**: `github.com/EduGoGroup/wapp-cloudlink v0.17.0` (paquete
  `cloudlinkv1`).
- **`wapp-shared`**: `llm v0.4.5` (etapas, `Parse*`, plantillas), `auth v0.5.0`,
  `config v0.3.0`, `envelope v0.2.1`, `health v0.1.1`, `intents v0.1.0`, `logger v0.2.0`,
  `textmatch v0.1.0`.
- **`identity-shared/auth v0.3.1`** — la excepción de I-ECO-4.
- **S3/R2**: `aws-sdk-go-v2/service/s3 v1.98.0`, usado **solo** para URLs prefirmadas.
- **KMS**: `cloud.google.com/go/kms v1.33.0`. Construido pero **apagado**: el default de
  `WAPP_KEK_PROVIDER` es `env` (`internal/platform/config/config.go:653`).
- **Prometheus** `client_golang v1.23.2`; **excelize v2.11.0** (import de catálogo xlsx y
  export de bandeja, solo en `internal/apipublica`); **`golang.org/x/time v0.15.0`** (token
  buckets); **`jsonschema/v6 v6.0.3`** solo en tests; **`testcontainers-go v0.44.0`** para los procesos de
  `test/procesos/`.
- **Frontend: NO HAY.** Cero plantillas Go, cero JS. API JSON pura + gRPC.

---

## 5 · Convenciones de código

- **Estructura modular por CAPACIDAD**, no hexagonal global. `internal/modulos/` tiene siete
  módulos (`acceso`, `captacion`, `catalogo`, `conversacion`, `edge`, `inferencia`,
  `solicitudes`), cada uno con su frontera, sus tablas y su API; lo que comparten dos módulos
  vive en `internal/nucleo/`, la infraestructura en `internal/platform/`, la cara HTTP pública en
  `internal/apipublica/` y el cableado en `internal/arranque/`. Trabaja **dentro del módulo**; no
  crees capas transversales nuevas. La **única** zona hexagonal
  (`domain/usecase/ports/infra/transport`) es `internal/modulos/acceso/iam/`, y es deliberado.
- **Idioma**: nombres en inglés, comentarios y documentación en español (I-CP-8).
- **Tamaño**: ≤ 500 líneas por `.go`, con tolerancia hasta 600; por encima, el fichero se parte
  por tema. Lo vigila `TestFileSize` (`internal/modulos/file_size_test.go`).
- **Candados de forma**: la lógica vive en `internal/candados/` (fronteras entre módulos, un
  fichero un test, exportados cubiertos, tamaño, sin base viva…) y demuestra que muerde contra
  árboles de prueba en su `testdata/`; los tests finos que la aplican al árbol real son los de
  `internal/modulos/*_test.go` (`TestFronteras`, `TestUnFicheroUnTest`,
  `TestExportadosCubiertos`, `TestFileSize`, `TestNoPending`) y
  `test/procesos/sin_bd_viva_test.go`. Si uno se pone rojo, se arregla el código: no se añade
  una excepción.
- **Tests contra Postgres**: son **procesos de negocio en caja negra**, en `test/procesos/`,
  contra el binario `cmd/server` y un Postgres levantado con testcontainers (etiqueta de
  compilación `integracion`, `make test-procesos`, necesita Docker). **Ningún test apunta a un
  Postgres vivo**, y un proceso que no puede correr **falla, no se salta**: `make test-procesos`
  sale rojo con `SKIP>0`. `make test` son los unitarios, sin base.
- **Comentario-como-ADR**: el **44,8 %** de las líneas de producción son comentario (medido el
  2026-10-11: líneas que empiezan por `//` sobre el total, en los 607 `.go` de `cmd` e `internal`
  que no son `_test.go` ni `testdata`). Cuando cambies una decisión, **actualiza el comentario
  que la narraba**; un comentario que describe un bloqueo ya levantado manda a investigar a quien
  lo lea.
- **Lint**: `.golangci.yml` habilita **16 linters** (`linters.enable`, `.golangci.yml:15`) más
  **2 formateadores** (`formatters.enable`, `.golangci.yml:75`: `gofmt` y `goimports`), que en la
  v2 del fichero **ya no se declaran como linters**. Entre los 16, `errcheck` con
  `check-type-assertions: true` **y `check-blank: true`**, `gosec`, `contextcheck`, `nilerr`,
  `errorlint`, `gocyclo` (min-complexity 15) y `revive`. **Un aviso de lint se arregla, no se
  silencia.** Quedan **15 `//nolint`** fuera de los `_test.go` (medido el 2026-10-11:
  `grep -rn --include='*.go' '//nolint' cmd internal | grep -v _test.go`), **todos con
  justificación escrita**; no añadas otro.
- **Tests de cableado**: hay **15** en `internal/arranque/`
  (`ls internal/arranque/*cablead*` → 15). Es la herramienta de este repo para vigilar
  invariantes que viven en una línea repetible del arranque (I-CP-4 es el ejemplo). Fuera de los
  candados y del arranque, los tests **no** leen el código como texto: miden conducta.
- **Nada de `TODO`/`FIXME`.** La deuda se marca con `DEUDA-NNN.N`, 🔴, ⚠️ y 🟡, y se anota
  en `deuda.md`. No hay ni una marca `TODO:` o `FIXME` en el código (medido el 2026-10-11): las
  apariciones de `TODO` a grep pelado son la palabra española *todo* en mayúsculas.

### 5.1 · Las normas que dejó la reconstrucción

`internal/` se reconstruyó por módulos entre el 2026-09-27 y el 2026-10-11 (qué fue y dónde está
su expediente: [`modular-reorganization.md`](modular-reorganization.md)). El método con el que se
hizo terminó con ella; estas son las reglas suyas que **siguen obligando** a quien escriba código
mañana. Conservan su identificador (`E-n`) porque los comentarios del código las citan así
(«05 E-13», «E-9»): `05` era el documento del método, y lo que de él sigue vivo es esta sección.

**E-3 · Un fichero, un test.** Por cada `x.go` de producción hay un `x_test.go` en el mismo
directorio, y cubre el contrato de `x.go`, no el de sus vecinos. Lo vigila `TestUnFicheroUnTest`.
Las excepciones se **comprueban**, no se listan:

| Fichero | Qué lleva en su lugar |
|---|---|
| `doc.go` sin ninguna declaración | Nada |
| Fichero solo de `//go:embed` | Lo prueba el test de quien lee lo embebido |
| Fichero **solo de interfaces** (un puerto) | Una **suite de contrato** en el paquete hermano `<paquete>helpertest`: una función `Contrato(t *testing.T, …)` que cada implementación ejecuta desde su propio test. Sin esa suite, el puerto necesita test |
| Puerto de **entrada** de un directorio de `candados.InboundPortDirsWithoutSuite` (`internal/candados/inbound_ports.go`; hoy solo `internal/modulos/acceso/iam/ports/in`) | Sin suite: lo cubre el test del caso de uso que lo implementa |

Los paquetes `…helpertest` (suites y dobles de test) quedan fuera de este candado; un doble con
lógica lleva igualmente su test.

**E-5 · Ni un `t.Skip`.** Un test que no puede correr **falla**; no se salta, ni por falta de
Postgres ni para aparcar algo a medias. Un `--- SKIP` bajo `rc=0` esconde lo que una pantalla en
rojo enseña. Medido el 2026-10-12: cero llamadas reales en `cmd`, `internal` y `test`
(`grep -rn 't\.Skip' --include='*_test.go' cmd internal test` da 14 líneas, todas en
`internal/candados/sinbdviva*_test.go`, que es el candado que las prohíbe y las nombra para
probarse).

**E-6 · Los tests de fichero no tocan la base.** Lo que habla con Postgres se prueba en dos
sitios: el **puerto** tiene su suite de contrato, que corre ya contra un doble en memoria del
paquete `…helpertest`; y **la misma suite** corre contra el adaptador Postgres en
`test/procesos/<paquete>_contrato_test.go`, con testcontainers. La suite se escribe una vez y
sirve a las dos. El adaptador Postgres lleva además su test unitario con lo que se prueba sin
base (constructor, validación de argumentos, mapeo de filas y de errores); su cobertura unitaria
es baja a propósito. El doble en memoria **imita lo que hace Postgres en producción**: un doble
que promete lo que producción no hace es peor que no tenerlo.

**E-7 · El test prueba conducta.** Se prueba lo que el fichero promete, nunca cómo lo hace. Leer
el código como texto (AST) queda para los candados y los tests de cableado del arranque.

**E-9 · Todo exportado se nombra en su test, y la cobertura no bloquea.** Un símbolo exportado de
`x.go` que no aparece en `x_test.go` hace fallar `TestExportadosCubiertos`. La cobertura, en
cambio, es un **informe**: `make cobertura-ficheros` sale con `rc=0` aunque haya ficheros por
debajo de su línea de referencia. La meta son tests que detecten un fallo de lógica, no una cifra.

**E-11 · Nombres en inglés; en español, los comentarios.**

| En **inglés** | En **español** |
|---|---|
| Nombres de fichero, de directorio y de paquete · tipos, interfaces, funciones, métodos, campos, variables, constantes y errores centinela · nombres de `Test…`, de los subtests (`t.Run`) y de las etiquetas de caso | Los comentarios · la documentación · los mensajes de commit · los **mensajes de fallo de los tests** (`t.Errorf`, `t.Fatalf`) |

Tres cosas que **no** cambian: (1) los textos observables se copian literales aunque estén en
español —el identificador va en inglés, el texto no se toca:
`var ErrInvalidRef = errors.New("contact_ref inválida")`—; (2) lo ya escrito conserva su nombre:
no hay renombres masivos ni «de paso»; (3) el vocabulario que nació en español se queda —los
siete módulos, `nucleo`, `arranque`, `apipublica`, `candados`, `Contrato`, `Montaje`, el sufijo
`_contrato` de los ficheros de una suite y la etiqueta de compilación `integracion`—. No tiene
candado: el idioma lo vigila quien revisa.

**E-13 · Tamaño de fichero.** Objetivo ≤ 500 líneas por `.go`, de producción **y** de test;
tolerancia hasta 600 cuando partir rompería un tema que se lee de una vez; por encima de 600 se
parte. Lo vigila `TestFileSize`. Cómo se parte:

- por tema, **solo moviendo declaraciones** de primer nivel con su comentario, sin cambiar un byte;
- cada trozo lleva el **sufijo de su origen** y una línea que lo dice: `postgres_test.go` →
  `postgres_has_test.go`, `postgres_list_test.go`…;
- un trozo de producción `x_<tema>.go` lleva su gemelo `x_<tema>_test.go` (E-3) y sus exportados
  se nombran ahí (E-9); el `x_test.go` que conserva el nombre sigue nombrando todos los exportados
  de `x.go`;
- los trozos de una suite se llaman `<tema>_contrato.go`, no `contrato_<tema>.go`.

Si al partir se mueven líneas, se re-apuntan las citas `fichero:línea` de esta documentación.

**Las tres listas cerradas.** Son excepciones con nombre y apellido; **ampliar cualquiera pide una
decisión escrita de Jhoan**, no un commit:

| Lista | Dónde | Qué exime |
|---|---|---|
| `candados.OversizedFiles` | `internal/candados/filesize.go` | Los ficheros que ya pasaban de 600 líneas cuando nació E-13, cada uno con su techo: no pueden crecer |
| `candados.InboundPortDirsWithoutSuite` | `internal/candados/inbound_ports.go` | Los directorios de puertos de entrada sin suite de contrato (E-3) |
| `candados.ContractAdapterDirs` | `internal/candados/contract_adapters.go` | Los pares suite → adaptador en que el adaptador Postgres **no** vive en el paquete padre de su suite: el `*_contrato_test.go` de `test/procesos/` que importa la suite puede importar también ese adaptador. Fuera de eso, un proceso no importa dominio |

**Más convenciones, sin candado.** Las vigila quien revisa:

- **El SQL vive en ficheros `*postgres*`**, separado de la lógica del paquete.
- **El reloj se inyecta** donde la lógica **decide** por el tiempo (vencimientos, ventanas,
  rachas): es lo que deja probarla sin dormir. No es una prohibición de `time.Now()`.
- **Apagarse no es un fallo.** Un trabajo de fondo cortado por la cancelación del contexto se
  devuelve sin cobrarle el intento y sin una línea a `ERROR`.
- **Al borrar código, su documentación se reescribe.** No se deja un apartado «(histórico)» ni dos
  versiones de lo mismo: la doble información es lo que más desvía a quien lee, persona o modelo.
  Si un documento o un comentario nombra una ruta que no existe, el que está mal es él.

### 5.2 · Cómo se añade un módulo

Un módulo nuevo es raro —los siete salieron de medir el código, no de un organigrama— y lo decide
Jhoan. Si se decide, toca cinco sitios, y los candados avisan de los que falten:

1. **La carpeta**: `internal/modulos/<módulo>/`, con su paquete o sus subpaquetes. Lo que vayan a
   compartir dos módulos no va en ninguno de los dos: va en `internal/nucleo/`.
2. **Las fronteras**: una entrada en la tabla `Capas` de `internal/modulos/fronteras_test.go` con
   los módulos que **puede** importar (vacía si es hoja), y su nombre en `Conmutados` y en
   `allModules` del mismo fichero. Cada import nuevo módulo→módulo es una arista que se añade ahí
   a propósito: `TestFronteras` falla con cualquiera que no esté.
3. **El cableado**: se construye en la fase de `internal/arranque/` que le corresponda (la lista
   `fases` de `internal/arranque/orquestador.go`), nunca en `cmd/server`.
4. **Lo que expone**: las rutas públicas se montan en `internal/apipublica/`, en un fichero de su
   tema. Rutas, rpc, métricas y goroutines nuevas cambian la huella:
   `internal/arranque/testdata/huella-vieja.golden` se actualiza **a propósito** y en el mismo
   commit, nunca «para que pase» (`TestHuella`).
5. **Los tests**: cada fichero nace con su gemelo (E-3) y sus exportados nombrados (E-9); si
   habla con Postgres, su puerto trae suite de contrato y la corrida contra la base va a
   `test/procesos/` (E-6).

Y la documentación: la tabla de módulos y la de aristas de [`arquitectura.md`](arquitectura.md)
§4.1, y lo que exponga, en [`contratos.md`](contratos.md).

---

## 6 · Trampas conocidas (lo que un agente hace mal aquí si nadie se lo dice)

1. **Creer que el README dice la verdad.** Para saber qué existe se lee
   `internal/arranque/orquestador.go`: la lista `fases` (nueve, en una pantalla) y el fichero de
   cada una son el inventario real. Las cifras tienen dueño —rutas y variables en
   [`contratos.md`](contratos.md), tablas y esquema en
   [`esquema-postgres.md`](esquema-postgres.md)— y cada una lleva la regla con la que se contó.
   Verifica contra el código.
2. **Buscar las rutas en un solo fichero.** Las del listener público las montan los ficheros de
   `internal/apipublica/`, cada uno las de su tema; las del listener admin,
   `internal/arranque/rutas_admin.go`. El inventario completo lo fija
   `internal/arranque/testdata/huella-vieja.golden`, que vigila `TestHuella` (ver
   [`contratos.md`](contratos.md)). Un inventario hecho a ojo sobre un solo fichero se deja
   rutas.
3. **Añadir una ruta sin mirar si su dependencia puede ser `nil`.** Muchas rutas son
   **condicionales**: si su dependencia llega `nil`, la ruta **no se monta** y responde un 404
   de ruta inexistente (`mountRoles` y `mountMembers`, `internal/apipublica/roleplane.go:123` y
   `:157`). Matiz deliberado: `POST /api/v1/members` se monta **siempre que haya administración
   de membresía**, tenga o no el despliegue credencial M2M de identity, y sin ella degrada a
   **503**, nunca a 404 (`internal/apipublica/roleplane.go:168`).
4. **Tocar el `WriteTimeout` global creyendo que gobierna todo.** `POST
   /api/v1/intakes/{id}/quote-suggestion` es **la única ruta con plazo de escritura propio**
   (`writeDeadline`, `internal/apipublica/writedeadline.go:100`, montado en
   `internal/apipublica/intakereports.go:227`): espera al modelo **dentro**
   de la petición (24,8–35,5 s medidos en UAT) contra un `WriteTimeout` global de 10 s.
5. **Suponer que `/metrics` es inocuo.** El scrape **barre de paso** las rachas de
   auto-respuesta vencidas y las manda al histograma (`internal/arranque/fase7_flujos.go:139`). Si nadie raspa
   `/metrics`, esos episodios **no se cierran nunca**.
6. **Esperar ver una métrica recién desplegada.** Un `CounterVec` de Prometheus **no aparece
   en `/metrics` hasta su primer incremento**, y el reinicio lo borra. Su ausencia tras
   desplegar no es un fallo.
7. **Cambiar `WAPP_STORAGE_S3_PREFIX` creyendo que mueve algo.** Está en `.env.example` y en
   los runbooks, y **no la lee nadie**: el prefijo es la constante compilada
   `mediaKeyPrefix = "wapp/media"` (`internal/apipublica/media.go:123`). Ver `deuda.md`.
8. **Desplegar sin R2/S3 vivo.** `NewR2PresignClient` valida el bucket con `HeadBucket` y si
   falla **el proceso no levanta** (`internal/arranque/flows.go:75`). Es fail-fast a
   propósito, pero acopla el arranque de IAM y del gateway a un almacén que solo usa el nodo
   `media`.
9. **Abrir un PR esperando validación.** `ci.yml` es `workflow_dispatch`: **un PR no valida
   nada**. Ver `operacion.md`.
10. **Leer un `rc=0` de `go test` como «todo pasó».** Un `--- SKIP` cuenta igual que un
    `--- PASS`, y `make ci-local` **no toca Postgres**: no incluye los procesos. Lo que pide
    base son las **128** funciones `Test…` de `test/procesos/` (medido el 2026-10-11:
    `grep -rh '^func Test' test/procesos | wc -l`), que solo corren con `make test-procesos` y
    Docker; ese target cuenta los SKIP por ti y sale rojo si hay uno. Un `ci-local` verde sin
    `test-procesos` no dice nada de la conducta contra la base. Y lee el código de salida **sin
    pipe**: con `| tail` delante, el `rc` es el de `tail`.
11. **Migrar contra un pooler.** Ver I-CP-7.
12. **Tocar `documentations/literal-aviso-sesion-pasiva.md`.** Es un contrato congelado: el
    literal exacto de un aviso que viaja al usuario final. No lo edites de paso ni lo
    dupliques.
