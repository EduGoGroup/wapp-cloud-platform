# F3 · `edge` — el túnel con cada Edge: gRPC, enrolamiento, lease, flota, acuses

> **Estado: ✅ cerrada el 2026-10-06** (F3-05 💻: e2e con mTLS real, las 7 suites contra Postgres y el proceso de enrolamiento contra los dos binarios, `make test-procesos` `RC=0 · PASS=812 · SKIP=0` en cada uno; `8121564` … `876b096`, hallazgos 74–83) — F3-01 arrancó el 2026-10-04 sobre `dev` @ `8896f13`, con el inventario E-12 de las hojas
> **aprobado por Jhoan** ([`arquitectura.md`](arquitectura.md) §1.1.a). **F3-02 hecha el 2026-10-04**: `fleet` y `filtercfg` en verde
> (inventario en §1.1.b). **F3-03, tanda 1 de 3 hecha el 2026-10-04**: inventario de `grpc` aprobado (§1.1.c) y `types`,
> `server`, `receipt_sink`, `worklane` y `send` en verde. **F3-03, tanda 2 de 3 hecha el 2026-10-05**: `connect` (en 4 trozos), `auth`, `config_push`, `readiness` y `diagnostics` en verde, `Connect` ya atiende el stream; **F3-03, tanda 3 de 3 hecha el 2026-10-05**: `inference` (en 3 trozos), `plaza` y `greeting` en verde, con el literal 🔒 afirmado byte a byte y la pareja ADR-0048 completa: **`grpc` está entero** (F3-03 cerrada). **F3-04 hecha el 2026-10-06**: `bridge_gateway.go`, la cara nueva de `edge` en `apipublica` (D1–D6) y `conmutar(edge)`: el arranque nuevo cablea **un solo** `edge/grpc.Server`, `bridge_iam.go` borrado, `acceso` en `Conmutados`, huella igual (hallazgos 65–73). **F3-05 hecha el 2026-10-06**: cierre local con mTLS (hallazgos 74–83); sigue F4. Spec escrita el 2026-09-28 sobre `dev` @ `1b18932`, releída en `bad573a`.
> Norma: [`05`](../../05-metodo-contratos-y-tdd.md). Forma: [`00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md).
> Marco común: [`00-marco/`](../00-marco/README.md). Rutas: **autoridad**
> [`FX-cara-http/mapa-de-rutas.md`](../FX-cara-http/mapa-de-rutas.md) (filas D1–D6, J12–J17; E1–E2 se mudan en F7, D-FX-1/D-F7-4) y
> sus tareas TX.8–TX.11. Patrones de [`F1`](../F1-nucleo-contact/README.md): rojo **solo con
> exportados** y adaptadores de arranque `internal/arranque/bridge_<x>.go` (`05` §4.2).
>
> Recalibrado el 2026-10-03 tras la parada de F1 (`05` E-12, §4.2, E-9, E-4; `plan/DECISIONES.md` §3).
>
> ✎ **D-F1-10 (Jhoan, 2026-10-02)**: los paquetes de suite de contrato y de dobles llevan el sufijo compuesto
> **`helpertest`**, el único que los candados de fichero eximen ([`DECISIONES.md`](../DECISIONES.md) §2). Esta spec los
> nombraba con `…test` (`receiptstest`, `ingesttest`, `diagnosticstest`, `leasetest`, `enrolltest`, y `fleettest` para el
> paquete **nuevo**): se actualizó el sufijo, nada más. El `internal/gateway/fleet/fleettest` viejo conserva su nombre.

## Objetivo, en tres líneas

1. Reconstruir `internal/gateway/{grpc,enroll,lease,session,fleet(/fleettest)}` y
   `internal/{diagnostics,inferstats,receipts,ingest,filtercfg}` en `internal/modulos/edge/…`
   (aplanado) por contrato → rojo → verde, **sin cambiar un comportamiento**: 🔒 `lease` es la mitad
   servidora de la doble llave y el kill-switch anti-clon.
2. Conmutar: `cmd/server-modular` construye **un solo** `*grpc.Server` nuevo (conexiones vivas,
   carriles, acuses e inferencias en vuelo) y lo **inyecta** en los consumidores viejos que aún lo
   usan (runtime, notificador de solicitudes, selector LLM vía `bridge_gateway.go`, y el `ConfigPush` de
   la cara vieja para E1–E2 hasta F7); muda 6 rutas de `:8103` y 6 de `:8100` (FX TX.8–TX.11).
3. Conservar byte a byte el literal `AVISO_SESION_PASIVA_V1`, las tres reglas del ADR-0048 (el canal
   de control no es una sesión) y el contrato `wapp-cloudlink v0.17.0`, que no cambia.

## Entradas (tiene que ser cierto para empezar)

| # | Condición | Cómo se comprueba |
|---|---|---|
| E1 | F2 cerrado: `internal/modulos/acceso` en verde y conmutado; `bridge_iam.go` vivo | `ls internal/modulos/acceso` · `grep -rn 'pendiente.Implementar' internal/modulos/acceso \| wc -l` → 0 |
| E2 | F1 cerrado: `internal/nucleo/contact` en verde (lo importan `fleet` y `grpc`) | `ls internal/nucleo/contact` |
| E3 | F0: `session.ErrSessionOffline` **es** el centinela de `platform` y `inferstats.Agregado` **es** alias del tipo de `platform/metrics` (F0 `arquitectura.md` §✎, filas de `admin.go` e `inferstats.go`) | `go list -f '{{.Imports}}' ./internal/platform/... \| grep -cE 'internal/(gateway\|inferstats)'` → 0 |
| E4 | El código viejo no cambió desde esta spec | `git log --oneline 1b18932..origin/dev -- internal/gateway internal/diagnostics internal/inferstats internal/receipts internal/ingest internal/filtercfg` vacío |
| E5 | Decisiones FX D-FX-1, D-FX-2 y D-FX-3 contestadas (o asumidas por recomendación) | [`FX-cara-http/README.md`](../FX-cara-http/README.md) |
| E6 | `dev` verde con la toolchain fijada | skill `validar-antes-de-cerrar` |

## Salidas (es cierto al cerrar)

- `internal/modulos/edge/{grpc,enroll,lease,session,fleet,fleet/fleethelpertest,diagnostics,inferstats,receipts,ingest,filtercfg}`
  con **38** ficheros de producción (+ ✚ de [`diseno.md`](diseno.md) §1) en verde; 0 pendientes; 0 SKIP.
- Un test por promesa del contrato; mutantes en el nivel complejo; procesos de F9. `make cobertura-ficheros` es
  informe (la tabla va al PR; no bloquea). Las 7 suites de puerto con BD, verdes en memoria **y** en Postgres (P4).
- El literal `AVISO_SESION_PASIVA_V1` afirmado byte a byte **y** contra `documentations/literal-aviso-sesion-pasiva.md`
  (solo cambia la ruta relativa: `../../../../documentations/…`).
- `cmd/server-modular`: **un** `*edge/grpc.Server`, inyectado en runtime, notificador, `filtercfg`,
  handlers de `:8100` y (vía `bridge_gateway.go`, con su test de cableado) en el selector LLM; `bridge_iam.go`
  **borrado** y, con él, `acceso` dentro de `Conmutados` (`edge` entra en F4, al morir `bridge_gateway.go`);
  `huella_test` igual (2 rpc, 22 + 73 rutas); `cmd/server` intacto; `go.mod` sin cambios en
  `wapp-cloudlink`.
- e2e de gRPC con mTLS y el proceso «Enrolamiento de un Edge y su lease», en local (con traspaso solo mientras
  existan los dos entornos).

## Orden de lectura

1. [`requisitos.md`](requisitos.md) · 2. [`arquitectura.md`](arquitectura.md) · 3. [`diseno.md`](diseno.md) ·
4. [`reglas.md`](reglas.md) · 5. [`tareas.md`](tareas.md).

## Bloques de sesión

Un bloque por sesión, 45–90 min, **por paquete** (rojo y verde del paquete seguidos, según su nivel E-12). Cada sesión
cierra con tres cosas: tareas `[x]` con SHA, un bloque en `ESTADO.md` y los hallazgos nuevos en este README. Traspaso
web ↔ local solo mientras existan los dos entornos.

| Sesión | Entorno | Tareas | Punto de parada |
|---|---|---|---|
| [**F3-01**](../sesiones/F3-01-web-inventario-y-hojas.md) · inventario E-12 + hojas (`session`, `inferstats`, `receipts`, `ingest`, `diagnostics`, `lease`, `enroll`) | 🌐 | T3.1–T3.9, T3.15–T3.18 | **Jhoan aprueba el inventario** (antes, ni una línea de código) · 0 pendientes en las hojas · PR |
| [**F3-02**](../sesiones/F3-02-web-fleet-filtercfg.md) · `fleet` y `filtercfg` | 🌐 | T3.10, T3.11, T3.19, T3.20 | 0 pendientes en esos dos · suite de `fleet` verde · PR |
| [**F3-03**](../sesiones/F3-03-web-grpc.md) · `grpc` (13 ficheros, complejo; ADR-0048) | 🌐 | T3.12–T3.14, T3.21–T3.23 | 0 pendientes en `edge` · literal y pareja ADR-0048 verdes · PR |
| [**F3-04**](../sesiones/F3-04-web-bridge-conmutar-y-rutas.md) · adaptador, cara nueva y conmutación | 🌐 (TX.10 🌐→💻) | T3.24–T3.28 | huella igual · un gw · 6+6 rutas · cableado verde · `bridge_iam.go` borrado · `acceso` en `Conmutados` · PR |
| [**F3-05**](../sesiones/F3-05-cli-cierre-mtls.md) · cierre local (mTLS real, suites contra Postgres, procesos) | 💻 | T3.29–T3.30 (+ parte 💻 de T3.27) | e2e gRPC verde · 7 suites en Postgres · proceso contra los dos binarios · `dev` empujado |

⚠️ Tamaño **sin medir**: F3-01 (7 paquetes, 2.576 líneas viejas) y F3-03 (`grpc`, 4.566) pueden no caber en 90 min. Si
no caben, la sesión para en un punto limpio de [`tareas.md`](tareas.md), cierra con las tres cosas y se relanza.

## Contradicciones encontradas (con `04`/`05`/marco/FX, medidas contra el código)

1. **`05` E-6 dice que `diagnostics`, `gateway/enroll`, `gateway/fleet`, `gateway/lease` e `ingest`
   no tienen gemelo en memoria.** Lo tienen todos, dentro de ficheros que no se llaman `memory*`:
   `diagnostics.MemoryStore` (`diagnostics/diagnostics.go:107`), `enroll.MemoryStore` y
   `enroll.MemoryEdgeCertRepository` (`gateway/enroll/store.go:48`, `edgecert.go:38`),
   `fleet.MemoryRepository` (`gateway/fleet/fleet.go:380`), `lease.MemoryRepository`
   (`gateway/lease/repository.go:62`), `ingest.MemoryDeduper` (`ingest/dedupe.go:25`). El recuento
   de `05` se hizo por nombre de fichero (D-F3-1).
2. **`00-marco/estructura.md` §5 anuncia un puente `gateway → flujos` (a `conversacion`) hasta F8.**
   Medido con `go list`: la única arista de `gateway/**` hacia `flujos` es `flujos/contact`, que es
   `nucleo/contact` desde F1. **F3 no necesita ningún puente a `conversacion`**; su única arista a
   otro módulo es `grpc → acceso/iam/{domain,ports/in}` (dirección permitida).
3. **FX D-FX-3** propone un puente de **identidad** `var ErrSessionOffline = <viejo>.ErrSessionOffline`.
   Con el ✎ de F0 (`gateway/session/registry.go:22`: `ErrSessionOffline` pasa a **ser** el de
   `platform`, F0 `arquitectura.md` fila `admin.go`), el nuevo `edge/session` puede declarar
   `var ErrSessionOffline = <el de platform>`: identidad compartida **sin** puente a código viejo.
   Recomendado en D-F3-2 (y TX.10 se reduce a eso).
4. **`documentations/contratos.md` §1 y `arquitectura.md` §2.2** dicen que el bucle `Recv` resuelve
   **inline** `Ack` e `InferenceResult`. El código resuelve inline **cuatro**: `Incoming`, `Ack`,
   `InferenceResult` y `Pong` (`gateway/grpc/connect.go:252-305`), más el handshake; al carril van
   `Heartbeat` (coalescido), `Receipt`, `DiagnosticsBundle` y los tres de auth (`:277-350`). ADR-0040
   (fuera de este repo) lo enumera sin `InferenceResult` porque es anterior a él.
5. **`contratos.md` §1** sitúa `route()` en `connect.go:211`; hoy los `case` están en `:252-347`.
6. **`04` §3** da a `gateway/grpc` 37 `_test.go` y `ingest` 3: son los viejos; con `05` serán 13 y 2
   tests nuevos + los paquetes `…helpertest`. Los 7 tests de carga (`load_*`, `curva_pool_t55`,
   `deuda_050_2`) son **mediciones**, no reglas: no tienen sitio ni en F3 ni en F9 (D-F3-5).

## Decisiones que necesita (de Jhoan, con recomendación)

| # | Pregunta | Recomendación |
|---|---|---|
| D-F3-1 | Los gemelos en memoria que hoy viven en ficheros de producción (6, contradicción 1): ¿se quedan ahí o se mudan a `<paquete>helpertest`? | **A `<paquete>helpertest`** (patrón E-6, cuyo origen es `fleettest`; el sufijo es `helpertest` por **D-F1-10**, 2026-10-02: la pregunta se decidió el 2026-09-30 con `<paquete>test`): el fichero de producción queda con el puerto y sus tipos, y el doble corre la suite. Cambia el contenido, no los nombres del árbol de `04`, salvo `fleet/fleettest`, que nace como `fleet/fleethelpertest` |
| D-F3-2 | Identidad de `ErrSessionOffline` entre viejo y nuevo | **Vía `platform`** (contradicción 3), sin puente; FX TX.10 se reduce a un test de identidad `errors.Is(nuevo, viejo)` |
| D-F3-3 | `ingest` no declara puerto (lo declara el consumidor, `flujos/runtime/runtime.go:116`). ¿Se añade `ingest.Deduper` (solo interfaz) para que `ingesthelpertest.Contrato` tenga contra qué correr? | **Sí**, fichero ✚ `ingest/deduper.go` (solo interfaz, E-3) |
| D-F3-4 | 🔒 `lease/repository_postgres.go:106,117` escribe `public.tenants.revoked_at` (deuda D-9: tabla de otro módulo sin API interna). ¿Se corrige en la reconstrucción? | **No**: el lease se reconstruye **sin cambiar comportamiento** (`05` §6); el SQL se copia literal y la deuda sigue anotada |
| D-F3-5 | Los 7 tests de carga/pool del gateway (`load_integration_test.go`, `load_ack_integration_test.go`, `curva_pool_t55_integration_test.go`, `deuda_050_2_pool_integration_test.go`): ¿se reescriben? | **No**: son mediciones publicadas (Plan 050 · T5.x, DEUDA-050.2). Se conservan en el árbol viejo hasta F10 y se dice en el commit |
| D-F3-6 | ¿Se adelanta F9 para correr «Enrolamiento de un Edge y su lease» contra el binario nuevo al cerrar F3? | **Subsumida por D-F9-1** (recomendación: sí): T3.30 **es** la pasada 9C de `edge` (T9.24); solo se tacha si D-F9-1 = no. Es el único oráculo del kill-switch extremo a extremo con mTLS real |
| D-F3-7 | Nivel de los adaptadores Postgres de una sola sentencia (`receipts/postgres.go`, `enroll/edgecert.go`) | ✅ **medio** (Jhoan, 2026-10-04, F3-01) |
| D-F3-8 | Dónde y cuándo van las pasadas de las suites de las hojas contra Postgres | ✅ **en F3-05, en `test/procesos/<x>_contrato_test.go`** (Jhoan, 2026-10-04, F3-01) |

## Entradas, comprobadas el 2026-10-04 (F3-01, `dev` @ `8896f13`)

E1 ✅ (`acceso`, 0 pendientes) · E2 ✅ · E3 ✅ (0) · E4 ✅ con matiz: `git log 1b18932..origin/dev` sobre el código viejo da
**dos** commits, `5305134` y `6d83620`, que son justo los de F0 que E3 exige (`platform` deja de depender de `inferstats` y
de `gateway/session`) · E5 ✅ · E6 ✅ (`make toolchain` `TOOLCHAIN=OK`; `make ci-local` rc=0 antes de tocar nada).
`session.ErrSessionOffline` viejo **es** `platform/httpapi.ErrSessionOffline` (`gateway/session/registry.go:24`): D-F3-2 se
sostiene. `time.Sleep` en los tests viejos de `grpc` (T-16): **20**, en 7 ficheros (`mtls_test` 9, `server_test` 5,
`load_integration` 2, y 1 en `load_ack_integration`, `readiness_orden`, `worklane_internal` y `tenant_revoke`).

## Hallazgos

### F3-01 (2026-10-04)

1. **`ingest/dedupe.go` y `receipts/memory.go` no nacen.** Su único contenido era el doble en memoria, que se muda a
   `<paq>helpertest` (D-F3-1). `ingest` queda en `deduper.go` ✚ + `postgres.go`, y el «38» de la spec baja a **37 (+1 ✚)**
   ficheros de producción (regla: ficheros con declaraciones tras la mudanza).
2. **Valor cero = «procede» (D-F2-10): se dice y no se cambia.** 🔒 `lease.State.Revoked bool` (cero = vigente),
   `TenantRevoked` sin fila → `false` y `Get` con `found=false` → no revocado: cambiarlo es cambiar el lease (`05` §6,
   D-F3-4); el fail-closed lo lleva el `error` (R-L5). `Deduper.Seen` devuelve `false` = «nuevo, procésalo», también con
   error, y el consumidor es fail-open a propósito (`flujos/runtime/incoming.go:1141`); la firma es estructural con
   `runtime.IngestDeduper`. No hay ningún enum con `iota` en los 7 paquetes.
3. **`enroll` no normaliza el código de activación** (ni `TrimSpace`, ni rechaza el vacío): se compara tal cual en memoria
   y en SQL. D-F2-11/13 no aplican; se porta igual y el test lo afirma con un corpus adversario.
4. **Los dobles divergen del real a propósito.** `enroll` en memoria distingue `ErrCodeNotFound`/`Expired`/`Used` y
   Postgres solo devuelve `ErrCodeInvalid`; `diagnostics` en memoria sobrescribe un `command_id` repetido y Postgres falla
   por clave primaria. La suite afirma solo lo común.
5. **`inferstats.Agregado` es alias de `platform/metrics/inferencia`**, no de `platform/metrics` (E3 y `diseno.md` §3 lo abrevian).
6. **Los `integration_test.go` viejos de `lease` y `enroll` llevan tests ajenos**: `fleet` y las migraciones `0002`, `0003`
   y `0058`. No son de estos paquetes; van a F9 (`diseno.md` §6).
7. **Exportados sin uso en producción que se portan igual** (E-1): `receipts.Store.List`, `ingest.WithRetention` y `WithSweep`.
8. **No hay herramienta de mutantes** en el `Makefile`: se hacen a mano (mutar, ver el rojo, deshacer) y se listan en el PR.
9. **T3.7 cita un `repository_integracion_test.go` junto a `lease`** que contradice el patrón de F1/F2 (el tag `integracion`
   solo vive en `test/procesos`). No se escribe: D-F3-8.
10. **R-S4 dice «envíos concurrentes serializados» y el `Registry` no serializa el `Send`**: lo hace el envoltorio por
    stream del gateway (R-G12, F3-03). Se portó lo que hace el código (el registro es seguro en concurrencia y no retiene
    el mutex durante el `Send`); la redacción de `diseno.md` §3 queda por corregir al abrir F3-03.
11. **`diagnostics.Record` y `diagnostics.Bundle` son nominales**: el `Store` nuevo no encaja en los consumidores viejos
    (`publicapi.DiagnosticsStore`, el `BundleReceiver` del gateway viejo) sin convertir tipos. No hace falta adaptador si
    D5/D6 se mudan a `apipublica` y el gateway es el nuevo en el mismo commit (F3-04, T3.28): se comprueba allí.
    `receipts.Sink.Record` e `ingest.Deduper.Seen` sí encajan tal cual.
12. **Para los montajes de F3-05 contra Postgres**: `diagnostics` pide `SetConsent` (upsert en `tenant_diagnostics_consent`)
    y `Expire` (`UPDATE … expires_at` al pasado), y su purga de `CreateRequest` es **global** (borra vencidas de cualquier
    tenant): base propia por caso. `enroll` pide `SeedCode` y un observador `Records` (`SELECT` sobre `edge_certs`); `lease`,
    `SeedTenant`. `receipts` e `ingest` solo piden dos `session_id` únicos.
13. **Divergencias doble ↔ Postgres que las suites no afirman a propósito**: `receipts` (un `ReceiptAt` cero vuelve como
    la época Unix desde Postgres y como cero desde el doble); `lease` (`MarkTenantRevoked` de un tenant inexistente: el doble
    lo marca, Postgres no toca fila); `enroll` (`fingerprint` es `UNIQUE` en Postgres; el doble mira el vencimiento antes
    que el uso).
14. **`diagnostics.Postgres` gana un campo no exportado `now`** (fijado a `time.Now` en `NewPostgres`, sin opción pública)
    para poder matar el mutante del borde exacto del vencimiento. No cambia la API ni la conducta.
15. **Mutantes de F3-01: 132, 131 muertos.** `session` 21/21 · `inferstats` 26/27 · `ingest/postgres` 22/22 ·
    `diagnostics/postgres` 36/36 · `lease/lease` 13/13 · `lease/repository_postgres` 8/8 · `enroll/store_postgres` 5/5. Dos
    nacieron vivos y se mataron con un test (`session`: fuga de la goroutine del `Send`, con `testing/synctest`;
    `diagnostics`: `Record` a medias, `67129db`). 🟡 El vivo es equivalente (`inferstats.cloneCounts` guarda un mapa vacío
    como vacío en vez de `nil`: no se ve por la API exportada); lo mataría un test interno que fije ese detalle.
16. **Sin test**: las ramas de error de `issuer.Issue`/`issuer.Revoke` en `lease.go` (el `Issuer` de `wapp-cloudlink` no
    falla con una clave válida y no se puede inyectar sin tocar producción). `fakedb_test.go` está duplicado en `lease` y
    `enroll` (129 líneas): compartirlo pediría un paquete nuevo.
17. **Dos commits intermedios no pasan todos los gates por sí solos** (el PR entero sí): `9b57756` falla `exportados_cubiertos`
    hasta `715cfcf`, y `f783e76` lleva tres tests sin `gofmt` hasta `10b8d4e`.

**De F3-02** (`fleet` y `filtercfg`, 2026-10-04):

18. **7.º `*fakedb*`**: `fleet/repository_postgres_fakedb_test.go` (292 l) une guion por sentencia, `endErr`, `closeErr`,
    `affected` y `affectedErr` (decisión de Jhoan: variante local). Si aparece un octavo, toca paquete compartido. En el
    driver falso, un NULL para `sql.NullString` es `nil` sin tipo: un `[]byte(nil)` se escanea como válido y vacío.
19. **`repository_postgres.go` medido**: 11 métodos de **una** sentencia, sin transacción ni cerrojo; nivel mixto por trozo
    (complejo `_selfpn` y `_greeting`, medio el resto, D-F3-7). **Mutantes a mano: 31, 31 muertos** (21 de `self_pn`, 10 del
    saludo; 9 solo-SQL los mata la comparación byte a byte). En `filtercfg`, 9 de 9; contra `Memoria`, 10 de 10.
20. **Ramas inalcanzables portadas tal cual**: `"fleet: cerrar filas: …"` y `"fleet: cerrar filas de perfiles: …"` (tras agotar
    las filas, `database/sql` entrega el fallo de `Close` por `rows.Err()`, que sale como `"… iterar …"`), y
    `"filtercfg: serializar payload: %w"` (el `json.Marshal` de esos tipos no falla). Sin test; el contrato lo dice.
21. ✅ ~~🟡~~ *(aceptado por Jhoan el 2026-10-06: se queda como está, divergencia inobservable)* **Única línea que se aparta del viejo**: `scanSession` hace `defaultProfile(Profile(profile))`; el viejo, `Profile(profile)`.
    Inobservable con Postgres (`COALESCE(profile,'passive')` y el `CHECK` de la 0063). Así `defaultProfile` tiene llamante de
    producción en el paquete nuevo (el doble se llevó su copia). Si se prefiere la letra, es revertir una línea y un caso de test.
22. **Divergencias doble ↔ Postgres que la suite no afirma a propósito**: `MarkOffline` / `MarkLoggedOut` de una sesión
    desconocida (el doble crea la fila pasiva, Postgres no: decisión de Jhoan, portar tal cual; lo fija `memoria_test.go`) ·
    orden de `List` · marca nunca fijada (`time.Time{}` frente a `'epoch'`, que **no** es `IsZero`) · errores (prefijo `fleet: …`
    frente al de `contact` desnudo) · contexto cancelado · perfil desconocido en la foto.
23. **Trampa heredada del índice ciego** (afirmada en el corpus, no corregida): con `phone_e164`, `573001112233:5@s.whatsapp.net`
    normaliza a `5730011122335` —el dígito del *device* se concatena— y da **otro** índice. El corpus (26 entradas: `a@@b`,
    árabe-índicos, *fullwidth*, U+00A0, U+2003, U+200B, U+FEFF…) **no diverge** entre la regla vieja y `nucleo/contact.Normalize`.
    Queda por comprobar en F3-03 que `grpc/connect` limpia el JID antes de `SetSelfPn`.
24. **Un caso «gana X entre filas» con dos filas es probabilístico** contra un doble basado en mapa (7 de 8 falsos verdes): se
    siembran varias sesiones y varias filas (`792af20`).
25. **Un adaptador partido por E-13 puede tener un ciclo entre trozos** (el *struct* en uno, los auxiliares en otro): el primer
    verde, `e106e6d`, toca dos ficheros de producción. Y en rojo el *struct* nace vacío (`unused`); los campos llegan con el verde.
26. **`unused` corre con `tests: true`**: un auxiliar no exportado que solo usa su test no se marca; puede nacer un commit antes
    que su llamante. Los tests de auxiliares van en fichero aparte que nace en el verde, para que el rojo compile.
27. **Un `x_test.go` interno no puede importar su `…helpertest`** (ciclo): `fleet.Repository` se nombra por reflexión, con la
    lista cerrada de sus 10 métodos, lo que además vigila que no crezca.
28. **`fleethelpertest` importa `testing`** al compartir paquete con la suite; el `fleettest` viejo lo evitaba a propósito.
    `slowrepo_test.go` usa un temporizador real de 1 ns (ni duerme ni mide) para la rama en que vence la espera.
29. **Tipos nominales** (como el 11): `fleet.Profile` y `TenantProfiles` nuevos no encajan en `flowadmin.ProfilePusher` ni en el
    `Source` viejo; `ConfigPusher` sí es estructural. Lo resuelve F3-04 (T3.28). **Valores cero dichos y no cambiados**
    (D-F2-10): `Profile ""` → pasivo; `State ""` cuenta como vivo; `HealthSnapshot{}.Degraded() == false`; `*Pusher` nil → `nil`.
30. **Para F3-05** (suite de `fleet` contra Postgres): `crypto.NewEnvKeyProvider` con `IndexB64` explícita + `NewFieldCipher`;
    `SeedTenant` inserta en `public.tenants`; `Profiles` llama a `ProfilesByTenant`. Casos propios fuera de la suite, con siembra
    por SQL: guarda de `SetSelfPn` (no reescribe; se auto-sana al rotar la KEK, dos KEK), `degraded_since`, «gana passive»,
    sobre ilegible, `PendingGreeting` / `MarkGreeted` (no están en el puerto) y la carrera de dos `MarkGreeted`.
31. **Comentarios rancios del viejo corregidos al portar**: cuatro de nueve citas `fichero:línea` ya no casaban (ahora nombran
    el símbolo); «el comparando de (3)» → (2); y `SetSelfPn` ya no dice que vacía la columna en claro (retirada en la 0070).

**De F3-03, primera sesión** (`grpc`, tanda 1: tipos, servidor, carril y envío; 2026-10-04):

32. **Hallazgo 23 comprobado: `connect` NO limpia el JID.** `persistSelfPn` hace `contact.Normalize(KindPhoneE164, hb.GetSelfPn())`
    sobre el valor crudo del latido. Un `573001112233:5@s.whatsapp.net` se persistiría como `5730011122335`. Conducta del viejo:
    se copia y se afirma en la tanda 2 (`connect_heartbeat_test.go`); no se corrige. ✅ ~~🟡~~ Comprobado el 2026-10-06 contra `main` de
    `wapp-edge-agent`: el latido lleva `SelfPn: e.selfPN`, y ese valor sale de `domain.SelfPNFromJID`
    (`internal/domain/jid.go`), que corta el agente (`.`), el dispositivo (`:`) y el servidor. **El Edge manda hoy el número
    ya limpio**; que el Cloud no limpie solo muerde con un Edge que no pase por esa función.
33. **`grep pendiente.Implementar` → 0 no significa «`grpc` entero»** cuando se trabaja por tanda: los contratos de las tandas
    siguientes aún no existen. Entre tandas, `Connect` devuelve `Unimplemented` (el `Server` embebe
    `UnimplementedCloudLinkServer`, que deja `Register` en verde sin adelantar el contrato de `Connect`).
34. **Tres ficheros sin exportados no admiten rojo** (`types.go`, `worklane.go`, `send_ack.go`; T-17): nacen en el verde con su
    test. Y el rojo de `server.go` es débil para las `With*` (solo «son `Option` no nulas»): lo que dejan dentro se afirma en
    `server_defaults_test.go`, que nace en el verde porque mira campos no exportados.
35. **El orden no fue «todo el rojo y luego el verde»**: el verde de `types` + `server` va antes del rojo de `send`, porque
    `send_test.go` nombra `defaultAckTimeout` (invariante 8 s < 10 s) y tiene que compilar con `-tags pendiente`.
36. **Cinco commits intermedios (`66bbd83` … `cb9b3b1`) llevan avisos `unused` del lint** (3–7: `acksMu`, `trackMu`, `connCtx`,
    campos de `pendingAck` y `edgeKey`), que desaparecen según llegan sus usuarios; 0 desde `49b43ae`. Candados, `vet` y
    `gofmt` sí pasan en los once. Es el coste del ciclo entre trozos (hallazgo 25).
37. **`sessionsForEdge` se adelantó** a `connect_session.go` (su fichero de destino, tanda 2), solo, con su test: lo
    necesitan `RevokeLease` y `RevokeTenant`. `offlinePersistTimeout` no nace aún: el carril usa `defaultWorkBudget`
    (los dos valen 5 s).
38. ✅ ~~🟡~~ *(resuelto el 2026-10-06 por decisión de Jhoan, `6446e10`: los cuatro tests del carril —los tres del freno y `TestWorkLaneDrainWaitsForTheWork`— corren en burbuja de `testing/synctest`; el mutante «la cola no frena» cae en 40 de 40 procesos con `GOMAXPROCS` de 1 a 4, y «`drain` no espera» cae también. `letOthersRun` desaparece. Contrapartida: un fallo dentro de la burbuja con goroutines aún bloqueadas acaba en `panic: deadlock` y corta la corrida del paquete. Las negativas con `letRun` de `connect` e `inference_result` no se tocaron. Lo que sigue describe cómo estaba)* **Negativas de concurrencia sin reloj: el mutante moría, pero de forma probabilística** (D-F2-12). «La cola llena frena»
    y «`drain` espera» no admiten prueba estrictamente determinista sin tiempo. El test correcto no puede fallar; al mutante
    se le da ocasión con idas y vueltas por el worker de otra sesión y `runtime.Gosched`. Murió en todas las corridas
    (`-race -count=100`, `GOMAXPROCS=1 -count=10`). No se escribió candado AST. **Por decidir (Jhoan)** si basta.
39. **Relojes reales que quedan en los tests**, solo donde el contrato ES un plazo: presupuesto del job de 1 ms, `drain` de
    1 ns y 3 ms, `WithAckTimeout(1 ns)`, lectura de `ctx.Deadline()` y un `watchdog` de 5 s que convierte un cuelgue en fallo.
    Ningún `time.Sleep`.
40. ✅ ~~🟡~~ *(resuelto por D-F3-10 el 2026-10-06, `ce6911a`: `RevokeTenant` avisa también a los Edge vivos que `fleet` no lista, y el test se llama ahora `TestRevokeTenantNotifiesLiveEdgesUnknownToFleet`; el `Ping` sin `*SendError` queda aceptado. Lo que sigue describe la conducta tal como se copió)* **Conducta del viejo afirmada tal cual**: `RevokeTenant` solo avisa a los Edge que lista `fleet`. Un Edge
    con sesión viva que `fleet` no lista —o cualquiera si no hay `fleet` inyectado— no recibe el push y se entera en su
    siguiente `Renew` (`TestRevokeTenantOnlyNotifiesEdgesKnownToFleet`). Y `Ping` devuelve el error del empuje **sin**
    `*SendError`, a diferencia de `SendText`/`SendMedia`; sigue sin llamante de producción.
41. **Un test propio tenía una carrera** (corregido en `e03c304`): exigía la línea de log de la espera cuando el llamante
    cancela justo tras el empuje, y ese llamante puede salir por el empuje (`ErrPushAbandoned`) o por la espera. Apareció
    corriendo mutantes ajenos. Ahora el rastro se afirma sobre `awaitAck` directo.
42. **`errcheck` con `check-blank` marca `var _ interface{…} = (*SendError)(nil)`** (el tipo implementa `error`): el contrato
    de duck-typing (`StreamCaido()`, `CommandID()`) queda como test. Le pasará igual a `InferError`/`Motivo()` en la tanda 3.
43. **Comentarios rancios retocados al portar**: los «⚠️ CORREGIDO el 2026-08-18» se funden en el enunciado ya corregido;
    `ReceiptSink` ya no dice «la única implementación es log-only» (existe `receipts.Sink`); el comentario de paquete explica
    por qué se llama `grpc` y conserva el prefijo `gatewaygrpc:` en los textos; «`OnEdgeReady` no tiene llamante en
    `bootstrap.go`» no se portó (sin verificar contra el arranque nuevo). El google gRPC se importa como `googlegrpc`.
44. **Mutantes de la tanda 1** (a mano): `worklane` 44 (42 muertos, 2 vivos equivalentes: el corte temprano del segundo
    `seal()` y el `q.items[0] = nil`), `send_ack` 20 (19 y 1 equivalente: cerrar los canales dentro o fuera de `acksMu`),
    `send` 47 (45 y 2 que no compilan), `send_revoke` 14 (14).

**De F3-03, segunda sesión** (`grpc`, tanda 2: conexión, auth, config, readiness y diagnóstico; 2026-10-05):

45. ✅ ~~🟡~~ *(resuelto por D-F3-9 el 2026-10-06, `1f96651`: el cierre del stream viejo ya no deja de rastrear una sesión que reconectó; el nuevo se aparta del viejo. Lo que sigue describe el defecto tal como se copió)* **Defecto del viejo copiado y afirmado (tanda 2)**: el seguimiento por Edge (`edgeSessions`) **no
    distingue streams**. En una reconexión rápida, el cierre del stream viejo hace `untrackSession` de una sesión que sigue
    viva por el nuevo, y nada vuelve a rastrearla: hasta su siguiente reconexión, `PushConfig`, `RevokeLease`, `warmEdges` y
    la elección por `edgeSessions` no la alcanzan, y con la última se borra el readiness del Edge. R-G4 solo protege
    Registry, acuses y flota. Fijado en `TestConnectReconnectionSurvivesTheOldStreamClosing`.
46. **Hallazgo 32 (el 23) afirmado, no corregido**: `persistSelfPn` no limpia el JID antes de normalizar. Corpus de 29 casos
    escritos a mano (`connect_heartbeat_selfpn_test.go`), contrastado una vez contra el `Normalize` viejo fuera del commit:
    29 de 29. Consecuencia fijada en `TestDeviceLimitMissesASessionThatReportsItsJID`: una sesión que reporta su JID con
    sufijo de dispositivo **no cuenta** para el tope del número real. Adversario del corpus: un dígito no ASCII entre dígitos
    ASCII se pierde en silencio y sale **otro número válido**.
47. **Mientras falte la tanda 3, un `InferenceResult` se pierde**: `route` no tiene su `case` y el frame cae en el `default`
    (un `Debug "payload EdgeToCloud desconocido"`); tampoco se saluda a ninguna sesión (`greetIfNeeded`) ni se cancelan
    inferencias al caer el stream. Los tres sitios llevan `// TODO(F3-03 tanda 3)`. No afecta a nada que corra: el paquete
    nuevo no se cablea hasta F3-04.
48. **Los rojos de `auth`, `config_push` y `connect` son débiles**, como el 34: las `With*` solo pueden afirmar «es una
    `Option` que `New` acepta» y los handlers no tienen cara exportada sin `Connect`. R-G9, R-G10 y el multi-Edge nacen en
    el verde.
49. **`offlinePersistTimeout` no nació**, contra lo que anotó la tanda 1: en el viejo solo lo usa `worklane.go` como
    presupuesto por defecto, y el nuevo ya usa `defaultWorkBudget`. Sería una constante sin llamante de producción.
50. **Conductas del viejo copiadas tal cual y afirmadas**: `PushConfig` calienta cada Edge del seguimiento **aunque su
    empuje fallara**; `warmOnRegister` no deduplica ni tiene guarda de `sessionID` vacío; `observeReadiness` no excluye el
    canal de control; `pushConfigsInBand` sin `sender` es un no-op sin log; el logout en banda no exige identidad mTLS y se
    audita con actor vacío; sin `authn` no se audita nada; `renewLease` de un Edge revocado no da error (re-empuja la
    revocación); si `IssueInitial` falla, `registerSession` vuelve **sin** empujar la config, pero si falla el push del
    lease sí la empuja. La guarda de `registerSession` que excluye `__wapp_control__` es **rama muerta** bajo `Connect`
    (que nunca registra ese id); su comentario viejo contradice ADR-0048 y se copió tal cual.
51. **Negativas de concurrencia con muerte probabilística del mutante** (misma clase que el 38): «calentar antes del
    `wg.Wait()`» en `PushConfig` y «`seal()` antes de encolar los `MarkOffline`» en el cierre del stream. El test correcto
    no puede fallar. Dos mutantes de `connect_route`/`connect` mueren por el `watchdog` de 5 s, no por aserción inmediata.
52. **Mutantes de la tanda 2** (a mano): `readiness` 52 (52 muertos; los cuatro de T-6), `config_push` rama ADR-0048 20 (20;
    los cuatro de T-5), `connect_route` 49 (49), `connect_session` 50 (50), `connect` 50 (48 y **2 vivos equivalentes** en
    `peerIdentity`: sin `p.AuthInfo == nil` o ignorando el `ok` de la aserción se llega al mismo `TLSInfo` cero). Tres
    supervivientes de la primera pasada de `connect` obligaron a añadir dos tests (el `ctx` del stream hacia
    `onSessionRegistered`/`onControlChannel`, y el orden de `seal()`).
53. **Identidad por `bufconn` sin mTLS**: una credencial de transporte de test que no cifra y da a cada conexión un
    `credentials.TLSInfo` con solo el sujeto. `peerIdentity` se ejercita por el cable; el rechazo de un certificado ajeno
    sigue siendo de F3-05. Relojes reales que quedan en los tests: `WithSendTimeout`/`WithWorkTimeout` de 1 ms donde el
    contrato **es** el plazo, y el `watchdog`. Ningún `time.Sleep`, ningún `t.Skip`.
54. **Tests viejos no portados en la tanda 2**: `TestConnectMultiSesionRace` y los dos `TestConnectCarril…` (su promesa ya
    está en `worklane_*` y `types_test`), `TestElHandshakeSigueResolviendoseEnElBucleRecv` (cubierto por el orden que afirma
    `TestConnectRegistersEachSessionOnItsFirstFrame`). `seedEdgeSessions` murió: lo sustituye `trackSession`.

**De F3-03, tercera sesión** (`grpc`, tanda 3: inferencia, plaza y aviso de sesión pasiva; 2026-10-05):

55. **Mutantes de la tanda 3** (a mano, 323 escritos): `inference` 45 (44 muertos, 1 vivo equivalente: el orden de
    `inferenceReasons`, que se consume como conjunto), `inference_result` 87 (80; 4 equivalentes: sin `defer timer.Stop()`,
    sin el `if res == nil` de `readInference` —los *getters* del proto son nil-safe—, sin `UNSPECIFIED` en `reasonOfFrame`
    —cae al mismo `default`— y sin el `return` del huérfano de `deliverInference`; 2 huecos cerrados; **1 abierto**, el 56),
    `inference_dispatch` 88 (85 y 3 huecos cerrados), `greeting` 66 (61, 2 que no compilan, 3 huecos cerrados), `plaza` 18
    (14, 2 que no compilan, 1 equivalente: sin el `if !ok` se llega al mismo `("", false)`; 1 hueco cerrado), el cableado
    de la tanda en `connect_route` 15 (15) y en `connect` 7 (5, 1 equivalente: el orden entre `cancelSessionAcks` y
    `cancelSessionInfers`, dos mapas con candados distintos; 1 hueco cerrado). Los **10 huecos** obligaron a **8 tests**
    nuevos (`3758144`, `af8a160`, `3aff23c`, `8360ab0`, `746e5fa`, `fdaf4d7`, `231c74b`). Los dos mutantes del literal 🔒 y
    el de su id mueren por `TestPassiveSessionNotice*`.
56. ✅ ~~🟡~~ *(resuelto el 2026-10-06 por decisión de Jhoan: `edb08bf`, un test con `testing/synctest` que acota la espera a plazo + un margen y mata el mutante sin tocar producción; y `04eedd7`, el presupuesto se calcula una sola vez, conducta idéntica)* **Hueco de test, cerrado**: en `awaitInference` el presupuesto se calcula **dos veces**
    (`inference_result.go`: el `time.NewTimer(inferTimeout(timeout) + s.inferGrace)` y, aparte, el `budget` del log), igual
    que en el viejo (`internal/gateway/grpc/inference.go:519` y `:535`). El mutante `+ 2*s.inferGrace` en el temporizador
    **sobrevive**: ningún test acota por arriba el plazo real, y el log no lo delata. Matarlo pide un reloj inyectable o
    calcular el presupuesto una sola vez; las dos cosas tocan producción y se apartan del viejo, así que no se hizo.
57. **Conductas del viejo copiadas tal cual y afirmadas** (`Infer` / `PlazaDe`): el candidato vivo no se contrasta con el
    tenant; un destino sin stream no cae al origen; un fallo de escritura del stream se rotula `timeout`; un plazo positivo
    por debajo del milisegundo viaja como `timeout_ms = 0`; con un origen vivo que no está en el seguimiento del tenant
    (sesión de otro tenant; la reconexión rápida del hallazgo 45 ya no lleva a este caso, D-F3-9), `Infer` **envía** y `PlazaDe` dice que **no hay plaza**.
58. **`origin != ""` es la única defensa contra una sesión de id vacío** en `inferenceSession`: `session.Registry.Register`
    acepta el id vacío. Hoy es inalcanzable desde `connect` (no registra un `session_id` vacío); queda afirmado por
    `TestInferWithoutCandidateNeverRoutesThroughABlankSession`.
59. **Hueco de la tanda 2 cerrado de paso** (`db2d5bd`): `defer s.cancelSessionAcks(sid)` sobrevivía. Nada fijaba que los
    envíos en vuelo despierten **antes** de drenar el carril; ahora lo fija
    `TestConnectCancelsTheSendsInFlightBeforeDrainingTheLane`, gemelo del de las inferencias (`8360ab0`). El mutante muere
    por el *watchdog* de 5 s, no por una aserción inmediata.
60. **Candados que solo delata `-race`**: quitar `infersMu` (dos sitios), el candado del registro de la inferencia o
    `trackMu` en `edgeOfSession` sobrevive a `go test` sin `-race` y muere siempre con él. `ci-local` corre con `-race`:
    un gate sin él no los ve. `trackMu` en `candidatesByReadiness` no moría ni con `-race` hasta
    `TestInferFallbackReadsTheFleetUnderItsLock`.
61. **Los tests de `connect_route_heartbeat_test.go` NO cambiaron al entrar el saludo**, contra lo que anotó la tanda 2: su
    flota no cumple `sessionGreeter`, y una flota sin el puerto ni saluda ni rompe. La línea de tiempo con saludo
    (`SetSelfPn` > `SaveHealth` > `Upsert` > `PendingGreeting` > `MarkGreeted`) la fija `greeting_route_test.go`.
62. **Hallazgo 47 resuelto** (`8f87d7e`): `route` entrega el `InferenceResult` *inline*, entre el Ack y el Heartbeat. Y al
    caer el stream las inferencias en vuelo despiertan con `edge_offline` en vez de agotar su plazo (`d3dd137`). No queda
    ningún `// TODO(F3-03 tanda 3)`.
63. **`inference.go` nació en tres trozos** (E-13) con ciclo entre ellos (hallazgo 25): `infers`, `infersMu`, `inferGrace`
    (en `Server`) y `pendingInfer` (en `types.go`) van en el commit de `inference_result` (`5db5b76`), su primer usuario.
    `inference_result.go`, `greeting.go` y `types.go` no tienen exportados: nacen en el verde con su test (T-17).
    Correspondencias E-11 de la tanda 3: `Motivo*` → `Reason*` (valores intactos), `motivosInferencia` →
    `inferenceReasons`, `motivoDeFrame` → `reasonOfFrame`, `motivoDePush` → `reasonOfPush`, `rutaPreferida` →
    `preferredRoute`, `candidatasPorReadiness` → `candidatesByReadiness`, `edgeDeSesion` → `edgeOfSession`,
    `avisoSesionPasivaID` → `passiveSessionNoticeID`, `avisoSesionPasivaV1` → `passiveSessionNoticeV1`, `commandIDDe` →
    `commandIDOf`. `PlazaDe` **no** se traduce (T-1: el aforo lo busca por aserción de tipo).
64. **La sesión se cortó a medias y el saludo quedó en un *worktree* nacido de `dev`**, no de la rama de la tanda: sus dos
    commits (`7b76b3d`, `f8d6920`) se integraron al relanzar con `cherry-pick` (`ad9886d`, `c7a61a1`), sin conflictos. Las
    copias de los mutantes se crearon **fuera** del árbol del repo, para no cruzarse con `ci-local`, y ya están borradas.
65. **Correspondencias E-11 de F3-04** (la spec nombraba en español lo que aún no existía): `plazos.go` → `deadlines.go`;
    `Montar{Mensajes,Sesiones,Diagnosticos}` → `Mount{Messages,Sessions,Diagnostics}`; `margenDeEscritura` → `writeMargin`;
    `streamCaidoFrom` → `streamClosedFrom`; `msgGuardaVencida`/`msgStreamCaido`/`msgEdgeNoLee`/`msgPresupuestoAgotado` →
    `msgGuardExpired`/`msgStreamClosed`/`msgEdgeNotReading`/`msgBudgetExhausted`; `Deps.DiagnosticsBundleTTL` →
    `DiagnosticsDeps.BundleTTL`. Los textos observables son literales y el método `StreamCaido()` del *duck-typing* no se
    traduce. El rojo de TX.8 fueron **tres** commits (uno por grupo de ficheros: `0e5b69a`, `be818c9`, `488539b`), no uno.
66. **`apipublica/limits.go` no tiene consumidor de producción en F3**: el 413 lo usan áreas de F6–F8, y hasta entonces lo
    sostiene ante `unused` solo su test. `FX-cara-http/diseno.md` §1 dice que porta `errorBody`, que nació en `response.go`
    en F2: esa línea estaba caducada y **se corrigió en el mismo PR**. `TestTechos_TodosNombranSuCifra` (viejo) recorre siete endpoints de F6–F8: cada área
    afirmará su cifra al mudarse.
67. **La lista de ficheros de T3.28 se quedó corta y `http.go` no ve el contenedor.** También cambiaron `auth_jwt.go`
    (`buildJWKSConfig` devuelve el `ConfigPayload` nuevo) y `send_budget_cableado_test.go` (vigila ahora
    `edge.messages.SendBudget`). Los *deps* de D1–D6 los arma `edgeDepsOfTheNewFace(c)` en `fase8_transporte.go` y viajan a
    `buildPublicAPIServer` en un parámetro nuevo. En `publicapi.Deps`, los siete campos de TX.11 **se quitan** en vez de
    asignarse a `nil`: con el `fleet` nuevo ya no compilaría asignarlos. Con ellos se fueron tres cables que solo leían
    D1–D6 en la cara vieja (`SendBudget`, `Health`, `DiagnosticsBundleTTL`).
68. ✅ **Resuelto en el mismo PR** (la spec ya dice lo medido; ver al final). **`arquitectura.md` §6 esperaba de más**: `go list -deps ./cmd/server-modular | grep internal/gateway/` no da
    solo `internal/gateway/grpc`, da **cuatro** (`grpc`, `lease`, `fleet`, `session`). `lease` lo arrastra `gateway/grpc`;
    `fleet` y `session` los importan además `internal/publicapi` e `internal/flujos/admin`, así que no salen del binario al
    morir `bridge_gateway.go` (F4), sino en F7/F8. Lo que sí se cumple y se vigila: en `internal/arranque`, de producción,
    solo `bridge_gateway.go` importa `internal/gateway/**` (`TestCableado_OnlyTheBridgeImportsTheOldGateway`). *Resolución*:
    R3.6.c en sí era cierto (habla solo de `gateway/grpc`); lo falso era la línea «solo `internal/gateway/grpc`» de
    `arquitectura.md` §6, que se corrige, y la verificación por `grep` de R3.6.c, que depende del alias del import, se
    apoya ahora en los dos tests de cableado.
69. **El `nil` de interfaz que resolvía `bridge_iam.go` lo resuelven ahora dos costuras** de `fase4_gateway.go`
    (`edgeAuthenticatorPort`, `edgeAuditorPort`): sin delegado, el gateway recibe un `nil` de verdad, no un puntero nulo
    dentro de una interfaz (`TestCableado_AbsentAccessServicesReachTheGatewayAsTrueNil`). ✅ **Resuelto en el mismo PR.** Con `bridge_iam.go` desaparece
    la traducción de centinelas nuevo → viejo: el gateway nuevo habla los de `acceso` nuevo. Lo cubren los tests de
    `edge/grpc`; que un login con credenciales malas dé lo mismo que el binario viejo quedó sin medir en la conmutación. *Resolución*:
    da lo mismo **por construcción**, y está afirmado en tres eslabones: (a) `authErrorCode` es **idéntico byte a byte** en
    los dos gateways (`diff` vacío) y los cuatro centinelas tienen el mismo texto en `internal/iam/domain` y en
    `acceso/iam/domain`; (b) el gateway nuevo clasifica los de `acceso` nuevo (`auth_inband_test.go`, tabla de los cuatro
    más uno envuelto); (c) los servicios **reales**, por las costuras del arranque, devuelven esos centinelas:
    `TestCableado_RealAccessServicesSpeakTheSentinelsTheGatewayClassifies`, que sustituye a los dos
    `…RealServiceInvalidInputIsOldSentinel` que murieron con el adaptador. El e2e de F3-05 lo confirmará por el cable, pero
    ya no es una duda abierta.
70. ✅ **Resuelto en el mismo PR.** **D1 sigue registrada en el mux viejo, con `Sender` a `nil`**, tapada por la nueva en el compuesto (no tiene
    condición de montaje). Si algo la alcanzara sería un *nil-pointer*: el e2e de F3-05 debe confirmar que
    `POST /api/v1/messages` resuelve por la cara nueva. *Resolución*: el handler viejo no está «tapado», es **inalcanzable**:
    el `POST` lo resuelve siempre la nueva (lo afirma ya el candado de mudanzas sobre el compuesto real, en los dos
    perfiles) y con cualquier otro método la vieja no casa, porque solo registró el `POST`, así que responde el 405 de la
    nueva. Lo fija `TestCableado_TheOldFaceNeverServesMessages` con siete métodos. Y `MountMessages` tampoco comprueba `Sender`/`Sessions` (conducta
    del viejo, copiada y fijada por `TestMountMessages_AlwaysMounts`).
71. **Conductas heredadas, copiadas y ahora fijadas por test** (✅ una se corrigió por **D-F3-11**, ver al final; el resto
    se queda): D3/D4 responden sus errores en
    **texto plano** y la cadena su 401/403 en JSON; D5 hace el *rollback* (`DeleteRequest`) con el contexto de la petición,
    sin plazo propio (si el cliente se va, la fila puede quedar pendiente hasta el TTL); el 400 de cuerpo inválido de D5 va
    **después** de las dos consultas (cuerpo roto sobre sesión ajena → 404); `context.Canceled` en `GetBundle` es 500, no
    504; el fallo de empuje de D5 contesta con textos de mensajes («no se pudo enviar el texto»). No se portó la guarda
    inalcanzable «diagnóstico remoto no configurado» (el montaje ya exige las tres dependencias). *Resolución (D-F3-11,
    2026-10-06, en el mismo PR)*: el *rollback* de D5 va ahora por `rollbackRequest`, desenganchado de la cancelación de
    la petición y con el plazo de BD; 🔴 la cara nueva **se aparta de la vieja** en eso, como en D-F3-9 y D-F3-10. Lo fija
    `TestMountDiagnostics_Request_RollbackSurvivesTheClientLeaving`, visto en rojo contra el código copiado.
72. **El gate de la ficha no selecciona los `TestBootWiring_*`** de `access_wiring_test.go` y `bridge_contact_test.go`
    (`-run 'Mudanzas|Huella|Cableado|Identidad'`): solo corren en el gate amplio y en `ci-local`. Los tests nuevos de F3-04
    sí casan (`TestCableado_…`, `TestIdentidad_…`). `TestBootWiring_AccessWhitelistIsTight` se borró con su lista blanca: la
    sustituye la regla 3 de fronteras, que con `acceso` en `Conmutados` ya prohíbe a `internal/arranque` importar
    `internal/{iam,platformadmin,entitlements}`.
73. **Entorno**: `.bin/golangci-lint run` a pelo usa la caché **del usuario**, y dio 3 `gosec` falsos sobre un *worktree*
    ya borrado de otra sesión. `make lint` (caché por *checkout*) da 0. El lint se corre siempre por `make`: **anotado en `PROTOCOLO-CLI.md` §1 en el mismo PR**.

**De F3-05** (cierre local con mTLS, 2026-10-06; rama `reorg/f3-05-cierre-mtls` desde `dev` @ `9d9c033`):

74. **Las 7 suites de puerto con BD pasan contra Postgres sin ninguna divergencia con su doble** (regla de conteo: el
    test padre más sus subtests con `-v`): `lease.Repository` 16, `enroll.CodeStore` 8, `enroll.EdgeCertRepository` 5,
    `fleet.Repository` 43, `diagnostics.Store` 23, `receipts.Store` 13, `ingest.Deduper` 7 — **115 PASS, 0 SKIP**. Ninguna
    suite ni adaptador se retocó para llegar al verde. Un mutante por adaptador puso su suite en rojo (los de `lease`,
    `enroll`, `receipts` e `ingest`, en una sola corrida conjunta y no aislados; los de `fleet` se corrieron antes de un
    retoque de lint del test y no se repitieron después).
75. ✅ ~~🟡~~ *(resuelto el 2026-10-06 por decisión de Jhoan, `61ded8b`, en el mismo PR: `enrollhelpertest.EdgeCertRecord` y `fleethelpertest.{TenantProfiles,HealthSnapshot}` son alias exportados, como en `iam`; los rodeos genéricos de los dos `_contrato_test.go` desaparecen. Los candados no pidieron nada: los `…helpertest` están exentos. Lo que sigue describe cómo estaba)* **El candado `ProcessImports` no deja nombrar tipos del puerto** (solo sus `New…`), y dos Montajes los devuelven:
    `enroll.EdgeCertRecord` y `fleet.TenantProfiles`/`HealthSnapshot`. `enroll_contrato_test.go` y `fleet_contrato_test.go`
    lo resuelven con genéricos donde el compilador infiere el tipo (un tipo espejo con restricción `~struct{…}` en
    `enroll`): pasa el candado y deja de compilar si el registro cambia, pero se aparta del precedente de `iam` (alias
    exportados en el `helpertest`). **Por decidir (Jhoan)** si se prefieren los alias.
    `fleet_contrato_selfpn_test.go` no importa nada de `internal/`: recibe el adaptador por un *rig* del otro fichero.
76. **Lo que solo protegen los casos propios, y lo no llevado.** Quitando entera la guarda de `SetSelfPn`, los 42 casos
    de la suite de `fleet` siguen verdes: solo la matan `TestFleetContract_SetSelfPn_SameNumber_DoesNotRewriteTheRow` y
    `…RotatedKEK_HealsTheRowOnce`. Del hallazgo 30 **no se llevaron** `PendingGreeting` / `MarkGreeted` y su carrera
    (fuera del puerto) ni el sobre incompleto sembrado por SQL (sí el ilegible por KEK ausente). La poda perezosa de
    `ingest` no se ejerce contra Postgres: el candado impide `WithSweep` / `WithRetention`.
    ✅ *Resuelta la parte de `fleet` el 2026-10-06 por decisión de Jhoan (`e41df74`, en el mismo PR)*:
    `fleet_contrato_greeting_test.go` lleva a Postgres `PendingGreeting` (solo una fila pasiva, con número y sin saludar;
    preguntar no es un *claim*), `MarkGreeted` (marca una vez y solo su fila), la carrera (16 marcas con barrera, 12
    rondas: exactamente una gana) y el sobre incompleto en cinco formas (`Get`/`List` sirven la sesión sin número y sin
    error; `PendingGreeting` devuelve su error literal). Los dos métodos fuera del puerto se llaman por una interfaz
    local del test. Nueve mutantes nuevos, todos muertos, y **repetidos** los tres de `fleet` que el 74 dejó sin
    re-correr (guarda de `SetSelfPn`, `BlindIndex` sin normalizar, `degraded_since`): muertos. Sigue sin ejercerse la poda
    de `ingest`. 🟡 **Leído en el código, no ejecutado ni afirmado**: una fila con el sobre a medias pero con
    `self_pn_bidx` y `self_pn_kek_id` intactos no se auto-sanaría con el latido siguiente (ninguna rama de la guarda de
    `SetSelfPn` casa; solo sana si falta el `kek_id`): serviría número vacío y daría error en `PendingGreeting` sin fin.
    Sin contrastar con el viejo; por decidir si merece un test que la fije o una corrección.
77. **D-F3-11 probada contra Postgres, en el adaptador** (`TestDiagnosticsContract_DeleteRequest_SurvivesTheCancelledRequest`):
    con el contexto de la petición cancelado, `DeleteRequest` falla y la fila queda; con
    `context.WithTimeout(context.WithoutCancel(ctx), plazo)`, la forma de `rollbackRequest`, la fila se borra de verdad.
    No es el handler HTTP (un `_contrato_test.go` no puede importar `apipublica`): el handler lo fija F3-04 con el doble.
78. **Sin `WAPP_IDENTITY_URL` el gateway no tiene autenticador** y contesta `internal` a todo login: el arnés no la ponía.
    Nace `opcionesServidor.IdentityLogin` (opt-in; solo lo usa el proceso nuevo) con la mitad «login» del doble de
    identity (`identidad_login_test.go`, `TestArnes_IdentityLogin`) y el canal de control en el Edge de prueba
    (`edge_falso_auth_test.go`, `TestArnes_EdgeAuth`). Ficheros del arnés tocados: `edge_falso_test.go` (un campo),
    `edge_falso_commands_test.go` (un `case`), `edge_falso_selftest_commands_test.go` (su «comando desconocido» era un
    `UserAuthResponse`, que ahora se interpreta), `identidad_test.go` y `servidor_test.go`.
79. ✅ **Hallazgo 69 confirmado por el cable**: los códigos de error del login en banda son **idénticos** en los dos
    binarios — `invalid_credentials` (contraseña mala, correo desconocido), `invalid_input` (campos vacíos, 400 de
    identity), `tenant_mismatch` (operador de otra empresa, usuario sin empresa, refresh de A por el Edge de B),
    `user_inactive` (403 de identity), `internal` (5xx de identity), `refresh_invalid`. Todos con `message` vacío y sin
    tokens. **Tres** Edge (dos de una empresa y uno de otra; dos de ellos solo por el canal de control) hacen login a la
    vez, 8 rondas: cada uno recibe lo suyo (claims, `whoami`, buzón por `command_id`), una fila `edge.auth.*` por
    petición, y el canal de control no deja fila en `fleet_sessions` ni lease (ADR-0048).
80. ✅ **Hallazgo 70 y T3.27 confirmados por el cable**, con los mismos literales en los dos binarios:
    `POST /api/v1/messages` online 200 (el Edge recibe el `SendText` con ese `command_id`), offline 502, sesión
    inexistente o de otra empresa 404, otros métodos 405, y ningún pánico en el log; `POST /admin/flows/start` a una
    sesión offline, **502** `sesión offline: no hay stream vivo para el Edge` (las puertas de `/admin` y de flujos no
    distinguen «offline» de «nunca existió»; `/api/v1/messages` sí).
81. **El arnés sabe reiniciar el servidor** sobre la misma base, claves y puertos (`servidor_restart_test.go`,
    `TestArnes_ServerRestart`). Con él, R-L2/R-L3/R-L8: un Edge revocado sigue revocado y su contador no retrocede, y un
    Edge enrolado tras el reinicio con la empresa cortada nace revocado. 🟡 **Parar el servidor con tres Edge conectados
    tarda ≈ 10 s en los dos binarios** (< 0,5 s sin Edge; dentro del tope de 15 s del arnés): causa sin investigar.
82. **No llevado o no afirmable de caja negra**: la mitad de R-C5 «un push fallido no cambia la respuesta HTTP»; el 504 y
    los 409 de `flows/start`; `tenant_mismatch` por canal sin identidad (el mTLS estricto lo impide) e `internal` sin
    autenticador; `UserLogout` con `all_sessions`; la firma del access token (se comprueba el `kid` y que `whoami` lo
    acepta). Ya cubierto antes y no duplicado: push de filtros (P9, P3), migraciones idempotentes (P10), CA ajena (P1).
    ✅ **El mutante del cruce entre Edge (HS-14/HS-15) se corrió el 2026-10-06, con permiso de Jhoan**: en
    `edge/grpc/auth.go`, toda respuesta de auth sale por el stream del primer Edge que hizo login, en vez de por
    `cc.sender`. `TestP1_OperatorLoginOverControlChannel` contra el **nuevo**: rc=1, rojo en
    `concurrent_logins_each_edge_gets_its_own` («no llegó el UserAuthResponse … al Edge»); contra el **viejo**, que no
    lleva el mutante, rc=0 (control). Muere por la respuesta que **no llega** al Edge que la pidió, a los 15 s del tope,
    y ese primer fallo detiene el proceso: las aserciones de claims y de buzón no llegaron a evaluarse con este mutante.
    Mutante deshecho con `git checkout`; árbol limpio.
83. **Las tres diferencias intencionadas (D-F3-9, D-F3-10, D-F3-11) no se comparan entre binarios**: los casos se
    diseñaron para no depender de ellas (en R-G21 los dos Edge están en la flota y no reconectaron). **Orquestación**:
    cuatro sub-agentes en *worktrees* puestos en el SHA de `dev`, en paralelo y sobre el mismo paquete `procesos`, con
    prefijo de fichero en cada identificador: 11 `cherry-pick` sin un conflicto. El *scratchpad* compartido sí se pisó
    (un agente sobrescribió el guion de otro): cada sub-agente debe usar nombres propios ahí.
