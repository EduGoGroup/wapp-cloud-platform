# F1 · Tareas

> Formato de [`00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §3. Skills: `contrato-tdd`
> (cada fichero), `reconstruir-modulo` (la fase), `validar-antes-de-cerrar` (cada gate),
> `traspaso-web-local` (T1.16→T1.17), `procesos-testcontainers` (T1.13, T1.18). `N` =
> `internal/nucleo/contact`. Todo gate se lee **sin pipe**; todo commit lleva la línea `Piloto:` de
> [`reglas.md`](reglas.md) §4. La web trabaja en su rama y abre PR hacia `dev`, que se integra
> **sin squash** (`gh pr merge --merge`): rojo y verde siguen siendo commits distintos.

## Bloque A · contratos y rojo de `nucleo/contact` · 🌐 · T1.1–T1.7
Para cuando: `make test-pendiente` cuenta **11** en `internal/nucleo` · `make ci-local` rc=0 · PR abierto hacia `dev`.

> ✎ **D-F1-10 (2026-10-02): hoy `contacthelpertest`** (`a18d4c0`), y el sufijo que exime de los tres candados de fichero es
> `helpertest` (`06f08a8`). Las tareas `[x]` de este bloque conservan el nombre `contacttest`, el sufijo `…test` y la cifra
> `FICHEROS_EVALUADOS=9` con que se cerraron; hoy son 10, porque `huellatest` vuelve a medirse (README, hallazgos 9 y 21).
> ✎ **D-F1-13 (2026-10-02, `88b1d85`)**: son 11: de `contacthelpertest` la cobertura solo exime los ficheros de suite, y `estado.go` se mide.

- [x] **T1.1 · Verdad de campo y entradas** · 🌐 · dep. F0 cerrado · cumple R1.1.e — cerrada en `afa63f3`
  - **Ficheros**: `plan/F1-nucleo-contact/README.md` (estado → «en curso», SHA de arranque)
  - **Hecho cuando**: las 6 entradas del README se comprueban con su comando y el resultado queda en el commit; la sonda `internal/nucleo/sonda/x.go` sin test hace fallar `make ci-local` (entrada E2) y **se borra sin commitear**; T-1 reconfirmado con golangci-lint v2.12.2 (sonda fuera del repo).
  - **Gate**: `GOWORK=off make ci-local > /tmp/g.log 2>&1; echo GATE_RC=$? >> /tmp/g.log; tail -1 /tmp/g.log` → `GATE_RC=0`
  - **Commit**: `docs(reorganizacion-modular): F1 arranca — entradas verificadas`
- [x] **T1.2 · rojo(nucleo): contrato y test de `contact.go`** · 🌐 · dep. T1.1 · cumple R1.1.a–c, R1.2.a–c, R1.4.c–d — cerrada en `b37a8c8`
  - **Ficheros**: `N/contact.go`, `N/contact_test.go`
  - **Hecho cuando**: 8 exportados (sin `Contact`, D-F1-4) con comentario-promesa de reglas R-01…R-11 y textos de [`diseno.md`](diseno.md) §5; 3 `panic(pendiente.Implementar)`; el test cubre cada promesa y cada texto; `-run '^TestNormalize_Telefono$'` da rc≠0.
  - **Gate**: `GOWORK=off go vet -tags pendiente ./internal/nucleo/...; echo rc=$?` → 0
  - **Commit**: `rojo(nucleo): contrato de contact/contact`
- [x] **T1.3 · rojo(nucleo): contrato y test de `resolver.go`** · 🌐 · dep. T1.2 · cumple R1.2.a–b, R1.4.c — cerrada en `d915d41` (+ precisión del comentario de `Resolver` en `b001c35`)
  - **Ficheros**: `N/resolver.go`, `N/resolver_test.go`
  - **Hecho cuando**: centinelas, `Resolver`, `StateMigrator` (comentarios corregidos T-4, T-5, diseño §2), `Ref.Sendable` y `RefsFrom` con `panic`; el test fija R-20, R-22, N-05 y los textos; menciona `Resolver`/`StateMigrator` con aserciones de compilación (diseño §6).
  - **Gate**: igual que T1.2
  - **Commit**: `rojo(nucleo): contrato de contact/resolver`
- [x] **T1.3b · rojo/verde(candados): los paquetes `…test` no se miden en cobertura** · 🌐 · dep. T1.3 · **nueva, D-F1-6 (Jhoan, 2026-10-01)** — cerrada en `68897a8` (rojo) y `776d6a2` (verde)
  - **Por qué**: `contacttest/contrato.go` (la suite) saldría al 0 % en `make cobertura-ficheros` y rompería `ci-local` (README §Hallazgos 9). Se ejecutó **antes** de T1.4 para que ningún commit de la rama rompa el gate.
  - **Ficheros**: `internal/candados/cobertura.go` (solo `Evaluables` y los comentarios de contrato), `cobertura_test.go` (el test del rojo se fusionó aquí en el verde) y los árboles `testdata/cobertura/paquetes-test/{pasa,muerde}/`.
  - **Hecho cuando**: `go test -race ./internal/candados/... ./cmd/cobertura-ficheros/...` rc=0 (131 PASS, 0 SKIP) · cobertura de `cobertura.go` 99,3 % · `make cobertura-ficheros` rc=0 con `FICHEROS_EVALUADOS=9` (era 10: sale `internal/arranque/huellatest`, efecto colateral anotado en D-F1-6).
  - **Commits**: `rojo(candados): los paquetes …test no se miden en cobertura (D-F1-6)` · `verde(candados): Evaluables no mide los paquetes …test (D-F1-6)`
- [x] **T1.4 · rojo(nucleo): suite `contacttest.Contrato` y doble de estado** · 🌐 · dep. T1.3 · cumple R1.3.a — cerrada en `8f2a4db`
  - **Ficheros**: `N/contacttest/contrato.go`, `N/contacttest/estado.go`, `N/contacttest/estado_test.go` (tras el cierre del bloque, `contrato.go` se partió en 9 ficheros por tema, en `7069532`: README hallazgo 20)
  - **Hecho cuando**: los 19 casos de diseño §3 escritos, sin `t.Skip`; el doble (`NuevoEstado()`, implementa `StateMigrator` y `Estado`, conserva el canónico en conflicto) nace **completo** con su test en verde (D-F1-3); `go doc` muestra `Contrato(t *testing.T, nuevo func(t *testing.T) Montaje)`.
  - **Gate**: `GOWORK=off go test -race ./internal/nucleo/contact/contacttest/; echo rc=$?` → 0
  - **Commit**: `rojo(nucleo): suite de contrato de contact.Resolver y doble de estado`
- [x] **T1.5 · rojo(nucleo): contrato y test de `repository_memory.go`** · 🌐 · dep. T1.4 · cumple R1.3.b — cerrada en `89b223b`
  - **Ficheros**: `N/repository_memory.go`, `N/repository_memory_test.go` (`package contact_test`, T-2)
  - **Hecho cuando**: struct sin campos, `NewMemoryResolver`/`Resolve`/`Destino` con `panic`; el test corre `contacttest.Contrato` y añade: migrador nil, migrador que falla (texto `contact: migrar flow_state en fusión:`), una llamada por huérfano.
  - **Gate**: vet `-tags pendiente` rc=0 · `go test -tags pendiente -run '^TestMemoryResolver_Contrato$' ./internal/nucleo/contact/` rc≠0
  - **Commit**: `rojo(nucleo): contrato de contact/repository_memory`
- [x] **T1.6 · rojo(nucleo): contrato y test de `repository_postgres.go`** · 🌐 · dep. T1.3 · cumple R1.2.a, R1.4.a — cerrada en `32b7bfb`
  - **Ficheros**: `N/repository_postgres.go`, `N/repository_postgres_test.go`
  - **Hecho cuando**: `PostgresResolver` (sin campos), `NewPostgresResolver`, `Resolve`, `Destino` con `panic` y el comentario largo del porqué (MD-046.5, deadlock, sin lector de `push_name`) **sin** referencias por línea (T-6, T-7); el test afirma `ErrNoRefs` sin BD con `db` nil. Los tests de las funciones puras llegan con ellas en T1.11 (T-1).
  - **Gate**: igual que T1.5
  - **Commit**: `rojo(nucleo): contrato de contact/repository_postgres`
- [x] **T1.7 · Cierre del bloque A** · 🌐 · dep. T1.2–T1.6 — cerrada en `b001c35` (último commit de código del bloque; T1.7 no tiene commit propio: el cierre es el `docs(reorganizacion-modular): F1-01 cierra el bloque A` y el PR)
  - **Hecho cuando**: `grep -rn 'pendiente.Implementar' --include='*.go' internal/nucleo | wc -l` → **11**; `make test-pendiente` coincide; `ci-local` rc=0; PR abierto; minutos del bloque anotados.
  - **Gate**: skill `validar-antes-de-cerrar` completa
  - **Commit**: — (solo PR)
  - **Medido (sesión F1-01, 2026-10-01)**: `GOWORK=off make ci-local` → `GATE_RC=0` (86 líneas `ok` —80 paquetes con tests + 6 que `cobertura-ficheros` vuelve a correr; no son 86 paquetes—, 0 issues, golangci-lint v2.12.2, go1.26.5) · `go vet -tags pendiente ./...` rc=0 · `make test-pendiente` → `PENDIENTES=11`, `ROJOS=4` · `grep` en `internal/nucleo` → 11 · `--- SKIP` = 0 (con `-v`) · cada rojo corrido solo (`TestNormalize_Telefono`, `TestRefsFrom`, `TestPostgresResolver_SinRefs_ErrNoRefs`, `TestMemoryResolver_Contrato`) rc=1 por el `panic` · `make cobertura-ficheros` rc=0 (`FICHEROS_EVALUADOS=9`, `POR_DEBAJO=0`, `EXENTOS_POSTGRES=1`) · `git diff --stat origin/dev..HEAD -- internal/flujos internal/bootstrap internal/gateway internal/intakes internal/publicapi cmd/server` vacío · `git cat-file -e` de los 9 SHA.
  - **Minutos del bloque**: 53 min de pared desde el arranque hasta `b001c35` (14:05 → 14:58 UTC), con la ola 2 (candados ‖ `contacttest` ‖ postgres) y las olas 0–1 en paralelo; las líneas `Piloto:` de cada commit dan 9 · 10 · 0 · 6 · 0 · 15 · 5 · 7 · 1 min (T1.1, T1.2, T1.3, T1.3b, T1.4, T1.6, T1.5 y la precisión de `resolver.go`; los sub-agentes corrieron en paralelo, **no son aditivos**). Se corrió `ci-local` 4 veces (sonda de E2, gate base —una falló por la caché de lint, ver README «Fricción»—, base repetido y cierre): duración por corrida **sin medir** con precisión.
  - **Tras el cierre, en la misma rama y el mismo PR** (anotado por la revisión independiente, 2026-10-01): `8365132` (`docs`: la regla de idioma **E-11** en `05`, L-1 en `DECISIONES.md` y el hallazgo 19 del README) y `7069532` (`refactor(nucleo)`: `contacttest/contrato.go` partido en 9 ficheros `*_contrato.go`, movimiento puro; hallazgo 20). **PR #19 integrado en `dev` sin squash**: merge `6650e55` (12 commits, de `afa63f3` a `7069532`).

## Bloque B · verde fichero a fichero · 🌐 · T1.8–T1.13
Entrada: PR del bloque A integrado en `dev` (✔ PR #19, merge `6650e55`, 2026-10-01). Para cuando: 0 pendientes en `internal/nucleo` · `make cobertura-ficheros` ≥ 80 % en 3 ficheros · `vet -tags integracion ./test/procesos/...` rc=0 · PR abierto.

- [x] **T1.8 · verde(nucleo): `contact.go`** · 🌐 · dep. T1.7 · cumple R1.1.d — cerrada en `9e8f740` (contact.go 97,6 %)
  - **Ficheros**: `N/contact.go`, `N/contact_test.go` (sin etiqueta)
  - **Hecho cuando**: lógica de `V/contact.go` con su porqué; `go test -race ./internal/nucleo/contact/ -run 'Normalize|NewRef|ValidateKind|ErrInvalidRef'` rc=0; cobertura del fichero ≥ 80 % (base vieja 98,0 %).
  - **Gate**: `GOWORK=off go test -race ./internal/nucleo/contact/; echo rc=$?` → 0 · `make cobertura-ficheros`
  - **Commit**: `verde(nucleo): contact/contact`
- [x] **T1.9 · verde(nucleo): `resolver.go`** · 🌐 · dep. T1.8 — cerrada en `8e7a891` (resolver.go 100,0 %; tests de `pickDestino`, `dedupeRefs`, `destinoPref` y `lidServer`)
  - **Hecho cuando**: `Sendable`, `RefsFrom`, `pickDestino`, `dedupeRefs`, `lidServer`, `destinoPref`; tests de los dos auxiliares añadidos (E-7); ≥ 80 % (base 95,2 %).
  - **Gate** y **Commit**: igual · `verde(nucleo): contact/resolver`
- [x] **T1.10 · verde(nucleo): `repository_memory.go`** · 🌐 · dep. T1.9 · cumple R1.3.b — cerrada en `222c4c8` (repository_memory.go 95,6 %; suite 19/19 con `-race`, 0 SKIP)
  - **Hecho cuando**: la suite pasa entera contra memoria con `-race`, 0 SKIP (`-v`); ≥ 80 % (base 91,1 %).
  - **Gate**: `GOWORK=off go test -race -v ./internal/nucleo/... > /tmp/t.log 2>&1; echo rc=$?; grep -c -- '--- SKIP' /tmp/t.log` → `rc=0` y `0`
  - **Commit**: `verde(nucleo): contact/repository_memory`
- [x] **T1.11 · verde(nucleo): `repository_postgres.go`** · 🌐 · dep. T1.9 · cumple R1.4.a–b, R1.4.d — cerrada en `8307afb` (exento E-6, 31,1 %; funciones puras al 100 %; SQL idéntico; hallazgo 15: se conserva lo viejo)
  - **Hecho cuando**: SQL copiado literal (`diff <(grep -o 'public\.[a-z_]*' V/repository_postgres.go | sort -u) <(… N …)` vacío); funciones puras de diseño §2 con sus tests unitarios (sin BD); fuera del umbral (E-6), cobertura anotada.
  - **Nombres (E-11: correspondencia de la fase)**: [`diseno.md`](diseno.md) §2 nombra en español funciones que aún no existen; se escriben en inglés: `codificarRef` → `encodeRef`, `sobrePushName` → `pushNameEnvelope`, `elegirCanonico` → `pickCanonicalDB` (los tres, los nombres del código viejo: `V/repository_postgres.go:184`, `:221` y `:363`), `abrirFilas` → `openRows` (nuevo); `nullStr` se queda. Es la misma tabla del README, hallazgo 19, traída aquí porque `05` E-11 manda anotarla en el `tareas.md` de la fase. Los textos observables (errores, columnas) no se traducen.
  - **Gate** y **Commit**: igual que T1.8 · `verde(nucleo): contact/repository_postgres`
- [x] **T1.12 · refactor(nucleo) y medición del verde** · 🌐 · dep. T1.10, T1.11 — cerrada sin commit propio (no hizo falta `refactor`); medida sobre `4bc398d`: `PENDIENTES=0 · ROJOS=0`; cobertura contact.go 97,6 % · resolver.go 100,0 % · repository_memory.go 95,6 % · contacthelpertest/estado.go 92,6 % · repository_postgres.go EXENTO (31,1 %). D-F1-7 se aplicó después, en `4bbd138` (`refactor(nucleo)`, decisión de Jhoan)
  - **Hecho cuando**: pendientes en `internal/nucleo` = 0; tabla de cobertura por fichero anotada en el PR; `refactor` solo si hace falta (tests verdes antes y después).
  - **Commit**: `refactor(nucleo): …` o ninguno
- [x] **T1.13 · procesos(contact): la suite contra `PostgresResolver`** · 🌐→💻 · dep. T1.11 · cumple R1.3.c · **solo si D-F1-2** — cerrada en la web en `4bc398d` (fichero, `vet -tags integracion` rc=0, `sin_bd_viva` verde, grep de R9.4.d ajustado); pre-chequeo web viejo y nuevo rc=0 · 20 PASS · 0 SKIP. **La corrida que cuenta es T1.18** (💻 F1-04, [traspaso](../../traspasos/TRASPASO-F1-B-suite-postgres.md))
  - **Ficheros**: `test/procesos/contact_contrato_test.go` (`//go:build integracion`) y **reutiliza el arnés que ya dejó F9-A** (F9-02, 2026-10-01: `main_test.go` —un contenedor `postgres:17-alpine`, plantilla migrada una vez— y `base_test.go` —`nuevaBase(t, proceso)`, una base clonada por **nombre de proceso**, `proc_<proceso>_<binario>`, no por prueba (`test/procesos/base_test.go:136-141`): pedir dos veces el mismo nombre mientras la primera base sigue viva hace fallar el `CREATE DATABASE` y `nuevaBase` llama a `t.Fatalf`—; skill `procesos-testcontainers`): **no se recrea**, y la suite de contrato pide su base con `nuevaBase` y un nombre propio. ⚠️ `contacthelpertest.Contrato` llama a `nuevo` **una vez por caso** (19, en serie: no hay `t.Parallel`) y exige un `Montaje` limpio: el de Postgres necesita un nombre de proceso distinto por caso, o el mismo solo si la base del caso anterior ya se borró (el `Cleanup` del subtest)
  - ✎ **Desviación decidida (D-F1-8, Jhoan, 2026-10-02)**: los 2 tenants se siembran **por SQL** (`INSERT INTO public.tenants … RETURNING id::text`), no con `postgres.NewTenantRepository`; el fichero importa solo `contacthelpertest`, `internal/nucleo/contact` (constructor del adaptador) e `internal/platform/crypto` (sus argumentos), y el `grep` de R9.4.d pasa a mirar imports directos. D-F1-7 (la marca de `Estado`) se aplica antes, en su `refactor(nucleo)` propio.
  - **Hecho cuando**: el `Montaje` de Postgres siembra 2 tenants con `postgres.NewTenantRepository(db).Create` y observa `flow_state` por SQL; `vet -tags integracion` rc=0; si Docker responde en la web, corrida de **pre-chequeo** anotada (no cierra).
  - **Gate**: `GOWORK=off go vet -tags integracion ./test/procesos/...; echo rc=$?` → 0 · candado `sin_bd_viva_test.go` verde
  - **Commit**: `procesos(contact): la suite de contrato contra Postgres`

## Bloque C · adaptador y conmutación · 🌐 · T1.14–T1.16
Entrada: PR del bloque B integrado. Para cuando: huella igual · `go list -deps` prueba el paquete nuevo · traspaso escrito · PR abierto.

- [ ] **T1.14 · rojo(arranque): contrato y test de `puente_contact.go`** · 🌐 · dep. T1.12 · cumple R1.4.e
  - **Ficheros**: `internal/arranque/puente_contact.go`, `…/puente_contact_test.go`
  - **Hecho cuando**: `puenteContact` con `Resolve`/`Destino` en `panic` y `var _ viejo.Resolver = (*puenteContact)(nil)` (T-1); el test afirma copia de refs, texto de error idéntico, `errors.Is` con centinela viejo **y** nuevo, y el **corpus de equivalencia** viejo ↔ nuevo de `Normalize`/`NewRef`/`RefsFrom`/`Sendable` (casos de R-02…R-07, R-22, N-05).
  - **Gate**: vet `-tags pendiente` rc=0 · el test solo, rc≠0
  - **Commit**: `rojo(arranque): contrato del adaptador de contact`
- [ ] **T1.15 · verde(arranque): `puente_contact.go`** · 🌐 · dep. T1.14
  - **Hecho cuando**: test en verde con `-race`; ≥ 80 %.
  - **Commit**: `verde(arranque): adaptador de contact`
- [ ] **T1.16 · conmutar(nucleo): el arranque nuevo cablea `nucleo/contact`** · 🌐 · dep. T1.15 · cumple R1.4.b, R1.5.a–d
  - **Ficheros**: la copia de `flows.go` en `internal/arranque` (y la de `fase3_almacenes.go` si hace falta), `puente_contact.go` (+ `nuevoResolverDeContactos(db, cipher, kp)`), su test (aserción de cableado: devuelve `*puenteContact` sobre `*contact.PostgresResolver`); se elimina `contactsPG` de la copia (T-8); `traspasos/TRASPASO-F1-nucleo-contact.md`
  - **Hecho cuando**: `go list -deps ./cmd/server-modular | grep -c 'internal/nucleo/contact$'` → 1 · `go list -deps ./cmd/server | grep -c 'internal/nucleo/'` → 0 · `grep -rn 'viejo\.NewPostgresResolver\|flujos/contact.*NewPostgresResolver' internal/arranque` → 0 · huella sin diferencias · `git diff --stat <inicio>..HEAD -- internal/flujos internal/bootstrap cmd/server` vacío.
  - **Gate**: `validar-antes-de-cerrar` completa
  - **Commit**: `conmutar(nucleo): el arranque nuevo cablea nucleo/contact`

## Bloque D · cierre local e informe · 💻 · T1.17–T1.19
Para cuando: suite contra Postgres corrida · todo en `dev` · `informe-piloto.md` escrito.

- [ ] **T1.17 · Recibir el traspaso y repetir los gates** · 💻 · dep. T1.16 · cumple R1.6.a
  - **Hecho cuando**: `make ci-local` con v2.12.2 rc=0 leído del log; los tres PR integrados en `dev` sin squash (`git log --oneline` muestra cada `rojo`/`verde` por separado); la §7 del traspaso refutada o confirmada contra el código.
- [ ] **T1.18 · La suite contra Postgres, en local** · 💻 · dep. T1.13, T1.17 · cumple R1.3.c–d
  - **Hecho cuando**: `WAPP_PROCESOS_BINARIO=viejo GOWORK=off go test -tags integracion -race -v -run Contact ./test/procesos/ > /tmp/p.log 2>&1; echo rc=$?` → 0 (✎ 2026-10-02: sin `WAPP_PROCESOS_BINARIO=viejo|nuevo`, `TestMain` sale con código 2; a la suite le da igual cuál), `--- SKIP` = 0, tiempo total y de arranque del contenedor anotados; divergencias memoria ↔ Postgres listadas. Sin D-F1-2: «no corrido» y motivo.
- [ ] **T1.19 · docs(reorganizacion-modular): informe del piloto F1** · 💻 · dep. T1.18 · cumple R1.7.a
  - **Ficheros**: `plan/F1-nucleo-contact/informe-piloto.md` (plantilla abajo), `README.md` (estado), sección `CERRADO` del traspaso
  - **Commit**: `docs(reorganizacion-modular): informe del piloto F1`

- [ ] **T1.20 · PARADA — decide Jhoan** · Jhoan · dep. T1.19 · cumple R1.7.b
  - **Hecho cuando**: la sección 10 del informe tiene la respuesta de Jhoan a P1–P7, con fecha. Hasta entonces **no empieza F2**.

## Informe — plantilla de `informe-piloto.md` (se escribe en T1.19, no antes)

Cada número con su comando. Lo no medido se escribe «sin medir», nunca se estima en silencio.

```markdown
# Informe del piloto F1 · nucleo/contact
## 0 · En cinco líneas
## 1 · Coste por fichero
| Fichero | Sesiones | Min. rojo | Min. verde | Commits | Líneas contrato (rojo) | Líneas finales (coment./código) | Líneas de test |
(fuentes: líneas `Piloto:` de los commits · `git log --format='%h %ad %s' --date=iso -- <fichero>` ·
`git show <sha-rojo>:<fichero> | wc -l` · `grep -cE '^\s*//'`) — incluir contacthelpertest y puente_contact.
Comparar con la referencia vieja: 170 · 151 · 183 · 460 líneas (59/47/33/190 de comentario).
## 2 · Cobertura por fichero
Obtenida (`make cobertura-ficheros`) frente a la base vieja 98,0 % · 95,2 % · 91,1 % · 0 %; minutos
extra para llegar al 80 %; qué sentencias quedaron sin cubrir y por qué.
## 3 · La suite de contrato
Casos · rc y duración en memoria · rc y duración contra Postgres (contenedor, migración, casos) ·
divergencias encontradas · ¿la firma `Montaje` bastó?
## 4 · Candados
| Candado (`05` §5) | Veces que falló | Motivo | ¿Cazó un defecto real? | ¿Estorbó? |
Incluir el lint `unused` (T-1) y el ciclo de imports (T-2) aunque no sean candados.
## 5 · Puentes y adaptadores
Puentes de `05` §4.1 (esperado 0) · adaptadores de arranque (1: líneas, tests, vida hasta F8).
## 6 · Reglas viejas
Portadas (de R-01…R-33 y N-01…N-05) · no portadas, con motivo · nuevas descubiertas.
## 7 · Conmutación y huella
Resultado de la huella, de `go list -deps` y del test de cableado.
## 8 · Extrapolación (con su advertencia)
Minutos por fichero × ficheros de cada fase (de `04` §3) → sesiones estimadas F2–F8. `contact` es
una hoja de 4 ficheros sin estado; `runtime` (F8) tiene 23 ficheros y estado en memoria.
## 9 · Preguntas de la parada
## 10 · Decisión de Jhoan (fecha)
```

**Preguntas de la parada (sección 9), que Jhoan contesta:**

| # | Pregunta | Qué dato del informe la informa |
|---|---|---|
| P1 | ¿**Seguir igual**, **acelerar** (contrato+rojo+verde de un paquete en una sola pasada; varios paquetes por sesión) o **acotar** (reconstruir solo los módulos con deuda y portar el resto con menos ceremonia)? | §1, §8 |
| P2 | D-12: ¿el 80 % por fichero se mantiene, sube, baja o pasa a otra medida? | §2 |
| P3 | ¿Bloques de sesión más grandes (p. ej. un módulo pequeño entero por sesión)? | §1 (minutos por bloque) |
| P4 | ¿La suite con `Montaje` (D-F1-1) y el arnés adelantado (D-F1-2) se adoptan para F2–F8? | §3 |
| P5 | ¿El adaptador de tipos en el arranque (D-F1-5) es el mecanismo estándar? Coste previsto en F2 (`acceso`), cuyos tipos consumen muchos paquetes viejos | §5 |
| P6 | ¿Se acepta que los tests de auxiliares no exportados nazcan en el `verde` (T-1) como excepción escrita a E-4? | §4 |
| P7 | ¿Qué de las contradicciones del `README.md` de F1 se corrige en `04`/`05`? | README · §4 |
