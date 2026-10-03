# F10 · Tareas

> **Todo F10 es local (💻)**: sin sesión web, sin PR y sin traspaso (solo si una sesión se corta). Cada sesión commitea
> directo en `dev` y cierra con tres cosas: tareas `[x]` con SHA, bloque en `ESTADO.md`, hallazgos en el README de la fase.
> `traspasos/TRASPASO-F10-relevo.md` conserva el nombre, pero aquí es el **acta** del relevo.
> Skills: `validar-antes-de-cerrar` (todos los gates), `procesos-testcontainers` (T10.15) y `desplegar-ecosistema`
> del ecosistema para el paso a `main` (T10.22).
> Gate en todas: `GOWORK=off make ci-local > /tmp/g.log 2>&1; echo "GATE_RC=$?" >> /tmp/g.log; tail -1 /tmp/g.log` → `GATE_RC=0`.
> Sin umbral de cobertura (P2): `make cobertura-ficheros` es un informe, no bloquea.
> Adaptadores (E-11, `05` §4.2): F10 no crea ni retira ningún `bridge_<x>.go`; exige que no quede ninguno.

## Bloque A · preparación y dorada · sesión F10-02 · T10.1–T10.3

Entrada: F9 `CERRADO`; F10-01 hecha. Para cuando: la dorada commiteada y verificada contra el viejo en ejecución.

- [ ] **T10.1 · Verdad de campo y capturas de referencia** · dep. F9 cerrada · cumple R10.3.c, R10.3.d, R10.4.d
  - **Ficheros**: `traspasos/TRASPASO-F10-relevo.md` (§1 y §3)
  - **Hecho cuando**: re-medidos con los comandos de `diseno.md` §1 y §4 (los números de hoy crecerán);
    adaptadores `bridge_*.go` en `internal/arranque` = 0, puentes (import) = 0, `Conmutados` con todos los módulos
    de F1–F8, pendientes = 0; copiado al acta el resultado de T9.15 sobre el mutante `maxTxAttempts = 1`
    (hallazgo 38 de F1: si el mutante **sobrevive**, se anota y T10.11 queda bloqueada); capturada la salida de
    `go run ./cmd/prompts -comprobar <dir>` y de `go run ./cmd/casebank` sin `-consentido` para comparar después
  - **Gate**: el de la cabecera
  - **Commit**: `docs(reorganizacion-modular): F10, verdad de campo antes del relevo`
- [ ] **T10.2 · relevo: la huella del viejo, congelada** · dep. T10.1 · cumple R10.2.c
  - **Ficheros**: `internal/arranque/testdata/huella-vieja.golden`, `internal/arranque/huella_test.go` (compara con la dorada **y**, mientras exista, con el viejo en ejecución), el `huella_vieja_test.go` de F0 (bandera de regeneración)
  - **Hecho cuando**: la dorada casa con el viejo en ejecución y con el nuevo; un caso `muerde` (quitar una ruta) hace fallar el test
  - **Gate**: el de la cabecera
  - **Commit**: `relevo: la huella del viejo, congelada`
- [ ] **T10.3 · Preparación de la prueba en UAT** · dep. T10.2
  - **Ficheros**: `traspasos/TRASPASO-F10-relevo.md` (§4: los comandos de `diseno.md` §2, literales)
  - **Hecho cuando**: T10.1–T10.2 empujados a `dev` (`git push origin dev`, rc sin pipe); el SHA que se desplegará está escrito en el acta

## Bloque B · la prueba en UAT en sustitución · sesión F10-03 · T10.4–T10.6 · necesita UAT (SSH)

Entrada: T10.3; D-F10-1 decidida (ventana y fecha). Para cuando: acta con veredicto. La ventana (≥ 24 h) es
espera, no sesión: se abre con T10.4 y la sesión se relanza al final para T10.5–T10.6.

- [ ] **T10.4 · Captura y cambio de binario** · dep. T10.3 · cumple R10.1.a–c
  - **Hecho cuando**: `diseno.md` §2.1 y §2.2 hechos; §2.3 · 1–6 verdes con sus salidas en el acta
  - **Gate**: `go version -m /proc/$pid/exe` → `path …/cmd/server-modular`; líneas de clave `key_source=config|file`
- [ ] **T10.5 · La ventana** · dep. T10.4 · cumple R10.1.d–e
  - **Hecho cuando**: la tabla de `diseno.md` §2.4 rellena con números al final de la ventana (mínimo 24 h, D-F10-1); si un corte salta, §2.5 ejecutado en < 5 min y la tarea queda `[~]` con el hallazgo
