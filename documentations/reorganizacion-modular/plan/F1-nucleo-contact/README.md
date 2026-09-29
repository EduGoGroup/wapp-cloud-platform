# F1 · `nucleo/contact` — el piloto con parada

> **Estado: por empezar** (spec escrita el 2026-09-28 sobre `dev` @ `1b18932`). Norma:
> [`05`](../../05-metodo-contratos-y-tdd.md). Forma: [`00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md).

## Objetivo, en tres líneas

1. Reconstruir `internal/flujos/contact` (4 ficheros de producción, 964 líneas) en `internal/nucleo/contact`
   por contrato → rojo → verde, con su **suite de contrato** corrida por memoria **y** por Postgres.
2. Conmutar: `cmd/server-modular` cablea el resolver **nuevo** (a través de un adaptador de tipos en
   `internal/arranque`) con la huella idéntica; `cmd/server` no cambia ni un byte.
3. **Medir el método** (coste por fichero, umbral del 80 %, fricción de candados, suite doble) y
   **parar**: Jhoan decide si se sigue igual, se acelera o se acota (`05` §6 y §9.1).

## Entradas (tiene que ser cierto para empezar)

| # | Condición | Cómo se comprueba |
|---|---|---|
| E1 | F0 cerrado y en `dev`: `internal/pendiente`, etiqueta `pendiente`, `make test-pendiente`, `make cobertura-ficheros`, `go vet -tags pendiente ./...` dentro de `ci-local` | `ls internal/pendiente internal/arranque cmd/server-modular` · `grep -n 'test-pendiente\|cobertura-ficheros' Makefile` |
| E2 | Los candados de `05` §5 activos **y con alcance sobre `internal/nucleo/**`** (viven en `internal/modulos/`, pero F1 no escribe en `modulos/`) | Un `internal/nucleo/sonda/x.go` sin `x_test.go` hace fallar `un_fichero_un_test_test.go` (prueba de humo en T1.1, se borra) |
| E3 | `internal/arranque` es la copia de F0 y **todavía cablea `flujos/contact` viejo** | `grep -rn 'contact.NewPostgresResolver' internal/arranque` → 1 acierto (la copia de `bootstrap/arranque/flows.go:88`) |
| E4 | `internal/apipublica` existe vacío (D-10) | `ls internal/apipublica` |
| E5 | El paquete viejo no cambió desde esta spec | `git log --oneline 1b18932..origin/dev -- internal/flujos/contact` vacío; si no, se relee §E-8 de [`diseno.md`](diseno.md) |
| E6 | `dev` verde con la toolchain fijada (Go 1.26.5, golangci-lint v2.12.2) | skill `validar-antes-de-cerrar` |

## Salidas (es cierto al cerrar)

- `internal/nucleo/contact/` con 4 ficheros de producción en **verde**, cada uno con su `x_test.go`;
  `internal/nucleo/contact/contacttest/` con la suite `Contrato` y el doble de estado.
- `grep -rn 'pendiente.Implementar' internal/nucleo | wc -l` → **0**; SKIP en `internal/nucleo` → **0**.
- `make cobertura-ficheros` ≥ 80 % en `contact.go`, `resolver.go`, `repository_memory.go`
  (`repository_postgres.go` fuera, E-6).
- La suite corrida contra `PostgresResolver` con testcontainers por la sesión local (si Jhoan acepta
  D-F1-2), o anotada «no corrida» con su motivo.
- `cmd/server-modular` enlaza `internal/nucleo/contact`; `cmd/server` no (`go list -deps`); huella igual.
- `plan/F1-nucleo-contact/informe-piloto.md` escrito con la plantilla de [`tareas.md`](tareas.md) §Informe.
- **Parada** registrada: la decisión de Jhoan, con fecha, en el propio informe.

## Orden de lectura

1. [`requisitos.md`](requisitos.md) — qué se exige, en EARS.
2. [`arquitectura.md`](arquitectura.md) — consumidores, adaptador, cableado, estado en memoria, rutas.
3. [`diseno.md`](diseno.md) — los contratos fichero a fichero, la suite, las reglas viejas (E-8).
4. [`reglas.md`](reglas.md) — trampas con `fichero:línea` y definición de hecho.
5. [`tareas.md`](tareas.md) — las tareas, los bloques de sesión y la plantilla del informe.

## Bloques de sesión

| Bloque | Entorno | Tareas | Punto de parada |
|---|---|---|---|
| **A** · contratos y rojo | 🌐 | T1.1–T1.7 | `make test-pendiente` cuenta **11** pendientes en `nucleo` · `make ci-local` rc=0 · PR a `dev` |
| **B** · verde fichero a fichero | 🌐 | T1.8–T1.13 | pendientes de `nucleo` = 0 · cobertura ≥ 80 % en 3 ficheros · suite de Postgres escrita y `vet -tags integracion` rc=0 · PR |
| **C** · adaptador y conmutación | 🌐 | T1.14–T1.16 | huella igual · `go list -deps` prueba el paquete nuevo · PR · traspaso escrito |
| **D** · cierre local e informe | 💻 | T1.17–T1.19 | suite contra Postgres corrida · `dev` integrado · `informe-piloto.md` escrito |
| — · **PARADA** | Jhoan | T1.20 | Jhoan contesta las preguntas del informe |

## Contradicciones encontradas (con `04`/`05`, medidas contra el código)

1. **`04` §3** pinta `nucleo/contact/(+ 6 _test.go)`: son los 6 tests **viejos** movidos. Con `05` son
   **4** tests nuevos + `contacttest/` (3 ficheros); los 4 ficheros de integración viejos
   (`repository_postgres_test.go` y los tres `*_integration_test.go`, 12 `Test*`) **no** vienen (F9).
2. **`05` §5** sitúa los candados en `internal/modulos/`; `nucleo` queda fuera de `modulos/`. Su
   alcance tiene que incluir `internal/nucleo/**` (entrada E2), o el piloto de los candados correría
   sin candados. **Recogido en F0**: su `diseno.md` §4 («Alcance») y T0.7 lo incluyen, junto con
   `internal/apipublica`; el marco lo dice en [`../00-marco/estructura.md`](../00-marco/estructura.md) §2.
3. **`05` E-3** da la suite solo a un fichero «solo de interfaces». `resolver.go` **no** lo es: tiene 3
   centinelas, `Ref.Sendable`, `RefsFrom` y dos auxiliares (`resolver.go:17-150`). Lleva
   `resolver_test.go` **y** la suite del puerto.
4. **`05` E-3** fija `Contrato(t, nuevo func() Puerto)`. No basta para un puerto con BD: Postgres
   exige tenants existentes con UUID (FK `contacts.tenant_id`, `0006_contacts_cifrado.sql:46`) y la
   fusión migra `flow_state`, que el puerto no deja observar. Se propone
   `Contrato(t, nuevo func(t *testing.T) Montaje)` (D-F1-1).
5. **`05` E-6** habla de «mapeo de errores de `pgx`». `contact` usa `database/sql` (pgx solo como
   driver); el reintento ante `40P01`/`40001` vive en `platform/storage/postgres.WithTx`
   (`tx.go:98`), no en `contact`. Lo que se extrae son funciones puras de filas y de sobres.
6. **`05` §4** dice que los dos binarios «no comparten los paquetes de dominio». En F1 no es así:
   `cmd/server-modular` sigue enlazando `flujos/contact` viejo, porque 6 consumidores viejos llaman a
   sus funciones puras (`arquitectura.md` §2). Es correcto (sin estado), pero la frase solo vale en F10.
7. **La huella de `05` §4 es ciega a `contact`**: el paquete no registra rutas, rpc, métricas ni
   goroutines (medido: `grep -n 'go func\|metrics\|prometheus' internal/flujos/contact/*.go` → 0 en
   producción). Huella igual no prueba la conmutación; la prueban la aserción de cableado y `go list`.
8. **`02` §3.2** lista como consumidores `gateway/fleet`, `gateway/grpc` e `intakes`. Son **7** paquetes
   de producción (`go list`, `arquitectura.md` §2). Y los «12 imports de `publicapi`» son **1** de
   producción (`flows.go`) + **11** de test.

## Decisiones que necesita (de Jhoan, con recomendación)

| # | Pregunta | Recomendación |
|---|---|---|
| D-F1-1 | Firma de la suite: `Contrato(t, func(t) Montaje)` con dos tenants y un observador de estado, en vez de `func() Puerto` | **Sí**, y adoptarla como patrón para todo puerto con BD (F2+) |
| D-F1-2 | ¿F1 adelanta el mínimo del arnés de F9 (`TestMain` con testcontainers + plantilla migrada) para correr la suite contra Postgres? | **Sí**: medir «suite en memoria + Postgres» es objetivo del piloto (`05` §6); F9 lo hereda. Si no, se anota «no corrido» y el informe lo dice |
| D-F1-3 | Los paquetes `…test` (suite y dobles) quedan **exentos** de `un_fichero_un_test_test.go` y `exportados_cubiertos_test.go`; los dobles con lógica llevan test propio | **Sí** (`05` E-3 no nombra el fichero de la suite). F0 lo deja previsto y **condicionado** a esta decisión en el diseño de los candados (F0 `diseno.md` §4.2–§4.3) |
| D-F1-4 | No portar el tipo `Contact` (`contact.go:54-61`): no se instancia en todo el repo (medido) | **No portarlo**, y decirlo en el commit (E-8) |
| D-F1-5 | El adaptador de tipos en `internal/arranque` (viejo `contact.Resolver` ← nuevo) como mecanismo estándar: aparecerá en cada fase cuyos tipos consuma código viejo | **Sí**, con tabla de vida (nace/muere) en cada `arquitectura.md` |
