# F4 · Diseño — la vista micro

> `V` = paquete viejo de referencia @ `1b18932` · `N` = `internal/modulos/inferencia`. Exportados
> contados con la regla awk de [`../00-marco/estructura.md`](../00-marco/estructura.md) (funciones,
> métodos, tipos, `var`/`const` de primer nivel y dentro de bloques) — **aprox.**, incluye métodos
> exportados de tipos no exportados. 🔴 En el **rojo** solo existen exportados (regla T-1 de F1:
> `unused` rompe el lint); los auxiliares (`motivoDe`, `avisar`, `etapa`, `plazo`, `seccion`…)
> nacen en su `verde`. Su test nace también en el **verde** y **solo si llevan regla de negocio o ramas no triviales**
> (`05` E-4, P6): `motivoDe` sí (su orden de ramas es contrato, T-6); no se testea fontanería ni `if err != nil`; el resto
> lo cubre F9. Niveles de ceremonia por paquete: §6.

## 1 · El árbol que se crea

```
internal/modulos/inferencia/
├── prompts/
│   ├── prompts.go       prompts_test.go      porta V/prompts/prompts.go   (9 exportados · 299 l)
│   └── volcar.go        volcar_test.go       porta V/prompts/volcar.go    (4 · 108 l)
├── degradation/
│   ├── degradation.go   degradation_test.go  porta V/degradation/degradation.go (28 · 449 l)
│   ├── postgres.go      postgres_test.go     porta V/degradation/postgres.go    (4 · 227 l) — su verdad: suite contra Postgres (P4) y F9
│   └── degradationhelpertest/
│       ├── contrato.go  (sin test propio, D-F1-3)   suite del puerto Store
│       └── memoria.go   memoria_test.go             doble con el arbitrio del índice único
├── tenantllm/
│   ├── tenantllm.go     tenantllm_test.go    porta V/tenantllm/tenantllm.go (9 · 182 l)
│   ├── postgres.go      postgres_test.go     porta V/tenantllm/postgres.go  (6 · 222 l) — su verdad: suite contra Postgres (P4) y F9
│   └── tenantllmhelpertest/
│       ├── contrato.go  (sin test propio)
│       └── memoria.go   memoria_test.go
└── llmvia/
    ├── notify.go        notify_test.go       porta V/llmvia/notify.go  (10 · 280 l)
    ├── llmvia.go        llmvia_test.go       porta V/llmvia/llmvia.go  (20 · 656 l)
    ├── c2_via_test.go                        candado C2 (AST, D-F4-2)
    └── local/
        ├── local.go         local_test.go         porta V/llmvia/local/local.go (21 · 533 l)
        └── calentamiento.go calentamiento_test.go porta V/llmvia/local/calentamiento.go (2 · 116 l)
internal/arranque/
└── bridge_inferencia.go  bridge_inferencia_test.go   adaptador de arranque, nivel simple (arquitectura §4; `05` §4.2)
```

26 ficheros en `N` + 2 en `internal/arranque`. Tests viejos que hay que **leer** (E-8), por paquete
nuevo: `prompts` ← `V/prompts/prompts_test.go` (7) · `degradation` ← `degradation_test.go` (11) y
`postgres_integration_test.go` (6, para la suite) · `tenantllm` ← `postgres_integration_test.go`
(15, para la suite) · `llmvia/local` ← `local_test.go` (12), `presupuesto_test.go` (7) · `llmvia` ←
`selector_test.go` (11), `turno_test.go` (7), `plaza_test.go` (5), `calentamiento_test.go` (2),
`notify_internal_test.go` (2), `withlocaloptions_acumula{,_whitebox}_test.go` (1+1), `c2_via_test.go` (1).

## 2 · Contratos, fichero a fichero

### `prompts/prompts.go` (porta `V/prompts/prompts.go`)

