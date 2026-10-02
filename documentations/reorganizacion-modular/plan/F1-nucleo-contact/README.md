# F1 · `nucleo/contact` — el piloto con parada

> **Estado: en curso — bloque A (sesión F1-01, 🌐, 2026-10-01), arrancado sobre `origin/dev` @ `77df20f`** e integrado en `dev` por el
> PR #19 (sin squash, merge `6650e55`)
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
  `internal/nucleo/contact/contacthelpertest/` con la suite `Contrato` y el doble de estado.
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
   **4** tests nuevos + `contacttest/` (3 ficheros; ✎ D-F1-10, 2026-10-02: hoy `contacthelpertest/`, y de aquí abajo los
   hallazgos conservan el nombre con el que se escribieron); los 4 ficheros de integración viejos
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
   ✎ **D-F1-10 (2026-10-02): el efecto colateral queda resuelto.** El sufijo que exime pasa a ser `helpertest` (`06f08a8`).
   `huellatest` conserva su nombre —lo importa `internal/bootstrap/arranque/huella_vieja_test.go:34`, código viejo que no se toca
   (E-1)—, pierde la exención y **vuelve a medirse**: tiene su `huellatest_test.go`, sus 11 exportados aparecen en él y da 91,8 %,
   así que no muerde ninguno de los tres candados. `make cobertura-ficheros`: `FICHEROS_EVALUADOS=10 · POR_DEBAJO=0 ·
   EXENTOS_POSTGRES=1`. La suite sigue exenta con su nombre nuevo, `contacthelpertest` (`a18d4c0`).
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

20. **`contacttest/contrato.go` (737 líneas) se partió por tema** (petición de Jhoan, 2026-10-02): es el único sitio donde
    están escritas todas las promesas del puerto (19 casos, sus ayudas y los comentarios de cada regla) y crece con cada
    promesa nueva, con cada divergencia memoria ↔ Postgres que aparezca (T1.10, T1.18) y con D-F1-7. Ahora: `contrato.go`
    (entrada: `Montaje`, `Estado`, `Contrato`, tabla de casos), `resolve_`, `isolation_`, `merge_`, `destination_`,
    `concurrency_` y `pushname_contrato.go` (los casos) y `fixtures_` y `assertions_contrato.go` (las ayudas); el más
    largo tiene 168 líneas. Es un movimiento puro: las 56 declaraciones (con su comentario) tienen el mismo hash antes y
    después. Cada fichero lleva una cabecera que dice qué crece ahí, y el comentario de paquete, cómo añadir un caso.

### Hallazgos de la revisión independiente de S9–S11 (2026-10-01, sobre `dev` @ `6650e55`)

> Revisión de F9-01, F9-02 y F1-01 (`45e01a4..6650e55`). Son **hechos** comprobados contra el código de `6650e55`; lo que pide una
> decisión va a la tabla de abajo (D-F1-10…D-F1-12) y **no se decide aquí**. Los de F9, en el
> [README de F9](../F9-procesos/README.md) (contradicción 19 y 22–30). Las correcciones de código viajan en la rama `reorg/revision-s9-s11`.
>
> ✎ **2026-10-02**: D-F1-11, D-F1-12 y D-F1-13 llevan «aplicada la recomendación de la revisión» (Jhoan no decidió cada una por
> separado; pidió aplicar las recomendaciones y las confirma al integrar el PR). Lo que quedó y lo que sigue abierto, en la nota ✎ de
> los hallazgos 22, 23 y 21. Queda abierta **D-F1-14**, nueva. Los hallazgos 25 y 26, de ese mismo día, van al final.

