# F1 · `nucleo/contact` — el piloto con parada

> **Estado: en curso — bloque A (sesión F1-01, 🌐, 2026-10-01), arrancado sobre `origin/dev` @ `77df20f`**
> (spec escrita el 2026-09-28 sobre `dev` @ `1b18932`). Norma:
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

### Entradas verificadas (T1.1 · sesión F1-01 · 2026-10-01 · `origin/dev` @ `77df20f`)

| # | Resultado |
|---|---|
| E1 | ✔ `internal/pendiente`, `internal/arranque`, `cmd/server-modular` existen; `Makefile` con `test-pendiente`, `cobertura-ficheros`, `vet-pendiente`, `vet-integracion` dentro de `ci-local` |
| E2 | ✔ la sonda `internal/nucleo/sonda/x.go` sin test (en un *worktree* desechable, ya borrado, nada commiteado) hace fallar `make ci-local` (`SONDA_RC=2`): `TestUnFicheroUnTest: internal/nucleo/sonda/x.go: falta x_test.go` |
| E3 | ✔ 1 acierto, pero en **`internal/arranque/flows.go:89`** (la copia de F0; el `:88` del README es el del viejo) |
| E4 | ✔ `internal/apipublica` existe (`apipublica.go`, `estrangulador.go` y sus tests): ya no está «vacío» |
| E5 | ✔ `git log --oneline 1b18932..origin/dev -- internal/flujos/contact` vacío; último cambio del paquete viejo: `4a901c1` |
| E6 | ✔ go1.26.5 y golangci-lint v2.12.2 (los fijados); `GATE_RC` de `make ci-local` sobre el árbol limpio: ver el commit de T1.1 |
| T-1 | ✔ reconfirmado con **v2.12.2** y la config del repo, con una sonda **fuera del repo**: `unused` marca un `const` y un `func` no exportados sin uso (2 issues); un tipo no exportado mantenido por `var _ I = (*t)(nil)` da 0 issues |

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

### Hallazgos del bloque A (sesión F1-01, 2026-10-01; medidos contra `origin/dev` @ `77df20f`)

9. **La cobertura por fichero rompía `contacttest/contrato.go`** (→ D-F1-6, T1.3b). `Evaluables`
   (`internal/candados/cobertura.go`) medía todo fichero de producción sin `Implementar` y con sentencias, y los
   paquetes `…test` solo estaban exentos de `un_fichero_un_test` y `exportados_cubiertos` (D-F1-3). La suite solo
   la ejecutan los tests de las implementaciones, desde otros paquetes, y `go test -cover` sin `-coverpkg` no lo
   cuenta: 0 % → `POR_DEBAJO=1` → `ci-local` rc≠0. Jhoan eligió eximir `…test` también de la cobertura
   (`68897a8` rojo → `776d6a2` verde). ⚠️ **Efecto colateral**: `internal/arranque/huellatest` (paquete `huellatest`,
   91,8 %) también sale de la medida y `FICHEROS_EVALUADOS` pasa de **10 a 9** (los documentos de F0 que citan 10 son
   la línea base histórica). Si se quiere recuperar: renombrar `huellatest` o acotar la condición a `internal/{modulos,nucleo}`.
10. **`resolver_test.go` no puede afirmar que `MemoryResolver`/`PostgresResolver` implementan `Resolver`**
    (`diseno.md` §6 lo pedía): en T1.3 esos tipos aún no existen. `resolver_test.go` usa un doble local; las aserciones
    del puerto van en `repository_memory_test.go` (T1.5) y `repository_postgres_test.go` (T1.6).
11. **`var _ Resolver = (*PostgresResolver)(nil)` no basta para el candado de exportados**: casa por **ident** y solo
    mira el `x_test.go` del mismo fichero (`internal/candados/exportados.go`), así que `Resolve` y `Destino` necesitan
    un ident cada uno en su test (expresión de método `(*T).Destino` o llamada). `diseno.md` §6 decía lo contrario.
12. **R-11 («ningún error de `Normalize` contiene el valor») es parcial**: `"%w: wa_username vacío: %q"` lleva el value
    (en blanco, no es PII). El contrato lo dice y el test lo fija; el texto se copia literal.
