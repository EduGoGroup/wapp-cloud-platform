# Producto — qué es la reconstrucción, por qué, y qué no se toca

> *Steering* de producto del plan. Medido el **2026-09-28** sobre `dev` @ `1b18932`. Cada número
> dice con qué comando se contó; si vas a fiarte de uno para decidir, **vuelve a correrlo**.

## 1 · Qué es

`internal/` de `wapp-cloud-platform` se **reconstruye** en `internal/modulos/<módulo>/`: cada
fichero del árbol nuevo **se crea** (nada se mueve), primero con su **contrato sin lógica** y un
**test escrito desde ese contrato**, en rojo; después con la lógica portada del paquete viejo, en
verde. Mientras dura, conviven **dos arranques**: `cmd/server` (el viejo, lo que corre en UAT y el
**oráculo**) y `cmd/server-modular` → `internal/arranque` (el nuevo). Al final (F10, el **relevo**)
`cmd/server` pasa a usar el arranque nuevo y el código viejo se borra con sus tests. La norma es
[`05`](../../05-metodo-contratos-y-tdd.md); este fichero no la repite.

## 2 · Por qué: el objetivo de Jhoan

🔒 Jhoan Medina, 2026-09-27: **limpiar los tests en primera instancia**. El árbol por módulos es el
vehículo; el fin es que cada fichero **nazca cubierto** y que el test de un fichero se encuentre.

| Hoy (`05` §1, recontado el 2026-09-28) | Cifra | Cómo |
|---|---:|---|
| Ficheros de producción en `internal/` + `cmd/` | **349** | `find internal cmd -name '*.go' ! -name '*_test.go' \| wc -l` |
| Ficheros de test | **534** | `find internal cmd -name '*_test.go' \| wc -l` |
| Tests con fichero homónimo (`x.go` ↔ `x_test.go`) | 190 | `05` §1 (no recontado) |
| Tests organizados por escenario, sin homónimo | 344 | `05` §1 (no recontado) |
| Paquetes Go del módulo | **78** (73 en `internal/`, 5 en `cmd/`) | `GOWORK=off go list ./... \| wc -l` |

Tres promesas, las tres comprobables por un candado (`05` §5):

1. **Un fichero ↔ un test** (E-3): para saber qué cubre `x.go` se abre `x_test.go`.
2. **Contratos explícitos por módulo** (E-2): cada exportado dice qué recibe, qué devuelve y qué
   errores da **antes** de que exista la lógica; esa es la conversación de fronteras que ADR-0010
   pedía (ADR-0010, fuera de este repo: *monolito modular por dominios; la comunicación entre
   módulos pasa por una API interna explícita*).
3. **Cobertura desde el nacimiento** (E-9): cada exportado aparece en su test ya en rojo; en verde,
   ≥ 80 % de sentencias **por fichero** (D-12), fuera los adaptadores Postgres.

## 3 · Qué NO es

- **No cambia comportamiento.** Ni una ruta, ni un rpc, ni una tabla, ni una variable, ni un texto
  que vea un humano (§4). Si un diff lo cambia, la tarea está mal hecha.
- **No resuelve lo de [`01`](../../01-factibilidad.md) §5**, y no se promete:
  - las **14 tablas compartidas** entre módulos (ADR-0010 incumplido; `../../../arquitectura.md`
    §4), incluida la escritura de `gateway/lease` sobre `public.tenants`;
  - las **cinco goroutines de fondo sin supervisor** (`internal/bootstrap/arranque/fase9_fondo.go:54,63,75,84,99`);
  - el resto de [`deuda.md`](../../../deuda.md);
  - el **ciclo de negocio** conversación ↔ captación ↔ solicitudes (D-7: se congela y se vigila con
    la lista blanca de `fronteras_test.go`; cortarlo es otro plan).
- **No impone hexagonal.** Cada paquete conserva su forma; `iam` sigue siendo la única zona
  hexagonal (constitución §5).
