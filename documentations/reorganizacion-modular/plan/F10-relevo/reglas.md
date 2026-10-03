# F10 · Reglas — lo que no se toca, las trampas y la definición de hecho

## 1 · Lo que no se toca

| Qué | Por qué |
|---|---|
| Ni una ruta, rpc, tabla, variable, métrica, texto observable, el literal del aviso pasivo, el contrato CRM ni la doble llave | `03` §1. Lo vigilan la dorada de la huella y los procesos |
| `internal/platform/**` (salvo sus 9 tests con BD si D-F10-5) | No es módulo; lo comparten los dos arranques hasta hoy y el único después |
| `internal/platform/storage/postgres/migrations/structure/*.sql` | Full-replay por hash: el esquema del relevo es **el mismo** que el de UAT, y eso es lo que hace segura la marcha atrás |
| La unidad systemd, el `EnvironmentFile`, la ruta `bin/server` y el comando de compilación en UAT | D-9: el despliegue no cambia |
| `documentations/reorganizacion-modular/` (salvo `ESTADO.md` y `README.md`) | Es la historia de la reconstrucción |
| `docs/` de la raíz de wApp | Documentación vieja y condenada del ecosistema: no se usa ni se escribe |
| `main` | Solo a petición de Jhoan (regla 4 del ecosistema) |

## 2 · Prohibiciones

- 🚫 **Borrar antes de probar en UAT.** El orden es: prueba en sustitución (bloque B) → relevo en el repo
  (bloque C). Tras el borrado ya no hay oráculo ni binario viejo que restaurar desde el repo.
- 🚫 **Borrar el arranque viejo antes de congelar su huella** (commit 1 de `arquitectura.md` §3).
- 🚫 **Borrar `deadlock_integration_test.go` (y el resto de la integración vieja) sin que T9.15 (P3) ejerza el
  reintento de `postgres.WithTx` y haga caer el mutante `maxTxAttempts = 1`** (hallazgo 38 de F1): es lo único que
  hoy ejerce ese reintento. Si el mutante sobrevive, se para y se arregla P3 antes (R10.3.d, T10.11).
- 🚫 **Empezar el relevo con un adaptador `bridge_*.go` vivo, un puente (import) declarado o `Conmutados`
  incompleto** (`05` §4.1 y §4.2). Son tres comprobaciones distintas; la entrada exige las tres.
- 🚫 **Dos binarios contra la misma base o los mismos puertos**, tampoco en UAT: la prueba es **en
  sustitución**, nunca al lado (`04` §2.2).
- 🚫 **`stop` + `start`** de `wapp-cloud`: el BFF (`PartOf=`) no vuelve con el `start`; siempre `restart`.
  Y reiniciar a mano las dos consolas.
- 🚫 **Dar por bueno el despliegue sin el §6 y el §9 del runbook de UAT**: instalado ≠ corriendo, y una
  clave `generated` pierde mensajes con `/healthz` en verde.
- 🚫 **Regenerar la dorada de la huella para «que pase»**. Si el arranque único no casa con la dorada,
  el relevo cambió un contrato: se para.
- 🚫 **Reescribir citas históricas** del ecosistema (bitácoras, planes cerrados): se anota la ruta
  nueva al lado, no se falsea lo que era cierto.
- 🚫 **Squash** o juntar commits del relevo: un commit por tarea, directo en `dev`; cada uno es un paso revertible.
- 🚫 **Tratar `make cobertura-ficheros` como gate**: es un informe sin umbral (P2).
- 🚫 Un secreto en el acta de UAT: se dice dónde vive la credencial, nunca cuál es.

## 3 · Trampas conocidas

