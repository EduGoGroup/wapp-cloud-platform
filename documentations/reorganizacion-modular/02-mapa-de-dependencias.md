# 02 · Mapa de dependencias — lo medido y la agrupación candidata

> Medido el **2026-09-27** con `GOWORK=off go list` sobre `7cd3a0c`. **Regla de conteo**: una
> arista A → B existe si un fichero **de producción** (no `_test.go`) del paquete A importa el
> paquete B. Los tests no cuentan para el grafo (sí para los candados de `01` §3.1). El script para
> volver a medirlo está en §5: **vuelve a correrlo antes de fiarte de estas tablas**, porque el
> repo se mueve.

---

## 1 · Inventario por carpeta de primer nivel

29 carpetas bajo `internal/`, 78 paquetes Go. Líneas contadas con `wc -l` (incluyen comentario,
que aquí es ~47 % del código de producción).

| Carpeta | Ficheros prod | Líneas prod | Líneas test |
|---|---:|---:|---:|
| `flujos` | 79 | 25.200 | 48.202 |
| `intakes` | 28 | 10.237 | 14.904 |
| `intake` | 24 | 9.218 | 15.101 |
| `publicapi` | 33 | 8.762 | 22.182 |
| `iam` | 44 | 8.209 | 8.873 |
| `gateway` | 28 | 7.920 | 14.714 |
| `platform` | 27 | 5.648 | 7.098 |
| `bootstrap` | 22 | 3.644 | 3.044 |
| `catalogimport` | 6 | 2.412 | 1.708 |
| `integrations` | 9 | 1.629 | 2.736 |
| `llmvia` | 4 | 1.585 | 2.026 |
| `platformadmin` | 4 | 1.558 | 2.360 |
| `intakeahead` | 3 | 955 | 1.287 |
| `casebank` | 4 | 786 | 1.000 |
| `reanalisis` | 1 | 772 | 1.234 |
| `entitlements` | 3 | 683 | 794 |
| `degradation` | 2 | 676 | 1.034 |
| `turnoacotado` | 3 | 624 | 600 |
| `prompts` · `tenantllm` · `diagnostics` · `receipts` · `inferstats` · `tenantvars` · `filtercfg` · `ingest` · `intentcfg` · `evidence` | 1-2 c/u | 80-407 c/u | — |
| `contracts` | 0 | 0 | 176 (solo un test) |

Lectura: **cuatro carpetas (`flujos`, `intake`, `intakes`, `publicapi`) son el 58 % del código de
producción**, y 17 de las 29 tienen menos de 1.000 líneas. El problema de legibilidad no es el
tamaño: es que **29 cajas planas no dicen cuáles van juntas**.

---

## 2 · Quién depende de quién hoy (por carpeta de primer nivel)

`bootstrap` (composition root) y `publicapi` (la cara HTTP) dependen de casi todo, por diseño, y se
omiten. El resto:

| Carpeta | Depende de |
|---|---|
| `catalogimport` | flujos |
| `entitlements` | platform |
| `filtercfg` | gateway |
| `flujos` | entitlements, gateway, intake, intakes, integrations, platform |
| `gateway` | diagnostics, flujos, iam, inferstats, platform |
| `iam` | entitlements |
| `intake` | evidence, flujos, intakes, platform |
| `intakeahead` | evidence, intake, intentcfg |
| `intakes` | flujos, platform |
| `integrations` | intakes, platform, tenantvars |
| `llmvia` | degradation, gateway, tenantllm |
| `platform` | **gateway, iam, inferstats** ← la base depende de dominios |
| `platformadmin` | iam, platform |
| `reanalisis` | entitlements, flujos, intake, intakes, tenantllm |
| `tenantllm` · `tenantvars` | platform |
| `turnoacotado` | flujos, llmvia |

Más importados (fan-in): `platform` 13 · `flujos` 8 · `gateway` 6 · `intakes` 6 · `iam` 5 ·
`entitlements` 5 · `intake` 5.

**Ciclos entre carpetas ya hoy**: `platform ↔ gateway`, `flujos ↔ gateway`, `flujos ↔ intake`,
`flujos ↔ intakes`. Go no los ve porque son entre *carpetas*, no entre *paquetes*.

---

## 3 · Los paquetes que están en la carpeta equivocada