21. ✅ **Resuelto por D-F1-10 (Jhoan, 2026-10-02; `a18d4c0`, `06f08a8`): ver el final del hallazgo.** Lo que se midió el 2026-10-01:
    **la exención de los paquetes `…test` es por SUFIJO del nombre de paquete** (→ **D-F1-10**; matiza D-F1-3 y D-F1-6). Los tres
    candados usan la misma condición, `strings.HasSuffix(f.Paquete, "test")` (`internal/candados/cobertura.go:165`,
    `unfichero.go:49`, `exportados.go:41`): un paquete de **producción** cuyo nombre acabe en «test» (`latest`, `contest`, `attest`,
    `protest`, `fastest`…) queda exento de los **tres**. Medido con una sonda en un árbol desechable de `6650e55`
    (`internal/nucleo/latest/x.go` e `internal/nucleo/contest/x.go`: un exportado sin test, sin mención y al 0 %):
    `go test ./internal/modulos/` rc=0 y `make cobertura-ficheros` rc=0 con `FICHEROS_EVALUADOS=9 · POR_DEBAJO=0` (no los lista). Con
    el mismo fichero en `manifest`, `digest`, `request` e `ingest`, que **no** acaban en «test», `un_fichero_un_test` sí muerde
    («falta x_test.go»). Hoy no hay ningún paquete afectado: en el alcance de los candados solo acaban en `test` `huellatest` y
    `contacttest` (y `fleettest`, en el árbol viejo). Los árboles de `testdata` **no fijan ese borde**: con el mutante
    `HasSuffix(f.Paquete, "est")` en los tres ficheros, `go test ./internal/candados/ ./internal/modulos/ ./cmd/cobertura-ficheros/`
    da rc=0. Dos matices a D-F1-6: `contacttest/estado.go` es un doble **con lógica** y test propio (31 de 34 sentencias, 91,2 %) y
    queda sin medir; y `05` E-6 manda crear un doble en memoria en el paquete `…test` de 12 puertos, que serán implementaciones
    completas exentas de los tres candados.
    ✅ **Resolución (D-F1-10, Jhoan, 2026-10-02): el sufijo que exime pasa a ser el COMPUESTO `helpertest`.** Estrecha D-F1-3 y
    D-F1-6; no las deroga. Dos commits en la rama `reorg/revision-s9-s11`:
    - `a18d4c0` (`refactor(nucleo)`): `internal/nucleo/contact/contacttest` pasa a llamarse **`contacthelpertest`** (directorio y
      cláusula `package` de sus 11 ficheros; movimiento puro). Cambian de prefijo dos literales que ningún test afirma:
      `contacthelpertest.Contrato: nuevo es nil…` (`contacthelpertest/contrato.go:114`) y `contacthelpertest: migrar estado de…`
      (`contacthelpertest/estado.go:102`).
    - `06f08a8` (`verde(candados)`): **una** definición, en `internal/candados/candados.go`: `helperTestSuffix = "helpertest"`
      (`:81`) e `isHelperTestPackage` (`:101`), que es cierto si el nombre termina en `helpertest` **y** tiene al menos un carácter
      delante (un paquete llamado `helpertest` a secas **no** está exento). La usan los tres candados (`unfichero.go:56`,
      `exportados.go:45`, `cobertura.go:171`) y `haySuiteContrato` (`unfichero.go:176-178`): la suite de un puerto solo de
      interfaces se busca en el paquete `<paquete>helpertest` de `<dir>/<paquete>helpertest`; una en `<paquete>test` ya no vale.
      Los árboles de `testdata` fijan ahora el borde: `latest` y `cosatest` a secas **muerden**; `cosahelpertest` queda exento, con
      su gemelo de control `cosa`. 17 mutantes, y los 17 caen (entre ellos la regla vieja, `"test"`, y el `"est"` que el
      2026-10-01 sobrevivía).

    En el árbol real: `contacthelpertest` sigue exento de los tres; `huellatest` no se renombra y vuelve a medirse (nota del
    hallazgo 9: `FICHEROS_EVALUADOS=10`); `internal/gateway/fleet/fleettest`, código viejo, conserva su nombre y está fuera del
    alcance de los candados. **No lo resuelve D-F1-10**: `contacthelpertest/estado.go`, doble con lógica (91,2 %), sigue sin
    medirse, y los dobles que manda crear `05` E-6 nacerán exentos (→ **D-F1-13**, abierta).
    **Excepción a E-11**: E-11 dice que lo ya escrito no se renombra (hallazgo 19). El renombre de `contacttest` es una
    excepción a esa regla, decidida expresamente por Jhoan en D-F1-10.
    ✎ **2026-10-02 · D-F1-13 aplicada (`88b1d85`).** De un paquete `…helpertest`, la cobertura por fichero exime ya **solo los
    ficheros de suite**: `contrato.go` y `*_contrato.go` (`isContractSuiteFile`, en `internal/candados/candados.go`, junto a
    `isHelperTestPackage`: mira la cláusula `package` y el nombre base del fichero). El porqué de eximirlos es mecánico: la suite
    solo la ejecutan los tests de las implementaciones, desde otros paquetes, y `go test -cover` sin `-coverpkg` le da 0 % en el
    perfil del suyo; a un doble lo ejecuta el test de su propio paquete, y su cobertura es real. Los dobles con lógica se miden con
    el umbral normal (80 %, D-12): `FICHEROS_EVALUADOS` pasa de 10 a **11** y entra `contacthelpertest/estado.go` con 91,1 %
    (31 de 34 sentencias: el «91,2 %» de arriba es el mismo cociente, redondeado), `POR_DEBAJO=0` (medido por la sesión que lo
    implementó, con `make cobertura-ficheros`). 26 mutantes, caen los 26. Estrecha D-F1-6 y D-F1-10; no las deroga. `05` E-3 y E-6
    ya lo dicen.
    **Queda por decidir** (→ **D-F1-14**, nueva y abierta): `un_fichero_un_test` y `exportados_cubiertos` **no se tocaron** y siguen
    eximiendo el paquete `…helpertest` entero (D-F1-3). La misma sesión midió qué pasaría si también ellos eximieran solo los
    ficheros de suite (cambio local, revertido): hoy pasarían en verde —`go test ./internal/modulos/` rc=0; `un_fichero_un_test`,
    44 ficheros recorridos y 0 violaciones; `exportados_cubiertos`, 88 y 0—, porque `estado.go` tiene su `estado_test.go`, este
    nombra sus 5 exportados, y `contacthelpertest` no tiene más ficheros que esos dos y los 9 de suite. No se aplicó: para
    `un_fichero_un_test` choca con `05` E-3, fila «Dobles de test» («su propio test solo si tienen lógica»), y con D-F1-3, cerrada.
    Con la exención por fichero, un doble **sin** lógica mordería, y haría falta la excepción «doble sin ninguna `func` con cuerpo»
    (la de F0 [`diseno.md`](../F0-andamiaje/diseno.md) §4.2); en `internal/candados` habría que volver a fijar 8 tests (18
    subtests).
