# Estructura — el árbol destino, las piezas transitorias y las convenciones

> *Steering* de estructura. El árbol **fichero a fichero** vive en
> [`04`](../../04-estructura-final.md) §3; aquí va a nivel de **paquete**, con el único cambio que
> introdujo **D-10** (2026-09-27): la cara HTTP nueva `internal/apipublica`. Rutas viejas medidas
> sobre `dev` @ `1b18932`.

## 1 · El árbol destino (tras F10), por paquete

```
cmd/                              no se mueve · server/ ✎ importa internal/arranque (F10)
├── server/ · migrate/ · prompts/ · casebank/ · debug_inferencia/
internal/
├── arranque/                     ✚ el cableado nuevo, una fase por módulo (sustituye a bootstrap/)
├── apipublica/                   ✚ D-10 · la cara HTTP /api/v1 NUEVA, un fichero por área (como hoy)
├── pendiente/                    ✚ una función: Implementar(nombre) (desaparece o queda vacía de usos en F10)
├── nucleo/
│   └── contact/                  ← flujos/contact
├── platform/                     no es módulo; se queda (3 ✎ en F0: httpapi/admin.go, httpapi/audit_mw.go, metrics/inferstats.go)
│   └── config · crypto · httpapi · logging · metrics(/flowlifecycle) · ratelimit · storage/{objectstore,postgres(/migrations 🔒)}
└── modulos/
    ├── fronteras_test.go · un_fichero_un_test_test.go · exportados_cubiertos_test.go   ✚ candados
    ├── acceso/        entitlements · iam/{domain, infra/{identity,memory,postgres}, ports/{in,out}, transport/http, usecase} · platformadmin
    ├── edge/          diagnostics · enroll · filtercfg · fleet(/fleettest) · grpc · inferstats · ingest · lease 🔒 · receipts · session
    ├── inferencia/    degradation · llmvia(/local) · prompts · tenantllm
    ├── catalogo/      (raíz: catalog.go, sacado de cart) · catalogimport · indice ⚠️ renombre de intake/catalogo
    ├── solicitudes/   intakes(/quotetext, /telemetria) · integrations(/crmpush, /sigv1) · tenantvars
    ├── captacion/     anclaje · casebank · evidence · intake · intakeahead · intentcfg · pipeline · reanalisis · stages
    └── conversacion/  admin · content · engine · events · model · modules(/cart, /media, /menu, /survey) · runtime · store · trigger · turnoacotado
test/
└── procesos/                     ✚ F9 · un solo paquete: la suite de integración por proceso
```

**Cambios frente a `04` §3**, todos cerrados:

| `04` §3 decía | Queda | Por qué |
|---|---|---|
| `internal/publicapi/` «no se mueve, solo cambian sus imports» | `internal/apipublica/` **nueva**; `internal/publicapi/` vieja **vive hasta F10** y se borra allí | D-10: `publicapi` guarda dos *singletons* con estado en memoria (`*gatewaygrpc.Server`, las conexiones vivas de los Edge; `*runtime.Runtime`, las ventanas del motor — `internal/publicapi/publicapi.go:40,101,170,183`), así que una ruta que los usa no puede quedarse en el viejo cuando su módulo conmuta. Detalle y mapa ruta a ruta: [`FX-cara-http/`](../FX-cara-http/README.md) |
| `05` §9.2 / `03` D-10: repartir en `modulos/<m>/http/` | **No** se reparte: los handlers van en `apipublica`, un fichero por área | D-10. `acceso/iam/transport/http` (las dos rutas de `auth.go`) sigue siendo del módulo, como hoy |
| `modulos/solicitudes/contracts/` | **Desaparece** | `05` §6 (F6): su validación del esquema CRM pasa al contrato de `integrations` |
| (no estaba) | `internal/pendiente/`, `test/procesos/`, candados de `modulos/` | `05` E-2, §5, §7 |

## 2 · Las piezas transitorias

