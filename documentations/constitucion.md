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
receta: `grep -rn "whatsmeow" --include='*.go' .` devuelve **9 aciertos**
(`internal/receipts/receipts.go:8` y `:38`, `internal/ingest/dedupe.go:13`,
`internal/flujos/contact/{resolver.go:10, repository_postgres.go:65,
deadlock_integration_test.go:36}`, `internal/publicapi/sessions.go:72`,
`internal/gateway/fleet/fleet.go:184` y `:228`) y **todos son comentarios** que nombran la pieza.
La regla: el invariante se verifica sobre `go.mod`, no sobre los comentarios. **Sin candado**: ningún `depguard` lo vigila (`.golangci.yml` no lo
declara); es una regla sostenida por revisión.

### I-ECO-2 · Doble llave: la DEK es del cliente, el lease es del servidor

Dos llaves independientes, y esta pieza **solo** tiene la segunda:

- **DEK** — descifra el almacén de `whatsmeow` en el Edge. **La custodia el cliente y jamás
  cruza el contrato.** No hay campo en el proto que la transporte, ni columna donde guardarla.
- **Lease** — autoriza a operar. **Lo emite y lo revoca este repo**, y es el kill-switch
  anti-clon: revocado, el Edge deja de despachar aunque tenga la DEK.

Dónde vive el lado servidor: `internal/gateway/lease/`. La firma es Ed25519, la clave privada
se lee de fichero (`WAPP_LEASE_PRIVATE_KEY_FILE`) o de base64
(`WAPP_LEASE_PRIVATE_KEY_B64`), y la pública se imprime al arrancar para configurar el Edge.
Revocación por `POST /admin/leases/revoke`, permiso `leases.revoke`.

🔴 **Homónimo peligroso: ver §3.1.** «DEK» en el código de este repo nombra **otra cosa**.

**Cómo se comprueba.** La revocación es *pegajosa* y hay tests de gateway que la vigilan
(`internal/gateway/lease/`). El invariante «la DEK del ADR-0007 no entra aquí» **no tiene
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
`internal/bootstrap/auth.go:18`, `internal/platform/httpapi/authmw.go:9`,
`internal/iam/usecase/grants.go:6` e `internal/iam/usecase/exchange.go:9`. No es un
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
  (`internal/prompts/volcar.go:19`). **Solo el prefijo `pN-` es contrato**: el resto del
  nombre es tuyo (`internal/prompts/prompts.go:205 etapaDeNombre`). Un fichero sin extensión
  `.tmpl` se ignora en silencio — es la forma de dejar notas al lado.
- **NO hay recarga en caliente, y es a propósito.** Cambiar texto sin reiniciar significaría
  que dos peticiones simultáneas corren prompts distintos.
- `go run ./cmd/prompts -comprobar <dir>` valida un directorio **sin arrancar el servidor**.
- 🔴 **Un directorio inválido ABORTA EL ARRANQUE.** No hay degradación a «sigo con el texto
  compilado»: `internal/prompts/prompts.go:113 ErrPromptsDir` lo declara y
  `internal/bootstrap/bootstrap.go:1592 cargarPlantillasDePrompt` lo propaga. El motivo está
  escrito allí mismo: *«un operador que editó un fichero y no ve el efecto es peor que un
  proceso que no arranca, porque el segundo se nota»*.
- El arranque **deja en el log de dónde salió cada plantilla** (`p4=/ruta/…` o
  `compilada`). Esa línea no es adorno: contesta sola «¿qué texto corrió?».

🔴 **La sub-regla que costó 14 jobs muertos: en el esquema NO puede haber un valor que su
propio validador rechace.** El modelo **copia el ejemplo**. P4 fue **0 de 14 en campo** por un
`"package_size": 0` impreso en su esquema, que `validarPaquete` rechazaba. Por eso
`llm.ValidarPlantilla` corre el `Parse*` de la etapa **sobre el ejemplo del esquema** y falla
si lo rechaza (`internal/prompts/prompts.go:193`).

**Candado.** `TestValidarPlantilla…` en el módulo `wapp-shared/llm` muta
`"package_size": 30` → `0` y exige el error; aquí, `internal/prompts/prompts_test.go`.

### I-CP-2 · 🔴 La inferencia la ORQUESTA este repo; el Edge solo la SIRVE

Es **lo contrario** de lo que decía el diseño original, y sigue habiendo documentación vieja
que dice lo otro. La doctrina vigente (ADR-0045, en el repo de documentación del ecosistema):

- **El Cloud** construye el prompt, trocea, valida la salida y decide. Todo eso vive aquí:
  `internal/intake/stages/`, `internal/prompts/`, `internal/llmvia/`.
- **El Edge** es un servidor de inferencia: *prompt entra → JSON sale*. **No interpreta nada.**
- La clasificación es **pull**, no push. El campo `intent` del proto está `reserved`: no
  existe el canal por el que el Edge empujaba una interpretación.

