# Deuda viva de `wapp-cloud-platform`

> Medida el **2026-08-30** leyendo el código. Cada entrada trae `fichero:línea`, la
> **consecuencia** (qué pasa si nadie la toca) y **cómo se cerraría**.
> Lo que no verifiqué va marcado **NO VERIFICADO**.

**Este repo no marca deuda con `TODO`.** Un `grep -rnE '// TODO\b|FIXME|HACK\b|XXX'` sobre
producción devuelve **cero** (las apariciones a grep pelado son la palabra española *todo*). La
deuda se marca con `DEUDA-NNN.N`, 🔴, ⚠️ y 🟡 en el propio comentario.

---

## 1 · Seguridad y datos — lo primero de la lista

### D-1 · 🔴 `conversation_event_messages` está cifrada y FUERA del censo de rotación

- **Dónde**: la tabla se escribe cifrada en `internal/flujos/events/store.go:811`
  (`body_enc, body_dek, body_kek_id`); el censo es `rekeyTargets` en
  `internal/platform/crypto/rekey.go`, con **8 sobres en 7 tablas**, y esta **no está**. Su
  única aparición en ese fichero es un comentario (`rekey.go:226`).
- **Consecuencia**: una rotación de KEK se declararía **completa** dejando esas filas
  **ilegibles para siempre** al retirar la KEK vieja. Y no es un búfer en vuelo: es la **copia
  canónica** del literal del cliente, la que sostiene auditar y regenerar. El propio
  `rekey.go:48` describe ese daño… para otras tablas.
- **El candado no cubre esto**: `internal/platform/crypto/rekey_integration_test.go` rota sobre
  las entradas **declaradas** — es un test *de la lista*, no *de la ausencia*. Y se cuenta mal
  a sí mismo: su cabecera (`:18`) dice «los SIETE sobres en SEIS tablas» cuando hoy son 8 en 7.
- **Cómo se cierra**: (a) añadir la entrada al censo; (b) escribir el aserto que faltaba —
  *«¿qué tablas tienen una columna `*_kek_id` y no están en `rekeyTargets`?»*, derivado del
  catálogo de columnas, **con guarda anti-cero** para que no salga verde midiendo cero tablas;
  (c) recontar la cabecera del test.

### D-2 · 🔴 `intake_jobs.artifacts` guarda literal del cliente EN CLARO

- **Dónde**: lo dice la propia migración —
  `internal/platform/storage/postgres/migrations/structure/0083_intake_jobs_retencion.sql:80`
  («`artifacts` guarda literal del cliente (punto 2b), EN CLARO») y el `COMMENT ON TABLE` de
  `:102`.
- **Consecuencia**: las `evidence` de P2 son **subcadenas del mensaje del cliente por
  contrato**. Están en claro, **fuera del censo de rotación** y **sin vaciado en estado
  terminal**, con retención indefinida y declarada. El desvío **no caduca solo**.
- **Estado**: **DECIDIDO, no cerrado.** La salida elegida es podar **solo la `evidence`**,
  conservando la parte estructurada, para que redelivery y auditoría sigan funcionando. Falta
  ejecutarlo: mecanismo, test sobre filas reales, **mutación en rojo**, `COMMENT ON COLUMN` y
  la nota en el inventario de minimización.
- **Residuo a limpiar de paso**: el `COMMENT` de `0072:741` **afirma lo contrario** y sigue en
  el árbol.

### D-3 · ⚠️ El KMS está construido y APAGADO — es una decisión, no un olvido

- **Dónde**: `internal/platform/crypto/kms_gcp.go` y `keyprovider_kms.go` existen; el default
  de `WAPP_KEK_PROVIDER` es **`env`** (`internal/platform/config/config.go:654`).
- **Consecuencia**: la KEK vive en una variable de entorno, no en un KMS. Es el estado
  esperado hoy (coste cero); encenderlo es un **gate de negocio** con dueño, no un cambio de
  fichero. Se anota para que nadie lo lea como un descuido ni lo «arregle» de paso.