| Pieza | Nace | Muere | Qué es |
|---|---|---|---|
| `cmd/server-modular/` (`main.go`, `integration_test.go`) | F0 | F10 | Copia de `cmd/server/main.go` que llama al arranque nuevo (D-9). 🔴 Nunca a la vez que `cmd/server` contra la misma BD o puertos (`04` §2.2) |
| `internal/arranque/` | F0 (copia exacta del arranque viejo, cableando paquetes **viejos**) | se queda | Cada fase `conmutar(<m>)` lo pasa a los paquetes nuevos |
| `internal/arranque/huella_test.go` | F0 | se queda (compara contra el viejo hasta F10) | La huella: rutas, rpc, métricas, goroutines, variables |
| `internal/apipublica/` | F0, **vacía** | se queda | Se monta **delante** de `publicapi`: sus rutas ganan, el resto cae al viejo. Crece fase a fase |
| `internal/publicapi/` (vieja) | — | F10 | Sirve al arranque viejo siempre, y al nuevo lo que `apipublica` aún no tiene. Tras F8 no sirve nada en el binario nuevo |
| `internal/pendiente/` | F0 | F10 (cero usos; `sin_pendientes_test.go`) | `func Implementar(nombre string) …` para `panic(pendiente.Implementar("pkg.Func"))` |
| `internal/modulos/fronteras_test.go` | F0 | se queda (sin puentes en F10) | Lista blanca de imports entre módulos + **puentes** declarados al código viejo |
| `internal/modulos/un_fichero_un_test_test.go` · `exportados_cubiertos_test.go` | F0 | se quedan | E-3 y E-9. Candados AST permitidos (E-7). 🔴 Viven en `modulos/` pero su **alcance** es todo el código nuevo: `internal/{modulos,nucleo,apipublica,pendiente,candados}` y `internal/arranque/huellatest` (F0 [`diseno.md`](../F0-andamiaje/diseno.md) §4); `05` §5 solo nombra `modulos/`. `fronteras` alcanza además `internal/arranque` |
| `test/procesos/sin_bd_viva_test.go` | F0 (sin etiqueta) | se queda | Ninguna referencia a `WAPP_TEST_DB_DSN`, puerto fijo o `WithReuseByName` |
| `test/procesos/{main,arnes}_test.go` y un fichero por proceso | F9 | se quedan | `//go:build integracion`. `WAPP_PROCESOS_BINARIO=viejo\|nuevo` |
| `sin_pendientes_test.go` | F10 | se queda | Cero `pendiente.Implementar` |
| `documentations/reorganizacion-modular/traspasos/` | primera vez que un bloque lo necesite | se queda (historia) | `TRASPASO-<fase>-<tema>.md` (skill `traspaso-web-local`) |

Si `internal/modulos/` solo tiene ficheros de test, lleva además un `doc.go` con el comentario de
paquete (excepción de E-3), para que `go vet ./...` y los candados tengan un paquete claro.

### 2.1 · Puentes y adaptadores: qué nace, dónde, y en qué fase muere

Dos mecanismos distintos, que no se confunden: el **puente de import** (`05` §4.1) es un import de
un paquete **nuevo** a uno **viejo**, declarado en `internal/modulos/fronteras_test.go` con su
`Muere`; el **adaptador** (D-F1-5) es un tipo no exportado en `internal/arranque/puente_<x>.go`
que deja a un consumidor **viejo** usar un objeto **nuevo** (o viceversa) sin que ningún paquete
nuevo importe lo viejo. Sacado de las fases, con las recomendaciones de
[`../DECISIONES.md`](../DECISIONES.md); la columna ⚠️ dice dónde discrepaban.

**Puentes de import** (lista de `Puentes` de `fronteras_test.go`):

