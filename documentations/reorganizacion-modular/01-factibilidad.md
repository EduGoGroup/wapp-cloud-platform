# 01 · Factibilidad de reorganizar `internal/` por módulos

> Medido el **2026-09-27** sobre la rama local `refactor/arranque-por-fases` (`7cd3a0c`, dos
> commits por delante de `origin/dev` = `9493cea`). Cada cifra dice cómo se contó; el grafo y el
> script para volver a medirlo están en [`02-mapa-de-dependencias.md`](02-mapa-de-dependencias.md).

---

## 1 · Veredicto

**Factible. El riesgo de romper algo hacia fuera es prácticamente nulo; el coste real está hacia
dentro y es de dos naturalezas muy distintas que conviene no mezclar:**

| Tiempo | Qué es | Quién lo verifica | Coste |
|---|---|---|---|
| **A · Mover** | Llevar paquetes a carpetas de módulo y reescribir los import paths. **Cero cambio de comportamiento** | El compilador (100 % de los imports olvidados), más los candados AST que ya existen | Bajo-medio: es volumen, no dificultad |
| **B · Cortar ciclos** | Sacar los paquetes que viven en la carpeta equivocada e invertir un puñado de dependencias con interfaces | Tests unitarios + integración con Postgres real | Medio: trabajo de diseño, módulo a módulo |
| ~~C · Fronteras de datos~~ | Que ningún módulo toque tablas de otro (lo que pide ADR-0010 al pie de la letra: 14 tablas compartidas hoy) | — | Alto. **No se propone ahora**: es una serie de planes posteriores, uno por tabla |

La recomendación es **A y después B, en olas separadas**, y dejar C para cuando un dolor concreto
lo pida (§5).

---

## 2 · Por qué es factible — la evidencia

### 2.1 · `internal/` es invisible fuera del módulo Go

Go prohíbe importar un paquete `internal/` desde otro módulo. **Ningún repo hermano puede
depender de dónde está una carpeta de aquí.** Se comprobó además a mano: en el `edge/`, el
`guardian/` y el `shared/` del ecosistema, `wapp-cloud-platform/internal` aparece **solo en 5
comentarios** (3 en el BFF y la consola del cliente, 1 en `wapp-ctl` del Edge, 1 en un test del
BFF) y **en ningún import**.

### 2.2 · Los contratos externos son de cable, no de carpeta

Todo lo que otros consumen —las 95 rutas HTTP, los 2 rpc gRPC, las 47 tablas, las 70 variables
de entorno, los flags de los CLI, los nombres de métricas, el literal congelado del aviso de
sesión pasiva, el contrato CRM `wapp-crm-v1`— se define por **cadenas y esquemas**, no por la
ruta del fichero que lo implementa. Mover el fichero no cambia la cadena. La lista completa de lo
que no se toca está en [`03-pendientes-y-contratos.md`](03-pendientes-y-contratos.md) §1.

### 2.3 · El grafo de paquetes no tiene ciclos, y el compilador es el árbitro

78 paquetes (`go list ./...`), y Go no admite ciclos entre paquetes. Por tanto **cualquier
reagrupación en carpetas compila** en cuanto se reescriben los import paths, y un import olvidado
**no compila**: no hay forma silenciosa de dejarse uno.

### 2.4 · Se construye aislado, sin los repos hermanos

`GOWORK=off go build ./...` → **rc=0 en 22 s**. `go.mod` no tiene ni un `replace`, y las cuatro
dependencias del grupo (`wapp-cloud-platform`, `wapp-shared`, `wapp-cloudlink`,
`identity-shared`) son **repos públicos** (`gh repo view … --json visibility`). Claude Code en la
web puede compilar y correr los tests unitarios sin credenciales ni `go.work`.

### 2.5 · Hay precedente, y salió bien

El corte de `bootstrap.Run` (991 líneas) en **nueve fases** (deuda D-10, commit `0dc6b88`) fue
exactamente este tipo de cambio: estructura nueva **sin cambiar ni un objeto, ni un cable, ni el
orden**, con un candado (`requiere()`) que convierte un error de orden en un error de arranque con
nombre. El mismo patrón sirve aquí.

### 2.6 · Lo pide el ADR que gobierna esta pieza

ADR-0010 («arquitectura cloud modular», en la documentación del ecosistema) fija un **monolito
modular por dominios, un solo despliegue**, y deja escrito: *«hoy hay 29 módulos bajo `internal/`,
frente a los 4 dominios que el ADR proponía a refinar […] merece una revisión de fronteras»*.
Esta reorganización **es** esa revisión. No contradice ningún ADR: no extrae servicios, no añade
despliegues, no introduce broker ni Redis, no toca la doble llave.

---

## 3 · Qué lo encarece — lo que el compilador NO ve

Esto es lo que hay que tener pendiente. Ninguno es un bloqueo; todos son trabajo o vigilancia.

### 3.1 · Los candados que leen el código fuente por RUTA

