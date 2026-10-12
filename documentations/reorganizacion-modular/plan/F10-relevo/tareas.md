# F10 · Tareas

> **Todo F10 es local (💻)**: sin sesión web, sin PR y sin traspaso (solo si una sesión se corta). Cada sesión commitea
> directo en `dev` y cierra con tres cosas: tareas `[x]` con SHA, bloque en `ESTADO.md`, hallazgos en el README de la fase.
> `traspasos/TRASPASO-F10-relevo.md` conserva el nombre, pero aquí es el **acta** del relevo.
> Skills: `validar-antes-de-cerrar` (todos los gates), `procesos-testcontainers` (T10.15) y `desplegar-ecosistema`
> del ecosistema para el paso a `main` (T10.22).
> Gate en todas: `GOWORK=off make ci-local > /tmp/g.log 2>&1; echo "GATE_RC=$?" >> /tmp/g.log; tail -1 /tmp/g.log` → `GATE_RC=0`.
> Sin umbral de cobertura (P2): `make cobertura-ficheros` es un informe, no bloquea.
> Adaptadores (E-11, `05` §4.2): F10 no crea ni retira ningún `bridge_<x>.go`; exige que no quede ninguno.

## Bloque A · preparación y dorada · sesión F10-02 · T10.1–T10.3 y T10.24

Entrada: F9 `CERRADO`; F10-01 hecha. Para cuando: la dorada commiteada y verificada contra el viejo en ejecución.