| Puente | Nace | Muere | Por qué | ⚠️ |
|---|---|---|---|---|
| `modulos/solicitudes/intakes/telemetria` → `internal/flujos/store` | F6 (T6.10) | F8 (T8.31) | `store.FlowEvent` en su puerto (`telemetria.go:23,64-66`) | — |
| `modulos/captacion/stages` → `internal/flujos/store` | F7 (T7.9) | F8 (T8.31) | `store.Intake`, `store.FlowEvent` en `draft.go` | — |
| `modulos/captacion/reanalisis` → `internal/flujos/events` | F7 (T7.12) | F8 (T8.31) | `events.ThreadEntry`, `events.KindMessage` (puerto `Hilo`) | — |
| `modulos/captacion/reanalisis` → `internal/flujos/runtime` | F7 (T7.12), **solo si** no se evita | F8 (T8.31) | la constante `runtime.DefaultThreadLimit` | F7 README cuenta **3** puentes; F7 [`arquitectura.md`](../F7-captacion/arquitectura.md) §4 **recomienda evitarlo** (el constructor recibe el límite; el arranque lo lee del runtime viejo) y quedarse en 2. Se decide en T7.12, sin Jhoan |
| *(alternativa)* `modulos/catalogo{,/indice}` → `internal/flujos/model` | F5 | F8 | solo si **D-F5-1 = A**; con la recomendación (B) `model` se reconstruye en F5 y no hay puente | — |
| *(alternativa)* `apipublica/intents.go` → `internal/intentcfg` | F3 | F7 | solo si **D-FX-1/D-F7-4 = no**; con la recomendación las intenciones se mudan en F7 y no hay puente | FX lo planificaba así hasta el 2026-09-29 (corregido) |
| *(alternativa)* `modulos/edge/session` → `internal/gateway/session` | F3 | F8 | solo si **D-F3-2 = no** (entonces D-FX-3); con la recomendación, viejo y nuevo son el centinela de `platform` desde F0 (T0.17) y no hay puente | F8 lo contaba como puente a retirar (corregido) |

F1, F2, F3 y F4 no declaran ningún puente (F3: la única arista de `gateway/**` a `flujos` era
`flujos/contact`, que es `nucleo` desde F1 — F3 README, contradicción 2).

**Adaptadores y segundas instancias en `internal/arranque`**:

| Pieza | Nace | Muere | Qué adapta | ⚠️ |
|---|---|---|---|---|
| `puente_contact.go` | F1 (T1.14) | F8 (T8.32) | `flujos/contact.Resolver` viejo (runtime, admin) ← `nucleo/contact` nuevo; F6 deja de necesitarlo para el notificador | — |
| `puente_iam.go` | F2 (T2.28) | **F3** (T3.28 lo borra) | `in.Authenticator`/`in.Auditor` viejos del gateway viejo ← `acceso` nuevo; traduce 4 centinelas | F8 [`arquitectura.md`](../F8-conversacion/arquitectura.md) §5.2 lo daba vivo hasta F8 (corregido) |
| `puente_gateway.go` | F3 (T3.24) | **F4** (T4.24 lo borra) | `llmvia/local.Frame` viejo (`Infer` con `InferRequest` viejo) ← `edge/grpc` nuevo; implementa `Infer` **y** `PlazaDe` (F3 reglas T-1) | F8 §5.2 lo daba hasta F8; F3 (arquitectura §4) y F4 (T4.24, reglas §5.8) dicen F4, y mandan ellas: con `llmvia` nuevo el `Frame` ya habla el tipo nuevo |
| `puente_inferencia.go` | F4 (T4.10, T4.25) | por partes: `puenteConfigLLM` en **F7**, `puenteTurnero` en **F8** | `reanalisis` viejo pide `tenantllm.Config` viejo; `turnoacotado` viejo pide `llmvia.TurnoRequest` y `ErrViaSinTurnoAcotado` viejos (D-F4-4) | — |
| 2.ª instancia **vieja** de `intakes.Postgres` (sin estado) | F6 (T6.24, D-F6-1) | F8 | `cart.NewProjector` viejo pide `RevisionWriter`/`ShippingEnsurer` con tipos viejos. Alternativa: `puente_intakes.go` si D-F6-1 = no | — |
| `puente_captacion.go` | F7 (T7.23, D-F7-1) | F8 (T8.32) | `adelantoViejo`, `compositorViejo` y la clausura del sink: conversión de `WindowKey` entre el agregador/compositor viejos y `intakeahead`/`reanalisis` nuevos | — |
| 2.ª instancia **vieja** de `intake.Postgres` (sin estado) | F7 (T7.23, D-F7-1) | F8 | `NewIntakeAggregator`/`NewSourceTextComposer` viejos piden `JobStore`/`SourceTextWriter` viejos | — |
| *(ninguno)* `entitlements.Resolver`, gw para runtime/notificador/`filtercfg`/J12–J15, puertos de solicitudes del runtime y del sink | — | — | **estructurales**: el objeto nuevo se inyecta tal cual (F2 arquitectura §4, F3 §4, F6 §4) | F8 §5.2 los daba «sin medir» |