El repo se protege con tests que parsean el código (AST) en vez de ejecutarlo. **26 ficheros de
test** usan `go/parser`/`go/ast`. Los que dependen de una ruta relativa o de un import path
escrito como texto son estos:

| Test | Qué ruta fija | Si se mueve el paquete… |
|---|---|---|
| `internal/bootstrap/arranque/invitaciones_cableado_test.go:98` | `"../../publicapi/roleplane.go"` | falla en voz alta ✅ |
| `internal/bootstrap/arranque/roleplane_cableado_test.go:69` | `"../../publicapi/roleplane.go"` | falla en voz alta ✅ |
| `internal/intakes/inv1_aprobar_ast_test.go:46,63-67` | `../publicapi`, `../flujos/runtime`, `../flujos/modules/cart`, `../intake/pipeline`, `../intake/stages`, `../intake` | falla en voz alta ✅ (`:132`, previsto por su autor) |
| `internal/integrations/crmpush/contrato_ast_test.go:46` | `../../flujos/runtime` | falla en voz alta ✅ (`:116`) |
| `internal/intake/catalogo/frontera_test.go:39` | `../../flujos` | falla en voz alta ✅ (exige >100 ficheros leídos) |
| 🔴 `internal/intake/catalogo/frontera_test.go:20` | **import path como constante**: `".../internal/intake/catalogo"` | **PASA EN VACÍO**: si el paquete se mueve y la constante no, el test compara contra una ruta que ya nadie importa y sale verde sin vigilar nada |
| 🔴 `internal/bootstrap/arranque/platform_permissions_test.go:48` | detecta handlers por el **nombre de paquete** `"platformadmin."` | si se **renombra** el paquete (no si se mueve), deja de reconocer las rutas de plataforma — el candado de I-CP-5 |
| `internal/gateway/grpc/greeting_internal_test.go:327` | `"../../../documentations/literal-aviso-sesion-pasiva.md"` | falla (fichero no encontrado) |
| `internal/degradation/degradation_test.go:135` · `internal/tenantllm/postgres_integration_test.go:364` | `../platform/storage/postgres/migrations/structure…` | fallan; el segundo es de integración y **solo corre con Postgres** |
| `internal/contracts/…:17` · `internal/integrations/contract_body_test.go:38` · `internal/publicapi/crmcallback_schema_test.go:14` | `"../../docs/contracts/wapp-crm-v1"` | fallan si cambia la profundidad del paquete |

**Regla que se deriva**: cada ola de movimiento debe (a) reescribir estas rutas en el mismo commit
y (b) comprobar **a mano** los dos casos 🔴, porque un rc=0 no los delata.

### 3.2 · El estilo *comentario-como-ADR*: 707 ficheros citan rutas

La casa escribe en los comentarios **dónde** vive cada cosa. **707 ficheros `.go`** de `internal/`
y `cmd/` citan una ruta `internal/…` (2.264 menciones), más **198** menciones en la
`documentations/` de este repo. No rompen la compilación, pero **se pudren**: una documentación que
apunta a carpetas que ya no existen es peor que ninguna, y este repo vive de ella.

Por eso la reescritura de rutas **no puede ser manual**: tiene que salir de **una única tabla
«ruta vieja → ruta nueva»** aplicada por script a código, comentarios y documentación a la vez.

### 3.3 · Lo que está FUERA de este repo y no puede tocar quien implemente

La documentación del ecosistema (`documentations/` de la raíz de wApp) tiene **2.565 menciones de
rutas `internal/` en 150 ficheros**, más la bóveda de análisis y los 5 comentarios de repos
hermanos. Claude Code en la web no los ve. Los tiene que realinear **después** una sesión con
acceso a la raíz, con la misma tabla de mapeo. Detalle en `03` §2.

### 3.4 · Mover no quita ciclos: los hace VISIBLES

Hoy, agrupando por el primer segmento de `internal/`, ya hay ciclos entre carpetas
(`platform ↔ gateway`, `flujos ↔ gateway`, `flujos ↔ intake`, `flujos ↔ intakes`). La razón es que
**varios paquetes viven en la carpeta equivocada**:

- **El catálogo vive dentro del carrito.** `flujos/modules/cart` define `Catalog`, `Article`,
  `Category`, `Variant`, `ParseCatalog`, `PriceListOf`, `SanitizeNote` (`catalog.go`, `note.go`,
  `revalidate.go`: ~930 líneas), y de ahí los consumen `catalogimport`, `intake/catalogo`,
  `intake/pipeline`, `intake/stages` y `reanalisis`. Es un dominio propio metido en un módulo del
  motor conversacional.
- **La identidad de contacto vive dentro de flujos.** `flujos/contact` lo usan `gateway/fleet`,
  `gateway/grpc` e `intakes`. Es un núcleo compartido.
- **El soporte transversal depende de dominios.** `platform/httpapi` importa `gateway/session`
  (`ErrSessionOffline`) e `iam/ports/in` (`AuditInput`, `Auditor`); `platform/metrics` importa
  `inferstats`. «Plataforma» debería estar abajo del todo y no lo está.

