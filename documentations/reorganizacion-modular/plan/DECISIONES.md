# Registro de decisiones del plan

> **Qué es.** Las decisiones que el plan necesita de **Jhoan**, reunidas en un solo sitio y
> ordenadas por **cuándo bloquean**. Cada fase las explica con detalle en su `README.md` (sección
> «Decisiones que necesita»); aquí va la pregunta en una línea, la recomendación del equipo que
> escribió la spec, y **la sesión que no puede arrancar sin ella**.
>
> **Cómo se usa.** Jhoan rellena la columna «Decisión» con `sí`, `no` u otra cosa, y la fecha. Una
> sesión **no arranca** si alguna decisión de su tabla está vacía: la sesión lo comprueba en su paso
> de verdad de campo. Si una decisión se toma distinta de la recomendación, la sesión que la consume
> lee aquí el cambio y lo aplica (y lo anota en su `tareas.md`).

## 0 · Ya cerradas (2026-09-27, sesión de plan)

| # | Decisión | Dónde se aplica |
|---|---|---|
| D-2 | Árbol `internal/modulos/<m>/` | Todo el plan |
| D-5 | Los 7 módulos de `04`: `acceso` (iam + platformadmin + entitlements) · `edge` · `conversacion` · `catalogo` · `captacion` · `inferencia` · `solicitudes` | F2–F8 |
| D-9 | `cmd/server-modular` temporal; en F10 `cmd/server` usa el arranque nuevo; **una** prueba en UAT en sustitución antes del relevo | F0, F10 |
| D-10 | 🔄 **Cara HTTP única NUEVA, `internal/apipublica`, construida por olas** (estrangulador delante del `publicapi` viejo; cada módulo muda sus rutas al conmutar) — sustituye la recomendación original de repartir en `modulos/<m>/http` | [`FX-cara-http/`](FX-cara-http/README.md) |
| D-11 | Etiquetas `pendiente` e `integracion`; **cero `t.Skip`** en código nuevo | Todo |
| D-12 | 80 % de sentencias por fichero al llegar a verde, fuera adaptadores Postgres; se recalibra tras F1 | Todo |
| W-1 | Docker en la web: la primera sesión web **prueba** Docker + testcontainers; si funciona, la web corre los procesos como pre-chequeo; **cierra la sesión local** | F0-A, F9 |

## 1 · Antes de F0 — bloquean la primera sesión o un bloque de F0

| # | Pregunta | Recomendación | Bloquea | Decisión |
|---|---|---|---|---|
| F-1 | ¿Quién fusiona los PR de la web en `dev`? | Jhoan con «Rebase and merge» (bloques 🌐); la sesión local con `merge --no-ff` (bloques 🌐→💻). **Nunca squash** | Toda sesión web | |
| F-2 | Aplicar en claude.ai/code el *setup script* y las variables de [`00-marco/flujo-web-local.md`](00-marco/flujo-web-local.md) §3 | Sí, antes de la primera sesión web | F0-01 | |
| T-1 | Que `make lint` **falle** si `golangci-lint` no es `v2.12.2` (hoy `Makefile:42` usa el del `PATH`; en local hay v2.14.0) | Sí (T0.26) | F0 bloque B | |
| D-F0-1 | `internal/arranque` nace **por copia** del arranque viejo, sin ciclo contrato→rojo→verde, y queda fuera de «un fichero, un test» y de la cobertura por fichero | Sí | F0 bloque D | |
| D-F0-2 | Se permite **un** fichero de test en el paquete viejo, `huella_vieja_test.go`, que escribe la dorada de la huella | Sí | F0 T0.14 | |
| D-F0-3 | Tres líneas de **alias** en dominio viejo para cortar los ✎ de `platform` (`session/registry.go:22`, `iam/ports/in/usecases.go:129`, `inferstats/inferstats.go:144`) — excepción a E-1 | Sí | F0 bloque E | |
| D-F4-1 | 🔴 Los barridos AST viejos que recorren **todo** `internal/` (`llmvia/c2_via_test.go:117`, `iam/infra/postgres/membresia_unica_ast_test.go:74`) se pondrán rojos con el árbol nuevo: una línea en cada uno para que ignoren `internal/{modulos,nucleo,arranque,apipublica,pendiente}` — segunda excepción a E-1, **se ejecuta en F0**. Subsume D-F2-2 | Sí | F0 (y sin ella F2 y F4 no pasan del rojo) | |
| D-F1-3 | Los paquetes `…test` (suites de contrato y dobles) quedan exentos de `un_fichero_un_test_test.go` y `exportados_cubiertos_test.go` | Sí | F0 bloque C (diseño de los candados) | |
| D-FX-5 | Los ficheros comunes de `apipublica` nacen con su **primer consumidor** (F2/F3), no en F0/F1 | Sí | F0 bloque E | |
| D-F9-1 | 🔍 **Adelantar F9**: arnés (9A) tras F0; procesos contra el binario viejo (9B) tras la parada de F1 y antes de F2; pasada contra el nuevo **dentro de cada conmutación** (9C); cierre (9D) antes de F10. Si no, T9.34 es la alternativa (todo al final) | Sí | Orden de las sesiones tras F0 | |
| D-F1-2 | Adelantar a F1 el mínimo del arnés de F9 para correr la suite de contrato de `contact` contra Postgres (encaja con D-F9-1) | Sí | F1 bloque B/D | |