**Consecuencia medida que cambia lo que puedes construir:** `Signal.Intent` **es hoy siempre
`nil` en producción**, así que las reglas `flow_triggers kind='llm'` **NO PUEDEN DISPARAR**.
La rama sigue viva (`internal/flujos/trigger/config_resolver.go`, `KindLLM` en
`trigger.go:45`) y **sus tests están verdes**, midiendo código que producción no alcanza. Lo
declara el propio código en `internal/flujos/runtime/incoming.go:963`. No lo «arregles» de
paso: es un frente de producto con dueño. Ver `deuda.md`.

### I-CP-3 · 🔴 El switch por vía (`local` | `api`) vive en `llmvia` y en NINGÚN otro sitio

`internal/llmvia/llmvia.go:219` es el único `switch via`. Su único hermano legítimo es
`Selector.PlazaDe`, en el mismo paquete. Si te encuentras preguntando «¿qué vía usa este
tenant?» fuera de `internal/llmvia/`, lo que necesitas es **otro método en el puerto**.

**Candado.** `internal/llmvia/c2_via_test.go` — es un test **AST**, no de conducta.

### I-CP-4 · La plaza única: W=1 worker de pipeline, K=1 por Edge

El pipeline de captación corre con **un** worker (`bootstrap.go:1077`). Duplicar esa línea
**no daría ningún error**: dos workers reclamarían sin pisarse en la base y se bloquearían
mutuamente en la única plaza de inferencia del Edge. El aforo por Edge lo toma el worker antes
de la cadena (`internal/intake/pipeline/plaza.go`).

**Candado.** `internal/bootstrap/pipeline_captacion_cableado_test.go` — un test de cableado,
porque el invariante vive en una línea repetible.

### I-CP-5 · 🔴 Los permisos de PLATAFORMA acaban en `.any`

El listener admin `:8100` mezcla dos planos: rutas de tenant y rutas de **operador de
plataforma** (nosotros, no el cliente). Lo que las distingue es el sufijo **`.any`** del
permiso: `tenants.read.any`, `fleet.read.any`, `users.provision.any`… La migración `0060` se
lo concede solo a `platform_admin` y se lo **niega** al glob `*` de `tenant_admin` con un deny
`*.any`. Sin ese deny, cualquier admin de cliente alcanzaría el plano de plataforma.

**Candado.** `internal/bootstrap/platform_permissions_test.go` →
`TestINV056_1_PlatformPermissionsMustEndInDotAny`, con caso negativo.

⚠️ **Trampa de estilo que impone el candado**: el detector lee **texto fuente** y reconoce una
ruta de plataforma buscando la cadena `"platformadmin."` en el argumento de `adminHandler(...)`
(`bootstrap.go:1384`). Por eso esos handlers **deben** construirse inline y no pre-armarse en
`adminRouteDeps`. Un refactor «de limpieza» que los mueva a campos deja el candado **ciego sin
ponerse rojo**.

### I-CP-6 · El gate de features es FAIL-CLOSED

Los tres modos de no-resolución de `RequireFeature` devuelven **403**, nunca 500 ni un pase
libre (`internal/entitlements/middleware.go:39`). La resolución (feature efectiva de un
tenant): un override en `tenant_features` **gana**; sin override manda el plan; sin `plan_id`
se trata como `basic` (`COALESCE(plan_id,'basic')`, `internal/entitlements/postgres.go:151`).

Hay **11 constantes de feature en Go** (`internal/entitlements/entitlements.go:45-176`) y
**14 sembradas** en las migraciones. Las tres de más (`stt_audio`, `owner_app` y
`passive_profiles`) **no gatean nada**. Ver `deuda.md`.

### I-CP-7 · El esquema es FULL-REPLAY: todo el DDL tiene que ser idempotente

El runner (`internal/platform/storage/postgres/migrations/migrate.go`) **reejecuta TODOS** los
`.sql` cuando cambia `SchemaVersion` **o** el hash del contenido. No hay «ya aplicada».

⇒ **Toda migración que escribas debe poder correr N veces.** `CREATE TABLE IF NOT EXISTS`,
`ADD COLUMN IF NOT EXISTS`, `INSERT … ON CONFLICT DO NOTHING`.

⚠️ Trampa conocida del mecanismo: `CREATE TABLE IF NOT EXISTS` **no repone una columna que una
migración posterior borró** — el `COMMENT ON COLUMN` que va debajo mata el segundo arranque.

Se serializa con `pg_advisory_lock` sobre **una conexión dedicada** (`migrate.go:61`) y se
ordena **por nombre** (`:142`). ⚠️ Contra un pooler en modo transacción el lock se puede
repartir entre sesiones: apunta siempre al **host directo**.