- [ ] **T10.6 · Acta y veredicto** · dep. T10.5
  - **Ficheros**: `traspasos/TRASPASO-F10-relevo.md` (acta, `diseno.md` §2.6)
  - **Hecho cuando**: veredicto de Jhoan escrito. «Sigue» → D-F10-2 aplicada. «Vuelta atrás» → viejo restaurado y verificado; F10 vuelve a su entrada
  - **Commit**: `docs(reorganizacion-modular): F10, acta de la prueba en UAT`

## Bloque C · el relevo en el repo · sesión F10-04 · T10.7–T10.14

Entrada: T10.6 con «sigue». Para cuando: la definición de hecho del bloque C (`reglas.md` §4). Un commit por
tarea, directo en `dev`, en este orden (`arquitectura.md` §3). No necesita Docker ni UAT.

- [ ] **T10.7 · relevo: `cmd/server` usa el arranque nuevo** · dep. T10.6 · cumple R10.2.a–b, R10.3.c
  - **Ficheros**: `cmd/server/main.go`, `cmd/server/integration_test.go` (✎, D-F10-4), `cmd/server/flows_integration_test.go` (borrado), `cmd/casebank/*.go` y `cmd/prompts/main.go` si aún importan lo viejo
  - **Hecho cuando**: `GOWORK=off go list -deps ./cmd/server | grep -c 'internal/arranque$'` → 1; salidas de `cmd/prompts` y `cmd/casebank` iguales a las de T10.1
- [ ] **T10.8 · relevo: fuera `cmd/server-modular` y la variable del arnés** · dep. T10.7 · cumple R10.5.a
  - **Ficheros**: `cmd/server-modular/` (borrado), `test/procesos/main_test.go`, `Makefile` (`test-procesos` sin bucle de binarios)
  - **Hecho cuando**: `grep -rn WAPP_PROCESOS_BINARIO --include='*.go' --include=Makefile . | wc -l` → 0; vet `-tags integracion` rc=0
- [ ] **T10.9 · relevo: fuera la cara vieja y el estrangulador (FX · TX.25)** · dep. T10.7
  - **Ficheros**: `internal/publicapi/` (33 + 64), `internal/apipublica/estrangulador.go` y lo que diga TX.25 de [`FX-cara-http/tareas.md`](../FX-cara-http/tareas.md)
- [ ] **T10.10 · relevo: fuera el arranque viejo** · dep. T10.2, T10.9
  - **Ficheros**: `internal/bootstrap/` (22 de producción + 21 tests: los 20 de hoy más `huella_vieja_test.go`; entre ellos `platform_permissions_test.go` y los **9** `*cablead*_test.go` viejos); `huella_test.go` queda solo contra la dorada
  - **Hecho cuando**: I-CP-5 y los candados de cableado siguen en `internal/arranque` (F0) y pasan
- [ ] **T10.11 · relevo: fuera los paquetes viejos** · dep. T10.10 · cumple R10.3.a–b, R10.3.d
  - **Precondición (hallazgo 38 de F1)**: el acta de T10.1 dice que T9.15 (P3) ejerce el reintento de
    `postgres.WithTx` y que el mutante `maxTxAttempts = 1` **cae**. Si no, **no se borra**
    `internal/flujos/contact/deadlock_integration_test.go` ni el resto de la integración vieja: la sesión para
  - **Ficheros**: los 26 directorios restantes de `diseno.md` §1, con sus tests; `go.mod`/`go.sum` tras `GOWORK=off go mod tidy`
  - **Hecho cuando**: `ls -d internal/*/` → `apipublica arranque modulos nucleo platform`; `go build ./... && go vet ./...` rc=0; `go mod tidy` sin diff pendiente
- [ ] **T10.12 · relevo: cero pendientes** · dep. T10.11 · cumple R10.4.a–b, R10.4.d
  - **Ficheros**: `no_pending_test.go` (donde lo ubique `00-marco/estructura.md`; las specs lo llamaban `sin_pendientes_test.go`: como aún no existe, nace con nombre en inglés, `05` E-11 y D-F1-12, 2026-10-02) con su caso `muerde`; `internal/modulos/fronteras_test.go` (puentes de import: lista vacía y caso `muerde`; `Conmutados`: todos los módulos); con D-F10-3: `internal/pendiente/` borrado, `Makefile` sin `test-pendiente` ni `vet -tags pendiente`, `.golangci.yml` sin la etiqueta
  - **Hecho cuando**: `grep -rn 'pendiente.Implementar\|go:build pendiente' --include='*.go' . | wc -l` → 0; `ls internal/arranque/bridge_*.go 2>/dev/null | wc -l` → 0