22. **R-27, R-28 y R-29 están diferidas a un proceso de F9 cuya spec no las recoge** (→ **D-F1-11**). El código nuevo las manda a F9 en
    sus comentarios: «Hoy ningún test pone rojo la ausencia del reintento: es cosa del proceso «entrante a respuesta» de F9»
    (`internal/nucleo/contact/repository_postgres.go:146-147`, R-29); «Esa propiedad solo se puede clavar contra Postgres, en los
    procesos de F9» (`repository_memory.go:81-82`, R-28); y la lista «Lo que la suite NO afirma» (`contacthelpertest/contrato.go:82-86`).
    [`diseno.md`](diseno.md) §4 dice lo mismo en las filas R-26·R-27, R-28 y R-29. La spec de F9 no las nombra:
    `grep -n -i 'deadlock\|40P01\|push_name\|primer nombre\|R-27\|R-28\|R-29' plan/F9-procesos/*.md` → vacío (P3, «Del entrante a
    la respuesta», T9.15, incluido). En F10 se borran los tests viejos que hoy las fijan
    (`internal/flujos/contact/push_name_cifrado_integration_test.go` y `deadlock_integration_test.go`): esas reglas quedarían sin test.
    ✎ **2026-10-02 · D-F1-11 aplicada (`4b226c9`).** R-27, R-28 y R-29 están ya en la spec de P3:
    [`../F9-procesos/diseno.md`](../F9-procesos/diseno.md) §4, pasos 6–8 y una tabla por regla (qué se afirma por SQL, el contrato de
    `nucleo/contact` que la difiere y el test viejo que la cubre hoy), R9.6.d en `requisitos.md` y T9.15 en `tareas.md` de F9; las
    filas de [`diseno.md`](diseno.md) §4 apuntan de vuelta. Solo `.md`. El `grep` de arriba ya no da vacío. Quedan tres cosas:
    - **«Sin medir»** si P3 vigila el reintento acotado de `postgres.WithTx`: el test viejo tampoco lo vigila, y T9.15 lo mide con
      el mutante `maxTxAttempts = 1` en una copia desechable. Si sigue verde, la carencia queda declarada, no resuelta.
    - **R-27 no figura en «Lo que la suite NO afirma»** de `contacthelpertest/contrato.go`, que sí lista R-28 y R-29. La suite
      tampoco afirma R-27 (`grep -rn 'R-27' internal/nucleo/contact/contacthelpertest` → vacío): el sobre del `push_name` no se ve
      por el puerto. No se toca el `.go`: queda anotado para quien edite ese comentario.
    - **Cita caducada**: la de R-29 en `repository_postgres.go:146-147`. La frase «Hoy ningún test pone rojo la ausencia del
      reintento…» sigue en el comentario de `internal/nucleo/contact/repository_postgres.go`, en otra línea; se cita sin número.
