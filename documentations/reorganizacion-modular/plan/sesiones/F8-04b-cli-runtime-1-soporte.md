# F8-04b · F8 · `runtime` (1b): el verde del soporte · 💻 CLI

> ✎ **2026-10-10 (D-F8-11, Jhoan)**: sesión nueva. Es la segunda mitad de lo que era F8-04 («contratos de los 23 y
> soporte»), partida **antes** de lanzarla: los contratos son [`F8-04`](F8-04-cli-runtime-1.md) y aquí va **solo el verde de
> los 12 de soporte** (T8.26). Motivo: hallazgo 29 del [README de F8](../F8-conversacion/README.md).

| | |
|---|---|
| Fase · bloque | [F8 · conversacion](../F8-conversacion/README.md) · 4b · `runtime` (1b): el verde del soporte |
| Entorno | 💻 solo local |
| Nivel (E-12) | complejo (estado en memoria y concurrencia): esquema completo E-2…E-9 |
| Duración objetivo | 45–90 min |
| Tareas | T8.26 |
| Depende de | F8-04 |
| Decisiones | D-F8-11 (F8-04 en dos sesiones) |
| Se para cuando | los 12 de soporte verdes (`runtime`, `keyedmutex`, `event_sink`, `log_sink`, `tenant_resolver`, `self_numbers`, `summary_sources`, `streak`, `welcome`, `thread`, `send`, `webhook_sink`) con `go test -race` rc=0 por commit; mutantes en `keyedmutex.go` y `streak.go`; suites contra Postgres de `self_numbers` y `tenant_resolver` verdes; `pendiente.Implementar` de `runtime` solo en los 11 del núcleo; `make ci-local` rc=0 con 0 SKIP; PR a `dev` abierto desde la rama de la sesión |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F8-04, los contratos de los 23) está integrada y empujada en `dev` (PR por abrir al escribir esta ficha).
- [ ] Decisiones rellenas en [`../DECISIONES.md`](../DECISIONES.md): D-F8-11 (F8-04 en dos sesiones).
- [ ] `go1.26.5` y `golangci-lint v2.12.2` disponibles (`make toolchain`).
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F8-04b del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F8 · conversacion → documentations/reorganizacion-modular/plan/F8-conversacion/
- Bloque: 4b · `runtime` (1b): el verde del soporte
- Tareas: T8.26 de plan/F8-conversacion/tareas.md
- Te paras cuando: los 12 de soporte están verdes (`runtime`, `keyedmutex`, `event_sink`, `log_sink`, `tenant_resolver`, `self_numbers`, `summary_sources`, `streak`, `welcome`, `thread`, `send`, `webhook_sink`) con `go test -race` rc=0 por commit; los mutantes de `keyedmutex.go` y `streak.go` mueren; las suites contra Postgres de `self_numbers` y `tenant_resolver` están verdes; `pendiente.Implementar` de `runtime` queda solo en los 11 del núcleo; `make ci-local` rc=0 con 0 SKIP; PR a `dev` abierto desde la rama de la sesión.
- Decisiones: D-F8-11 (F8-04 en dos sesiones) en plan/DECISIONES.md. Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo, contrato-tdd, validar-antes-de-cerrar, procesos-testcontainers (arnés de las suites de `self_numbers` y `tenant_resolver`), describir-pr.

- Base: F8-04 integrada en `dev` (los 23 contratos de `runtime` en rojo, `runtimehelpertest` verde, candado de rachas escrito; T8.18–T8.21 `[x]` con SHA): compruébalo con `git log origin/dev` y, si no está en `dev`, PARA y dilo. Trabaja en TU rama partida de `origin/dev`, nunca en `dev` (regla 6 del `CLAUDE.md`).