Son los que fabrican esos ciclos. Qué símbolos cruzan, contados con `grep -oE 'pkg\.[A-Z]\w*'`
sobre ficheros de producción:

### 3.1 · El catálogo, dentro del carrito

`internal/flujos/modules/cart` (41 ficheros) contiene el **modelo del catálogo** en `catalog.go`
(582 l), `note.go` (150 l) y `revalidate.go` (195 l). Fuera del motor conversacional lo usan:

| Consumidor | Símbolos (nº de usos) |
|---|---|
| `intake/**` | `Catalog` 30 · `Article` 25 · `Category` 14 · `Variant` 6 · `ParseCatalog` 6 · `SanitizeNote` 5 · `PriceListOf` 2 |
| `catalogimport` | `ParseCatalog` 8 · `SystemSKUPrefix` 6 · `Catalog` 6 · `Variant` · `Component` · `CatalogWarning` |
| `reanalisis` | `SanitizeNote` 5 |

→ **Candidato a paquete propio `catalogo`**, del que dependerían el carrito, la captación y el
importador. ⚠️ Hay que comprobar antes que `revalidate.go` no tire de estado del carrito.

### 3.2 · La identidad de contacto, dentro de flujos

`internal/flujos/contact` — `Ref` 7 · `Normalize` 4 · `PostgresResolver` 3 · `KindPhoneE164` 3 ·
`Resolver` · `KindWAUsername`, usado por `gateway/fleet`, `gateway/grpc` e `intakes`.

→ **Candidato a núcleo compartido** (`nucleo/contact` o similar). Es PII cifrada con índice ciego:
mover **no** debe tocar su cifrado ni sus tablas.

### 3.3 · La base que depende de dominios

| Arista | Qué usa |
|---|---|
| `platform/httpapi → iam/ports/in` | `AuditInput` (5), `Auditor` (1): el middleware de auditoría |
| `platform/httpapi → gateway/session` | `ErrSessionOffline` (3) |
| `platform/metrics → inferstats` | el colector de inferencia |

→ Tres inversiones pequeñas (una interfaz o un error centinela bajado a `platform`), o bien sacar
el middleware de auditoría de `platform` al módulo de acceso.

---

## 4 · Una agrupación CANDIDATA y los ciclos que sobreviven

> ⚠️ **Candidata, no decidida.** Es la hipótesis con la que se midió; los nombres y los bordes
> son decisiones abiertas (`03` §3).

| Módulo candidato | Paquetes actuales |
|---|---|
| `acceso` | `iam/**` |
| `operador` | `platformadmin`, `entitlements` |
| `edge` | `gateway/**`, `diagnostics`, `inferstats`, `receipts`, `ingest`, `filtercfg` |
| `conversacion` | `flujos/**` (menos `contact`), `turnoacotado` |
| `catalogo` | `catalogimport`, `intake/catalogo` (+ el catálogo de §3.1 cuando se extraiga) |
| `captacion` | `intake/**` (menos `catalogo`), `intakeahead`, `evidence`, `reanalisis`, `casebank`, `intentcfg` |
| `inferencia` | `llmvia/**`, `prompts`, `tenantllm`, `degradation` |
| `solicitudes` | `intakes/**`, `integrations/**`, `contracts`, `tenantvars` |
| `nucleo` | `flujos/contact` |
| *(sin módulo)* | `platform` (soporte), `publicapi` (cara HTTP), `bootstrap` (cableado) |

### 4.1 · `intake` e `intakes` no son lo mismo, y por eso van a módulos distintos

Se llaman casi igual y el propio código avisa (`internal/intake/store.go`: *«⚠️ NO CONFUNDIR CON
`internal/intakes` (en plural)»*). Uno es **el proceso**; el otro, **el objeto que produce**.