| Exportado | Promete |
|---|---|
| `MarcaInstruccion = "--- INSTRUCCION ---"` · `MarcaEsquema = "--- ESQUEMA ---"` · `Extension = ".tmpl"` · `HuecoVersion = "{{version}}"` · `OrigenCompilado = "compilada"` | Literales de formato **byte a byte** (los lee y escribe el operador) |
| `ErrPromptsDir` | `errors.New("prompts: directorio de plantillas inválido")` — texto exacto: `cmd/prompts` no añade prefijo propio porque este ya empieza por `prompts:` (`cmd/prompts/main.go:34-39`) |
| `Cargadas{Plantillas map[llm.Etapa]llm.Plantilla; Origen map[llm.Etapa]string}` | Siempre **las cuatro** etapas de `llm.EtapasAjustables`; `Origen` = ruta del fichero o `compilada` |
| `Cargar(dir string) (Cargadas, error)` | `dir` vacío (tras `TrimSpace`) ⇒ las compiladas, **sin error**. Solo mira ficheros (no dirs) con extensión `.tmpl` **sin distinguir mayúsculas** (`strings.EqualFold`); los recorre **ordenados por nombre**; la etapa sale del **prefijo** `pN-` del nombre base en minúsculas (el resto es libre). Falla envolviendo `ErrPromptsDir` si: el dir no se lee; un `.tmpl` no empieza por etapa conocida + guion (texto `no empieza por ninguna etapa conocida`); dos ficheros reclaman la etapa (`reclaman la etapa`); `Parsear` falla; `llm.ValidarPlantilla` falla (`no se puede servir`, y dentro el texto de `wapp-shared`: `lo rechaza su propio validador`). **Nunca** degrada al compilado |
| `Parsear(contenido string) (llm.Plantilla, error)` | Lo anterior al primer marcador es preámbulo y **no** viaja; cada sección se toma **verbatim** (sin `TrimSpace`: la instrucción de P5 empieza pegada al margen y las de P2–P4 con línea en blanco); consume exactamente el salto (`\n` o `\r\n`) tras la línea del marcador; marcador que no va solo en su línea ⇒ error; `ESQUEMA` antes que `INSTRUCCION` ⇒ error `el orden de las secciones` (se comprueba **antes** de trocear); sección vacía ⇒ error; `{{version}}` → `llm.ArtifactVersion` (hoy 1) en las dos secciones |

### `prompts/volcar.go`

| Exportado | Promete |
|---|---|
| `NombreDeFichero` | `p2-extraer-ideas.tmpl` · `p3-especificar-item.tmpl` · `p4-normalizar-cantidades.tmpl` · `p5-redactar-cotizacion.tmpl` (literales) |
| `QueHaceLaEtapa` | Las 4 frases de `V/prompts/volcar.go:28-33`, literales |
| `Volcar(dir) ([]string, error)` | Crea `dir` (0o750) y escribe los 4 ficheros (0o600) con `Serializar`; **no sobrescribe** (texto `ya existe y NO se sobrescribe`); `dir` vacío ⇒ error `volcar necesita un directorio`; todo envuelto en `ErrPromptsDir` |
| `Serializar(e, p) string` | Inversa **exacta** de `Parsear`; escribe `"version": {{version}}` en vez del número; el preámbulo lleva el aviso del `"package_size": 0` (`volcar.go:96-100`) |

### `degradation/degradation.go`

| Exportado | Promete |
|---|---|
| `Reason` (tipo) · `Reason.Valid()` · `Reason.String()` · `Reasons()` | Vocabulario **cerrado de 8** en el orden de la migración: `ollama_down breaker_open edge_offline timeout api_error credencial lease_invalid edge_sin_capacidad`; `Reasons()` devuelve **copia**; `Valid` false para motivos sanos (`fastlane`, `atajo_determinista`, `sin_texto`, `umbral_no_alcanzado`) |
| `ReasonOllamaDown` … `ReasonEdgeSinCapacidad` (8 const) | Literales exactos (viajan al wire y a la BD) |
| `ViaLocal = "local"` · `ViaAPI = "api"` · `ValidVia(v)` | **Duplicados a propósito** de `tenantllm` (no se importa el paquete de la credencial, `degradation.go:176-186`); el test afirma que coinciden |
| `VentanaPorDefecto = 15 * time.Minute` | — |
| `ErrMotivoDesconocido` · `ErrViaDesconocida` · `ErrTenantVacio` | Textos exactos de `degradation.go:219-223` |
| `Notice{ID, TenantID, Reason, Via, WindowStart, WindowEnd, Occurrences, ReadAt, CreatedAt, LastSeenAt}` · `Notice.Leida()` | **Sin** campo de texto libre ni `SessionID` (INV-6); `Leida` = `!ReadAt.IsZero()` |
| `ListFilter{SoloSinLeer, Limit, Offset}` | El tenant **no** está en el filtro (INV-7) |
| `Store` (`Save(ctx, Notice) (creado bool, err)` · `List(ctx, tenantID, ListFilter) ([]Notice, error)`) | `Save` aplica dedupe (tenant, motivo, vía, inicio de ventana): `creado` = primer fallo de la ventana; **no valida** vocabulario. `List`: más reciente primero, nunca `nil` |
| `Notifier{Ventana, Ahora}` · `NewNotifier(store, ventana)` · `Record(ctx, tenant, reason, via, at) (bool, error)` · `RecordAhora` | `Record` valida tenant → motivo → vía **antes** de tocar el store; `Notifier` sin store ⇒ error (no panic); ventana ≤ 0 ⇒ 15 min, resuelto **en el uso** (un `&Notifier{}` literal se comporta igual); `LastSeenAt = at.UTC()` |
| `VentanaDe(at, v) (inicio, fin)` | `inicio = at.UTC().Truncate(v)`, `fin = inicio+v`; v ≤ 0 ⇒ 15 min; función pura (misma clave en dos procesos y dos TZ) |