### D-4 · 🔴 En UAT los cuatro listeners bindean a `*` y el admin queda expuesto

- **Dónde**: no es código roto, es configuración: `WAPP_HTTP_ADDR=:8100` (y las otras tres) van
  **sin host**, así que el proceso escucha en todas las interfaces. En el VPS de UAT, con el
  cortafuegos apagado, **el listener admin `:8100` y el `5432` de Postgres responden desde
  Internet** (verificado conectando desde fuera el 2026-08-29).
- **Consecuencia**: `:8100` es el plano de operadores. `/metrics` y `/healthz` no piden auth.
- **Cómo se cierra**: fuera del código — cortafuegos y `WAPP_HTTP_ADDR=127.0.0.1:8100`. Se
  anota aquí porque el default de la variable **invita** al fallo.

---

## 2 · Ramas vivas que no puede alcanzar nadie

### D-5 · 🔴 Las reglas `flow_triggers kind='llm'` NO PUEDEN DISPARAR

- **Dónde**: `internal/flujos/runtime/incoming.go:963` lo declara con todas las letras;
  la rama sobrevive en `internal/flujos/trigger/config_resolver.go:52` y `KindLLM` en
  `trigger.go:45`.
- **Por qué**: `Signal.Intent` **es hoy siempre `nil` en producción**. La clasificación pasó de
  *push* a *pull* (I-CP-2) y el campo del proto quedó `reserved`: no hay de dónde leerla.
- **Consecuencia**: hay **símbolo, rama y tests verdes** sobre algo que producción no alcanza.
  Un lector concluye que la funcionalidad existe. **Nada vigila** que esa rama tenga productor.
- **Cómo se cierra**: no aquí. Es un frente de producto con dueño (el sustituto natural —pedir
  la clasificación en el turno— **no cabe**: una decisión de arranque se toma en el turno y una
  inferencia tarda segundos, p50 medido 8,1 s). Lo que sí procede: un test que exija
  *«`KindLLM` tiene productor de producción, o el símbolo se retira»*.

### D-6 · 🟡 `internal/intake/anclaje` no tiene llamante de producción

- **Dónde**: el paquete se importa en `internal/intake/pipeline/pipeline.go:76` y
  `internal/intake/stages/draft.go:17`, pero `Repartir` **no se llama en ningún sitio de
  producción** (`grep -rn '\.Repartir(' --include='*.go' . | grep -v _test` → vacío).
- **Consecuencia**: el borrador sale **sin adjuntos colgados de su línea**. Es un hueco
  **nombrado** (`pipeline.go:38`), no un olvido: cablearlo pide un lector que hoy no existe
  (`events.ThreadEntry` no trae ni los *media refs* ni el instante de cada turno, que son las
  dos entradas de `Repartir`).
- **Cómo se cierra**: ampliar `ThreadEntry` con esos dos datos y cablear la llamada en `draft`.

### D-7 · 🟡 `internal/flujos/admin.Register` solo lo llaman tests

- **Dónde**: `internal/flujos/admin/handlers.go:344`. Sus cuatro llamantes están todos en
  `_test.go`. Las dos rutas que monta (`/admin/flows`, `/admin/flows/start`) se registran
  **inline** en `internal/bootstrap/bootstrap.go:1462` y `:1464`.
- **Consecuencia**: **código muerto en producción** que aparece en cualquier inventario de
  rutas y hace contar dos veces. Su comentario dice «lo invoca `cmd/server/main.go`», y **eso
  ya no es cierto**.
- **Cómo se cierra**: borrar `Register` y adaptar los cuatro tests, o —si se quiere conservar—
  corregir el comentario y decir que es un helper de test.

### D-8 · 🟡 Dos features comerciales sin un solo consumidor

- **Dónde**: `stt_audio` y `owner_app` se siembran en `0039_seed_plan_taxonomy.sql` (plan
  `advisor_ai_pro`); `grep -rn '"stt_audio"\|"owner_app"' --include='*.go' internal/` **sin
  tests** devuelve **cero**.