- [x] **T10.1 · Verdad de campo y capturas de referencia** · dep. F9 cerrada · cumple R10.3.c, R10.3.d, R10.4.d — hecha en `7434ea91` (F10-02, 2026-10-11; PR #75, integrado): acta §1 y §3. Las cuatro condiciones se cumplen y el mutante cae; el `grep` de `pendiente.Implementar` da 24 y no 0 (ninguna es un contrato: hallazgo 1 del README)
  - **Ficheros**: `traspasos/TRASPASO-F10-relevo.md` (§1 y §3)
  - **Hecho cuando**: re-medidos con los comandos de `diseno.md` §1 y §4 (los números de hoy crecerán);
    adaptadores `bridge_*.go` en `internal/arranque` = 0, puentes (import) = 0, `Conmutados` con todos los módulos
    de F1–F8, pendientes = 0; copiado al acta el resultado de T9.15 sobre el mutante `maxTxAttempts = 1`
    (hallazgo 38 de F1: si el mutante **sobrevive**, se anota y T10.11 queda bloqueada); capturada la salida de
    `go run ./cmd/prompts -comprobar <dir>` y de `go run ./cmd/casebank` sin `-consentido` para comparar después
  - **Gate**: el de la cabecera
  - **Commit**: `docs(reorganizacion-modular): F10, verdad de campo antes del relevo`
- [x] **T10.2 · relevo: la huella del viejo, congelada** · dep. T10.1 · cumple R10.2.c — hecha en `374d8652` (F10-02, 2026-10-11; PR #75, integrado): la dorada es de texto e incluye la parte estática; `huella.json` de F0 se retira (hallazgo 2); mutante real `/healthz` caído
  - **Ficheros**: `internal/arranque/testdata/huella-vieja.golden`, `internal/arranque/huella_test.go` (compara con la dorada **y**, mientras exista, con el viejo en ejecución), el `huella_vieja_test.go` de F0 (bandera de regeneración)
  - **Hecho cuando**: la dorada casa con el viejo en ejecución y con el nuevo; un caso `muerde` (quitar una ruta) hace fallar el test
  - **Gate**: el de la cabecera
  - **Commit**: `relevo: la huella del viejo, congelada`
- [x] **T10.3 · Preparación de la prueba en UAT** · dep. T10.2 — hecha en el commit de cierre de F10-02 (2026-10-11; PR #75, integrado): acta §4. ✎ No hay `git push origin dev` (regla 6: rama y PR), y el SHA de `dev` lo fija F10-03 al abrir la ventana (hallazgo 3); el acta fija el último commit de código, `374d8652`
  - **Ficheros**: `traspasos/TRASPASO-F10-relevo.md` (§4: los comandos de `diseno.md` §2, literales)
  - **Hecho cuando**: T10.1–T10.2 empujados a `dev` (`git push origin dev`, rc sin pipe); el SHA que se desplegará está escrito en el acta
- [x] **T10.24 · Inventario de las divergencias visibles desde fuera (primera mitad de la deuda D-36)** · dep. F9 cerrada · añadida el 2026-10-11 por decisión de Jhoan · solo lectura — hecha en `7541c4f8` (F10-02, 2026-10-11; PR #75, integrado): 33 divergencias, 11 visibles y 22 internas; hecha leyendo el código, sin ejecutar los binarios (hallazgo 5)
  - **Por qué antes del relevo**: saber qué respondía el viejo es directo mientras existe (se corren los dos binarios); después hay que deducirlo de los comentarios
  - **Ficheros**: `divergencias-visibles.md` (nuevo, en esta carpeta); `documentations/deuda.md` (D-36)
  - **Hecho cuando**: cada comentario «Divergencia deliberada del viejo» de producción (26 en 14 ficheros el 2026-10-10: se re-mide, con el comando) y cada decisión de `plan/DECISIONES.md` que cambie conducta está clasificada **interna** / **visible desde fuera**; cada visible lleva ruta o trama, qué respondía el viejo y qué responde el nuevo (código, cuerpo, texto), y qué consumidor la recibe. **No** se abre ni se toca ningún consumidor: eso es la segunda mitad, tras el relevo
  - **Corte limpio**: si F10-02 no cabe en ~90 min, esta tarea se hace en una sesión propia; lo que no puede es quedar para después de T10.7
  - **Commit**: `docs(reorganizacion-modular): F10, inventario de las divergencias visibles desde fuera`

## Bloque B · la prueba en UAT en sustitución · sesión F10-03 · T10.4–T10.6 · necesita UAT (SSH)

Entrada: T10.3; D-F10-1 decidida (ventana y fecha); ✎ 2026-10-11: la fase [FW](../FW-whatsapp-enchufable/README.md) cerrada (TW.10), porque los criterios conversacionales de `diseno.md` §2.4 se miran con su WhatsApp falso. Para cuando: acta con veredicto. La ventana (≥ 24 h) es
espera, no sesión: se abre con T10.4 y la sesión se relanza al final para T10.5–T10.6.

- [x] **T10.4 · Captura y cambio de binario** · dep. T10.3 · cumple R10.1.a–c · ✎ hecha el 2026-10-11 (F10-03): modular @ `e82b6e92` en UAT desde las 22:17:54 UTC; acta §5.1–§5.4. El guion con el viejo **no** se corrió antes (Jhoan)
  - **Hecho cuando**: `diseno.md` §2.1 y §2.2 hechos; §2.3 · 1–6 verdes con sus salidas en el acta
  - **Gate**: `go version -m /proc/$pid/exe` → `path …/cmd/server-modular`; líneas de clave `key_source=config|file`
- [x] **T10.5 · La ventana** · dep. T10.4 · cumple R10.1.d–e · ✎ 2026-10-11 (Jhoan): **sin ventana de 24 h** («nadie usa UAT»), y la conversación, en local y no en UAT. Medido: acta §5.5 (a los 37 min) y §5.6 (local, viejo y nuevo en verde). Lo no visto: acta §5.7. Cerrada **sin** la tabla de 24 h, por esa decisión (PR #81)
  - **Hecho cuando**: la tabla de `diseno.md` §2.4 rellena con números al final de la ventana (mínimo 24 h, D-F10-1); si un corte salta, §2.5 ejecutado en < 5 min y la tarea queda `[~]` con el hallazgo
- [x] **T10.6 · Acta y veredicto** · dep. T10.5 · ✎ 2026-10-11: acta escrita (§5, `0326c622`); veredicto de Jhoan: **«sigue»** (§5.9); D-F10-2 aplicada (PR #81)
  - **Ficheros**: `traspasos/TRASPASO-F10-relevo.md` (acta, `diseno.md` §2.6)
  - ✎ 2026-10-11 (D-FW-3): «sigue» admite defectos **menores**, listados en el acta, si cada uno se puede corregir **sin** el código viejo; uno que pida comparar con el viejo es «vuelta atrás» o espera, y T10.7–T10.11 no empiezan
  - **Hecho cuando**: veredicto de Jhoan escrito. «Sigue» → D-F10-2 aplicada. «Vuelta atrás» → viejo restaurado y verificado; F10 vuelve a su entrada
  - **Commit**: `docs(reorganizacion-modular): F10, acta de la prueba en UAT`

## Bloque C · el relevo en el repo · sesión F10-04 · T10.7–T10.14 y T10.23

Entrada: T10.6 con «sigue»; ✎ 2026-10-11: T10.24 `[x]` (el inventario de divergencias se hace con el viejo aún presente). Para cuando: la definición de hecho del bloque C (`reglas.md` §4). Un commit por
tarea, directo en `dev`, en este orden (`arquitectura.md` §3). No necesita Docker ni UAT.

- [x] **T10.7 · relevo: `cmd/server` usa el arranque nuevo** · dep. T10.6 · cumple R10.2.a–b, R10.3.c — hecha en `3d638ed9` (F10-04, 2026-10-11; PR #82, integrado): salidas de `cmd/prompts` y `cmd/casebank` iguales a las del acta §3. ✎ El candado de fronteras clasificaba `cmd/server`, `cmd/casebank` y `cmd/prompts` como árbol viejo: pasan al nuevo (hallazgo 16)
  - **Ficheros**: `cmd/server/main.go`, `cmd/server/integration_test.go` (✎, D-F10-4), `cmd/server/flows_integration_test.go` (borrado), `cmd/casebank/*.go` y `cmd/prompts/main.go` si aún importan lo viejo
  - **Hecho cuando**: `GOWORK=off go list -deps ./cmd/server | grep -c 'internal/arranque$'` → 1; salidas de `cmd/prompts` y `cmd/casebank` iguales a las de T10.1
- [x] **T10.8 · relevo: fuera `cmd/server-modular` y la variable del arnés** · dep. T10.7 · cumple R10.5.a — hecha en `0e9eaa5c` (F10-04, 2026-10-11; PR #82, integrado): la base de un proceso pasa a `proc_<proceso>`; los logs, a `procesos.log` y `procesos-edge-real.log`
  - **Ficheros**: `cmd/server-modular/` (borrado), `test/procesos/main_test.go`, `Makefile` (`test-procesos` sin bucle de binarios)
  - **Hecho cuando**: `grep -rn WAPP_PROCESOS_BINARIO --include='*.go' --include=Makefile . | wc -l` → 0; vet `-tags integracion` rc=0
- [x] **T10.9 · relevo: fuera la cara vieja y el estrangulador (FX · TX.25)** · dep. T10.7 — hecha en `db5657b4` (F10-04, 2026-10-11; PR #82, integrado). ✎ **Va después de T10.10**, no antes: el arranque viejo era lo único que importaba `internal/publicapi` (hallazgo 15). Las 73 rutas del :8103 las fija `TestNewFace_RegistersExactlyTheGoldenRoutes` contra la dorada
  - **Ficheros**: `internal/publicapi/` (33 + 64), `internal/apipublica/estrangulador.go` y lo que diga TX.25 de [`FX-cara-http/tareas.md`](../FX-cara-http/tareas.md)
- [x] **T10.10 · relevo: fuera el arranque viejo** · dep. T10.2, T10.9 — hecha en `ae4c0291` (F10-04, 2026-10-11; PR #82, integrado). ✎ No depende de T10.9: va antes (hallazgo 15). El test del pool, el único sin gemelo, se portó a P0 (hallazgo 18)
  - **Ficheros**: `internal/bootstrap/` (22 de producción + 21 tests: los 20 de hoy más `huella_vieja_test.go`; entre ellos `platform_permissions_test.go` y los **9** `*cablead*_test.go` viejos); `huella_test.go` queda solo contra la dorada
  - **Hecho cuando**: I-CP-5 y los candados de cableado siguen en `internal/arranque` (F0) y pasan
- [x] **T10.11 · relevo: fuera los paquetes viejos** · dep. T10.10 · cumple R10.3.a–b, R10.3.d — hecha en `ad58d59f` (F10-04, 2026-10-11; PR #82, integrado): precondición comprobada en el acta §1.1 (el mutante cae). ✎ `internal/platform` sí se tocó: cuatro tests suyos importaban paquetes viejos (hallazgo 17). `go mod tidy`: `testify` pasa a indirecta
  - **Precondición (hallazgo 38 de F1)**: el acta de T10.1 dice que T9.15 (P3) ejerce el reintento de
    `postgres.WithTx` y que el mutante `maxTxAttempts = 1` **cae**. Si no, **no se borra**
    `internal/flujos/contact/deadlock_integration_test.go` ni el resto de la integración vieja: la sesión para
  - **Ficheros**: los 26 directorios restantes de `diseno.md` §1, con sus tests; `go.mod`/`go.sum` tras `GOWORK=off go mod tidy`
  - **Hecho cuando**: `ls -d internal/*/` → `apipublica arranque candados modulos nucleo pendiente platform` (✎ 2026-10-11, D-F10-10: `candados` se queda; `pendiente` sale en T10.12); `go build ./... && go vet ./...` rc=0; `go mod tidy` sin diff pendiente
- [x] **T10.12 · relevo: cero pendientes** · dep. T10.11 · cumple R10.4.a–b, R10.4.d — hecha en `a0c6564d` (F10-04, 2026-10-11; PR #82, integrado): el `grep` da 0; `internal/modulos/no_pending_test.go` muerde sobre el repo real; `Puentes` y `Conmutados` quedan fijados por test. `.golangci.yml` no llevaba la etiqueta: nada que quitar
  - **Ficheros**: `no_pending_test.go` (donde lo ubique `00-marco/estructura.md`; las specs lo llamaban `sin_pendientes_test.go`: como aún no existe, nace con nombre en inglés, `05` E-11 y D-F1-12, 2026-10-02) con su caso `muerde`; `internal/modulos/fronteras_test.go` (puentes de import: lista vacía y caso `muerde`; `Conmutados`: todos los módulos); con D-F10-3: `internal/pendiente/` borrado, `Makefile` sin `test-pendiente` ni `vet -tags pendiente`, `.golangci.yml` sin la etiqueta
  - ✎ **2026-10-11 · D-F10-10 (Jhoan)**: `internal/candados` **se queda**; en este commit se le recorta lo que distingue un contrato en rojo (el detector de `cobertura.go`, sus tests y su `testdata`, y las menciones en `candados.go`, `exportados.go` y `fronteras_test.go`: 11 líneas con `pendiente.Implementar` el 2026-10-11), para que el `grep` de abajo pueda dar 0
  - **Hecho cuando**: `grep -rn 'pendiente.Implementar\|go:build pendiente' --include='*.go' . | wc -l` → 0; `ls internal/arranque/bridge_*.go 2>/dev/null | wc -l` → 0
- [x] **T10.13 · relevo: la integración vieja, retirada** · dep. T10.11 · cumple R10.5.b–c — hecha en `90655d3d` (F10-04, 2026-10-11; PR #82, integrado): los 9 ficheros (25 `Test*`) borrados; 4 reglas sin cubrir se cerraron antes (hallazgo 19). El job `integration` de `ci.yml` no se ha ejecutado: solo su sintaxis
  - **Precondición**: la misma de T10.11 (hallazgo 38): lo que la batería vieja ejercía y nadie más, ya lo ejerce un proceso de F9
  - **Ficheros**: `Makefile` (`test-integration`: fuera, o acotado a `./internal/platform/...` con `postgres:17-alpine` si D-F10-5 = no), `.github/workflows/ci.yml` (job `integration` → procesos), los 9 tests de BD de `internal/platform` si D-F10-5 = sí (P10 verde en F9)
  - **Hecho cuando**: el grep de R10.5.b → 0 (o solo `platform`, nombrado)
- [x] **T10.23 · relevo: las supresiones de lint, revisadas (deuda D-31)** · dep. T10.12 · añadida el 2026-10-10 por decisión de Jhoan — hecha en `0157d3f0` (F10-04, 2026-10-11; PR #82, integrado): de 95 quedan **45**, 0 sin motivo, `make lint` → `0 issues.`. ✎ **2026-10-11 · D-F10-13 (Jhoan)**: 40 aprobadas; las 5 de producción que callan conducta prometida, los 17 `#nosec` que el inventario no contaba (hallazgo 20) y la activación de `nolintlint` van a una revisión de lint tras la migración (`deuda.md` D-31)
  - **Ficheros**: los que lleven `//nolint` tras el relevo (el código viejo ya no existe; hoy, 90 en el árbol nuevo: 70 en tests y 20 en producción); `documentations/deuda.md` (D-31 y la cifra de §7)
  - **Hecho cuando**: cada `//nolint` está **arreglado** o, si no se puede sin cambiar lo que se prueba o lo que se promete, queda con su motivo y aprobado por Jhoan en una lista; `grep -rnE '//nolint(:[a-z,]+)?\s*$' --include='*.go' . | wc -l` → 0 (ninguno sin motivo); `make lint` → `0 issues.`; D-31 cerrada o reducida a esa lista
  - **Commit**: `relevo: las supresiones de lint se arreglan o se justifican`
- [x] **T10.14 · docs: el repo dice las rutas nuevas** · dep. T10.11 · cumple R10.7.a — hecha en `ee7db989` (F10-04b, 2026-10-11; PR #84, integrado), con `63c461ab` (dos skills), `19ed4883` (tres skills retiradas) y `fee81399` (el árbol de `04` §3, que cierra D-40): `scripts/check-docs-tree.sh` → `FALTAN=0`, rc=0; el recuento de `diseno.md` §4 → 0; `GATE_RC=0`. Cifras remedidas: 95 rutas, 2 rpc, 6 CLI, **71** variables, **45** tablas (hallazgos 32–38). ✎ 2026-10-11 (Jhoan, en F10-04): salió de F10-04 a un PR propio, F10-04b (hallazgo 21); en F10-04 solo se hizo `deuda.md` D-31 y su cifra de §7
  - **Ficheros**: los de `diseno.md` §4 (147 menciones hoy); `ESTADO.md` y `README.md` de `reorganizacion-modular/` («relevo hecho en el repo; UAT y ecosistema pendientes»); `CLAUDE.md` (la sección «en curso» pasa a «hecha»; el índice del sistema es `internal/arranque/orquestador.go`)
  - ✎ **2026-10-11 · D-F10-8 (Jhoan): sin documentación del código viejo.** Borrado el viejo, la documentación de la pieza describe **solo** lo que existe: `contratos.md`, `arquitectura.md`, `constitucion.md`, `operacion.md`, `esquema-postgres.md`, `deuda.md` (§6 incluida) y el `README.md` del repo se **reescriben** contra el árbol nuevo, no se anotan. La salida «(histórico)» **deja de valer** fuera de `reorganizacion-modular/`. El árbol de `04` §3 se **regenera** desde el código (`go list`), con el comando escrito: cierra la deuda D-40. La propia carpeta `reorganizacion-modular/` sale del repo al final, en T10.25 (D-F10-9)
  - **Hecho cuando**: el recuento de `diseno.md` §4 → **0** fuera de `reorganizacion-modular/`, sin excepciones «(histórico)»; cada ruta, fichero y símbolo que la documentación de la pieza nombra **existe** en el árbol (comprobado con un comando, que se escribe)
  - **Commit**: `docs(reorganizacion-modular): relevo — las rutas nuevas en la documentación del repo`
  - Cierre del bloque: el gate del bloque C (`reglas.md` §4) con sus salidas en el acta, y `git push origin dev` (rc sin pipe). ✎ F10-04: el gate está en el acta §6, sobre `0157d3f0`; el push es de la rama y entra por PR (regla 6).

## Bloque D · cierre local · sesión F10-05 · T10.15–T10.17 · necesita Docker y UAT (SSH)

- [x] **T10.15 · Gates y procesos contra el único binario** · dep. T10.14 · necesita Docker — hecha el 2026-10-11 (F10-05; PR #83, integrado), sobre `dev` @ `21d527af`, sin commit propio (no produce código): `GATE_RC=0`, 0 `--- SKIP` con `-v`, `CUENTA=3 make test-procesos` `RC=0 · PASS=3915 FAIL=0 SKIP=0`; acta §7.1. ✎ El `git push origin dev` no se hizo: `dev` ya tenía el relevo por el PR #82 · ✎ 2026-10-11 (F10-04): T10.14 salió a F10-04b. ✎ **2026-10-11 · D-F10-12 (Jhoan): F10-05 no espera a F10-04b**; la dependencia de T10.14 deja de valer para el bloque D: nada de T10.15–T10.17 lee la documentación del repo, y D-F10-2 pide el relevo en UAT antes del 2026-10-18
  - **Hecho cuando**: `make ci-local` GATE_RC=0 con la toolchain fijada; `CUENTA=3 make test-procesos` `RC=0`, 0 SKIP, 0 FAIL; `git push origin dev` con su `rc` leído
- [x] **T10.16 · UAT desde el commit del relevo** · dep. T10.15 · cumple R10.6.a, R10.2.b · necesita UAT (SSH) — hecha el 2026-10-11 (F10-05; PR #83, integrado), sin commit propio: UAT corre `cmd/server` @ `21d527af` desde las 01:49:27 UTC del 2026-10-12, `vcs.modified=false`, claves `file` y `config`; acta §7.2–§7.3. ✎ Se compiló a `bin/server.new` y se renombró (acta §5.3), no `go build -o bin/server`; queda además `bin/server.modular-e82b6e92`
  - **Hecho cuando**: `git checkout dev && git reset --hard origin/dev`; `GOWORK=off go build -o bin/server ./cmd/server`; `systemctl restart wapp-cloud` + las dos consolas; §6 (`path …/cmd/server`, `vcs.revision` = SHA del relevo) y §9 (líneas de clave) del runbook verificados; `bin/server.viejo-*` se conserva hasta T10.22
- [x] **T10.17 · Acta `CERRADO`** · dep. T10.16 — hecha en `d096ce50` (F10-05, 2026-10-11; PR #83, integrado): acta §7, `CERRADO 2026-10-11`. Lo de §5.7 sigue sin ver (§7.4)
  - **Ficheros**: `traspasos/TRASPASO-F10-relevo.md` (`CERRADO <fecha>`), `ESTADO.md`
  - **Commit**: `docs(reorganizacion-modular): relevo cerrado en el repo y en UAT`

## Bloque E · fuera del repo · sesión F10-05b (✎ 2026-10-11: ficha propia) · T10.18–T10.21 · necesita la raíz de wApp y los repos hermanos

Cada tarea en **su** repo, con **su** gate, commit a `dev`. La tabla de mapeo es la de `04` §4 más D-9 y
D-10 (`diseno.md` §3.1). ✎ 2026-10-11: «commit a `dev`» es rama y PR en cada repo (regla 6).

- [x] **T10.18 · La documentación del ecosistema** · dep. T10.17 · cumple R10.7.b — hecha el 2026-10-12 (F10-05b; PR #85, integrado) en `4c74594` de la raíz de wApp, `main` local: 1.044 menciones reescritas en 142 ficheros, las 80 históricas anotadas con la ruta de hoy; el recuento de §3.1 da 84 = 80 históricas + 4 de `guardian/wapp-guardian-bff/internal/bootstrap` (la cota superior de T-12); `make check-docs` rc=0 con los 80 diagramas renderizados. ✎ La raíz no tiene remoto ni `dev`: commit directo en `main` local (Jhoan, en la sesión). ✎ Re-apuntar no es re-verificar: hallazgo 41
  - **Hecho cuando**: el recuento de `diseno.md` §3.1 (1.124 menciones hoy) → 0 fuera de lo histórico; `make check-docs` de la raíz verde
  - ✎ 2026-10-11 · D-F10-8: «lo histórico» aquí es **solo** el eje `historia/` y las fichas de planes cerrados del ecosistema, que cuentan qué pasó; los ejes que describen el sistema (constitución, diagramas, operación, funcionalidades) no conservan rutas viejas
- [x] **T10.19 · ADR-0010: la regla de conteo nueva** · dep. T10.18 · D-F10-7 — hecha el 2026-10-12 (F10-05b; PR #85, integrado) en `f68cd64` de la raíz de wApp, `main` local: texto presentado a Jhoan y aprobado en la sesión; tabla re-medida (**9** tablas, eran 14) con `scripts/adr-0010-tablas-por-modulo.py`, que queda en la raíz; citas del arranque y de los listeners corregidas
  - **Hecho cuando**: texto nuevo aprobado por Jhoan; su tabla re-medida con la regla nueva y el comando escrito; citas `bootstrap.go:1519/817/835` corregidas
- [x] **T10.20 · Los 12 comentarios de repos hermanos** · dep. T10.17 · cumple R10.7.c — hecha el 2026-10-11 (F10-05b): el grep de §3.3 → 0. Un commit y un PR por repo, **integrados en su `dev`** a petición de Jhoan: `wapp-guardian-bff` `2dbfee2` (#8), `wapp-client-console` `d3eced7` (#1), `wapp-platform-console` `86ba218` (#2), `wapp-edge-agent` `913e7b2` (#14), `wapp-cloudlink` `5d7c5fc` (#10), `wapp-shared` `c6419af` (#20). `make ci-local` rc=0 y 0 SKIP en cinco; ✎ en `wapp-edge-agent` rc=2 por un `make lint` que **ya fallaba en su `dev`** (hallazgo 43)
  - **Hecho cuando**: el grep de `diseno.md` §3.3 → 0; un commit por repo a `dev`; `make ci-local` de cada repo rc=0
- [x] **T10.21 · La bóveda `analisis/` y `CLAUDE.md` de la raíz** · dep. T10.17 — hecha el 2026-10-12 (F10-05b; PR #85, integrado) en `b4ff38b` de la raíz de wApp, `main` local: las 16 menciones re-apuntadas (recuento → 0) y los 4 enlaces rotos de las notas, resueltos. ✎ 13 de las 16 estaban en ficheros sin commitear desde el 2026-09-27/28: entraron antes en `10e1b5c`. ✎ No se usó la skill `analizar-arquitectura`: fue una sustitución de rutas, no una nota nueva
  - **Hecho cuando**: las 16 menciones re-apuntadas (skill `analizar-arquitectura` del ecosistema para las notas)
- [x] **T10.26 · Los comentarios del código no citan rutas viejas** · dep. T10.17 · D-F10-14 · añadida el 2026-10-11 por decisión de Jhoan · rama y PR propios; **antes de T10.25**
  - **Qué**: las ≈735 líneas de comentario del árbol que sobrevive que citan rutas borradas (`internal/flujos/…`), casi todas como procedencia (hallazgo 23). Se quita la cita y se conserva lo que el comentario explique del código actual. Solo comentarios: ni una línea de código
  - **No entra**: los tres textos `Help` de `internal/platform/metrics/metrics.go` (texto observable por `/metrics`: deuda D-36)
  - **Hecho cuando**: el recuento de rutas viejas en los `.go` → 0, con el comando escrito; `make ci-local` GATE_RC=0
  - ✎ **Hecha el 2026-10-12 (F10-05c; PR #86, en revisión)** en `a9a9d927` (las 304 cabeceras que solo decían la procedencia), `0e412644` (las 414 líneas restantes, en 302 ficheros) y `e7c60df6` (la cabecera de `cmd/server/main.go`); `50b3c9a7` re-apunta 160 citas `fichero:línea` de `documentations/*.md`. Medido hoy antes de tocar: **718** líneas de comentario en 590 ficheros (no 735), más 52 literales que no entran
  - **El comando del recuento** (líneas de comentario de los `.go` que citan una ruta vieja): `P='internal/(bootstrap|casebank|catalogimport|contracts|degradation|diagnostics|entitlements|evidence|filtercfg|flujos|gateway|iam|inferstats|ingest|intake|intakeahead|intakes|integrations|intentcfg|llmvia|platformadmin|prompts|publicapi|reanalisis|receipts|tenantllm|tenantvars|turnoacotado)\b'; grep -rnE "//.*$P" --include='*.go' cmd internal test | wc -l` → **0** (antes, 718). Sin el `//.*` da **52**: son literales —los casos y *fixtures* de `internal/candados`, los tests de cableado de `internal/arranque` y los tres `Help`—, que no eran de esta tarea (hallazgo 47). `make ci-local` → `GATE_RC=0`
- [ ] **T10.25 · La carpeta `reorganizacion-modular/` sale del repo y se archiva en la documentación de wApp** · dep. T10.21, T10.14 y T10.26 (D-F10-12, D-F10-14) · D-F10-9 · añadida el 2026-10-11 por decisión de Jhoan · **la última tarea que se hace desde esta carpeta**
  - **Antes de moverla**: lo que siga siendo **norma del código nuevo** se rescata a `documentations/constitucion.md` de la pieza (de `05`: E-11 nombres en inglés, E-13 tope de tamaño, los candados y cómo se añade un módulo; de `DECISIONES.md`, solo lo que obligue a futuro), reescrito sin rutas viejas
  - **Destino**: `documentations/archive/cloud-platform-modular-reorganization/` en la raíz de wApp (repo de documentación; carpeta `archive/` nueva, nombre en inglés), con un `README.md` que diga arriba: qué fue, fechas, el SHA de este repo donde vivía, y que **describe código que ya no existe**
  - **En este repo**: `documentations/reorganizacion-modular/` borrada; queda una ficha corta en `documentations/` (qué se hizo, el enlace al archivo, el SHA); `CLAUDE.md`, `documentations/README.md` y las skills del repo (`contrato-tdd`, `reconstruir-modulo`, `validar-antes-de-cerrar`, `procesos-testcontainers`, `describir-pr`, `traspaso-web-local`) sin enlaces a la carpeta: cada skill se reescribe o se retira
  - **Hecho cuando**: `ls documentations/reorganizacion-modular 2>&1 | grep -c 'No such file'` → 1; `grep -rn 'reorganizacion-modular' --include='*.md' . | wc -l` → solo la ficha; el recuento de rutas viejas de `diseno.md` §4 → **0 en todo el repo**, sin contar los 52 literales de código del hallazgo 47, que son deuda D-50 (D-F10-15: mide comentarios y documentación); `make check-docs` de la raíz verde, con `archive/` tratado como archivo (sus enlaces internos resuelven; no cuenta para los literales canónicos)
  - **Si se va a hacer F10-06 (`main`)**: sus instrucciones se leen ya desde el archivo

## Bloque F · `main` · sesión F10-06 · T10.22 — solo a petición expresa de Jhoan · necesita `main`, Docker y UAT (SSH)

- [ ] **T10.22 · `dev` → `main` y, si D-F10-6, tag** · dep. T10.17 y petición expresa de Jhoan
  - **Hecho cuando**: `main` = `dev` tras `make ci-local` y `make test-procesos` verdes sobre ese SHA; `sync-main-to-dev.yml` alineó `dev`; si hay tag (`v0.3.0` propuesto), `CHANGELOG.md` rellenado (va por detrás: `documentations/operacion.md` §4); `bin/server.viejo-*` borrado de UAT