### `degradation/postgres.go` (adaptador Postgres; su verdad la da la suite contra Postgres, P4, y F9)

`Postgres` · `NewPostgres(db)` · `Save` · `List`. **SQL idéntico** a `V/degradation/postgres.go:84-91`
(`INSERT … AS n … ON CONFLICT (tenant_id, reason, via, window_start) DO UPDATE SET occurrences = n.occurrences + 1, last_seen_at = GREATEST(…) RETURNING …`, `created_at` y `last_seen_at` con el mismo `$6`)
y `:143-150` (`NOT $2::boolean OR read_at IS NULL`, orden `window_start DESC, created_at DESC, id`).
Unitario sin BD: extraer y probar como funciones puras `acotar` (limit ≤ 0 ⇒ 50; > 200 ⇒ 200;
offset < 0 ⇒ 0), el «`LastSeenAt` cero ⇒ `WindowEnd`» y el mapeo `NULL read_at ⇒ cero`. Sin
`FieldCipher` a propósito (no hay nada sensible). Su SQL lo prueba la suite `Contrato` corrida contra Postgres con el arnés (T4.31).

### `tenantllm/tenantllm.go`

| Exportado | Promete |
|---|---|
| `ProviderAnthropic = "anthropic"` · `ProviderGemini = "gemini"` · `ProviderLocal = "local"` (documental) | Literales; `ProviderLocal` no gobierna ninguna rama |
| `ViaLocal = "local"` · `ViaAPI = "api"` · `ValidVia` | `ViaLocal` y `ProviderLocal` **no** se unifican (ejes distintos) |
| `ErrNotConfigured` | Texto exacto `tenantllm: el tenant no tiene configurada la vía LLM API` |
| `Config{TenantID, Via, Provider, Model, HasAPIKey, ConsentedAt, CreatedAt, UpdatedAt}` | **Sin** campo de clave (R4.4.a). En vía local, Provider/Model/ConsentedAt en cero |
| `Store` (`Get`, `Upsert(ctx, cfg, apiKey, consentedAt)`, `Delete`, `APIKey`) | Los cuatro promesas de `V/tenantllm/tenantllm.go:133-181`: sin fila ⇒ `found=false` (y eso **es** la vía local); `Upsert` = **reemplazo completo**; `api` exige clave no vacía y consentimiento no cero; `local` ignora la clave y deja NULL el eje `api`; `api→local` retira credencial y consentimiento; `Delete` idempotente; `APIKey` ⇒ `ErrNotConfigured` sin fila, sin sobre **o** con `via≠api` |

### `tenantllm/postgres.go` (adaptador)