Al cerrar **F8** tienen que quedar **cero**: T8.31 retira los puentes de import de F6–F7 (y F5 si
D-F5-1 = A), T8.32 borra los `puente_*.go` que queden (`puente_contact`, la parte `puenteTurnero`
de `puente_inferencia`, `puente_captacion`) **y las dos segundas instancias viejas**; T8.34 comprueba
`grep -rn 'internal/publicapi\|internal/gateway/session' internal/arranque internal/modulos internal/apipublica` → vacío.

### 2.2 · Los ficheros de `internal/arranque` conservan los nombres de la copia de F0

F0 copia `internal/bootstrap/arranque/` **con los mismos nombres** (T0.10: `http.go`,
`rutas_admin.go`, `fase7_flujos.go`, `fase8_transporte.go`…), y **así se quedan de F0 a F8**. Los
nombres del árbol final de [`04`](../../04-estructura-final.md) §3 (`transporte_http.go`,
`transporte_rutas_admin.go`, `fase7_conversacion.go`, `fase3_edge.go`, `fase4_inferencia.go`) **no**
se aplican durante la reconstrucción: tres tests copiados en F0 leen el fuente **por nombre**
(`invitaciones_cableado_test.go` → `auth.go` y `http.go`; `roleplane_cableado_test.go` y
`send_budget_cableado_test.go` → `http.go`), y un renombre los rompería sin tocarlos; además F0
[`arquitectura.md`](../F0-andamiaje/arquitectura.md) §3 deja el reparto por módulo a cada fase, y
F4 lo rechaza (D-F4-3). Toda fase nombra, pues, los ficheros **de la copia**. Si se quieren los
nombres de `04` §3, es un `refactor` aislado en F10 (decisión D-V-1 de
[`../DECISIONES.md`](../DECISIONES.md)).

## 3 · Convenciones de nombres

- **Módulos en español**, sin tilde (`conversacion`, `captacion`, `catalogo`): son carpetas nuevas.
- **La carpeta hoja conserva el nombre del paquete viejo** (D-3 aplanar, D-4). Excepción obligada:
  `intake/catalogo` → **`indice`** (`04` §5.1). `05` §8 permite renombrar si **los textos
  observables no cambian**; cualquier otro renombre va a «Decisiones que necesita» de su fase.
- 🔴 **`intake` ≠ `intakes`**: `captacion/intake` es la cola (solo `intake_jobs`);
  `solicitudes/intakes` es la solicitud. No se funden ni se renombran.
- **Tests**: `x_test.go` junto a `x.go`; funciones `TestX` y subtests con **nombres en español que
  digan la regla** (`"una frase vacía no es evidencia"`). Tabla de casos cuando hay varias entradas.
- **Suite de contrato de un puerto**: paquete `<paquete>test` (precedente `internal/gateway/fleet/fleettest`),
  función `func Contrato(t *testing.T, nuevo func(t *testing.T) Montaje)` (**D-F1-1**): el `Montaje`
  trae el puerto **y** lo que el puerto no deja ver (tenants sembrados por la FK, un observador de
  estado). La forma de `05` E-3, `func() <Puerto>`, solo vale para un puerto sin BD y queda como
  alternativa si D-F1-1 = no. El **doble en memoria** que la corre en unitario vive en el mismo
  `<paquete>test` (E-6); con **D-F1-3** el paquete `…test` entero queda fuera de «un fichero, un
  test» y de «exportados cubiertos», pero un doble con lógica lleva su test igual.
- **Adaptadores Postgres**: `postgres.go`, `*_postgres.go`, `repository_postgres.go`. El nombre es
  lo que los excluye del umbral de cobertura: no se inventan otros.
- **Identificadores y claves de wire en inglés**, comentarios y nombres internos en español (I-CP-8).
- **Sin `TODO`/`FIXME`**: la deuda se marca `DEUDA-NNN.N` y va a `deuda.md` (constitución §5).

## 4 · Cabecera de origen y commits

**Cabecera (E-10)**, en cada fichero nuevo que porta lógica, después del comentario de paquete o
del de fichero:

```go
// Porta internal/flujos/contact/contact.go @ 1b18932
```

- `<sha>` = SHA corto de `origin/dev` **en el momento en que se leyó** el fichero viejo
  (`git rev-parse --short origin/dev`), para que `git log <sha> -- <ruta vieja>` siga siendo la historia.