- [ ] **T10.13 · relevo: la integración vieja, retirada** · dep. T10.11 · cumple R10.5.b–c
  - **Precondición**: la misma de T10.11 (hallazgo 38): lo que la batería vieja ejercía y nadie más, ya lo ejerce un proceso de F9
  - **Ficheros**: `Makefile` (`test-integration`: fuera, o acotado a `./internal/platform/...` con `postgres:17-alpine` si D-F10-5 = no), `.github/workflows/ci.yml` (job `integration` → procesos), los 9 tests de BD de `internal/platform` si D-F10-5 = sí (P10 verde en F9)
  - **Hecho cuando**: el grep de R10.5.b → 0 (o solo `platform`, nombrado)
- [ ] **T10.14 · docs: el repo dice las rutas nuevas** · dep. T10.11 · cumple R10.7.a
  - **Ficheros**: los de `diseno.md` §4 (147 menciones hoy); `ESTADO.md` y `README.md` de `reorganizacion-modular/` («relevo hecho en el repo; UAT y ecosistema pendientes»); `CLAUDE.md` (la sección «en curso» pasa a «hecha»; el índice del sistema es `internal/arranque/orquestador.go`)
  - **Hecho cuando**: el recuento de `diseno.md` §4 → 0 fuera de `reorganizacion-modular/` y de lo marcado «(histórico)»
  - **Commit**: `docs(reorganizacion-modular): relevo — las rutas nuevas en la documentación del repo`
  - Cierre del bloque: el gate del bloque C (`reglas.md` §4) con sus salidas en el acta, y `git push origin dev` (rc sin pipe).

## Bloque D · cierre local · sesión F10-05 · T10.15–T10.17 · necesita Docker y UAT (SSH)

- [ ] **T10.15 · Gates y procesos contra el único binario** · dep. T10.14 · necesita Docker
  - **Hecho cuando**: `make ci-local` GATE_RC=0 con la toolchain fijada; `CUENTA=3 make test-procesos` `RC=0`, 0 SKIP, 0 FAIL; `git push origin dev` con su `rc` leído
- [ ] **T10.16 · UAT desde el commit del relevo** · dep. T10.15 · cumple R10.6.a, R10.2.b · necesita UAT (SSH)
  - **Hecho cuando**: `git checkout dev && git reset --hard origin/dev`; `GOWORK=off go build -o bin/server ./cmd/server`; `systemctl restart wapp-cloud` + las dos consolas; §6 (`path …/cmd/server`, `vcs.revision` = SHA del relevo) y §9 (líneas de clave) del runbook verificados; `bin/server.viejo-*` se conserva hasta T10.22
- [ ] **T10.17 · Acta `CERRADO`** · dep. T10.16
  - **Ficheros**: `traspasos/TRASPASO-F10-relevo.md` (`CERRADO <fecha>`), `ESTADO.md`
  - **Commit**: `docs(reorganizacion-modular): relevo cerrado en el repo y en UAT`

## Bloque E · fuera del repo · sesión F10-05 · T10.18–T10.21 · necesita la raíz de wApp y los repos hermanos

Cada tarea en **su** repo, con **su** gate, commit a `dev`. La tabla de mapeo es la de `04` §4 más D-9 y
D-10 (`diseno.md` §3.1). Si F10-05 no cabe en ~90 min, el punto limpio de corte es tras T10.17.

- [ ] **T10.18 · La documentación del ecosistema** · dep. T10.17 · cumple R10.7.b
  - **Hecho cuando**: el recuento de `diseno.md` §3.1 (1.124 menciones hoy) → 0 fuera de lo histórico; `make check-docs` de la raíz verde
- [ ] **T10.19 · ADR-0010: la regla de conteo nueva** · dep. T10.18 · D-F10-7
  - **Hecho cuando**: texto nuevo aprobado por Jhoan; su tabla re-medida con la regla nueva y el comando escrito; citas `bootstrap.go:1519/817/835` corregidas
- [ ] **T10.20 · Los 12 comentarios de repos hermanos** · dep. T10.17 · cumple R10.7.c
  - **Hecho cuando**: el grep de `diseno.md` §3.3 → 0; un commit por repo a `dev`; `make ci-local` de cada repo rc=0
- [ ] **T10.21 · La bóveda `analisis/` y `CLAUDE.md` de la raíz** · dep. T10.17
  - **Hecho cuando**: las 16 menciones re-apuntadas (skill `analizar-arquitectura` del ecosistema para las notas)

## Bloque F · `main` · sesión F10-06 · T10.22 — solo a petición expresa de Jhoan · necesita `main`, Docker y UAT (SSH)

- [ ] **T10.22 · `dev` → `main` y, si D-F10-6, tag** · dep. T10.17 y petición expresa de Jhoan
  - **Hecho cuando**: `main` = `dev` tras `make ci-local` y `make test-procesos` verdes sobre ese SHA; `sync-main-to-dev.yml` alineó `dev`; si hay tag (`v0.3.0` propuesto), `CHANGELOG.md` rellenado (va por detrás: `documentations/operacion.md` §4); `bin/server.viejo-*` borrado de UAT