| # | Trampa | Dónde | Qué hacer |
|---|---|---|---|
| T-1 | `go version -m` es la única forma fiable de saber qué binario corre; ningún binario de wApp tiene `-version` (un servidor **arranca** con ese flag) | `despliegue-uat.md` §6–§7 (ecosistema) | Mirar `path …/cmd/server-modular` vs `…/cmd/server` y `vcs.revision` en `/proc/$pid/exe` |
| T-2 | Las líneas de clave del log acumulan arranques viejos | `despliegue-uat.md` §9 | Solo vale el `tail -2` del último arranque; patrón `clave pública de(l)? (cifrado\|lease)` |
| T-3 | `/healthz` en verde no prueba que las goroutines de fondo vivan (fallo mudo, sin supervisor) | `documentations/operacion.md` §5.3; `internal/bootstrap/arranque/fase9_fondo.go` | Mirar `intake_jobs` atascados y `webhook_outbox` sin entregar (`diseno.md` §2.4) |
| T-4 | Un `CounterVec` no aparece en `/metrics` hasta su primer incremento: el `diff` de nombres puede mentir en los primeros minutos | `contratos.md` §8 | Repetir el `diff` al final de la ventana |
| T-5 | `cmd/casebank` y `cmd/prompts` importan paquetes viejos: si su fase no los re-apuntó, el borrado rompe la compilación | `go list` (`arquitectura.md` §2) | Re-apuntarlos en el commit 2 y comparar su salida (R10.3.c) |
| T-6 | `cmd/server/flows_integration_test.go` importa `internal/flujos/**` y solo corre con BD: con `ci-local` (sin BD) **compila** y parece inofensivo hasta que se borra `flujos` | `go list -f '{{.TestImports}}' ./cmd/server` | Borrarlo en el commit 2 (D-F10-4) |
| T-7 | `.github/workflows/ci.yml` es `workflow_dispatch`: nadie verá que su job `integration` (`postgres:16` + `WAPP_TEST_DB_DSN` + `go test -p 1 ./...`) quedó huérfano | `ci.yml` job `integration` | Cambiarlo en el commit 8 y correrlo **una vez a mano** si Jhoan quiere comprobarlo |
| T-8 | `ci.yml` corre `go mod tidy` y exige diff vacío: borrar paquetes puede dejar dependencias sin uso en `go.mod` | `ci.yml` paso «go.mod / go.sum tidy» | `GOWORK=off go mod tidy` en el commit 6 y revisar el diff |
| T-9 | Las consolas no tienen `PartOf=`: tras reiniciar el cloud siguen hablando con uno que ya no está, sin error en pantalla | `despliegue-uat.md` §8 | `systemctl restart wapp-client-console wapp-platform-console` siempre |
| T-10 | UAT sirve de `dev` para repos con plan en curso; teclear `origin/main` de memoria despliega el estado anterior y nada lo dice | `despliegue-uat.md` §3 | `git checkout dev && git reset --hard origin/dev`, y verificar con T-1 |
| T-11 | ADR-0010 medido con la regla vieja tras el relevo daría «un solo módulo» y una tabla de incumplimientos vacía: un falso cumplimiento | `diseno.md` §3.2 | Reescribir la regla **antes** de volver a medir |
| T-12 | En la doc del ecosistema, `internal/bootstrap` también es ruta del BFF y de las dos consolas | `diseno.md` §3.1 | Reescribir solo las menciones que son de **este** repo (por contexto o prefijo `wapp-cloud-platform`) |

## 4 · Definición de hecho

**De la prueba de UAT (bloque B)**: acta con las salidas de `diseno.md` §2.3 y la tabla de §2.4
rellenada con números; veredicto de Jhoan escrito; si hubo vuelta atrás, hallazgos con la fase
culpable y F10 vuelve a su entrada.

**Del relevo en el repo (bloque C)**, sobre el último commit:

```bash
L=/tmp/gate-f10.log
GOWORK=off make ci-local > $L 2>&1; echo "GATE_RC=$?" >> $L; tail -1 $L                 # GATE_RC=0
ls -d internal/*/                                                                           # apipublica arranque modulos nucleo platform
ls internal/arranque/bridge_*.go 2>/dev/null | wc -l                                        # 0 adaptadores (05 §4.2)
grep -rn 'pendiente.Implementar' --include='*.go' . | wc -l                                 # 0
GOWORK=off go test -v ./... 2>&1 | grep -c -- '--- SKIP'                                   # 0 (o solo platform, nombrados, si D-F10-5 = no)
grep -rn 'WAPP_PROCESOS_BINARIO' --include='*.go' --include=Makefile . | wc -l              # 0
GOWORK=off go list -deps ./cmd/server | grep -c 'internal/bootstrap\|internal/publicapi'    # 0
```

Y, leídos en `internal/modulos/fronteras_test.go`: `Puentes` (import) **vacía** y `Conmutados` con **todos** los
módulos de F1–F8. F10 no crea ni retira adaptadores: los exige a cero desde la entrada.

**De la fase**: además, `make test-procesos` `RC=0` contra `cmd/server` (necesita Docker); UAT desplegado desde el
commit del relevo con §6 y §9 del runbook verificados; acta `CERRADO`; `ESTADO.md` dice «relevo
hecho»; los 12 comentarios hermanos y la doc del ecosistema re-apuntados (bloque E) o, si no se
hicieron, listados como pendientes en `ESTADO.md` con su comando de recuento.

**De cada sesión** (45–90 min, todas locales): tareas `[x]` con SHA, un bloque en `ESTADO.md` y los hallazgos
nuevos en el README de la fase. Sin PR ni traspaso, salvo que la sesión se corte.