- **No sube Go ni el lint**, no añade migraciones (el esquema queda en `0.48.0`) y **no trae
  funcionalidad nueva**: mientras dure, todo arreglo se hace dos veces (en lo viejo, que corre en
  UAT, y en lo nuevo si ya existe) — `05` §9.1.
- **No toca nada fuera de este repo.** La documentación del ecosistema (2.565 menciones de rutas
  `internal/` en 150 ficheros), la regla de conteo de ADR-0010 y 5 comentarios en repos hermanos los
  realinea **después** una sesión local (`03` §2.1).

## 4 · Los contratos hacia fuera que NO se tocan

Resumen de [`03`](../../03-pendientes-y-contratos.md) §1, **recontado contra el código** el
2026-09-28. La columna «Vigila» dice qué lo protege durante la reconstrucción.

| Contrato | Cifra | Dónde vive hoy | Cómo se contó | Vigila |
|---|---|---|---|---|
| **Rutas HTTP** | **95** patrones: 66 + 7 en `:8103`, 22 en `:8100` | `internal/publicapi/{publicapi.go (51), roleplane.go (14), eventstelemetry.go (1)}` · `internal/bootstrap/arranque/http.go` (5, en 6 líneas: `POST /api/v1/signup` va en las dos ramas de un `if`, `:165` y `:168`) · `internal/iam/transport/http/auth.go:49-50` (2) · `internal/bootstrap/arranque/rutas_admin.go:64-112` (22) | `grep -hoE '(mux\|Mux)\.Handle(Func)?\("[^"]+"'` sobre esos 6 ficheros `\| sort -u \| wc -l` → 95. **Regla**: patrón registrado en producción, contado una vez; fuera los comentarios (`internal/platform/metrics/flowlifecycle/collector.go:8`) y `flujos/admin/handlers.go:345-346` (su `Register` solo lo llaman tests) | `huella_test.go` + procesos F9 |
| Permisos de ruta (scope, `.any` de plataforma) | I-CP-5 | `protect(...)` de `publicapi`, `adminHandler(...)` de `rutas_admin.go`, migración `0060` | `platform_permissions_test.go` (lee el texto `"platformadmin."`) | Contrato del arranque nuevo + proceso de acceso |
| **rpc gRPC** | **2**: `Connect` (bidi, `:8101`, mTLS) · `EnrollEdge` (unario, `:8102`, TLS de servidor) | `internal/gateway/grpc/server.go:363` · `internal/gateway/enroll/server.go:67` | `grep -rn 'Register.*Server(' internal` → 2; proto `wapp-cloudlink v0.17.0`, `cloudlink.proto:11,38` | `go.mod` sin cambios en `wapp-cloudlink` · huella |
| Mensajes `EdgeToCloud` atendidos | **10** | `internal/gateway/grpc/connect.go` | `grep -c 'case \*cloudlinkv1.EdgeToCloud_' …/connect.go` → 10 | Contrato de `edge/grpc` |
| **Tablas y vistas** que toca el código | **47** nombres (una es la vista `event_content`; incluye `schema_version`) | `internal/platform/storage/postgres/migrations/structure/` | `grep -rhoiE '(FROM\|INTO\|UPDATE\|JOIN)[[:space:]]+public\.[a-z_]+' --include='*.go' internal/ \| sed 's/.*public\.//' \| sort -u \| wc -l` → 47 | **No se mueve esa carpeta** (runner *full-replay*) |
| Esquema | **`0.48.0`**, **84** migraciones (`0001`…`0086`, faltan `0020` y `0021`) | `migrations/version.go:435` | `ls …/structure/*.sql \| wc -l` → 84 | `migrate -status` idéntico |
| **Variables de entorno** | **71** = 69 por `loader.Get*` + `FLOW_REPLY_RATE` por el ayudante `getFloat` (`config.go:818`, fuera del patrón `loader.Get`) + `WAPP_CONFIG_FILE` (por `os.Getenv`, `config.go:735`). `03` y `contratos.md` dicen 70: olvidan `FLOW_REPLY_RATE` | `internal/platform/config/config.go:730` (`Load`) | `awk '/^func Load\(\)/,/^}/' internal/platform/config/config.go \| grep -oE '(loader\.Get[A-Za-z]+\(\|get[A-Z][A-Za-z]*\(loader, )"[A-Z_0-9]+"' \| sort -u \| wc -l` → **70** (+ `WAPP_CONFIG_FILE` = 71), medido el 2026-09-29 sobre `dev` @ `7021144`; `grep -rnE 'os\.(Getenv\|LookupEnv)\(' --include='*.go' internal cmd \| grep -v _test` → solo `config.go:735` | Huella de nombres. ⚠️ **Nombre efectivo `WAPP_` + sufijo** (`EnvPrefix`, `config.go:24`): se busca el sufijo |
| Flags de CLI | `migrate -status` · `prompts -volcar -comprobar` · `casebank -tenant -consentido` (+ `debug_inferencia -model -ollama -force-pipeline`, utilidad fuera de banda) | `cmd/*/main.go` | `grep -nE 'flag\.(String\|Bool\|Int\|Duration)\(' cmd/*/main.go` | `cmd/` no se mueve |
| **Métricas Prometheus** | **17** nombres estáticos + **5** descriptores `NewDesc` | `internal/platform/metrics/metrics.go:72-502` · `inferstats.go:69-89` | `grep -rnE 'Name:[[:space:]]*"wapp_' --include='*.go' internal/ \| grep -v _test` → 18, menos `config.go:698` (`wapp_cloud`, nombre de BD) = 17 | Huella. 🔴 `wapp_auth_logins_total` no existe y no vuelve (`metrics_test.go:48-49`) |
| Goroutines de fondo | **5** | `fase9_fondo.go:54` outbox CRM · `:63` colector de ciclo de vida · `:75` agregador · `:84` intakeahead · `:99` worker del pipeline (W=1, I-CP-4) | `grep -nE 'go [a-zA-Z_.]+\(' …/fase9_fondo.go` → 5 | Huella |
| Textos de error en texto plano | prefijos de paquete (`"platformadmin: …"`) | `errors.New` de cada paquete | — | Contrato: **se copian literales** (`contrato-tdd`, paso 1) |
| 🔒 Literal `AVISO_SESION_PASIVA_V1` | byte a byte | `internal/gateway/grpc/greeting.go:13` + [`literal-aviso-sesion-pasiva.md`](../../../literal-aviso-sesion-pasiva.md) | `greeting_internal_test.go:281,327` | Contrato de `edge/grpc`; el `.md` **no se edita** |
| Contrato CRM `wapp-crm-v1` | 3 verbos: `intake.push` (outbox + HMAC) · `intake.status` (callback) · `catalog.pull` (**422** «catalog.pull diferido») | `docs/contracts/wapp-crm-v1/` (dentro de este repo) | `ls docs/contracts/wapp-crm-v1/` | La carpeta no se mueve; los tests que la leen solo ajustan su `../..` |
| **Doble llave** | DEK del cliente / Lease del servidor | `internal/gateway/lease/` (TTL `DefaultTTL = 15 min`, `lease.go:33`) | — | 🔒 `lease` se reconstruye **sin cambiar comportamiento** (F3) |