Lo que F8-03 y F8-04 te dejan dicho (léelo antes de empezar): hallazgos 21–29 del README de F8 y los de F8-04, 30–36.
- De F8-04 (rama `reorg/f8-04-runtime-contratos`, `cf327ca5` … `de6a5411`): los 12 de soporte son `runtime`, `keyedmutex`, `event_sink`, `log_sink`, `summary_sources`, `streak`, `webhook_sink`, `tenant_resolver`, `self_numbers`, `welcome`, `thread` y `send`. `runtime.go` ya está declarado y su test, sin etiqueta. `keyedmutex.go` y `streak.go` no tienen exportados: su `_test.go` solo lista los tests (y mutantes) que tienes que traer tú (hallazgo 31). `welcome`, `thread` y `send` cuelgan de `*Runtime`: sus tests pasan por el arnés (`harness_test.go`, `package runtime_test`) y mueren en `runtime.WithClock` mientras el núcleo esté en rojo; no pueden ir verdes antes de F8-05: dilo y deja su etiqueta, o PARA si eso contradice tu punto de parada.
- Las dos suites contra Postgres (`test/procesos/runtime_tenant_resolver_contrato_test.go` y `runtime_self_numbers_contrato_test.go`) pierden `pendiente` A LA VEZ (la segunda usa las siembras de la primera), y con ellas `postgres_fakedb_test.go` y los dos tests unitarios de los adaptadores.
- Hallazgo 32: los tests que pasan por el arnés no se han ejecutado nunca contra lógica real; cuenta con corregir tests, no solo con portar lógica.
- 🔴 Hallazgo 28: el lint del repo solo compila con la etiqueta `integracion` y NO mira los tests que llevan `//go:build pendiente`; sus avisos aparecen de golpe al pasar a verde (17 en `cart`). Córrelo sin etiqueta sobre los tests ANTES del verde. Y los avisos se ARREGLAN, no se silencian: ningún `//nolint` nuevo, ni en producción ni en tests (decisión de Jhoan, 2026-10-10; deuda D-31). Si uno no se puede arreglar sin cambiar lo que se prueba, PARA y dilo. Díselo así a cada sub-agente. Para mirar los tests que aún llevan la etiqueta, pásasela a mano (`make lint` no la admite; la etiqueta se SUMA a la `integracion` del `.golangci.yml`): `GOTOOLCHAIN=go1.26.5 GOLANGCI_LINT_CACHE=.bin/lint-cache GOWORK=off .bin/golangci-lint run --timeout=5m --build-tags pendiente ./internal/modulos/conversacion/...; echo rc=$?` → 0 issues.
- Hallazgo 25: `cart/projection.go` dice del dispatcher cosas que no se afirmaron en su contrato («loguea sin abortar», y que ya no es cierto desde D-054.4): se comprueba al portar `runtime`.
- Hallazgo 27: ficheros cuyos tests comparten una constante van verdes en el mismo commit, y se dice. Hallazgo 19: ficheros que se llaman entre sí no pasan el lint `unused` por separado: su verde va en un commit, y se dice.
- Hallazgo 29: esta sesión es SOLO el verde del soporte. Los 11 del núcleo (T8.27, T8.28) son de F8-05: no los pongas verdes aunque sobre tiempo.

No hay rama web que integrar ni traspaso que cerrar: creas TU rama desde `dev` (`git checkout -b <rama> dev`) y trabajas en ella, nunca sobre `dev` (regla 6 del `CLAUDE.md`).
Un commit `verde(conversacion): <fichero>` por fichero, con `go test -race` rc=0 en cada uno. `send.go`, `thread.go` y `welcome.go` cuelgan de `*Runtime`.
Reloj siempre inyectado (`streak.go`, T-7 de `reglas.md`); prohibido `time.Sleep`. Mutantes donde haya estado o concurrencia: `keyedmutex.go` y `streak.go`; cada mutante mata al menos un test.
La verdad de `self_numbers.go` y `tenant_resolver.go` la da su suite `Contrato` contra Postgres (P4), con testcontainers (skill `procesos-testcontainers`): nunca un Postgres vivo, nunca `WAPP_TEST_DB_DSN`, nunca `t.Skip`. F8-04 las dejó escritas con `//go:build integracion && pendiente`: al poner verde cada adaptador, quítale `pendiente` a su suite (queda `integracion`) y córrela.
Nombres en inglés en lo nuevo (E-11); solo comentarios y documentación en español; los textos observables se copian literales.
Orquesta con sub-agentes y protege tu contexto. Sub-agentes en worktrees: nacen de `origin/main`; diles que se pongan en el SHA de tu rama y corran `make tools`. Sus commits entran en tu rama por `cherry-pick`. Borra los worktrees antes de `make test-pendiente`.
Gates sin carga: no corras `make test-procesos` ni `make ci-docker` en paralelo con nada.

Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.

Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. Push de TU rama (`git push origin <rama>`, rc sin pipe) y PR a `dev` (`gh pr create --base dev`, o el PR ya abierto de esa rama; «integrar SIN squash»): nunca directo a `dev` (regla 6 del `CLAUDE.md`). No toques `main`.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F8-conversacion/tareas.md`](../F8-conversacion/tareas.md): T8.26 `[x]` con SHA.
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../F8-conversacion/README.md).
- Un PR con `--base dev` desde la rama de la sesión (push de la rama, rc sin pipe), con «integrar SIN squash»; nada commiteado directamente en `dev` (regla 6 del `CLAUDE.md`).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** en `ESTADO.md`, no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir. Solo entonces se escribe un traspaso.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