`Postgres` · `NewPostgres(db, cipher)` · los 4 métodos. SQL idéntico a `V/tenantllm/postgres.go:47-50,137-150,160-162,186-190`.
Descifra con el `api_key_kek_id` **de la fila** (rotación parcial). Errores sin la clave ni el blob.
Unitario sin BD: la validación previa al SQL de `Upsert` (vía fuera de vocabulario; `api` sin clave;
`api` sin consentimiento) como función pura, con sus tres textos (`V/tenantllm/postgres.go:101,117,125`).
🔴 **Homónimo**: `api_key_dek` es la DEK del **sobre de PII de negocio** (`FieldCipher`), **no** la
DEK del ADR-0007 (la del almacén de `whatsmeow`, que custodia el cliente y nunca cruza).

### Dobles y suites (`05` E-3, E-6)

| Fichero | Contrato |
|---|---|
| `tenantllmhelpertest/contrato.go` | `func Contrato(t *testing.T, nuevo func(t *testing.T) Montaje)` (`05` E-6, P4; el `Montaje` trae el `tenantllm.Store` y los tenants sembrados): las promesas del puerto de arriba, **con UUID bien formados** y un tenant ajeno para INV-7 (reproduce en conducta los 15 casos de `V/tenantllm/postgres_integration_test.go`, salvo los 4 `TestBackfill0073_*`, que son de la migración → F9) |
| `tenantllmhelpertest/memoria.go` + test | Doble que cumple la suite; guarda la clave en claro **solo en memoria de test**; su test corre `Contrato` |
| `degradationhelpertest/contrato.go` | `Contrato(t, nuevo func(t) Montaje)` (ídem, con `degradation.Store`): N `Save` misma clave ⇒ una fila y `creado` solo el primero; ventana siguiente ⇒ fila nueva; `List` acotada al tenant, orden y `[]` no nil (de `V/degradation/postgres_integration_test.go`) |
| `degradationhelpertest/memoria.go` + test | Arbitrio del índice único `(tenant, reason, via, window_start.UTC())`; cuenta llamadas (`Saves()`) para R4.5.b |

Las dos suites corren **en memoria y en Postgres** con el arnés de F9-A: es lo que garantiza que el doble y el adaptador
se comportan igual. La marca de estado de cada suite vigila **todas** las columnas que la operación puede tocar, no una
sola (hallazgo 35): en `tenant_llm`, vía, proveedor, modelo, sobre de la clave y consentimiento; en
`owner_degradation_notices`, `occurrences`, `last_seen_at`, `read_at` y la ventana.

### `llmvia/notify.go`

Exportados: `OrigenSeleccion = "seleccion"` · `OrigenPipeline = "pipeline"` · `OrigenTurno = "turno"`
· `ObservadorDegradacion func(origen, via, reason string)` · `WithDegradacionObservada(fn)` (nil se
ignora). Todo lo demás nace en verde: `motivoDe`, `avisador` (decorador de los 5 métodos de
`llm.LLMProvider`, que **no** envuelve el error), `avisar`, `contarDegradacion`.

**Tabla de `motivoDe`** (el orden de ramas **es** el contrato; `V/llmvia/notify.go:64-102`,
`notify_internal_test.go:30-83`):

| Error | Motivo | ¿Avisa? |
|---|---|---|
| `nil` · `llm.ErrLLMQuality` (también envuelto) — **primera rama** | — | No |
| Implementa `Motivo() string` (duck-typing, también envuelto) y `Reason(m).Valid()` | ese motivo | Sí |
| `Motivo()` no válido (`se_rompio_algo`, `fastlane`, `""`) | — | No |
| `tenantllm.ErrNotConfigured` | `credencial` | Sí |
| `api.ErrUnsupportedProvider` (**antes** que `ErrInvalidConfig`, que lo envuelve) | — | No |
| `api.ErrInvalidConfig` | `credencial` | Sí |
| `api.ErrUpstream` | `api_error` | Sí |
| cualquier otro | — | No |

`avisar`: con motivo ⇒ **primero** el observador (aunque no haya notificador), luego —si hay
notificador— `Record` con `context.WithTimeout(context.WithoutCancel(ctx), 3 s)` y el instante del
reloj del selector; un fallo de `Record` solo va al log (`degradación: no se pudo escribir el aviso
al dueño`), nunca cambia lo que recibe el llamante; log `Warn` solo si `creado`.

### `llmvia/local/local.go`

