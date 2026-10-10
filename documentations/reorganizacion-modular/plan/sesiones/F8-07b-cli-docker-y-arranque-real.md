# F8-07b · F8 · Docker y arranque real · 💻 CLI

> ✎ **2026-10-10 (Jhoan, tras F8-06b)**: sesión nueva. F8-07 se parte en tres: [`F8-07a`](F8-07a-cli-limpieza-d-f7-9.md)
> (limpieza), esta y [`F8-07`](F8-07-cli-cierre.md) (el cierre). Motivo: `make ci-docker` no corre desde F8-01 y la
> conmutación sigue validada solo en estático y por los procesos.

| | |
|---|---|
| Fase · bloque | [F8 · conversacion](../F8-conversacion/README.md) · 7b · Docker y arranque real |
| Entorno | 💻 solo local |
| Nivel (E-12) | no aplica (no se escribe código de producción; si un gate destapa un fallo, se dice y se decide) |
| Duración objetivo | 45–90 min |
| Tareas | T8.36, T8.37 (= T9.29) |
| Depende de | F8-07a |
| Decisiones | D-F9-1 (adelantar F9) |
| Se para cuando | `make ci-docker` rc=0; las suites `Contrato` de los puertos con BD verdes contra Postgres; `cmd/server-modular` arranca **solo** y recorre una conversación entera con el Edge falso; `make test-procesos` rc=0 con 0 SKIP contra el binario nuevo; el hallazgo 51 refutado punto por punto; PR a `dev` abierto desde la rama de la sesión |

## Qué se valida (y por qué ahora)

- **`make ci-docker`, lo primero.** Son los gates de `ci-local` dentro de un contenedor Linux limpio. La última vez que dio
  rc=0 fue en **F8-01** (2026-10-09). Ninguna sesión de F8-02 a F8-06b lo corrió («el bloque no lo pide»), así que todo el
  runtime, la conmutación (F8-06) y el arreglo de D-F7-9 (F8-06b) están validados **solo en el Mac**. Si sale rojo, se lee
  antes de repetir: la intermitencia conocida es `TestRendimiento_P99PorItem` del código viejo (hallazgo 62 (c) de F7).
- **El binario nuevo, arrancado de verdad.** Hasta hoy `cmd/server-modular` solo ha corrido dentro de los procesos de F9.
  Aquí arranca **solo** (nunca a la vez que `cmd/server` contra la misma base o los mismos puertos) y recorre «carrito» →
  línea → confirmar.
- **Sin WhatsApp.** El «Edge de prueba» es el **Edge falso** de `test/procesos` (`edge_falso_*_test.go`): un cliente gRPC
  que habla el contrato **real** de CloudLink (enrola, conecta con mTLS, late, obedece al lease) e **inyecta los
  entrantes**; no lleva `whatsmeow`. La sesión de WhatsApp del Edge real, caducada en local y en el VPS, **no hace falta**.
- **Lo que F8-06 dejó sin comprobar** (hallazgo 51): la identidad de `entResolver`, `kp` y `gw` en ejecución; el barrido
  del agregador y `RecoverAtBoot` con base real; el receptor de los hooks; que el mux de detrás esté vacío. De él, F8-06b ya
  midió los procesos contra el binario nuevo (hallazgo 52).

**Fuera**: el Edge **real** con su inyector (es otro repo; ver la nota de abajo); UAT (F10); cerrar la fase (F8-07).