- **Consecuencia**: un tenant puede tener la feature encendida y **no significa nada**.
- **Asimetría de trato**: `passive_profiles` está en el mismo caso pero **sí está documentado**
  como no-gateante (`internal/bootstrap/auth.go:527`, `internal/flujos/admin/sessions.go:111`,
  `internal/filtercfg/filtercfg.go:20`). La diferencia entre las tres es arbitraria.
- **Cómo se cierra**: documentar las tres igual, o retirar las dos que no gatean.

---

## 3 · Fronteras rotas y concentración de riesgo

### D-9 · 🔴 14 tablas compartidas entre módulos, y `gateway` ESCRIBE en `public.tenants`

- **Dónde**: `internal/gateway/lease/repository_postgres.go:106`
  (`UPDATE public.tenants SET revoked_at = now() …`) y `:117` (el `… = NULL`), cuando la tabla
  la crea y la sirve `platform` (`internal/platform/storage/postgres/tenant.go:48`).
- **Consecuencia**: el kill-switch comercial escribe en la tabla de identidad de otro módulo,
  **sin API interna de por medio**. Contra la disciplina «una tabla, un módulo». La lista
  completa de las 14 está en [`arquitectura.md`](arquitectura.md) §4.
- **Cómo se cierra**: (a) mover esos dos `UPDATE` detrás de un puerto de `platform`; (b) el
  candado — un test-AST «¿qué módulo toca qué tabla?». **La herramienta ya existe y se usa
  para otras cosas** (hay **8** `*_cableado_test.go` en `internal/bootstrap/`): nadie la apuntó
  aquí.

### D-10 · 🔴 `bootstrap.Run` son 991 líneas

- **Dónde**: `internal/bootstrap/bootstrap.go:106` → cierra en `:1096`. El fichero son 1.606
  líneas, **933 de ellas (58 %) comentario**.
- **Consecuencia**: una sola función construye cifrado, R2, motor, gateway, IAM, pipeline LLM,
  cinco goroutines y cuatro listeners. **Es imposible de revisar por partes** y cualquier
  reordenación de dependencias es un cambio de riesgo alto.
- **Cómo se cierra**: extraer por dominio a `construirX(deps) (X, error)` **sin cambiar el
  orden**, y dejar `Run` como la secuencia de llamadas. Los tests de cableado existentes son la
  red que hace esto viable.

### D-11 · 🟡 Cinco goroutines de fondo sin supervisión ni reinicio

- **Dónde**: `bootstrap.go:1033` (webhook), `:1045` (flowlifecycle), `:1060` (agregador),
  `:1070` (intakeAhead), `:1086` (pipeline) — todas `go X.Run(ctx)` a pelo.
- **Consecuencia, en palabras del propio código**: «sin este `Run`… el sistema seguiría
  funcionando y nadie vería un error; simplemente no habría adelanto nunca» (`:1065`) y «las
  ventanas cerrarían, los jobs quedarían `pending` y nadie los reclamaría nunca. **Ni un error
  en el log**» (`:1083`). Los cinco fallos son **mudos**.
- **La red actual** es un test de **cableado** (`pipeline_captacion_cableado_test.go`), que
  comprueba que se lanzan, no que sigan vivas.
- **Cómo se cierra**: un supervisor que relance con backoff y **emita una métrica de
  reinicio**, o como mínimo un `defer` que registre la muerte con su causa.

### D-12 · 🟡 `W = 1` es un invariante que vive en una línea repetible

- **Dónde**: `bootstrap.go:1077`. El comentario lo dice: «duplicar esta línea no daría ningún
  error: dos workers reclamarían sin pisarse… y se bloquearían el uno al otro en la única plaza
  del Edge».
- **Estado**: vigilado por `TestPipelineCaptacionCableado`, que **es lo correcto**. Se anota
  porque el candado es lo único que separa el diseño del fallo.