### I-CP-8 · Identificadores en inglés, nombre de negocio en la UI (INV-09)

Las claves que viajan por el wire y viven en la BD van en inglés (`pending_approval`,
`needs_info`); el nombre bonito vive en la UI y en la documentación, **nunca** en la base.
Ejemplo canónico: `internal/intakes/status.go:13`.

Los **comentarios y los nombres de función internos van en español**, que es la convención de
este repo. No la «corrijas».

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
`conversation_event_messages` se escribe cifrada y **no está en el censo**. Ver `deuda.md`.

**Candado.** `internal/platform/crypto/rekey_integration_test.go` rota sobre las entradas
**declaradas** — es un test *de la lista*, no *de la ausencia*. No atrapa una tabla omitida.

---

## 3 · Homónimos: las tres palabras que hacen concluir cosas falsas

### 3.1 · 🔴 «DEK» aquí NO es la DEK del ecosistema

| | DEK del ecosistema (doble llave) | `DEK` en el código de este repo |
|---|---|---|
| Qué descifra | el store de `whatsmeow` en el Edge | un campo de **PII de negocio** en PostgreSQL |
| Quién la custodia | **el cliente**, jamás cruza a la nube | **esta pieza**, envuelta por su KEK |
| Dónde se ve | no existe aquí | `internal/intakes/buyerdata.go:11`, columnas `*_dek` |

Confundirlas lleva a concluir que **el zero-knowledge está roto cuando no lo está**. Que en
`intake_buyer_data` haya una columna `data_dek` es exactamente lo que el diseño quiere: el
contenido de negocio sube a la nube y se cifra allí.

### 3.2 · «admin» nombra dos planos distintos

`/admin/*` en el listener `:8100` contiene **rutas de tenant** (enviar un mensaje, crear un
flujo) **y rutas de operador de plataforma** (crear tenants, aprobar altas). Lo único que las
separa es el sufijo `.any` del permiso (I-CP-5). «Es una ruta admin» no dice quién puede
llamarla.

### 3.3 · «P1» no vive aquí

El pipeline de este repo es **P2→P5**. `wapp-shared/llm` define `EtapaP2`…`EtapaP5` y **no
existe `EtapaP1`**. El prompt del clasificador de intenciones lo gobierna el **catálogo de
intenciones**, que se edita **por API** (`PUT /api/v1/intents`, `internal/intentcfg/`). Quien
venga a «tocar el prompt del clasificador» y abra `internal/prompts/` está en el sitio
equivocado.

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
  `internal/platform/storage/postgres/connect.go:30`).
- **gRPC** `google.golang.org/grpc v1.82.1` + `protobuf v1.36.11`.
- **Contrato edge↔cloud**: `github.com/EduGoGroup/wapp-cloudlink v0.17.0` (paquete
  `cloudlinkv1`).
- **`wapp-shared`**: `llm v0.4.5` (etapas, `Parse*`, plantillas), `auth v0.5.0`,
  `config v0.3.0`, `envelope v0.2.1`, `health v0.1.1`, `intents v0.1.0`, `logger v0.2.0`,
  `textmatch v0.1.0`.
- **`identity-shared/auth v0.3.1`** — la excepción de I-ECO-4.
- **S3/R2**: `aws-sdk-go-v2/service/s3 v1.98.0`, usado **solo** para URLs prefirmadas.
- **KMS**: `cloud.google.com/go/kms v1.33.0`. Construido pero **apagado**: el default de
  `WAPP_KEK_PROVIDER` es `env` (`internal/platform/config/config.go:654`).
- **Prometheus** `client_golang v1.23.2`; **excelize v2.11.0** (import de catálogo xlsx y
  export de bandeja); **`golang.org/x/time`** (token buckets); **testify v1.11.1**;
  **`jsonschema/v6`** solo en tests.
- **Frontend: NO HAY.** Cero plantillas Go, cero JS. API JSON pura + gRPC.

---

## 5 · Convenciones de código

- **Estructura modular por CAPACIDAD**, no hexagonal global. Cada módulo de `internal/` tiene
  su frontera, sus tablas y su API. Trabaja **dentro del módulo**; no crees capas
  transversales nuevas. La **única** zona hexagonal (`domain/usecase/ports/infra/transport`)
  es `internal/iam/`, y es deliberado.
- **Comentario-como-ADR**: el 46,8 % de la producción es comentario. Cuando cambies una
  decisión, **actualiza el comentario que la narraba**; un comentario que describe un bloqueo
  ya levantado manda a investigar a quien lo lea.