> 🟡 **Para decidir, no para esta sesión.** El Edge real (`wapp-edge-agent`) trae un inyector de entrantes de diagnóstico
> (`internal/adapters/control/diag/inyector.go`), nacido para medir un p99 y pensado para borrarse. Una prueba de
> integración con el **Edge real** y sin WhatsApp pasaría por ahí. No está decidido ni diseñado: es de otro repo.

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F8-07a) está integrada y empujada en `dev`.
- [ ] Decisiones rellenas en [`../DECISIONES.md`](../DECISIONES.md): D-F9-1 (adelantar F9).
- [ ] Docker encendido en el Mac.
- [ ] `go1.26.5` y `golangci-lint v2.12.2` disponibles (`make toolchain`).
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F8-07b del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F8 · conversacion → documentations/reorganizacion-modular/plan/F8-conversacion/
- Bloque: 7b · Docker y arranque real
- Tareas: T8.36 y T8.37 (= T9.29 de plan/F9-procesos/tareas.md) de plan/F8-conversacion/tareas.md
- Lee ENTERA la sección «Qué se valida (y por qué ahora)» de plan/sesiones/F8-07b-cli-docker-y-arranque-real.md.
- Te paras cuando: `make ci-docker` rc=0; las suites `Contrato` de los puertos con BD pasan contra Postgres; `cmd/server-modular` arranca solo y recorre una conversación con el Edge falso; `make test-procesos` y `make ci-local` dan rc=0 con 0 SKIP; el hallazgo 51 del README de F8 está refutado punto por punto; PR a `dev` abierto desde la rama de la sesión.
- Decisiones: D-F9-1 (adelantar F9) en plan/DECISIONES.md. Si falta alguna, PARA y dilo.
- Skills: validar-antes-de-cerrar, procesos-testcontainers, describir-pr.

- Base: F8-07a integrada en `dev` (T8.41–T8.43 `[x]` con SHA): compruébalo con `git log origin/dev` y, si no está, PARA y dilo. Trabaja en TU rama partida de `origin/dev`, nunca en `dev` (regla 6 del `CLAUDE.md`).

Orden: PRIMERO `make ci-docker` (rc sin pipe; no corre desde F8-01, 2026-10-09). Si sale rojo, LEE el log antes de repetir y separa lo conocido (`TestRendimiento_P99PorItem`, hallazgo 62 (c) de F7) de lo nuevo; un rojo nuevo es un hallazgo: dilo y PARA si pide tocar código de producción.
Después: las suites `Contrato` de `store`, `trigger`, `events.Store`, `self_numbers`, `tenant_resolver` e `intake` contra Postgres con el mismo `Montaje` que en memoria (P4), y los procesos del módulo (T9.29: «Entrante a respuesta», «De mensaje a borrador», «Re-análisis») contra el binario NUEVO. Nunca contra un Postgres vivo: testcontainers. `make test-procesos` nunca en paralelo con nada.
Arranque real: `cmd/server-modular` SOLO (nunca a la vez que `cmd/server` contra la misma base o puertos). El Edge de prueba es el Edge falso de `test/procesos`: habla el contrato real de CloudLink e inyecta los entrantes; la sesión de WhatsApp del Edge real está caducada y NO hace falta. Si T8.36 pide un e2e de `cmd/server-modular` que no existe como tal, dilo: no lo inventes sin preguntar.
Refuta, punto por punto, el hallazgo 51 del README de F8 (identidad de `entResolver`/`kp`/`gw` en ejecución, barrido del agregador y `RecoverAtBoot` con BD real, receptor de los hooks, mux de detrás vacío). Lo que no puedas ver desde fuera, dilo: no lo des por refutado.
No se escribe código de producción. Si un gate destapa un fallo real, PARA y pregunta.

Al terminar, las tres cosas: tareas [x] con SHA (o `[~]` diciendo qué falta), bloque en ESTADO.md, hallazgos en el README de la fase. Push de TU rama (`git push origin <rama>`, rc sin pipe) y PR a `dev` (`gh pr create --base dev`; «integrar SIN squash»): nunca directo a `dev`. No toques `main`.
No empieces la sesión siguiente (es F8-07, el cierre).
```

## Al terminar debe existir

- En [`../F8-conversacion/tareas.md`](../F8-conversacion/tareas.md): T8.36 y T8.37 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md`, con el rc de `make ci-docker` y lo refutado del hallazgo 51.
- Los hallazgos nuevos en el [README de la fase](../F8-conversacion/README.md).
- Un PR con `--base dev` desde la rama de la sesión, con «integrar SIN squash».

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** en `ESTADO.md`, no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