| Exportado | Promete |
|---|---|
| `DefaultFormat = "json"` · `MargenVeredicto = 7 s` · `DefaultTimeout = 30 s` | R4.6.a |
| `ErrSinTransporte` · `ErrSinTenant` · `ErrSinPresupuesto` | Textos `llmvia/local: …` exactos (`local.go:236,240,254`); `ErrSinPresupuesto` **sin** `Motivo()` (no avisa) |
| `Frame{ Infer(ctx, tenantID string, req edgegrpc.InferRequest) (string, error) }` | Lo satisface `*modulos/edge/grpc.Server` sin adaptador |
| `Provider` · `New(frame, tenantID, opts...)` | Falla **al construir** sin frame o sin tenant; defaults `json`, 30 s, techo encendido; inmutable y concurrente |
| `Option` · `WithFormat` (vacío se ignora) · `WithTimeout` (≤0 se ignora; **no** acota el plazo heredado) · `WithOriginSession` · `WithMaxOutputTokens(on)` · `WithTargetSession` · `ConPlantillas(map)` | — |
| Los 5 métodos de `llm.LLMProvider` | Prompt con `llm.Build…Prompt` (o `…PromptCon(plantilla)` si `ConPlantillas` trae la etapa P2–P5; P1 siempre compilado); `Infer` con `Temperature` del llamante, plazo heredado, `OriginSessionID`, `TargetSessionID`, techo y `class` por etapa (tabla R4.6.b); error del transporte **intacto**; salida por `llm.ExtractJSON` |

### `llmvia/local/calentamiento.go`

`TextoDeCalentamiento = "hola"` · `(*Provider).Warm(ctx, in llm.ClassifyRequestInput) error`:
sustituye `in.Text` por `"hola"`, prompt `BuildClassifyRequestPrompt`, techo 16, clase `lote`,
`Warmup: true`, sale por `TargetSessionID`; **no** mira la salida; devuelve solo el error del
transporte (o `ErrSinPresupuesto`).

### `llmvia/llmvia.go`

| Exportado | Promete |
|---|---|
| `ErrViaDesconocida` · `ErrSinConfig` · `ErrViaSinCalentamiento` · `ErrViaSinTurnoAcotado` | Textos exactos (`llmvia.go:58,61,296,519`) |
| `Store{Get; APIKey}` · `Notifier{Record(ctx, tenantID, degradation.Reason, via string, at time.Time) (bool, error)}` | Puertos; los satisfacen `*tenantllm.Postgres` y `*degradation.Notifier` |
| `Selector` · `NewSelector(cfg Store, log, opts...)` | `cfg == nil` ⇒ `ErrSinConfig`. Si el frame no sabe `PlazaDe(tenantID, origin) (string, bool)`, **un** `Warn` al construir (el aforo quedaría inerte) |
| `SelectorOption` · `WithFrame` · `WithNotifier` (nil admitido) · `WithLocalOptions` (🔴 **acumula**: dos llamadas suman, `llmvia.go:129-144`) · `WithClock` | — |
| `For(ctx, tenantID, originSessionID) (llm.LLMProvider, error)` | Sin fila ⇒ `local`; `local` ⇒ **no** pide la clave; `api` ⇒ pide `APIKey` **aquí y solo aquí**; vía fuera de vocabulario ⇒ `ErrViaDesconocida` (no elige por ti); fallo de `Get` ⇒ error `llmvia: leyendo la configuración LLM del tenant: …` (no se confunde con una vía); fallo al **construir** ⇒ aviso con `OrigenSeleccion`; éxito ⇒ provider envuelto por el decorador (sin notificador, **sin** envoltura). La sesión de origen **se añade** a las opciones del arranque (copia, nunca `append` sobre el slice compartido) |
| `Warm(ctx, tenantID, sessionID, in)` | `api` ⇒ `ErrViaSinCalentamiento` **sin** tocar el cable; `local` ⇒ `local.Provider.Warm` por `WithTargetSession`; **nunca** avisa ni cuenta |
| `PlazaDe(ctx, tenantID, origin) (edgeID, ok, err)` | `api` ⇒ `("", false, nil)` sin preguntar al frame; `local` sin enrutador ⇒ `false`; vía inventada o `Get` roto ⇒ error |
| `PlazoTurno = 12 s` · `TechoTurno int32 = 128` · `TurnoRequest{Prompt, Formato string}` · `Turno(ctx, t, s, TurnoRequest) (string, error)` | `api` ⇒ `ErrViaSinTurnoAcotado` sin cable; sin frame ⇒ `local.ErrSinTransporte`; `Infer` con temperatura 0, `Timeout` 12 s, 128 tokens, `ClaseInteractivo`, ctx `12 s + MargenVeredicto`; fallo ⇒ `avisar(…, OrigenTurno, err)`; devuelve el texto crudo |