### D-13 · 🟡 El candado de rutas de plataforma lee TEXTO FUENTE

- **Dónde**: `bootstrap.go:1384` — `TestINV056_1_PlatformPermissionsMustEndInDotAny` detecta
  una ruta de plataforma buscando la cadena `"platformadmin."` en el argumento de
  `adminHandler(...)`.
- **Consecuencia**: impone un estilo de escritura que **solo el comentario explica**: esos
  handlers deben construirse inline. Un refactor «de limpieza» que los mueva a campos de
  `adminRouteDeps` deja el detector **ciego sin ponerse rojo**.
- **Cómo se cierra**: derivar la clasificación del **permiso** (`.any`) en vez del texto del
  constructor.

### D-14 · 🟡 El arranque entero depende de R2/S3 vivo

- **Dónde**: `internal/publicapi/flows.go:75` — `NewR2PresignClient` valida el bucket con
  `HeadBucket` y **si falla el proceso no levanta**.
- **Consecuencia**: fail-fast a propósito, pero acopla el arranque de IAM, del gateway y del
  pipeline a un almacén que **solo usa el nodo `media`**.
- **Cómo se cierra**: degradar a «media apagado» con un aviso ruidoso, en vez de tumbar todo.

---

## 4 · Modos de fallo mudos y calidad de código

### D-15 · 🔴 No hay forma de medir el fallback de P5

- **Dónde**: `internal/intakes/quotetext/quotetext.go:137-160`, con la deuda escrita en el
  propio fichero.
- **Consecuencia**: cuando el texto del modelo se descarta, el motivo sale por un `log.Warn` y
  por `fallback_reason` en la respuesta HTTP — **y nada más**. Cero métrica, cero `flow_event`.
  Nadie puede responder «¿qué porcentaje de sugerencias las escribe de verdad el modelo?» ni
  «¿cuál de los nueve motivos manda?». Modo de fallo **mudo declarado a propósito**, porque la
  lista de cinco eventos de telemetría está **cerrada** por diseño.
- **Cómo se cierra**: un contador con etiqueta `motivo`, o un sexto evento — **decisión de la
  tarea dueña de la telemetría**, no de quien pase por aquí.

### D-16 · 🔴 `writeJSON` duplicado cinco veces, y cuatro con un `if` muerto

- **Dónde**: `internal/platform/httpapi/authmw.go:196`,
  `internal/flujos/admin/handlers.go:351`, `internal/iam/transport/http/http.go:50`,
  `internal/platformadmin/handlers.go:282`, `internal/publicapi/publicapi.go:1149`.
- **Consecuencia**: cuatro de las cinco terminan en
  `if _, werr := w.Write(body); werr != nil { return }` — un `if` **muerto**: el `return` es la
  última instrucción de todos modos, así que **el error de escritura se traga** y el compilador
  no puede avisar. La quinta ya aprendió la lección y tiene un `writeJSONErr` hermano
  documentado con el incidente (`publicapi.go:1156`), **pero ese aprendizaje no se propagó**.
- **Cómo se cierra**: un único helper en `platform/httpapi` con la variante que devuelve error,
  y las cinco copias apuntando ahí.

### D-17 · 🔴 El ritual `if cerr := rows.Close(); cerr != nil { _ = cerr }`

- **Cuántos**: **41 ocurrencias en 24 ficheros de producción** (medido con
  `grep -rn "cerr := rows.Close(); cerr != nil" --include='*.go' internal/ | grep -v _test`).
  Los más cargados: `internal/intakes/postgres.go` (5),
  `internal/flujos/store/repository_postgres.go` (4),
  `internal/iam/infra/postgres/{roles,memberships}.go` (3 cada uno),
  `internal/platformadmin/postgres.go` (3).
- **Consecuencia**: es un **no-op que satisface a `errcheck`** sin manejar nada. Si un
  `rows.Close()` empieza a fallar, **no hay una sola línea de log en ninguno de los 41 sitios**.