| | `internal/intake` (singular) → **captación** | `internal/intakes` (plural) → **solicitudes** |
|---|---|---|
| Qué es | La **cola de trabajo** que convierte una conversación en un borrador | La **solicitud**: pedido y presupuesto son el mismo objeto (ADR-0031) |
| Qué contiene | Ventana de agregación · máquina de `intake_jobs` · el worker (`pipeline/`) · las etapas P2→P3→P4→match→draft (`stages/`) · `anclaje/` · `catalogo/` | Cabecera y líneas · máquina de **11 estados** (`status.go`) · acciones de la dueña (aprobar, descartar, editar, pedir info, seña, envío, vencimiento) · revisiones · PII del comprador cifrada (`buyerdata.go`) · notificador · P5 (`quotetext/`) · `telemetria/` |
| Tablas | **Solo `intake_jobs`** | `intakes`, `intake_items`, `intake_revisions`, `intake_buyer_data` (y lee `tenant_settings`) |
| Cuándo actúa | Mientras el cliente escribe y justo después | Desde que existe el borrador hasta que se cierra |

**Cómo se tocan.** La dependencia va en **una sola dirección**, `intake → intakes`: la importan
`intake/pipeline/pipeline.go`, `intake/stages/{draft,match,match_lineas}.go`, porque la última
etapa (`draft`) **crea** la solicitud. En BD el puente es la FK lógica `intake_jobs.intake_id`.
`intakes` **nunca** importa `intake`. Es de los cortes más limpios del grafo, y la carpeta padre
(`modulos/captacion/intake` frente a `modulos/solicitudes/intakes`) deshace la confusión de
nombres **sin renombrar paquetes** (`03` §3, D-4).

**Dos bordes a revisar antes de fijar la agrupación** (van a D-5 de `03` §3):

1. **P5 (`intakes/quotetext`) queda en solicitudes aunque es una etapa LLM.** Tiene sentido:
   actúa sobre una solicitud que ya existe y la dispara la dueña, no el pipeline. Pero deja las
   etapas LLM repartidas: P2–P4 en captación, P5 en solicitudes.
2. **El re-análisis es un caso de uso con una pieza en cada dominio**, no tres copias:
   `internal/reanalisis` (772 l) **orquesta** `POST /api/v1/intakes/{id}/reanalyze`;
   `intake/reanalisis.go` pone lo de la cola (`AbrirReanalisis`, el segundo productor de
   `intake_jobs`); `intakes/reanalisis.go` pone lo de la solicitud (`ReanalysisTargetOf`,
   `PushRevisionByID`). Las dos piezas de dominio se quedan donde están; lo que hay que decidir
   es **dónde vive el orquestador**, que importa de captación, solicitudes **y** conversación
   (`flujos/{runtime,events}`, `cart`). En la candidata va a captación.

Midiendo esa agrupación **sin extraer todavía nada**, quedan **dos componentes con ciclo**:

### Ciclo 1 · la base — `acceso · operador · edge · plataforma · nucleo`

| Arista | Causa |
|---|---|
| `acceso → operador` | `iam/infra/{memory,postgres}` → `entitlements` |
| `operador → acceso` | `platformadmin` → `iam/{domain,infra/postgres,ports/out}` |
| `plataforma → acceso` | `platform/httpapi` → `iam/ports/in` |
| `plataforma → edge` | `platform/httpapi` → `gateway/session` · `platform/metrics` → `inferstats` |
| `edge → acceso` | `gateway/grpc` → `iam/{domain,ports/in}` |
| `edge → nucleo` · `nucleo → plataforma` | `contact`, y `contact` usa `platform/crypto` y `storage/postgres` |

**Se deshace con poco**: fusionar `acceso` y `operador` en un solo módulo (o decidir que
`entitlements` va con `acceso`) y las tres inversiones de §3.3. Tras eso, la base queda en capas:
`plataforma ← nucleo ← acceso ← edge`.

### Ciclo 2 · el negocio — `conversacion · captacion · catalogo · solicitudes`

| Arista | Causa |
|---|---|
| `conversacion → captacion` | `flujos/runtime` → `intake` (`WindowKey` 30, `StatusPending` 20, `MemoryStore`…: la ventana de agregación) |
| `conversacion → solicitudes` | `flujos/modules/cart` → `intakes` · `flujos/runtime` → `integrations/crmpush` |
| `captacion → conversacion` | `intake/{pipeline,stages}` → `cart`, `flujos/store` (`FlowEvent`, `Intake`, `SaveStage`) · `reanalisis` → `flujos/{runtime,events}` |
| `captacion → solicitudes` | `intake/{pipeline,stages}`, `reanalisis` → `intakes` |
| `solicitudes → conversacion` | `intakes/telemetria` → `flujos/store` (escribe `flow_events`) |
| `catalogo → conversacion` | `catalogimport`, `intake/catalogo` → `cart`, `flujos/model` |