23. **E-11 deja sin tocar la propia norma** (→ **D-F1-12**; `05` solo lo edita Jhoan). Tres sitios de `05` siguen con nombres en
    español para cosas nuevas: el ejemplo de §10 (`casos`, `entrada`, `quiere`, `textoNorm`, `frase`; `:465`, `:480-487`), la firma de
    E-3 (`:89`, `func Contrato(t *testing.T, nuevo func() Puerto)`, que además es la contraria a D-F1-1) y `sin_pendientes_test.go`
    (§5, `:289`: un fichero que aún no existe). Y E-11 no dice si el sufijo de fichero `_contrato` (`resolve_contrato.go`…, creado en
    `7069532`, **después** de E-11 —`8365132`— y a petición de Jhoan: hallazgo 20) entra en su excepción 3, que lista `Contrato` y
    `Montaje` como vocabulario del método pero no nombres de fichero.
    ✎ **2026-10-02 · D-F1-12 aplicada (`1507d78`).** Cuatro pasajes de `05`, cada uno con la marca «D-F1-12, decisión de Jhoan,
    2026-10-02»: (1) **E-3**: bajo la tabla de excepciones, un párrafo con las **dos** formas de la firma —con BD,
    `func Contrato(t *testing.T, nuevo func(t *testing.T) Montaje)` (D-F1-1); sin BD, `func Contrato(t *testing.T, nuevo func() Puerto)`—;
    (2) **E-11, excepción 3**: el sufijo de fichero `_contrato` **es** vocabulario del método (nombra `contrato.go` y los
    `*_contrato.go` de un paquete `…helpertest`; lo que va delante, en inglés); (3) **§5 y §6**: `sin_pendientes_test.go` pasa a
    `no_pending_test.go` (el fichero aún no existe, así que nace con nombre en inglés); (4) **§10**: el ejemplo, con nombres en
    inglés (`textoNorm`, `frase` → `normalizedText`, `phrase`; `casos`, `entrada`, `quiere` → `cases`, `input`, `want`). Los números
    de línea de arriba (`:465`, `:480-487`, `:89`, `:289`) caducaron: `05` pasó de 515 a 533 líneas. El nombre nuevo del candado de
    F10 está también en las specs que lo citaban y en los README de F10 y de F0 (la primera mención de cada documento dice el
    anterior). **Este hallazgo y la fila D-F1-12 no se renombran**: son el registro de cómo estaba. **Queda**: la **fila** de la
    tabla de E-3 sigue dando una sola firma (`func() Puerto`); las dos las da el párrafo de debajo, que lo avisa («la tabla de
    arriba da solo la segunda»). Otros restos de `05`, en el hallazgo 26; y specs de fases futuras con la firma vieja, en el 25.