- Un fichero con varios orígenes lleva **una línea por origen**. Un fichero sin origen (candados,
  `pendiente`, dobles nuevos) lleva `// Nuevo: <motivo en una línea>`.
- En `apipublica`: `// Porta internal/publicapi/<fichero>.go @ <sha>`.

**Commits** (`05` E-4 y [`plantilla-de-fase.md`](plantilla-de-fase.md) §3):
`andamiaje(f0): …` · `rojo(<m>): contrato de <fichero>` · `verde(<m>): <fichero>` ·
`refactor(<m>): …` · `conmutar(<m>): el arranque nuevo cablea <m>` · `procesos(<proceso>): …` ·
`relevo: …` · `docs(reorganizacion-modular): …`. Un `rojo` y su `verde` **nunca** en el mismo
commit; un `verde` por fichero. Pie obligatorio: la atribución que pida el entorno. Un cambio de
`go.mod` va en su propio commit (`chore(deps): …`).

## 5 · Mapa fase → módulo → paquetes viejos de referencia

De `04` §4 con el orden de `05` §6. «Referencia» = qué se **lee** (código y tests viejos, E-8); nada
se mueve. Las rutas HTTP de cada fase las fija [`FX-cara-http/`](../FX-cara-http/README.md).

| Fase | Módulo destino | Paquetes viejos de referencia | Puentes esperados (`05` §4.1) |
|---|---|---|---|
| F0 | `arranque`, `pendiente`, candados, `apipublica` vacía, ✎ de `platform` | `internal/bootstrap` + `internal/bootstrap/arranque` (20 tests, 11 AST) · `platform/httpapi/{admin,audit_mw}.go` · `platform/metrics/inferstats.go` | — (cablea paquetes viejos) |
| F1 · piloto | `nucleo/contact` | `internal/flujos/contact` (4 ficheros de producción) | — (solo `platform`) |
| F2 | `modulos/acceso` | `internal/iam/**`, `internal/platformadmin`, `internal/entitlements` | — |
| F3 | `modulos/edge` | `internal/gateway/{grpc,enroll,lease,session,fleet}`, `internal/{diagnostics,inferstats,receipts,ingest,filtercfg}` | — (medido por F3: la única arista `gateway` → `flujos` era `flujos/contact`, ya `nucleo`). Adaptadores: nace `puente_gateway`, muere `puente_iam` (§2.1) |
| F4 | `modulos/inferencia` | `internal/llmvia(/local)`, `internal/{prompts,tenantllm,degradation}` | — |
| F5 | `modulos/catalogo` | `internal/flujos/modules/cart/catalog.go`, `internal/intake/catalogo`, `internal/catalogimport` | — con D-F5-1 = B (se reconstruye `conversacion/model` aquí: es hoja); con A, → `flujos/model` hasta F8 |
| F6 | `modulos/solicitudes` | `internal/intakes/**` (+ `flujos/modules/cart/note.go`), `internal/integrations/**`, `internal/tenantvars`, `internal/contracts` (desaparece) | `intakes/telemetria` → `flujos/store` (viejo) hasta F8 |
| F7 | `modulos/captacion` | `internal/intake`, `internal/intake/{pipeline,stages,anclaje}`, `internal/{intakeahead,evidence,reanalisis,casebank,intentcfg}` | `stages` → `flujos/store`; `reanalisis` → `flujos/events` (y → `flujos/runtime` si T7.12 no lo evita) hasta F8. `cart` ya no: catálogo en F5, `SanitizeNote` en F6 |
| F8 | `modulos/conversacion` | `internal/flujos/**` (menos `contact`), `internal/turnoacotado` (23 ficheros de producción solo en `runtime`) | **se retiran todos** al cerrar, con los adaptadores (§2.1) |
| F9 | `test/procesos` | los 107 ficheros de integración viejos, **solo para consultar** | — |
| F10 | `cmd/server` → `arranque` | borra `internal/bootstrap`, `cmd/server-modular`, `internal/publicapi` y todos los paquetes viejos | cero |

Las carpetas del plan siguen la misma numeración: `plan/F<n>-<nombre>/` (seis ficheros cada una,
[`plantilla-de-fase.md`](plantilla-de-fase.md)), `plan/FX-cara-http/` (transversal) y
`plan/sesiones/` (los bloques numerados en sesiones globales).
