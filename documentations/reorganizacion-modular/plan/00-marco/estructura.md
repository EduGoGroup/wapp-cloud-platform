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
| `internal/modulos/un_fichero_un_test_test.go` · `exportados_cubiertos_test.go` | F0 | se quedan | E-3 y E-9. Candados AST permitidos (E-7) |
| `test/procesos/sin_bd_viva_test.go` | F0 (sin etiqueta) | se queda | Ninguna referencia a `WAPP_TEST_DB_DSN`, puerto fijo o `WithReuseByName` |
| `test/procesos/{main,arnes}_test.go` y un fichero por proceso | F9 | se quedan | `//go:build integracion`. `WAPP_PROCESOS_BINARIO=viejo\|nuevo` |
| `sin_pendientes_test.go` | F10 | se queda | Cero `pendiente.Implementar` |
| `documentations/reorganizacion-modular/traspasos/` | primera vez que un bloque lo necesite | se queda (historia) | `TRASPASO-<fase>-<tema>.md` (skill `traspaso-web-local`) |

Si `internal/modulos/` solo tiene ficheros de test, lleva además un `doc.go` con el comentario de
paquete (excepción de E-3), para que `go vet ./...` y los candados tengan un paquete claro.

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
  función `func Contrato(t *testing.T, nuevo func() <Puerto>)`. El **doble en memoria** que la corre
  en unitario vive en el mismo `<paquete>test` (E-6).
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
| F3 | `modulos/edge` | `internal/gateway/{grpc,enroll,lease,session,fleet}`, `internal/{diagnostics,inferstats,receipts,ingest,filtercfg}` | `gateway` → `flujos` es arista medida (`02` §2): puente a `conversacion` hasta F8 |
| F4 | `modulos/inferencia` | `internal/llmvia(/local)`, `internal/{prompts,tenantllm,degradation}` | — |
| F5 | `modulos/catalogo` | `internal/flujos/modules/cart/catalog.go`, `internal/intake/catalogo`, `internal/catalogimport` | → `conversacion/model` hasta F8 (o se reconstruye `model` aquí: es hoja) |
| F6 | `modulos/solicitudes` | `internal/intakes/**` (+ `flujos/modules/cart/note.go`), `internal/integrations/**`, `internal/tenantvars`, `internal/contracts` (desaparece) | `intakes/telemetria` → `conversacion/store` hasta F8 |
| F7 | `modulos/captacion` | `internal/intake`, `internal/intake/{pipeline,stages,anclaje}`, `internal/{intakeahead,evidence,reanalisis,casebank,intentcfg}` | → `conversacion` (`cart`, `store`, `runtime`, `events`) hasta F8 |
| F8 | `modulos/conversacion` | `internal/flujos/**` (menos `contact`), `internal/turnoacotado` (23 ficheros de producción solo en `runtime`) | **se retiran todos** al cerrar |
| F9 | `test/procesos` | los 107 ficheros de integración viejos, **solo para consultar** | — |
| F10 | `cmd/server` → `arranque` | borra `internal/bootstrap`, `cmd/server-modular`, `internal/publicapi` y todos los paquetes viejos | cero |

Las carpetas del plan siguen la misma numeración: `plan/F<n>-<nombre>/` (seis ficheros cada una,
[`plantilla-de-fase.md`](plantilla-de-fase.md)), `plan/FX-cara-http/` (transversal) y
`plan/sesiones/` (los bloques numerados en sesiones globales).