`var _ enrutadorDeEdges = (*edgegrpc.Server)(nil)` (comprobación en compilación de `PlazaDe`) nace en verde.

### `internal/arranque/bridge_inferencia.go` (adaptador de arranque, `05` §4.2 · nivel simple)

Sin exportados, sin estado, una pasada. `turneroBridge{sel *N.Selector}` implementa el `Turnero` del
`turnoacotado` **viejo** (`var _ turnoacotado.Turnero = …`), traduce `TurnoRequest` y el centinela
(R4.7.c). `llmConfigBridge{store *N/tenantllm.Postgres}` implementa `reanalisis.ConfigLLM` **viejo**
convirtiendo `Config` campo a campo. **Muere**: `llmConfigBridge` en F7 (conmuta `reanalisis`), `turneroBridge` y el
fichero en F8.

Test (`bridge_inferencia_test.go`), dos partes obligatorias:
- **traducción**: los dos caminos de cada tipo, más `errors.Is` sobre el centinela viejo;
- **cableado completo** (hallazgo 39): el arranque construye el selector y los almacenes **nuevos**, *y* ninguna fase
  de `internal/arranque` importa `internal/{llmvia,tenantllm,degradation,prompts}` viejos fuera del adaptador (grep por
  ruta de import), no solo el campo del contenedor.

## 3 · Candados de invariante que aterrizan aquí

| Candado viejo | Regla | Dónde queda |
|---|---|---|
| `internal/llmvia/c2_via_test.go` (AST, I-CP-3) | La vía se pregunta en un solo sitio; lista exacta de permitidos (en los dos sentidos) | `N/llmvia/c2_via_test.go` (D-F4-2). Barre `internal/{modulos,nucleo,arranque,apipublica}` hasta F10 y todo `internal/` desde F10. **Permitidos en el árbol nuevo**: `modulos/inferencia/llmvia/llmvia.go` · `…/tenantllm/tenantllm.go` · `…/tenantllm/postgres.go` · `…/tenantllm/tenantllmhelpertest/memoria.go` (el doble aplica la misma regla de persistencia) · `…/degradation/degradation.go` · `apipublica/tenantllm.go` (lo añade TX.13) · `modulos/captacion/reanalisis/reanalisis.go` (lo añade F7). Cada entrada entra **en el commit `verde` que introduce la comparación** (un permitido que no compara pone rojo el candado) |
| `withlocaloptions_acumula_{,whitebox_}test.go` | `WithLocalOptions` acumula | Aserción de conducta en `llmvia_test.go` (las dos opciones llegan al provider: plantilla ajustada **y** techo apagado a la vez); el caja-blanca no se porta |
| `degradation_test.go:170` (lee la `0075`) | Vocabulario Go ≡ `CHECK` SQL | `N/degradation/degradation_test.go`, ruta `../../../platform/storage/postgres/migrations/structure` (el `embed.FS` de `migrations` no está exportado) |
| `internal/gateway/grpc/inference_vocabulario_internal_test.go` | `Motivo*` del transporte ⊆ `Reasons()` | Vive en `edge` (F3); F4 lo re-apunta al `degradation` nuevo (T4.28) |
| `internal/bootstrap/arranque/turno_acotado_cableado_test.go` · `calentamiento_cableado_test.go` · `quotetext_cableado_test.go` · `pipeline_captacion_cableado_test.go` | Cableado del selector | Sus copias de F0 en `internal/arranque` se ajustan en `conmutar(inferencia)` (T4.27) |

## 4 · Textos y literales observables (byte a byte)

- Errores: los de §2 con su prefijo (`llmvia: …`, `llmvia/local: …`, `tenantllm: …`,
  `degradation: …`, `prompts: directorio de plantillas inválido`).