24. **Promesas sin aserción y comentarios-contrato imprecisos** (se corrigen en el mismo PR de la revisión, rama
    `reorg/revision-s9-s11`: `01ae55a` las aserciones y `73eb2a5` los comentarios). La lista de la revisión, cuyo detalle va en esos commits: una `Ref{}` vacía cuenta; los
    textos exactos de `contact_id no encontrado`, `ErrNoRefs` y `ErrNoDestino`; `@lid` no es sufijo; `WithTx` sin prefijo en dos
    casos más (el hallazgo 15 ya anotaba el patrón); el `push_name` en la rama de carrera; y `Destino` con varias refs del mismo
    `kind`. No piden decisión. Validado con la lógica vieja como oráculo (los 4 ficheros de `internal/flujos/contact` puestos en el
    sitio de los contratos, en una copia desechable): la suite pasa entera y los siete mutantes que antes sobrevivían caen.
    Siguen **sin aserción**, y escritos como tales en el contrato: el desempate por id menor (lo cubrirá el test de
    `pickCanonicalDB`, T1.11) y qué hace Postgres con una `Ref{}` después de contarla (solo se ve contra una base real: T1.13/T1.18).

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

### Hallazgos al aplicar las recomendaciones de la revisión (2026-10-02, sobre `88b1d85`)

> Salieron al registrar D-F1-11, D-F1-12 y D-F1-13. Son **hechos**, comprobados contra los ficheros de `88b1d85`; **no se corrige
> ninguno aquí**: las specs de otras fases pueden tener su razón y `05` solo lo edita Jhoan.

25. **Firmas de suite contrarias a D-F1-1 en specs de fases futuras** (decide Jhoan si se unifican). D-F1-1, cerrada el
    2026-09-30, fija `Contrato(t, func(t *testing.T) Montaje)` para **todo** puerto con BD, y `05` E-3 lo recoge desde D-F1-12.
    Cinco sitios dan otra forma para puertos que tienen adaptador Postgres (regla: las líneas de
    `grep -rn 'Contrato[A-Za-z]*(t' --include='*.md' plan` que traen un `func`; F2 y F6 sí usan `Montaje`; F3 y F5 no escriben la
    firma, y F7 la elide: `ContratoCola(t, …)`):
    - [`../F9-procesos/diseno.md`](../F9-procesos/diseno.md) §5, «Las suites de contrato contra Postgres»: «(E-3/E-6:
      `func Contrato(t *testing.T, nuevo func() Puerto)` en `<paquete>helpertest`, D-F1-10)». Es la sección de los 22 paquetes **con
      SQL**: todos sus puertos tienen BD.
    - [`../F4-inferencia/diseno.md`](../F4-inferencia/diseno.md), «Dobles y suites»:
      `func Contrato(t *testing.T, nuevo func() tenantllm.Store)` y `Contrato(t, nuevo func() degradation.Store)`. Los dos puertos
      tienen adaptador Postgres
      (`internal/tenantllm/postgres.go`, `internal/degradation/postgres.go`), y T4.31 corre las dos suites «sobre `NewPostgres` con
      la base clonada».
    - [`../F8-conversacion/diseno.md`](../F8-conversacion/diseno.md) §1.2 y §1.4:
      `triggerhelpertest.Contrato(t, func(t) trigger.Store)` y `storehelpertest.Contrato(t, func(t) store.Repository)`. Los dos con
      BD (`store_postgres.go`,
      `repository_postgres.go`). Es una forma intermedia: recibe `t`, pero devuelve el puerto, no un `Montaje`.
    - [`../F8-conversacion/requisitos.md`](../F8-conversacion/requisitos.md) R8.2.e:
      `func Contrato(t *testing.T, nuevo func(t *testing.T) <Puerto>)` para `store`, `trigger` y `content`. `store` y `trigger`
      tienen BD; `content` no tiene adaptador
      Postgres (`ls internal/flujos/content`: `static`, `json`, `router`) y su suite es «opcional» en el `diseno.md` de F8.
    - [`../00-marco/estructura.md`](../00-marco/estructura.md) §3: «La forma de `05` E-3, `func() <Puerto>`, solo vale para un
      puerto sin BD y queda como alternativa si D-F1-1 = no». D-F1-1 está cerrada en **sí**: esa condición ya no puede darse.