13. **`Estado{Sembrar, Dueno}` (D-F1-1) no distingue R-17**: «se conserva el estado del canónico» y «el del huérfano
    re-clavado en el canónico» dejan el **mismo dueño**. La suite afirma lo observable (un único dueño, el canónico, y las
    sesiones sin conflicto migran); lo escribe el contrato. Y `Dueno` falla el test si hay más de un dueño
    (`flow_state` tiene PK `(tenant_id, session_id, contact_id)`): el `Estado` de Postgres (T1.13) debe hacer lo mismo.
    → **D-F1-7** (abajo).
14. **Cosas del viejo que ningún test fijaba y el contrato ahora escribe**: solo dígitos ASCII; ceros a la izquierda se
    conservan; el LID corta en el primero de `@ _ :` y acepta cualquier servidor; su longitud de error es en **bytes**;
    el username no recorta sufijos; `RefsFrom` devuelve primero el número y luego el LID y el JID crudo se ignora si pn o lid
    dieron ref; el empate de antigüedad en la fusión es por id menor (el viejo de Postgres no pausa y no se afirma).
15. **Los errores de `postgres.WithTx` llegan SIN el prefijo `contact: `** (iniciar/confirmar transacción, agotar
    reintentos): solo el cuerpo de la transacción lo lleva. Y `Destino` (viejo) puede devolver a la vez una `Ref` válida y un
    error `cerrar filas` (un `defer` que asigna el error solo si no hubo otro); el contrato promete `Ref` cero en el resto de
    errores y trata `cerrar filas` aparte → **T1.11 decide**. T-7 estaba incompleta: más menciones a «backfill» en
    `repository_postgres.go:29,299,301,304-305`.
16. **`F9/requisitos.md` R9.4.d** solo admite en `test/procesos` imports de paquetes `…test` de `nucleo`, y T1.13 necesita
    importar `nucleo/contact` (el adaptador Postgres): inconsistencia entre specs, **a resolver antes del bloque B** (→ D-F1-8).
17. **Cifras de la spec caducadas** (no cambian la conclusión): hay **8** paquetes de producción y 65 ficheros de test
    consumidores (no 7/64: suman `internal/arranque/flows.go` y `internal/modulos/fronteras_test.go`); en la copia
    `internal/arranque/flows.go` el campo `contactsPG` está en `:36`, su asignación en `:92` y la construcción en `:89`;
    `contact.Contact{`/`Contact{` da un falso positivo de código (`memContact` en el viejo, `repository_memory.go:135`).
18. **`make test-pendiente` cuenta en TODO el repo** (`PENDIENTES` y `ROJOS`), no por directorio; «11 en `internal/nucleo`» sale
    del `grep` de T1.7 (que no excluye comentarios: no escribir el literal en comentarios). Reparto: 3 (`contact.go`) + 2
    (`resolver.go`) + 3 (memory) + 3 (postgres); `ROJOS=4`.

19. **Idioma (L-1 · `05` E-11, 2026-10-02): desde ahora, nombres en inglés y solo los comentarios en español.**
    Lo escrito en el bloque A **se queda como está** (`Contrato`, `Montaje`, `Estado`, `NuevoEstado`, `Sembrar`,
    `Dueno`, `EstadoMemoria`, los nombres de `Test…` y de casos, `pasa`/`muerde`). Lo que la spec nombra en español y
    **aún no existe** se escribe en inglés; correspondencia para el bloque B (T1.11): `codificarRef` → `encodeRef`,
    `sobrePushName` → `pushNameEnvelope`, `elegirCanonico` → `pickCanonicalDB` (los tres son los nombres del código
    viejo), `abrirFilas` → `openRows` (nuevo) y `nullStr` se queda. Los textos observables (errores, columnas) no se
    traducen. Si D-F1-7 se toma, el `refactor` de `Estado` es el momento natural de ponerle nombres en inglés.

#### Fricción de método y de entorno web (alimenta §4 del informe)

- Los sub-agentes con `isolation: worktree` **arrancaron en `2da10b4` (`main`)**, no en la rama de trabajo: el primer paso de
  cada prompt tiene que ser `git merge --ff-only <sha>`.
- `.claude/worktrees/` cuelga del repo: `gofmt -l .` (`fmt-check`) lo recorre y `git status` lo lista → excluirlo en
  `.git/info/exclude`; y no usar `git add -A`.
