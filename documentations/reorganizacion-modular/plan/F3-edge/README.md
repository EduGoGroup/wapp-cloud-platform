# F3 · `edge` — el túnel con cada Edge: gRPC, enrolamiento, lease, flota, acuses

> **Estado: por empezar** (spec escrita el 2026-09-28 sobre `dev` @ `1b18932`, releída en `bad573a`).
> Norma: [`05`](../../05-metodo-contratos-y-tdd.md). Forma: [`00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md).
> Marco común: [`00-marco/`](../00-marco/README.md). Rutas: **autoridad**
> [`FX-cara-http/mapa-de-rutas.md`](../FX-cara-http/mapa-de-rutas.md) (filas D1–D6, E1–E2, J12–J17) y
> sus tareas TX.8–TX.11. Patrones de [`F1`](../F1-nucleo-contact/README.md): rojo **solo con
> exportados** y adaptadores `internal/arranque/puente_<x>.go`.

## Objetivo, en tres líneas

1. Reconstruir `internal/gateway/{grpc,enroll,lease,session,fleet(/fleettest)}` y
   `internal/{diagnostics,inferstats,receipts,ingest,filtercfg}` en `internal/modulos/edge/…`
   (aplanado) por contrato → rojo → verde, **sin cambiar un comportamiento**: 🔒 `lease` es la mitad
   servidora de la doble llave y el kill-switch anti-clon.
2. Conmutar: `cmd/server-modular` construye **un solo** `*grpc.Server` nuevo (conexiones vivas,
   carriles, acuses e inferencias en vuelo) y lo **inyecta** en los consumidores viejos que aún lo
   usan (runtime, notificador de solicitudes, selector LLM vía `puente_gateway.go`); muda 8 rutas de
   `:8103` y 6 de `:8100` (FX TX.8–TX.11).
3. Conservar byte a byte el literal `AVISO_SESION_PASIVA_V1`, las tres reglas del ADR-0048 (el canal
   de control no es una sesión) y el contrato `wapp-cloudlink v0.17.0`, que no cambia.

## Entradas (tiene que ser cierto para empezar)

| # | Condición | Cómo se comprueba |
|---|---|---|
| E1 | F2 cerrado: `internal/modulos/acceso` en verde y conmutado; `puente_iam.go` vivo | `ls internal/modulos/acceso` · `grep -rn 'pendiente.Implementar' internal/modulos/acceso \| wc -l` → 0 |
| E2 | F1 cerrado: `internal/nucleo/contact` en verde (lo importan `fleet` y `grpc`) | `ls internal/nucleo/contact` |
| E3 | F0: `session.ErrSessionOffline` **es** el centinela de `platform` y `inferstats.Agregado` **es** alias del tipo de `platform/metrics` (F0 `arquitectura.md` §✎, filas de `admin.go` e `inferstats.go`) | `go list -f '{{.Imports}}' ./internal/platform/... \| grep -cE 'internal/(gateway\|inferstats)'` → 0 |
| E4 | El código viejo no cambió desde esta spec | `git log --oneline 1b18932..origin/dev -- internal/gateway internal/diagnostics internal/inferstats internal/receipts internal/ingest internal/filtercfg` vacío |
| E5 | Decisiones FX D-FX-1, D-FX-2 y D-FX-3 contestadas (o asumidas por recomendación) | [`FX-cara-http/README.md`](../FX-cara-http/README.md) |
| E6 | `dev` verde con la toolchain fijada | skill `validar-antes-de-cerrar` |

## Salidas (es cierto al cerrar)

- `internal/modulos/edge/{grpc,enroll,lease,session,fleet,fleet/fleettest,diagnostics,inferstats,receipts,ingest,filtercfg}`
  con **38** ficheros de producción (+ ✚ de [`diseno.md`](diseno.md) §1) en verde; 0 pendientes; 0 SKIP.
- `make cobertura-ficheros` ≥ 80 % en todo fichero no-Postgres de `edge`.
- El literal `AVISO_SESION_PASIVA_V1` afirmado byte a byte **y** contra `documentations/literal-aviso-sesion-pasiva.md`
  (solo cambia la ruta relativa: `../../../../documentations/…`).
- `cmd/server-modular`: **un** `*edge/grpc.Server`, inyectado en runtime, notificador, `filtercfg`,
  handlers de `:8100` y (vía `puente_gateway.go`) en el selector LLM; `puente_iam.go` **borrado**;
  `huella_test` igual (2 rpc, 22 + 73 rutas); `cmd/server` intacto; `go.mod` sin cambios en
  `wapp-cloudlink`.
- Traspaso para la sesión local: e2e de gRPC con mTLS y el proceso «Enrolamiento de un Edge y su lease».

## Orden de lectura

1. [`requisitos.md`](requisitos.md) · 2. [`arquitectura.md`](arquitectura.md) · 3. [`diseno.md`](diseno.md) ·
4. [`reglas.md`](reglas.md) · 5. [`tareas.md`](tareas.md).

## Bloques de sesión

| Bloque | Entorno | Tareas | Punto de parada |
|---|---|---|---|
| **A** · inventario verificado | 🌐 | T3.1 | tabla de arquitectura §1 re-medida · D-F3-* contestadas |
| **B** · rojo de las hojas (`session`, `inferstats`, `receipts`, `ingest`, `diagnostics`, `lease`, `enroll`) | 🌐 | T3.2–T3.9 | pendientes contados · `ci-local` rc=0 · PR |
| **C** · rojo de `fleet`, `filtercfg` y `grpc` (13 ficheros) | 🌐 | T3.10–T3.14 | todo `edge` en rojo · `vet -tags pendiente` rc=0 · PR |
| **D** · verde de las hojas | 🌐 | T3.15–T3.18 | 0 pendientes salvo `fleet`, `filtercfg`, `grpc` · PR |
| **E** · verde de `fleet`, `filtercfg` | 🌐 | T3.19–T3.20 | 0 pendientes en esos dos · PR |
| **F** · verde de `grpc` (13 ficheros, el grueso) | 🌐 | T3.21–T3.23 | 0 pendientes en `edge` · literal verde · PR |
| **G** · puente, cara nueva y conmutación | 🌐 (TX.10 🌐→💻) | T3.24–T3.28 | huella igual · un gw · 8+6 rutas · `puente_iam` borrado · PR · traspaso |
| **H** · cierre local (mTLS real, e2e, procesos) | 💻 (🌐→💻) | T3.29–T3.30 | e2e gRPC verde · `dev` integrado |

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
   tests nuevos + los paquetes `…test`. Los 7 tests de carga (`load_*`, `curva_pool_t55`,
   `deuda_050_2`) son **mediciones**, no reglas: no tienen sitio ni en F3 ni en F9 (D-F3-5).

## Decisiones que necesita (de Jhoan, con recomendación)

| # | Pregunta | Recomendación |
|---|---|---|
| D-F3-1 | Los gemelos en memoria que hoy viven en ficheros de producción (6, contradicción 1): ¿se quedan ahí o se mudan a `<paquete>test`? | **A `<paquete>test`** (patrón E-6, como `fleettest`): el fichero de producción queda con el puerto y sus tipos, y el doble corre la suite. Cambia el contenido, no los nombres del árbol de `04` |
| D-F3-2 | Identidad de `ErrSessionOffline` entre viejo y nuevo | **Vía `platform`** (contradicción 3), sin puente; FX TX.10 se reduce a un test de identidad `errors.Is(nuevo, viejo)` |
| D-F3-3 | `ingest` no declara puerto (lo declara el consumidor, `flujos/runtime/runtime.go:116`). ¿Se añade `ingest.Deduper` (solo interfaz) para que `ingesttest.Contrato` tenga contra qué correr? | **Sí**, fichero ✚ `ingest/deduper.go` (solo interfaz, E-3) |
| D-F3-4 | 🔒 `lease/repository_postgres.go:106,117` escribe `public.tenants.revoked_at` (deuda D-9: tabla de otro módulo sin API interna). ¿Se corrige en la reconstrucción? | **No**: el lease se reconstruye **sin cambiar comportamiento** (`05` §6); el SQL se copia literal y la deuda sigue anotada |
| D-F3-5 | Los 7 tests de carga/pool del gateway (`load_integration_test.go`, `load_ack_integration_test.go`, `curva_pool_t55_integration_test.go`, `deuda_050_2_pool_integration_test.go`): ¿se reescriben? | **No**: son mediciones publicadas (Plan 050 · T5.x, DEUDA-050.2). Se conservan en el árbol viejo hasta F10 y se dice en el commit |
| D-F3-6 | ¿Se adelanta F9 para correr «Enrolamiento de un Edge y su lease» contra el binario nuevo al cerrar F3? | **Sí si F9 adelantado está aceptado** (T3.30 condicionada): es el único oráculo del kill-switch extremo a extremo con mTLS real |