- **Cómo se cierra**: un helper `cerrarFilas(rows, log)` que registre, y sustituir las 41.

### D-18 · 🟡 Las cachés de entitlements no desalojan nunca

- **Dónde**: `internal/entitlements/postgres.go` (243 líneas): `p.cache[k] = …` (`:110`) y
  `p.effective[tenantID] = …` (`:136`), y **ningún `delete(` en todo el fichero**.
- **Consecuencia**: las entradas caducan **lógicamente** pero nunca se borran; los mapas crecen
  monótonamente con cada par (tenant, feature) visto. Hoy es acotado (14 features × N tenants).
- **Por qué es un olvido y no una decisión**: el limitador hermano **sí** tiene desalojo
  (`internal/platform/ratelimit/ratelimit.go:72 evictStaleLocked`). La asimetría no está
  justificada en ningún comentario.

### D-19 · 🟡 `internal/contracts` es un paquete Go que solo contiene un test

- **Dónde**: `ls internal/contracts/` → únicamente `contract_examples_test.go`.
- **Consecuencia**: deliberado y documentado, pero cualquier herramienta que mida cobertura o
  inventaríe paquetes lo verá raro. Se anota para que nadie lo «arregle» borrándolo: valida los
  ejemplos del contrato `wapp-crm-v1` contra su JSON Schema.

### D-20 · 🟡 El prompt del import de catálogo nunca se probó contra un LLM real

- **Dónde**: `internal/catalogimport/prompt.go:41`, con la deuda escrita y fechada
  (decidida el 2026-08-06).
- **Consecuencia**: «lo que hay aquí es el texto del design, no un texto verificado»: trátalo
  como **una hipótesis con formato de instrucción**.

---

## 5 · Deudas con nombre heredadas de los planes

| Marca | Dónde | Qué significa |
|---|---|---|
| **DEUDA-044.10** | `internal/intake/pipeline/pipeline.go:56` | Un aviso colgado del **desenlace feliz** de una operación que, en el caso que importa, **fracasa**: la inferencia fría muere por timeout sin emitir régimen, y el fallo borraba su propia evidencia. El worker ya corrige el patrón; la marca se conserva como recordatorio |
| **DEUDA-044.11** (sin dueño) | `internal/bootstrap/bootstrap.go:1197`, `internal/intake/stages/p4.go:144` | Un plazo que hay que escribir explícitamente en vez de heredarlo |
| **DEUDA-044.16** | `internal/intake/stages/match.go:42` y `:265`, `match_lineas.go:66`, `draft.go:259` | Un ítem malo **no** tira el borrador: se degrada y se anota como *warning* |
| **DEUDA-050.1** (cerrada con red) | `internal/gateway/grpc/connect.go:917-940` | Carrera de la reconexión rápida (`MarkOffline` diferido, `MarkOnline` inmediato). Mitigada preguntando «¿sigue caída?» **al ejecutar** el job, no al encolarlo. El comentario declara qué **no** cubre la red |
| **DEUDA-050.2** | `internal/platform/metrics/metrics.go:483` | El cuello mudado del head-of-line al pool; `wapp_db_wait_count` existe para decidirlo |

---

## 6 · Deuda documental (dentro de este repo)