🔴 **Rutas condicionales.** Muchas solo se montan si su dependencia en `publicapi.Deps` no es `nil`
(`internal/publicapi/roleplane.go:75`); `POST /api/v1/members` se monta siempre y degrada a 503. La
huella de los dos arranques **tiene que compararse con la misma configuración**, o dará diferencias
que no son del código.

**Doble llave, dicho entero** (ADR-0007, fuera de este repo): la **DEK** (AES-256) cifra el almacén
de `whatsmeow` en el Edge, la custodia el cliente y **nunca cruza a la nube**; el **Lease** lo emite
y **revoca** este repo y es el kill-switch anti-clon. Hacen falta los dos para despachar. 🔴 La
`DEK` que aparece en el código de este repo (`internal/intakes/buyerdata.go:11`, columnas `*_dek`)
es la del **envelope de PII de negocio**, otra cosa: ver [`glosario.md`](glosario.md).

## 5 · Los siete módulos (D-5) y el soporte

| Módulo | Qué es, en una línea | Paquetes viejos de referencia | Fase |
|---|---|---|---|
| `nucleo` *(soporte)* | La identidad de contacto que comparten varios módulos (PII cifrada con índice ciego) | `flujos/contact` | F1 |
| `acceso` | Quién eres y qué puedes: RBAC multi-tenant, canje de tokens, planes y *features*, operador de plataforma | `iam/**`, `platformadmin`, `entitlements` | F2 |
| `edge` | El túnel con cada Edge: gRPC, enrolamiento, **lease**, flota, sesiones, acuses, diagnóstico | `gateway/**`, `diagnostics`, `inferstats`, `receipts`, `ingest`, `filtercfg` | F3 |
| `inferencia` | El LLM: vía `local`\|`api`, prompts P2–P5, credencial del tenant, degradación | `llmvia/**`, `prompts`, `tenantllm`, `degradation` | F4 |
| `catalogo` | El catálogo del tenant: modelo (sacado del carrito), índice de búsqueda, importador | `flujos/modules/cart/catalog.go`, `intake/catalogo` (→ `indice`), `catalogimport` | F5 |
| `solicitudes` | La solicitud y su vida: bandeja, 11 estados, P5, CRM, variables del tenant | `intakes/**` (+ `cart/note.go`), `integrations/**`, `tenantvars` | F6 |
| `captacion` | La cola que convierte una conversación en borrador: ventana, `intake_jobs`, P2→P4, match, draft | `intake`, `intake/{pipeline,stages,anclaje}`, `intakeahead`, `evidence`, `reanalisis`, `casebank`, `intentcfg` | F7 |
| `conversacion` | El Motor de Flujos y sus cuatro módulos (menú, encuesta, carrito, media) | `flujos/**` (menos `contact`), `turnoacotado` | F8 |
| `platform` *(soporte)* | Config, cripto, métricas, BD, HTTP transversal. **No es módulo**; lo comparten los dos arranques | `platform/**` (se queda; tres ✎ en F0) | F0 |
| `arranque` *(soporte)* | El cableado nuevo, por módulo | `bootstrap` + `bootstrap/arranque` | F0 → F10 |
| `apipublica` *(soporte, D-10)* | La cara HTTP `/api/v1` **nueva**, construida por olas delante de la vieja | `publicapi` (vieja, hasta F10) | F0 vacía → F2–F8 |