## 2 · Antes de F1

| # | Pregunta | Recomendación | Bloquea | Decisión |
|---|---|---|---|---|
| D-F1-1 | Suite de contrato con forma `Contrato(t, func(t) Montaje)` como patrón para **todo** puerto con BD (la forma `func() Puerto` no basta: FK de tenants, estado no visible por el puerto) | Sí | F1 bloque A | |
| D-F1-4 | No portar el tipo `Contact` (cero instancias) | Sí | F1 bloque A | |
| D-F1-5 | Los tipos nuevos que consumen paquetes aún viejos se adaptan en `internal/arranque/puente_<x>.go` (nace al conmutar, muere cuando conmuta el consumidor) — mecanismo estándar, distinto de los «puentes» de import de `05` §4.1 | Sí | F1 bloque C | |

## 3 · La parada tras F1 (T1.20)

Las preguntas P1–P7 de [`F1-nucleo-contact/tareas.md`](F1-nucleo-contact/tareas.md) (seguir igual /
acelerar / acotar; recalibrar D-12; tamaño de bloque…), **con el `informe-piloto.md` delante**. Sin
esta decisión no arranca nada de F2 en adelante (ni F9-B).

## 4 · Antes de F9 (según D-F9-1: antes de 9A / 9B)

| # | Pregunta | Recomendación | Bloquea | Decisión |
|---|---|---|---|---|
| D-13 | Lista cerrada de procesos: P0 (humo del arranque) … P9 (diagnóstico / config push), los 8 de `05` §7.4 más dos | Sí | F9 bloque B | |
| D-F9-2 | **Sin** opción R2 en el arranque: con endpoint IP el SDK de S3 hace *path-style* solo | Sí | F9 bloque A | |
| D-F9-3 | S3 falso **dentro del proceso de test** | Sí | F9 bloque A | |
| D-F9-4 | Proceso P10 para los 9 ficheros / 25 `Test*` de `platform` con BD que sobreviven al relevo | Sí | F9 bloque B2 | |
| T-2 | Aceptar las subidas de dependencias que trae testcontainers v0.44.0 (`httpsnoop` 1.0.4→1.1.0, `otelhttp` 0.67→0.69, `klauspost/compress`; están en `cmd/server`) en un commit `chore(deps)` aislado, con la integración vieja antes | Sí | F9 bloque A | |

## 5 · Antes de cada módulo