| Qué | Dónde | Estado |
|---|---|---|
| `README.md` y el `CLAUDE.md` viejo decían esquema **0.47.0** | el real es **0.48.0** (`version.go:435`) | Corregido en `documentations/`; el `README.md` de la raíz del repo **sigue sin actualizar** |
| «Binarios: `cmd/server` y `cmd/migrate`» | son **cuatro** (faltan `cmd/prompts` y `cmd/casebank`) | ídem |
| «5 planes × 14 features» | son **6 planes** (falta `advisor_ai_local`, migración `0074`) | ídem |
| «Se despliega hoy contra **Neon**» | 🔴 **UAT ya no usa Neon**: Postgres 17 en Docker en el VPS, y **MinIO** en vez de R2 | ídem |
| El inventario de módulos describe **10** paquetes de `internal/` | hay **29** | ídem |
| `CHANGELOG.md:6` — `[Unreleased]` **vacío** | sobre 76 ficheros y +9.575/−271 líneas desde `v0.2.0`, con 7 commits `feat(` y dos migraciones nuevas | **Abierto** |
| `README.md:57` cita `publicapi.go:270` para `GET /api/v1/entitlements` | esa línea es hoy un comentario; la ruta se registra en `publicapi.go:559` | **Abierto** |
| `docs/runbooks/configurar-r2.md:16` dice «sin MinIO local» | UAT corre MinIO | **Abierto** |
| `.env.example` declara 47 claves; `Load()` lee **70** | y declara `WAPP_STORAGE_S3_PREFIX`, que no lee nadie | **Abierto** |

### D-21 · 🔴 `WAPP_STORAGE_S3_PREFIX` es configuración MUERTA

- **Dónde**: declarada en `.env.example:272` y prometida en `README.md:66` y en
  `docs/runbooks/configurar-r2.md:14`. **Pero `config.StorageConfig` no tiene campo `Prefix`**
  ni lo lee `Load()`, ni existe en `objectstore.R2Config` (`r2_factory.go:18`).
- **Consecuencia**: el aislamiento del bucket existe, pero **está compilado**:
  `const mediaKeyPrefix = "wapp/media"` en `internal/publicapi/media.go:28`. Quien cambie esa
  variable creyendo que mueve el prefijo **no moverá nada, y no habrá error**.
- **Cómo se cierra**: o se cablea de verdad, o se borra de `.env.example` y de los runbooks.

### D-22 · 🟡 Faltan los números de migración `0020` y `0021`, y nadie sabe por qué

- **Dónde**: `internal/platform/storage/postgres/migrations/structure/` — 84 ficheros
  numerados `0001`…`0086`, sin `0020` ni `0021`. No aparecen borrados en el historial de git.
  La única mención de pasada está en `0060_platform_console_grants.sql:64`.
- **Consecuencia**: un hueco sin explicar invita a reutilizar el número, que en un runner
  ordenado por nombre metería DDL en medio de una secuencia ya aplicada.
- **Cómo se cierra**: una línea en el comentario de `version.go` diciendo que nunca existieron.

### D-23 · 🟡 Tres tablas se crean y se borran en cada replay

- **Dónde**: `0014_iam_users.sql` y `0018_iam_api_keys.sql` crean `iam_users`,
  `iam_refresh_tokens` e `iam_api_keys`; `0038_retiro_iam_propio.sql:59` las dropea.
- **Consecuencia**: cada arranque recrea y destruye tres tablas muertas. Es **coherente** con
  append-only + full-replay (las migraciones no se editan hacia atrás), pero cuesta tiempo de
  arranque y confunde a quien lea el DDL.
- **Cómo se cierra**: no se cierra sin romper la regla. Se documenta, y ya está documentado
  aquí.

---

## 7 · Lo que está BIEN y conviene no romper

- **Cero secretos versionados.** `git ls-files` solo devuelve `.env.example`, con placeholders.
  `.env`, `.env.neon`, `certs/`, los binarios y `cloud.log` están en el árbol pero **ignorados**.
- **HMAC en tiempo constante** en el callback CRM (`internal/integrations/sigv1/sigv1.go:39`).
- **Fail-closed** en el gate de features: los tres modos de no-resolución dan **403**, no 500
  (`internal/entitlements/middleware.go:39`).
- La caché de entitlements **no sostiene el mutex** durante la consulta a BD
  (`internal/entitlements/postgres.go:98`).
- **`WAPP_TEST_REQUIRE_DB`** hace **ruidoso** el skip de integración: es la red exacta contra el
  falso verde descrito en `operacion.md` §1.2.
- Solo **13 `//nolint`** en toda la producción, **todos con justificación escrita**.