- **Lint**: `.golangci.yml` habilita **16 linters** (`linters.enable`, `:9-37`) más **2
  formateadores** (`formatters.enable`, `:69-71`: `gofmt` y `goimports`), que en la v2 del
  fichero **ya no se declaran como linters**. Entre los 16, `errcheck` con
  `check-type-assertions: true` **y `check-blank: true`**, `gosec`, `contextcheck`, `nilerr`,
  `errorlint`, `gocyclo` (min-complexity 15) y `revive`. Solo hay **13 `//nolint`** en toda la
  producción, **todos con justificación escrita**. Escribe la tuya o no lo pongas.
- **Tests de cableado (AST)**: hay **8** `*_cableado_test.go` en `internal/bootstrap/`, y son
  los 8 del repo entero (`find . -name '*_cableado_test.go' | wc -l` → 8).
  Es la herramienta de este repo para vigilar invariantes que viven en una línea repetible.
  Úsala antes de inventar otra.
- **Nada de `TODO`/`FIXME`.** La deuda se marca con `DEUDA-NNN.N`, 🔴, ⚠️ y 🟡, y se anota
  en `deuda.md`. Un `grep` por `TODO` en producción devuelve **cero** (las 249 apariciones a
  grep pelado son la palabra española *todo*).

---

## 6 · Trampas conocidas (lo que un agente hace mal aquí si nadie se lo dice)

1. **Creer que el README dice la verdad.** El `README.md` y el `CLAUDE.md` viejos afirmaban
   esquema `0.47.0` (es **0.48.0**), «dos binarios» (son **cuatro**), «5 planes» (son **6**),
   «se despliega contra Neon» (**UAT ya no usa Neon**: es un Postgres 17 en Docker en el
   propio VPS) y describían **10** paquetes de `internal/` de los **29** que hay. Verifica
   contra el código.
2. **Buscar las rutas en un solo fichero.** Están en tres (ver `contratos.md`). Un inventario
   que solo mire `internal/publicapi/` se deja **7 rutas**, incluidas `POST /api/v1/signup` y
   el canje de invitación.
3. **Añadir una ruta sin mirar si su dependencia puede ser `nil`.** Muchas rutas son
   **condicionales**: si su campo en `publicapi.Deps` es `nil`, la ruta **no se monta** y
   responde un 404 de ruta inexistente (`internal/publicapi/roleplane.go:75`). La única
   excepción deliberada es `POST /api/v1/members`, que se monta siempre y degrada a **503**.
4. **Tocar el `WriteTimeout` global creyendo que gobierna todo.** `POST
   /api/v1/intakes/{id}/quote-suggestion` es **la única ruta con plazo de escritura propio**
   (`conPlazoDeRedacción`, `internal/publicapi/publicapi.go:770`): espera al modelo **dentro**
   de la petición (24,8–35,5 s medidos en UAT) contra un `WriteTimeout` global de 10 s.
5. **Suponer que `/metrics` es inocuo.** El scrape **barre de paso** las rachas de
   auto-respuesta vencidas y las manda al histograma (`bootstrap.go:790`). Si nadie raspa
   `/metrics`, esos episodios **no se cierran nunca**.
6. **Esperar ver una métrica recién desplegada.** Un `CounterVec` de Prometheus **no aparece
   en `/metrics` hasta su primer incremento**, y el reinicio lo borra. Su ausencia tras
   desplegar no es un fallo.
7. **Cambiar `WAPP_STORAGE_S3_PREFIX` creyendo que mueve algo.** Está en `.env.example` y en
   los runbooks, y **no la lee nadie**: el prefijo es la constante compilada
   `mediaKeyPrefix = "wapp/media"` (`internal/publicapi/media.go:28`). Ver `deuda.md`.
8. **Desplegar sin R2/S3 vivo.** `NewR2PresignClient` valida el bucket con `HeadBucket` y si
   falla **el proceso no levanta** (`internal/publicapi/flows.go:75`). Es fail-fast a
   propósito, pero acopla el arranque de IAM y del gateway a un almacén que solo usa el nodo
   `media`.
9. **Abrir un PR esperando validación.** `ci.yml` es `workflow_dispatch`: **un PR no valida
   nada**. Ver `operacion.md`.
10. **Leer un `rc=0` de `go test` como «todo pasó».** Un `--- SKIP` cuenta igual que un
    `--- PASS`. Los **97** ficheros `*_integration_test.go` (**352 funciones**, **11,3 %** de la suite)
    **se saltan solos** sin `WAPP_TEST_DB_DSN`. **Cuenta los SKIP.** La regla del porcentaje:
    `grep -rh '^func Test' --include='*_integration_test.go' . | wc -l` → **352**, sobre
    `grep -rh '^func Test' --include='*_test.go' . | wc -l` → **3.116**.
11. **Migrar contra un pooler.** Ver I-CP-7.
12. **Tocar `documentations/literal-aviso-sesion-pasiva.md`.** Es un contrato congelado: el
    literal exacto de un aviso que viaja al usuario final. No lo edites de paso ni lo
    dupliques.