| # | Pregunta | Recomendación | Bloquea | Decisión |
|---|---|---|---|---|
| D-F2-1 | Los 3 candados AST del canje se quedan como **AST** (no pasan a procesos de F9: vigilan cosas que ningún proceso observa) | Sí | F2 bloque C | |
| D-F2-3 | Crear `platformadmin/puertos.go` y separar el SQL de `access_requests.go` | Sí | F2 bloque B | |
| D-F2-4 | `entitlements.Fake` → paquete `entitlementstest` | Sí | F2 bloque B | |
| D-F2-5 | Sin suites de contrato para `ports/in` | Sí | F2 bloque B | |
| D-F2-6 | Relojes inyectables | Sí | F2 bloque B | |
| D-F3-1 | Gemelos en memoria a `<paquete>test` | Sí | F3 bloque B | |
| D-F3-2 | La identidad de `ErrSessionOffline` se conserva vía `platform` (hace innecesario el puente de D-FX-3) | Sí | F3 bloque G | |
| D-FX-3 | *(Alternativa a D-F3-2)* `edge/session` conserva la identidad del centinela viejo hasta F8 | Solo si D-F3-2 = no | F3 bloque G | |
| D-FX-2 | Handlers de sesión en `apipublica/sessionadmin.go`, exportados, para servir también a `:8100` | Sí | F3 bloque G | |
| D-F3-3 | `ingest.Deduper` nuevo | Sí | F3 bloque B | |
| D-F3-4 | **No** corregir la escritura del lease en `public.tenants.revoked_at` (deuda D-9): copiarla literal | Sí | F3 bloque D–F | |
| D-F3-5 | No reescribir los 7 tests de carga | Sí | F3 | |
| D-F4-2 | Añadir a `05` §3.2 el candado C2 y el test de vocabulario de `degradation` (CHECK de la migración 0075) | Sí | F4 bloque B | |
| D-F4-3 | No re-partir las fases del arranque en F4 | Sí (no hacerlo ahora) | F4 bloque E | |
| D-F4-4 | Adaptadores de transición en `puente_inferencia.go` (el de `reanalisis` muere en F7; el de `turnoacotado`, en F8) | Sí | F4 bloque E | |
| D-F5-1 / D-F8-1 | Reconstruir `conversacion/model` **en F5** (es una hoja) en vez de tender un puente | Sí (opción B) | F5 bloque B1 | |
| D-F5-2 | El candado de frontera del índice pasa a `fronteras_test.go` como arista prohibida `conversacion → catalogo/indice` | Sí | F5 bloque B1 | |
| D-F5-3 | Portar el test de rendimiento del índice (P99 ≤ 5 ms) sin `t.Skip` | Sí | F5 bloque C | |
| D-F6-1 | Segunda instancia vieja (sin estado) de `intakes.Postgres` para el carrito viejo hasta F8 | Sí | F6 bloque H | |
| D-F6-2 | Los candados INV-1 se re-tocan en F7 y F8 (su lista de directorios cambia) | Sí | F6 bloque B | |
| D-F6-3 | El esquema CRM se valida desde 3 tests (excepción declarada a E-3) | Sí | F6 bloque C | |
| D-F6-4 | Conservar byte a byte el texto `cart: la indicación…` | Sí | F6 | |
| D-F6-5 | Reloj inyectado en `Summary` | Sí | F6 bloque B | |
| D-F6-6 | Separar el SQL de `buyerdata.go`, `crud.go` y `outbox_stats.go` en ficheros `*postgres*` | Sí | F6 bloque B | |
| D-F7-1 | Segunda instancia vieja de `intake.Postgres` + `puente_captacion.go` | Sí | F7 bloque H | |
| D-F7-2 | `cmd/casebank` cambia al paquete nuevo en F10, no en F7 | Sí | F7 | |
| D-F7-3 | `pipeline/memoria.go`: verificarlo en T7.1; si es doble, a `pipelinetest/` | Verificar | F7 bloque A | |
| D-F7-4 / D-FX-1 | Las rutas de intenciones (E1–E2) se mudan en **F7**; hasta entonces, cara vieja con el gateway nuevo inyectado (cero puentes) | Sí | F3 bloque G y F7 bloque G | |
| D-FX-4 | El adaptador SQL de telemetría se queda en la cara | Sí | F6 bloque G | |
| D-F8-2 | **No** reconstruir `admin.Register` (código muerto, deuda D-7) | No reconstruir | F8 bloque C | |
| D-F8-3 | Copiar los *goldens* del carrito como fixture | Sí | F8 bloque D | |
| D-F8-4 | La regla «events sin clasificador» pasa a `fronteras_test.go` | Sí | F8 bloque C | |
| D-F8-5 | El ciclo `conversacion` ↔ `captacion` va a la lista blanca, arista a arista | Sí | F8 bloque K | |
| D-F8-6 | Las deudas D-16 y D-17 se portan como están | Sí | F8 | |

## 6 · Antes del relevo (F10)

| # | Pregunta | Recomendación | Bloquea | Decisión |
|---|---|---|---|---|
| D-F10-1 | Ventana de la prueba en UAT en sustitución: **≥ 24 h** | Sí | F10 bloque B | |
| D-F10-2 | Si la prueba sale bien, dejar `server-modular` corriendo en UAT si el relevo llega en ≤ 7 días | Sí | F10 bloque B | |
| D-F10-3 | Retirar `internal/pendiente` en el relevo | Sí | F10 bloque C | |
| D-F10-4 | Portar `integration_test.go` y borrar `cmd/server/flows_integration_test.go` | Sí | F10 bloque C | |
| D-F10-5 | Quitar `make test-integration` si existe P10 | Sí | F10 bloque C | |
| D-F10-6 | Tag `v0.3.0` tras el relevo | Sí | F10 bloque F | |
| D-F10-7 | Nueva regla de conteo en ADR-0010 (propuesta en [`F10-relevo/diseno.md`](F10-relevo/diseno.md) §3.2) | Sí | F10 bloque E | |