**Parte se deshace moviendo** (extraer el catálogo de §3.1 elimina todas las aristas hacia `cart`
que no son del carrito), **pero el núcleo del ciclo es acoplamiento real**: la conversación
alimenta la captación (ventana de agregación) y la captación lee y escribe el almacén de la
conversación (`flow_events`, `store.Intake`). Cortarlo pide interfaces o bajar `flow_events` a un
registro de eventos compartido. **Es trabajo de diseño**, no de carpetas, y se propone dejarlo
delimitado y congelado en la primera fase (`01` §4).

---

## 5 · Cómo volver a medirlo

Autocontenido: solo necesita Go y Python 3, desde la raíz del repo.

```bash
GOWORK=off go list -f '{{.ImportPath}}|{{join .Imports ","}}' ./... 2>/dev/null \
  | sed 's|github.com/EduGoGroup/wapp-cloud-platform/||g' > /tmp/grafo.txt

python3 - <<'EOF'
import collections
# Mapeo candidato: prefijo de paquete -> módulo. Gana el prefijo más largo.
MAP = {
 'internal/iam':'acceso', 'internal/platformadmin':'operador', 'internal/entitlements':'operador',
 'internal/gateway':'edge', 'internal/diagnostics':'edge', 'internal/inferstats':'edge',
 'internal/receipts':'edge', 'internal/ingest':'edge', 'internal/filtercfg':'edge',
 'internal/flujos':'conversacion', 'internal/turnoacotado':'conversacion',
 'internal/flujos/contact':'nucleo',
 'internal/catalogimport':'catalogo', 'internal/intake/catalogo':'catalogo',
 'internal/intake':'captacion', 'internal/intakeahead':'captacion', 'internal/evidence':'captacion',
 'internal/reanalisis':'captacion', 'internal/casebank':'captacion', 'internal/intentcfg':'captacion',
 'internal/llmvia':'inferencia', 'internal/prompts':'inferencia', 'internal/tenantllm':'inferencia',
 'internal/degradation':'inferencia',
 'internal/intakes':'solicitudes', 'internal/integrations':'solicitudes',
 'internal/contracts':'solicitudes', 'internal/tenantvars':'solicitudes',
 'internal/platform':'plataforma', 'internal/publicapi':'api', 'internal/bootstrap':'arranque',
}
def mod(p):
    k = max((k for k in MAP if p == k or p.startswith(k + '/')), key=len, default=None)
    return MAP.get(k, 'cmd' if p.startswith('cmd') else '?? ' + p)
aristas = collections.defaultdict(set)
for l in open('/tmp/grafo.txt'):
    p, imps = l.strip().split('|')
    for i in filter(None, imps.split(',')):
        if i.startswith('internal/'):
            a, b = mod(p), mod(i)
            if a != b and a not in ('api', 'arranque', 'cmd'):
                aristas[(a, b)].add(f'{p} -> {i}')
g = collections.defaultdict(set)
for a, b in aristas: g[a].add(b)
for a in sorted(g): print(f'{a:13s} -> {", ".join(sorted(g[a]))}')
# Componentes fuertemente conexas (Tarjan): cada una con más de un módulo es un ciclo.
idx, low, pila, en, n, comps = {}, {}, [], set(), [0], []
def tarjan(v):
    idx[v] = low[v] = n[0]; n[0] += 1; pila.append(v); en.add(v)
    for w in g[v]:
        if w not in idx: tarjan(w); low[v] = min(low[v], low[w])
        elif w in en: low[v] = min(low[v], idx[w])
    if low[v] == idx[v]:
        c = []
        while True:
            w = pila.pop(); en.discard(w); c.append(w)
            if w == v: break
        comps.append(c)
for v in list(g):
    if v not in idx: tarjan(v)
for c in (c for c in comps if len(c) > 1):
    print('\nCICLO:', sorted(c))
    for a in c:
        for b in c:
            for e in sorted(aristas.get((a, b), ())): print(f'   {a} -> {b}:  {e}')
EOF
```

Cuando se decida el mapeo definitivo, **este mismo script, con el `MAP` final y convertido en test
de Go**, es la base natural del candado de fronteras que propone `01` §4 (Ola 0).
