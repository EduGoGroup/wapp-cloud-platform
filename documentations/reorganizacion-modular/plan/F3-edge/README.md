# F3 · `edge` — el túnel con cada Edge: gRPC, enrolamiento, lease, flota, acuses

> **Estado: en curso** — F3-01 arrancó el 2026-10-04 sobre `dev` @ `8896f13`, con el inventario E-12 de las hojas
> **aprobado por Jhoan** ([`arquitectura.md`](arquitectura.md) §1.1.a). **F3-02 hecha el 2026-10-04**: `fleet` y `filtercfg` en verde
> (inventario en §1.1.b). Spec escrita el 2026-09-28 sobre `dev` @ `1b18932`, releída en `bad573a`.
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
21. 🟡 **Única línea que se aparta del viejo**: `scanSession` hace `defaultProfile(Profile(profile))`; el viejo, `Profile(profile)`.
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