`contracts` desaparece como paquete (F6): su validación del esquema CRM pasa al contrato de
`integrations`. El detalle de paquetes y la fase de cada uno, en [`estructura.md`](estructura.md) §5.

## 6 · Decisiones cerradas

| Id | Decisión | Fecha | Fuente |
|---|---|---|---|
| M-1 | **No es un movimiento: es una reconstrucción** por contrato → rojo → verde; el código viejo no se toca | 2026-09-27 | `ESTADO.md` 1 · `05` |
| M-2 | **Dos arranques en paralelo**: `cmd/server` (oráculo) y `cmd/server-modular` → `internal/arranque` | 2026-09-27 | `ESTADO.md` 2 |
| M-3 | **Los tests viejos no se portan**: se consultan (E-8) | 2026-09-27 | `ESTADO.md` 3 |
| M-4 | **La integración se escribe de cero, por proceso**, caja negra, contra los dos binarios (F9); es condición del relevo | 2026-09-27 | `ESTADO.md` 4 · `05` §7 |
| M-5 | **testcontainers** con una instancia compartida por corrida y una base clonada por proceso; **nunca un Postgres vivo** | 2026-09-27 | `ESTADO.md` 5 · `05` §7.2 |
| M-6 | **Toda la documentación de este trabajo va a `dev`**; `main` solo a petición de Jhoan | 2026-09-27 | `ESTADO.md` 6 |
| M-7 | **Implementa Claude Code en la web**, que solo ve este repo | 2026-09-27 | `ESTADO.md` 7 |
| M-8 | **F1 es un piloto con parada**: tras `nucleo/contact` no se sigue sin decisión de Jhoan | 2026-09-27 | `05` §6, §9.1 |
| M-9 | D-4 se relaja: un paquete nuevo **puede** cambiar de nombre si **los textos observables** no cambian | 2026-09-27 | `05` §8 |
| D-2 | Árbol `internal/modulos/<m>/` | 2026-09-27 | sesión de plan |
| D-5 | **Siete módulos**: `acceso` (fusionado con `platformadmin` + `entitlements`), `edge`, `conversacion`, `catalogo`, `captacion`, `inferencia`, `solicitudes`, más `nucleo`, `platform`, `arranque`; los casos dudosos quedan donde los pone `04` | 2026-09-27 | sesión de plan |
| D-9 | `cmd/server-modular` temporal → `internal/arranque`; desaparece en F10, donde `cmd/server` usa el arranque nuevo (`go build -o bin/server ./cmd/server` no cambia). **Una** prueba en UAT en sustitución antes del relevo | 2026-09-27 | sesión de plan |
| D-10 | **Cara HTTP única nueva, por olas (estrangulador)**: `internal/apipublica` nace vacía en F0, el arranque nuevo la monta **delante** de `publicapi`; cada fase muda sus rutas en el ciclo en que conmuta; al cerrar F8 el viejo no sirve ninguna ruta en el binario nuevo; en F10 se borra. **Sustituye** a la recomendación de `03` D-10 y a `05` §9.2 («repartir en `modulos/<m>/http/`») | 2026-09-27 | sesión de plan · [`FX-cara-http/`](../FX-cara-http/README.md) |
| D-11 | Etiquetas `pendiente` (rojo) e `integracion` (procesos); **cero `t.Skip`** en código nuevo | 2026-09-27 | sesión de plan |
| D-12 | **80 %** de sentencias por fichero al llegar a verde, fuera los adaptadores Postgres; se recalibra tras F1 | 2026-09-27 | sesión de plan |
| W-1 | **Docker en la web**: la primera sesión web **prueba** testcontainers; si funciona, la web corre los procesos como **pre-chequeo**, pero **F9 y el relevo los cierra la sesión local** | 2026-09-27 | sesión de plan |

**Abiertas, que el plan asume con su recomendación** (`03` §3): D-1 alcance (opción 2: corregir
ubicaciones — catálogo fuera del carrito, `nucleo`, ✎ de `platform`) · D-3 aplanar (sí) · D-7 ciclo
de negocio (congelar) · D-13 lista de procesos (la de `05` §7.4; se cierra antes de F9) · el doble
de R2/S3 del arnés de F9 (`ESTADO.md`, «Pendientes»). D-6 y D-8 quedaron superadas.

## 7 · Cuándo está terminado

El plan termina en **F10 · Relevo** cuando, a la vez: `cmd/server` arranca `internal/arranque`;
`internal/bootstrap/`, `cmd/server-modular/`, `internal/publicapi/` y los paquetes viejos no
existen; `sin_pendientes_test.go` pasa (cero `pendiente.Implementar`); `fronteras_test.go` no
declara **ningún** puente; los procesos de F9 pasan contra el binario único; y **un** despliegue de
UAT con el binario de siempre funcionó (lo cierra la sesión local).