- Borrar un *worktree* tras una corrida de `ci-local` deja la caché de `golangci-lint` con rutas que ya no existen y la siguiente
  corrida da **40 issues falsos** (los `//nolint` no se pueden resolver): `golangci-lint cache clean`.
- `gh` no sirve en la VM (token inválido, GraphQL bloqueado): el PR se abre con la herramienta MCP de GitHub.
- La suite se validó **antes** del verde contra el `MemoryResolver` viejo (copia desechable y adaptador): 19/19 con `-race`,
  31 mutantes, 28 cazados (los 3 restantes, límites del puerto: hallazgo 13). Coste: ~15 min de un sub-agente, validación incluida.

## Decisiones que necesita (de Jhoan, con recomendación)

| # | Pregunta | Recomendación |
|---|---|---|
| D-F1-1 | Firma de la suite: `Contrato(t, func(t) Montaje)` con dos tenants y un observador de estado, en vez de `func() Puerto` | **Sí**, y adoptarla como patrón para todo puerto con BD (F2+) |
| D-F1-2 | ¿F1 adelanta el mínimo del arnés de F9 (`TestMain` con testcontainers + plantilla migrada) para correr la suite contra Postgres? | **Sí**: medir «suite en memoria + Postgres» es objetivo del piloto (`05` §6); F9 lo hereda. Si no, se anota «no corrido» y el informe lo dice |
| D-F1-3 | Los paquetes `…test` (suite y dobles) quedan **exentos** de `un_fichero_un_test_test.go` y `exportados_cubiertos_test.go`; los dobles con lógica llevan test propio | **Sí** (`05` E-3 no nombra el fichero de la suite). F0 lo deja previsto y **condicionado** a esta decisión en el diseño de los candados (F0 `diseno.md` §4.2–§4.3) |
| D-F1-4 | No portar el tipo `Contact` (`contact.go:54-61`): no se instancia en todo el repo (medido) | **No portarlo**, y decirlo en el commit (E-8) |
| D-F1-5 | El adaptador de tipos en `internal/arranque` (viejo `contact.Resolver` ← nuevo) como mecanismo estándar: aparecerá en cada fase cuyos tipos consuma código viejo | **Sí**, con tabla de vida (nace/muere) en cada `arquitectura.md` |
| D-F1-6 | *(decidida en F1-01)* Los paquetes `…test` quedan exentos también de la cobertura por fichero | **Sí** (decidido el 2026-10-01 por Jhoan; `DECISIONES.md`). Pendiente de mirar: el efecto en `huellatest` (hallazgo 9) |
| D-F1-7 | ¿`Estado` gana una **marca** (p. ej. `Sembrar(t, tenant, sesión, contacto, marca)` y `Dueno(…) (contacto, marca, ok)`) para que la suite distinga «se conserva el estado del canónico» de «se re-clava el del huérfano» (R-17)? Hoy solo ve el dueño (hallazgo 13) | **Sí, antes de T1.13** (el adaptador de Postgres de `Estado` aún no existe: cambiarlo ahora es barato; el doble `EstadoMemoria` y la suite se tocan en un commit `refactor`) |
| D-F1-8 | R9.4.d de F9 admite en `test/procesos` solo imports de paquetes `…test` de `nucleo`; T1.13 necesita `nucleo/contact` (hallazgo 16). ¿Se amplía R9.4.d a «los paquetes `…test` y el constructor del adaptador Postgres del puerto que prueban»? | **Sí**, con el `grep` de F9 ajustado en el mismo commit |
| D-F1-9 | ¿Se traduce también lo ya decidido que `05` E-11 exceptúa: los siete módulos de D-5, el vocabulario del método (`pendiente`/`Implementar`/`Contrato`/`Montaje`, las etiquetas) y `puente_<x>.go` (D-F1-5)? Aparecen en los candados, el `Makefile`, el hook y 81 sesiones | **No en bloque** (renombrar es caro y no cambia comportamiento). **Sí** para lo nuevo de F1 que aún no existe (`puente_contact.go`, `puenteContact`, `nuevoResolverDeContactos`, T1.14–T1.16): decidir antes de T1.14, porque D-F1-5 fijó el nombre `puente_<x>.go` |
