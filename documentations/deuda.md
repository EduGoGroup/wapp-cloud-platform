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
  **inline** en `internal/bootstrap/arranque/rutas_admin.go:101` y `:103`.
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
  como no-gateante (`internal/bootstrap/arranque/auth.go:527`, `internal/flujos/admin/sessions.go:111`,
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
  para otras cosas** (hay **9** tests de cableado en `internal/bootstrap/arranque/` —8
  `*_cableado_test.go` más `flow_options_cableadas_test.go`—, con copia en `internal/arranque/`
  desde F0): nadie la apuntó
  aquí.

### ~~D-10 · `bootstrap.Run` son 991 líneas~~ — ✅ CERRADA el 2026-09-04

- **Qué era**: `internal/bootstrap/bootstrap.go:106` → `:1096`, una sola función que construía
  cifrado, R2, motor, gateway, IAM, pipeline LLM, cinco goroutines y cuatro listeners. Imposible
  de revisar por partes, y con `gocyclo` **en su techo exacto** (15 de 15) — hasta el punto de
  que `nuevoStackLLMDeCaptacion` existía por escrito para absorber un `if err != nil` y no
  romper el lint del llamante.
- **Cómo se cerró**: exactamente como decía esta ficha —extraer **sin cambiar el orden** y dejar
  la secuencia de llamadas—. El composition root pasó a `internal/bootstrap/arranque`, con nueve
  fases que comparten un contenedor de campos **privados** (por eso es un subpaquete y no
  nueve), y `internal/bootstrap` quedó como fachada de los dos símbolos públicos de siempre.
  Ningún objeto, ningún cable y ningún orden cambió; tampoco cuál es el primer error que ve
  quien depura un arranque caído.
- **Lo que se ganó de propina**: cada fase declara `requiere()` y el orquestador lo comprueba
  antes de ejecutarla —el orden dejó de ser una promesa escrita en comentarios—, y el log dice
  ahora por qué fase iba (`arranque: fase 1/9 "infraestructura": …`).
- 🔴 **La lección, que es la parte cara**: los tests de cableado parseaban
  `parser.ParseFile("bootstrap.go")` **por nombre de fichero**, y el traslado tumbó nueve de
  golpe sin que un solo invariante hubiera cambiado. Ahora recorren el PAQUETE
  (`arranque/astpaquete_test.go`). El modo de fallo que eso cierra no es el rojo ruidoso que
  dieron: es el silencioso —mover la línea vigilada a otro fichero DEJANDO `bootstrap.go` en su
  sitio los habría dejado en **verde mirando un fichero donde ya no está lo que buscan**.

### D-11 · 🟡 Cinco goroutines de fondo sin supervisión ni reinicio

- **Dónde**: `arranque/fase9_fondo.go:54` (webhook), `:63` (flowlifecycle), `:75` (agregador),
  `:84` (intakeAhead), `:99` (pipeline) — todas `go X.Run(ctx)` a pelo.
- **Consecuencia, en palabras del propio código**: «sin este `Run`… el sistema seguiría
  funcionando y nadie vería un error; simplemente no habría adelanto nunca» (`:80`) y «las
  ventanas cerrarían, los jobs quedarían `pending` y nadie los reclamaría nunca. **Ni un error
  en el log**» (`:95`). Los cinco fallos son **mudos**.
- **La red actual** es un test de **cableado** (`pipeline_captacion_cableado_test.go`), que
  comprueba que se lanzan, no que sigan vivas.
- **Cómo se cierra**: un supervisor que relance con backoff y **emita una métrica de
  reinicio**, o como mínimo un `defer` que registre la muerte con su causa.

### D-12 · 🟡 `W = 1` es un invariante que vive en una línea repetible

- **Dónde**: `arranque/fase9_fondo.go:99`. El comentario lo dice: «duplicar esta línea no daría ningún
  error: dos workers reclamarían sin pisarse… y se bloquearían el uno al otro en la única plaza
  del Edge».
- **Estado**: vigilado por `TestPipelineCaptacionCableado`, que **es lo correcto**. Se anota
  porque el candado es lo único que separa el diseño del fallo.

### D-13 · 🟡 El candado de rutas de plataforma lee TEXTO FUENTE

- **Dónde**: `arranque/rutas_admin.go:29` — `TestINV056_1_PlatformPermissionsMustEndInDotAny` detecta
  una ruta de plataforma buscando la cadena `"platformadmin."` en el argumento de
  `adminHandler(...)`.
- **Consecuencia**: impone un estilo de escritura que **solo el comentario explica**: esos
  handlers deben construirse inline. Un refactor «de limpieza» que los mueva a campos de
  `adminRouteDeps` deja el detector **ciego sin ponerse rojo**.
- **Cómo se cierra**: derivar la clasificación del **permiso** (`.any`) en vez del texto del
  constructor.

### D-14 · 🟡 El arranque entero depende de R2/S3 vivo

- **Dónde**: `internal/bootstrap/arranque/flows.go:75` (copia en `internal/arranque/` desde F0) — `NewR2PresignClient` valida el bucket con
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

### Comportamientos heredados que la reconstrucción portó tal cual (F45-02, 2026-10-07)

Los seis que siguen los destapó el corpus adversario de F4 y F5. El código nuevo (`internal/apipublica`,
`internal/modulos/catalogo`) hace **lo mismo que el viejo** y lo deja **fijado por test**, porque la
norma exige equivalencia viejo ↔ nuevo (`reorganizacion-modular/05` E-12) y los procesos de F9 comparan
los dos binarios. **Ninguno se corrige antes del relevo (F10)**: arreglarlo solo en lo nuevo rompería
esa equivalencia. Al corregir uno, cambia también el test que hoy lo fija. Detalle y evidencia en los
hallazgos de F45-02 de `reorganizacion-modular/plan/F4-inferencia/README.md` y `…/F5-catalogo/README.md`.

### D-24 · 🟡 `GET /api/v1/degradation-notices` responde 500, no 504, al vencer el plazo de BD

- **Dónde**: `internal/publicapi/degradationnotices.go:150-153` (no usa `dbTimedOut504`); portado a
  `internal/apipublica/degradationnotices.go` y fijado por `TestMountDegradationNotices_DBTimeout`.
- **Consecuencia**: incoherente con las demás lecturas de la cara, que dan 504; quien consume la API
  no distingue «BD lenta» de «error interno».
- **Veredicto**: defecto. **Corregir tras F10.**

### D-25 · 🟡 PUT y DELETE de `/api/v1/tenant-llm` no tienen plazo de BD

- **Dónde**: `internal/publicapi/tenantllm.go:188,252,259,319` (llaman al store con `r.Context()` a
  secas, incluida la relectura del PUT); portado a `internal/apipublica/tenantllm.go`, por eso
  `TenantLLMDeps` no lleva `DBTimeout`.
- **Consecuencia**: con la BD lenta la petición queda colgada hasta que corte el cliente o el servidor.
- **Veredicto**: defecto. **Corregir tras F10.**

### D-26 · 🟡 El prefijo reservado de SKU se esquiva con un espacio por JSON, pero no por planilla

- **Dónde**: el validador estricto compara sin recortar (`internal/catalogimport/validator.go:558`),
  la planilla recorta la celda antes (`internal/catalogimport/tabular.go`), y el runtime descarta con
  `HasPrefix` sin recortar (`internal/flujos/modules/cart/catalog.go:342`). Portado a
  `internal/modulos/catalogo/{catalog.go,catalogimport/validator_item.go}`.
- **Consecuencia**: `" _shipping"` se acepta por JSON y se rechaza por planilla: los dos caminos de
  importación no dicen lo mismo. Hoy no hace daño, porque el runtime tampoco trata ese SKU como de
  sistema.
- **Veredicto**: defecto leve; **si se recorta o no es decisión de producto**. No antes de F10.

### D-27 · 🟡 Una subcategoría declarada con espacio final no se puede referenciar

- **Dónde**: `internal/catalogimport/validator.go:475` (el código declarado no se recorta) frente a
  `:572-575` (la referencia del artículo sí). Portado a `internal/modulos/catalogo/catalogimport/`.
- **Consecuencia**: una subcategoría `"01a "` es irreferenciable aunque el artículo la escriba idéntica.
  En la misma familia: `sku` y `code` del artículo viajan sin recortar (`:749-759`), así que `"CAFE"` y
  `"CAFE "` son dos SKU válidos.
- **Veredicto**: defecto. **Corregir tras F10.**

### D-28 · 🟡 Los precios de planilla aceptan todo lo que lea `strconv.ParseFloat`

- **Dónde**: `internal/catalogimport/tabular.go:452` (y `:428`, la cantidad con `strconv.Atoi`).
  Portado a `internal/modulos/catalogo/catalogimport/tabular_cells.go:125` (y `:101`).
- **Consecuencia**: `0x1p4` (16), `1_000`, `1e3`, `+5` y `.5` son precios válidos. Improbable que un
  cliente lo escriba.
- **Veredicto**: accidente, no defecto que duela. **Se deja**; se anota para que nadie lo descubra dos veces.

### D-29 · 🟡 El desalojo de la caché del índice puede elegir mal la víctima con un tenant de id vacío

- **Dónde**: `internal/intake/catalogo/cache.go:253` usa `victim == ""` como centinela de «aún no hay
  víctima», y `Obtener` no rechaza el id vacío. Portado a `internal/modulos/catalogo/indice/cache.go:299`.
- **Consecuencia**: teórica: no debería existir un tenant con id vacío.
- **Veredicto**: **se deja**; como mucho, rechazar el id vacío en `Obtener` tras F10.

### D-30 · 🟡 `PUT /api/v1/intents` responde prosa en vez de `feature_not_enabled`, y 500 en vez de *fail-closed*

> No es de los seis de F45-02: lo destapó F7-04 al portar E1–E2 y se decidió el 2026-10-09 (D-F7-13 de
> `reorganizacion-modular/plan/DECISIONES.md`; análisis en el hallazgo 63 de `…/plan/F7-captacion/README.md`). Misma
> familia: portado tal cual y fijado por test.

- **Dónde**: `internal/publicapi/intents.go:122-130` (el gate `llm_intent` está escrito a mano, no con
  `entitlements.RequireFeature`); portado a `internal/apipublica/intents.go:243-245`. Lo fija P9 contra los dos
  binarios (`test/procesos/p9_diagnostico_config_push_test.go:245`).
- **Consecuencia**: sin la feature, el 403 es `{"error":"el plan del tenant no incluye la clasificación de
  intenciones"}`: es el **único** endpoint con gate de plan que no responde
  `{"error":"feature_not_enabled","feature":"<clave>"}`, e incumple la norma de la documentación funcional de la raíz
  (un 403 con `feature_not_enabled` es plan; cualquier otro es permiso). Un cliente que hiciera *upsell* leyendo ese
  cuerpo —`wapp-client-console` ya lo hace con las demás rutas— mostraría «sin permiso». Y si el resolver de derechos
  falla, responde **500** donde el resto de gates cierra con 403. **Hoy no duele a nadie**: ningún cliente del
  ecosistema llama a esta ruta (el editor de intents en consola no existe, DT-37 de la deuda de la raíz); se usa por
  curl.
- **Veredicto**: defecto. **Se paga en F10** (muerte del binario viejo) **o al construir el editor de intents (DT-37), lo
  que llegue antes**: E2 migra a `RequireFeature`, que trae juntos el cuerpo canónico y el *fail-closed*. **No antes**,
  porque P9 comprueba la prosa contra los dos binarios y los procesos no ramifican por binario (R9.8.b): cambiar solo
  la cara nueva lo rompe contra el viejo. Al corregirlo, cambian con él el aserto de P9 y los tests de
  `internal/apipublica` que fijan la prosa y el 500.

### D-31 · 🟡 145 `//nolint` en el repo: avisos silenciados, no arreglados

> Abierta el 2026-10-10 por decisión de Jhoan al cerrar F8-03: **«limpio» es sin avisos silenciados**, no «el linter no
> informa de nada». Los 17 de los tests de `cart` se arreglaron en el acto (`d3f6c31b`); el resto se revisa **al final
> del plan**, no antes.

- **Dónde** (medido el 2026-10-10 sobre `d3f6c31b` con `grep -rn '//nolint' --include='*.go'`, una línea = una
  supresión): **145** en el repo. En el árbol nuevo (`internal/modulos`, `nucleo`, `arranque`, `apipublica`), **90**: 70
  en tests y 20 en producción; por linter, `gosec` 40, `errorlint` 34, `nilerr` 5, `contextcheck` 3, `gocritic` 2,
  `errcheck` 2 y uno de `unused`, `staticcheck`, `nilnil` y `gocyclo`. Los otros **55** viven en el código viejo y en el
  resto del repo, y mueren en su mayoría con el relevo. En producción, 34 en todo el repo. **Dos** no llevan motivo
  escrito (uno en el árbol nuevo).
- **Consecuencia**: `make lint` da `0 issues.` y sin embargo hay 145 avisos que nadie ve. Cada uno es una regla del
  linter apagada en una línea; los de `gosec` y `errorlint` son los que más pesan, porque un aviso real nuevo en esa
  misma línea tampoco saldría. Y hay un punto ciego que los fabrica: el linter compila con la etiqueta `integracion`
  (`.golangci.yml`), así que **no mira los tests con `//go:build pendiente`**; sus avisos aparecen de golpe al pasar a
  verde, cuando lo cómodo es silenciarlos (hallazgo 28 de `reorganizacion-modular/plan/F8-conversacion/README.md`).
- **Veredicto**: deuda de calidad, no defecto. **Se paga en F10**, tarea **T10.23** de
  `reorganizacion-modular/plan/F10-relevo/tareas.md`, después de que muera el código viejo (T10.11) y la etiqueta
  `pendiente` (T10.12): con el viejo fuera quedan menos y el punto ciego ya no existe. Cada supresión se arregla o, si
  no se puede sin cambiar lo que se prueba o se promete, queda con motivo y aprobada por Jhoan. **Mientras tanto**: en
  lo que se escriba nuevo no se añade un `//nolint` sin plantearlo antes.

---

## 5 · Deudas con nombre heredadas de los planes

| Marca | Dónde | Qué significa |
|---|---|---|
| **DEUDA-044.10** | `internal/intake/pipeline/pipeline.go:56` | Un aviso colgado del **desenlace feliz** de una operación que, en el caso que importa, **fracasa**: la inferencia fría muere por timeout sin emitir régimen, y el fallo borraba su propia evidencia. El worker ya corrige el patrón; la marca se conserva como recordatorio |
| **DEUDA-044.11** (sin dueño) | `internal/bootstrap/arranque/fase5_captacion.go:170`, `internal/intake/stages/p4.go:144` | Un plazo que hay que escribir explícitamente en vez de heredarlo |
| **DEUDA-044.16** | `internal/intake/stages/match.go:42` y `:265`, `match_lineas.go:66`, `draft.go:259` | Un ítem malo **no** tira el borrador: se degrada y se anota como *warning* |
| **DEUDA-050.1** (cerrada con red) | `internal/gateway/grpc/connect.go:917-940` | Carrera de la reconexión rápida (`MarkOffline` diferido, `MarkOnline` inmediato). Mitigada preguntando «¿sigue caída?» **al ejecutar** el job, no al encolarlo. El comentario declara qué **no** cubre la red |
| **DEUDA-050.2** | `internal/platform/metrics/metrics.go:483` | El cuello mudado del head-of-line al pool; `wapp_db_wait_count` existe para decidirlo |
| **H-1** (F9-02, 2026-10-01) · diferida a F6 | `internal/integrations/worker.go:209` y `:225`. Mismo patrón, **no observado**: `internal/platform/metrics/flowlifecycle/collector.go:255`, `:263`, `:348` y `:372` · `internal/flujos/runtime/aggregator.go:741` · `internal/intake/pipeline/pipeline.go:547` | El worker loguea a `ERROR` cuando se cancela el contexto a mitad de `recoverOrphans`/`pollOnce` (SIGTERM justo tras arrancar). Cosmético y **compartido** por `cmd/server` y `cmd/server-modular` (el nuevo cablea el mismo paquete); lo destapó `TestP0_Arranque/sin_errores` (1 fallo en 161 arranques en frío). **No se toca el código viejo**: se **evalúa** al reconstruir `integrations` en F6 (D-F6-7). ⚠️ Revisión independiente (2026-10-01): el mismo patrón —llamada a BD nada más arrancar y `Error` sin mirar `ctx.Err()`— está en otras tres goroutines de fondo que F6 **no** reconstruye: el colector de `flow_events` (`platform`), el agregador (`flujos/runtime`, F8) y el pipeline (`intake/pipeline`, F7). Triaje: es esta carrera un rojo de `sin_errores` cuyas líneas `ERROR` sean **todas** de cancelación, de una goroutine de fondo y de la parada (no solo «las dos del worker»); la regla entera, en la [contradicción 19 del README de F9](reorganizacion-modular/plan/F9-procesos/README.md). ✎ **2026-10-08 (D-F9-10, Jhoan)**: el worker **nuevo** ya no lo hace (F6-04) y `sin_errores` tolera los `ERROR` de cancelación posteriores a la señal de parada; el pipeline y el agregador lo prometen al reconstruirse (F7, F8). Sigue vivo en el binario viejo y en el colector de `platform`: cosmético |

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
- ~~Solo **13 `//nolint`** en toda la producción, **todos con justificación escrita**.~~ ✎ 2026-10-10: caducado. Hoy son
  **34** en producción y 145 en el repo, dos sin motivo: es la deuda **D-31**.