26. **Restos de `05` que nadie ha arreglado** (verificados contra `05` en `88b1d85`, 533 líneas; ninguno está anotado en
    `ESTADO.md`). Los reportó un agente al aplicar D-F1-12, que solo tocó los cuatro pasajes del hallazgo 23:
    - **E-1** (`:58`): «*Única excepción*: los tres ✎ de `platform`». `DECISIONES.md` tiene cerradas más excepciones a E-1:
      D-F0-2 (un fichero de test en el paquete viejo, `huella_vieja_test.go`), D-F0-3 (tres líneas de alias en dominio viejo) y
      D-F4-1 (una línea en dos barridos AST viejos: «segunda excepción a E-1»). El README de F0 ya señaló la de D-F0-3
      (su contradicción 2).
    - **E-9** (`:175`): «(umbral propuesto, D-12)». D-12 está cerrada desde el 2026-09-27 (`DECISIONES.md` §0): 80 %, a recalibrar
      tras F1.
    - **§7.4** (`:382`): «Procesos candidatos (a cerrar en D-13)», con 8 filas. D-13 está cerrada desde el 2026-09-30: P0–P9 (esos
      8 más P0 y P9), y P10 por D-F9-4.
    - **E-11, punto 2** (`:209-210`): «No hay renombres masivos ni «de paso»». No recoge la excepción que Jhoan decidió en
      D-F1-10, `contacttest` → `contacthelpertest` (final del hallazgo 21).
    - **E-11 no dice en qué idioma van los mensajes de fallo de un test** (`t.Errorf`, `t.Fatalf`). Su tabla (`:194-196`) reparte
      nombres (inglés) y comentarios, documentación y mensajes de commit (español); un mensaje de fallo no es ninguna de esas
      cosas. Lo único escrito es la nota del ejemplo de §10 (`:461-462`, D-F1-12): «los mensajes de fallo de los tests, que no son
      nombres, quedan como estaban», que habla de ese ejemplo y no da una regla.

## Decisiones que necesita (de Jhoan, con recomendación)