Con una agrupación candidata de ~10 módulos **sobreviven dos ciclos** (detalle y aristas exactas
en `02` §3): uno en la base (acceso · operador · edge · plataforma · núcleo) que se deshace con
tres movimientos pequeños, y otro en el corazón del negocio (conversación · captación · catálogo ·
solicitudes) que es **acoplamiento real** y solo se corta con interfaces.

**Esto no impide mover** (el tiempo A funciona igual con ciclos entre carpetas, porque Go solo
prohíbe ciclos entre paquetes), pero condiciona **qué se promete**: tras el tiempo A el árbol será
legible, no desacoplado.

### 3.5 · Los nombres de paquete y los textos de error son observables

`/api/v1/signup` devuelve sus errores **en texto plano**, y el BFF y `wapp-ctl` los enseñan tal
cual al usuario. Muchos errores llevan el nombre del paquete como prefijo
(`"platformadmin: el tenant_id de la aprobación no existe"`). **Mover** un paquete no cambia su
nombre; **renombrarlo**, sí, y con él textos que ve un humano. Regla: **se mueven carpetas, no se
renombran paquetes** (salvo decisión explícita, `03` §3).

### 3.6 · La mitad de la verificación necesita Postgres

Hay **97 ficheros de integración** que se saltan solos sin `WAPP_TEST_DB_DSN`, y la deuda DT-52
del ecosistema mide que eso son **438 tests en SKIP bajo un rc=0** (el 10,4 % de la suite). Un
movimiento de carpetas puede romper precisamente esos tests (ver `tenantllm` en §3.1). Si el
entorno web no tiene Docker/Postgres, **su verde no basta**: la ola no cierra sin una corrida local
con `make test-integration`, contando los `--- SKIP` con `-v` y leyendo el `rc` sin pipe.

### 3.7 · El tamaño del diff y el trabajo en paralelo

El tiempo A toca prácticamente todos los ficheros del repo (534 de test, ~330 de producción). Un
diff así **choca con cualquier rama viva** que toque los mismos paquetes (el Plan 057 lo hizo hace
días). Necesita una **ventana de congelación**: nada más se fusiona en `dev` mientras dura cada ola.

---

## 4 · Opciones

| | Qué incluye | Valor | Coste | Riesgo |
|---|---|---|---|---|
| **1 · Solo mover** | Tiempo A completo + un candado nuevo que congele las aristas entre módulos que hay hoy (lista blanca) y prohíba las nuevas | Árbol legible · el acoplamiento **deja de crecer** | Bajo-medio | Bajo |
| **2 · Mover + corregir ubicaciones** ✅ | Opción 1 + extraer `catalogo` del carrito, `contacto` a un núcleo, y sacar de `platform` lo que depende de dominios | Árbol legible **y** base sin ciclos · el ciclo de negocio queda delimitado y con nombre | Medio | Bajo-medio |
| **3 · Módulos con API interna y sin tablas compartidas** | Opción 2 + una fachada pública por módulo + las 14 tablas compartidas resueltas | Cumple ADR-0010 al pie de la letra | Alto | Medio-alto: toca SQL y comportamiento |

**Recomendación: la opción 2, en este orden**:

1. **Ola 0 — Preparación** (sin mover nada): el candado nuevo en su forma de *medición*, la tabla
   de mapeo definitiva y el script de reescritura, probado en seco.
2. **Olas de movimiento**, **una por módulo**, empezando por las hojas (los que nadie importa) y
   terminando por la base. Cada una deja el repo verde y es revertible con un `git revert`.
3. **Olas de corrección de ubicación** (catálogo, contacto, plataforma), cada una con sus tests.
4. El ciclo de negocio (conversación ↔ captación ↔ solicitudes) **se documenta y se congela**; se
   corta después, si duele, como plan propio.

La opción 3 no se descarta: es el destino natural de ADR-0010, pero se hace **tabla a tabla**,
cuando haya un motivo, no como parte de esta reorganización.

---

## 5 · Lo que esta reorganización NO resuelve (para no prometerlo)

- **Las 14 tablas compartidas entre módulos** (ADR-0010 incumplido; `../arquitectura.md` §4),
  incluida la escritura de `gateway/lease` sobre `public.tenants`. Mover carpetas no cambia quién
  ejecuta qué SQL.
- **`publicapi` sigue siendo «la cara»** (33 ficheros de producción, 22k líneas de test). Repartir
  sus handlers entre los módulos es posible, pero es la parte más cara y menos necesaria para la
  legibilidad: se propone dejarla como capa de transporte única en una primera fase.
- **Las cinco goroutines de fondo sin supervisor** (`arranque/fase9_fondo.go`) y el resto de la
  deuda de `../deuda.md`.
- **No impone hexagonal** a nadie. La constitución de la pieza dice *modular por capacidad*, con
  `iam` como única zona hexagonal **a propósito**. Cada paquete conserva su forma interna.