- Nombres de fichero de prompts y marcadores; `{{version}}`; `compilada`.
- Métrica `wapp_llm_degradacion_total` (declarada en `platform`) y los valores de etiqueta `origen`.
- Frame: `class` `interactivo`/`lote`, techos por etapa, `warmup`, `"hola"`.
- Logs con claves estables: `prompts: plantillas de las etapas ajustables` (arranque) ·
  `degradación: la vía LLM del tenant falló y se avisó al dueño` · `llmvia: el transporte de la vía local no sabe decir qué Edge atiende; …`.

## 5 · Reglas no obvias que el contrato debe llevar (E-8), con su incidente

1. **Dos relojes = inferencia muerta** (UAT 2026-08-23: un `DefaultTimeout` de 30 s propio cortó
   una inferencia de 36,5 s; `local.go:163-185`) ⇒ el adaptador **nunca** es más restrictivo que su
   llamante; prohibido `min(restante, p.timeout)`.
2. **`WithLocalOptions` asignaba en vez de acumular** y la palanca `WAPP_LLM_PROMPTS_DIR` estuvo
   **muerta** (`llmvia.go:129-141`).
3. **`WithOriginSession` sin llamante** (T1.7-8): la sesión de origen viajaba vacía y un segundo
   Edge no calentaba nunca su caché (`llmvia.go:188-194`) ⇒ test «la sesión pedida sale en el frame».
4. **`num_predict` 256 del Edge truncaba P2/P3** (265–293 tokens medidos, `local.go:58-63`) ⇒ techos por etapa.
5. **P4 0 de 14** por `"package_size": 0` en su esquema ⇒ `ValidarPlantilla` antes de servir, y el
   arranque aborta (`prompts.go:109-113`, `bootstrap/arranque/prompts.go:21-24`).
6. **`TrimSpace` en `Parsear` cambiaba P5** al encender el directorio (`prompts.go:232-237`).
7. **Motivo `credencial` vs proveedor no soportado**: mandar a rotar una clave buena (`notify.go:85-90`).
8. **`created_at` con el reloj de Postgres** dejaba `last_seen_at < created_at` (`degradation/postgres.go:66-76`).
9. **`Truncate` sin `.UTC()`** partiría la ventana entre procesos con TZ distinta (`degradation.go:379-385`).
10. **Tenant sin fila = vía `local`** y no «desconocido» (REQ-33; hoy lo son todos los tenants de UAT).

## 6 · Clasificación provisional por paquete (`05` E-12)

> **Provisional, sin medir: la fija el inventario E-12** (T4.1), que la baja a archivo y la aprueba Jhoan. Deducida de
> `arquitectura.md` §4 y §5. Si un archivo sale peor, sube de nivel.

| Paquete | Estado en memoria | Concurrencia | BD / transacciones | Consumidores (prod.) | Nivel provisional |
|---|---|---|---|---|---|
| `prompts` (2) | no | no | no | 2 (`arranque`, `cmd/prompts`) | **medio** (reglas de parseo y validación) |
| `degradation/degradation.go` | no | no | no (puerto) | 3 | **medio** |
| `degradation/postgres.go` + `degradationhelpertest` | no (el doble sí, de test) | no | **sí** (`ON CONFLICT`) | — | **complejo** (puerto con BD: suite con `Montaje`, memoria y Postgres) |
| `tenantllm/tenantllm.go` | no | no | no (puerto) | 4 | **medio** |
| `tenantllm/postgres.go` + `tenantllmhelpertest` | no (el doble sí, de test) | no | **sí** (+ cifrado) | — | **complejo** (ídem) |
| `llmvia/local` (2) | no (inmutable) | no | no | 1 (`llmvia`) | **medio** (relojes y presupuesto) |
| `llmvia` (2) | no (inmutable) | uso concurrente (T-4, test con `-race`); 0 goroutines | no | 7 (por interfaz, §4 de `arquitectura.md`) | **complejo** (muchos consumidores; mutantes en `motivoDe` y en la copia de opciones) |
| `internal/arranque/bridge_inferencia.go` | no | no | no | — | **simple** (adaptador) |

`c2_via_test.go` es un candado, no lleva nivel. **Adaptadores**: nace 1 fichero (`bridge_inferencia.go`, dos tipos) y
muere 1 (`bridge_gateway.go` de F3, en T4.24).