| # | Pregunta | Recomendación |
|---|---|---|
| D-F1-1 | Firma de la suite: `Contrato(t, func(t) Montaje)` con dos tenants y un observador de estado, en vez de `func() Puerto` | **Sí**, y adoptarla como patrón para todo puerto con BD (F2+) |
| D-F1-2 | ¿F1 adelanta el mínimo del arnés de F9 (`TestMain` con testcontainers + plantilla migrada) para correr la suite contra Postgres? | **Sí**: medir «suite en memoria + Postgres» es objetivo del piloto (`05` §6); F9 lo hereda. Si no, se anota «no corrido» y el informe lo dice |
| D-F1-3 | Los paquetes `…test` (suite y dobles) quedan **exentos** de `un_fichero_un_test_test.go` y `exportados_cubiertos_test.go`; los dobles con lógica llevan test propio | **Sí** (`05` E-3 no nombra el fichero de la suite). F0 lo deja previsto y **condicionado** a esta decisión en el diseño de los candados (F0 `diseno.md` §4.2–§4.3). ✎ **Estrechada por D-F1-10** (2026-10-02): el paquete exento es `…helpertest`. ✎ D-F1-13 (2026-10-02) **no** la estrecha: estos dos candados siguen eximiendo el paquete entero; extenderlo es D-F1-14, abierta |
| D-F1-4 | No portar el tipo `Contact` (`contact.go:54-61`): no se instancia en todo el repo (medido) | **No portarlo**, y decirlo en el commit (E-8) |
| D-F1-5 | El adaptador de tipos en `internal/arranque` (viejo `contact.Resolver` ← nuevo) como mecanismo estándar: aparecerá en cada fase cuyos tipos consuma código viejo | **Sí**, con tabla de vida (nace/muere) en cada `arquitectura.md` |
| D-F1-6 | *(decidida en F1-01)* Los paquetes `…test` quedan exentos también de la cobertura por fichero | **Sí** (decidido el 2026-10-01 por Jhoan; `DECISIONES.md`). ✎ **Estrechada por D-F1-10** (2026-10-02): el paquete exento es `…helpertest`, y el efecto en `huellatest` queda resuelto: vuelve a medirse, `FICHEROS_EVALUADOS=10` (hallazgo 9). ✎ **Estrechada por D-F1-13** (2026-10-02, `88b1d85`): de ese paquete solo quedan exentos de la cobertura los ficheros de suite; `FICHEROS_EVALUADOS=11` |
| D-F1-7 | ✅ **Decidida (Jhoan, 2026-10-02)**: sí, en un `refactor(nucleo)` propio antes de T1.13; lo nuevo en inglés, lo ya escrito no se renombra (D-F1-9 sigue abierta) · ¿`Estado` gana una **marca** (p. ej. `Sembrar(t, tenant, sesión, contacto, marca)` y `Dueno(…) (contacto, marca, ok)`) para que la suite distinga «se conserva el estado del canónico» de «se re-clava el del huérfano» (R-17)? Hoy solo ve el dueño (hallazgo 13) | **Sí, antes de T1.13** (el adaptador de Postgres de `Estado` aún no existe: cambiarlo ahora es barato; el doble `EstadoMemoria` y la suite se tocan en un commit `refactor`) |
| D-F1-8 | ✅ **Decidida (Jhoan, 2026-10-02)**: sí, con el `grep` sobre imports **directos** y los tenants sembrados por SQL (detalle en `DECISIONES.md`) · R9.4.d de F9 admite en `test/procesos` solo imports de paquetes `…helpertest` (`…test` hasta D-F1-10) de `nucleo`; T1.13 necesita `nucleo/contact` (hallazgo 16). ¿Se amplía R9.4.d a «los paquetes `…helpertest` y el constructor del adaptador Postgres del puerto que prueban»? | **Sí**, con el `grep` de F9 ajustado en el mismo commit |
| D-F1-9 | ¿Se traduce también lo ya decidido que `05` E-11 exceptúa: los siete módulos de D-5, el vocabulario del método (`pendiente`/`Implementar`/`Contrato`/`Montaje`, las etiquetas) y `puente_<x>.go` (D-F1-5)? Aparecen en los candados, el `Makefile`, el hook y 81 sesiones | **No en bloque** (renombrar es caro y no cambia comportamiento). **Sí** para lo nuevo de F1 que aún no existe (`puente_contact.go`, `puenteContact`, `nuevoResolverDeContactos`, T1.14–T1.16): decidir antes de T1.14, porque D-F1-5 fijó el nombre `puente_<x>.go` |
| D-F1-10 | ✅ **Decidida (Jhoan, 2026-10-02)** · *(de la revisión independiente, 2026-10-01; hallazgo 21)* ¿Se **estrecha** la exención de los paquetes `…test`? Era por sufijo del nombre: un paquete de producción `latest` o `contest` quedaba exento de los tres candados, y los dobles con lógica no se miden. Estrecha D-F1-3 y D-F1-6, que siguen en pie | **Sufijo compuesto `helpertest`** (`a18d4c0`, `06f08a8`; resolución en el hallazgo 21): queda exento solo el paquete cuyo nombre termina en `helpertest` con algo delante. Jhoan eligió el sufijo compuesto **frente a** la parte **(i)** de la propuesta de la revisión, que era estructural: eximir `Xtest` solo si vive en `<dir de X>/Xtest` y existe el paquete `X` (la forma de `haySuiteContrato`). Efectos: `latest` deja de estar exento; `contacttest` pasa a llamarse `contacthelpertest` y sigue exento; `huellatest` vuelve a medirse, lo que cierra el «pendiente de mirar» de D-F1-6. **Queda fuera** la parte **(ii)** de la propuesta (eximir de la cobertura solo los ficheros de suite): los dobles con lógica dentro de `…helpertest` siguen sin medirse → **D-F1-13**. ✎ **Estrechada por D-F1-13** (2026-10-02, `88b1d85`): la parte (ii) queda aplicada para la cobertura por fichero |
| D-F1-11 | ✅ **Aplicada la recomendación de la revisión** (2026-10-02, a petición de Jhoan de aplicar las recomendaciones; se confirma al integrar el PR) · `4b226c9` · *(de la revisión independiente, 2026-10-01; hallazgo 22)* ¿Se añaden R-27, R-28 y R-29 (el nombre tardío se sella, gana el primer nombre, ráfaga sin `40P01` con la siembra sin nombre) a la spec de **P3** de F9 (`plan/F9-procesos/diseno.md` §4)? Hoy el código las difiere allí y la spec no las nombra | **Sí**, antes de F9-03 (B1 escribe P3 en T9.15): si no, en F10 se borran los tests viejos y quedan sin test. **Aplicado**: P3, pasos 6–8 y su tabla (`plan/F9-procesos/diseno.md` §4), R9.6.d y T9.15. Queda «sin medir» si P3 vigila el reintento de `postgres.WithTx` (nota ✎ del hallazgo 22) |
| D-F1-12 | ✅ **Aplicada la recomendación de la revisión** (2026-10-02, a petición de Jhoan de aplicar las recomendaciones; se confirma al integrar el PR) · `1507d78` · *(de la revisión independiente, 2026-10-01; hallazgo 23)* ¿Se actualiza `05` para que cumpla E-11 (el ejemplo de §10, la firma de E-3 con las dos formas de D-F1-1, `sin_pendientes_test.go`) y se dice si el sufijo de fichero `_contrato` es vocabulario del método (excepción 3)? | **Sí** (solo Jhoan toca la norma). Mientras tanto, la skill `contrato-tdd`, `00-marco/glosario.md` y `00-marco/estructura.md` ya dan las dos formas de la firma. **Aplicado**: `05` da las dos firmas bajo la tabla de E-3, el ejemplo de §10 va en inglés, el candado de F10 se llama `no_pending_test.go` (esta fila conserva el nombre anterior: es registro) y el sufijo `_contrato` es vocabulario del método. Lo que queda, en la nota ✎ del hallazgo 23 y en el hallazgo 26 |
| D-F1-13 | ✅ **Aplicada la recomendación de la revisión** (2026-10-02, a petición de Jhoan de aplicar las recomendaciones; se confirma al integrar el PR) · `88b1d85` · *(resto de D-F1-10, 2026-10-02; hallazgo 21)* ¿Se miden los **dobles con lógica** que viven en un paquete `…helpertest`? D-F1-10 no lo resuelve: `contacthelpertest/estado.go` (doble con lógica y test propio, 91,2 %) sigue fuera de la cobertura por fichero, y `05` E-6 manda crear un doble en memoria en el `…helpertest` de 12 puertos, que serán implementaciones completas exentas de los tres candados | La revisión propuso, como parte (ii) de D-F1-10, eximir de la cobertura solo los ficheros de suite (`contrato.go` y `*_contrato.go`), para que los dobles con lógica se midan (hasta el 2026-10-02 esta fila decía «abierta, sin recomendación nueva»). **Aplicado** eso mismo (`isContractSuiteFile`): `FICHEROS_EVALUADOS` 10 → 11, entra `contacthelpertest/estado.go` (91,1 %; medido por la sesión que lo implementó). Estrecha D-F1-6 y D-F1-10; `un_fichero_un_test` y `exportados_cubiertos` siguen eximiendo el paquete entero (D-F1-3) → **D-F1-14** |
| D-F1-14 | *(resto de D-F1-13, 2026-10-02; nota ✎ del hallazgo 21)* ¿Se extiende «solo los ficheros de suite exentos» a `un_fichero_un_test` y `exportados_cubiertos`, que hoy eximen el paquete `…helpertest` entero (D-F1-3)? | **Abierta**, sin recomendación: solo hechos. Hoy pasarían en verde (44 y 88 ficheros recorridos, 0 violaciones; medido por la sesión que aplicó D-F1-13, cambio local revertido). Para `un_fichero_un_test` choca con `05` E-3, fila «Dobles de test» («su propio test solo si tienen lógica»), y con D-F1-3: haría falta la excepción «doble sin ninguna `func` con cuerpo». Decide Jhoan |
