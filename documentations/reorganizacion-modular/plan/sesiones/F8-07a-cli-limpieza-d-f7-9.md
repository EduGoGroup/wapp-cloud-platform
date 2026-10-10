# F8-07a · F8 · limpieza tras D-F7-9 · 💻 CLI

> ✎ **2026-10-10 (Jhoan, tras F8-06b)**: sesión nueva. F8-07 se parte en tres: esta, [`F8-07b`](F8-07b-cli-docker-y-arranque-real.md)
> (Docker y arranque real) y [`F8-07`](F8-07-cli-cierre.md) (el cierre). Trae lo que salió del repaso de pendientes de F8-06b
> (hallazgos 55, 57 y 58 del [README de F8](../F8-conversacion/README.md)).

| | |
|---|---|
| Fase · bloque | [F8 · conversacion](../F8-conversacion/README.md) · 7a · limpieza tras D-F7-9 |
| Entorno | 💻 solo local |
| Nivel (E-12) | medio (borra una operación de un puerto con BD y toca los tests de cableado) |
| Duración objetivo | 45–90 min |
| Tareas | T8.41, T8.42, T8.43 |
| Depende de | F8-06b (PR #69 integrado en `dev`, merge `2902ec40`) |
| Decisiones | D-F8-13 (rellena); la deuda D-35 de `deuda.md` (decidida: se borra) |
| Se para cuando | `PutSourceText` y `ComposeAtFlush` no existen en el árbol nuevo y `intake.JobStore` vuelve a cuatro operaciones, con los tests de cableado al día **sin debilitarse**; el caso adversario del callback del CRM tolera el corte de conexión y sigue fallando si el servidor acepta el cuerpo; los siete mutantes de D-F7-9 repetidos y muertos; `make ci-local` rc=0 con 0 SKIP; `make test-procesos` rc=0 contra el binario nuevo; PR a `dev` abierto desde la rama de la sesión |

## Qué se hace (y qué no)

Tres cosas pequeñas e independientes, **cada una en su propio commit y en este orden**:

1. **T8.41 · borrar lo que quedó huérfano.** Desde F8-06b la ventana cierra con su sobre (`CloseWithSourceText`) y el job de
   re-análisis nace con el suyo (`OpenReanalysis`): nadie llama ya en producción a `PutSourceText` ni a `ComposeAtFlush`. Se
   borran, con `SourceTextWriter`, el campo `SourceTextComposer.jobs` y el argumento del constructor, los casos
   `PutSourceText_*` de la suite y las aserciones de cableado que miraban `composer.jobs`. **`CloseWindow` NO se borra**:
   la usa el cierre sin sobre cuando falla la composición.
2. **T8.42 · el caso adversario del callback del CRM.** `TestP6_CRMBridge/callback_body_adversarial` se pone rojo de forma
   intermitente con «connection reset by peer» o «broken pipe» (hallazgos 14 y 58). **Antes de tocar** hay que leer el caso
   y confirmar que el corte es el servidor rechazando el cuerpo mientras el test aún lo envía: en F8-06b solo se leyó el
   mensaje del log. Si no es eso, la sesión **para y lo dice**. Arreglo solo en `test/procesos/`; el servidor no se toca.
3. **T8.43 · repetir los siete mutantes** que sostienen el arreglo de D-F7-9, por quien no escribió el código y **después
   de T8.41** (que cambia el compositor). Un mutante que no compila no cuenta como muerto.

**Fuera**: el código viejo; `make ci-docker` y el arranque real (son de F8-07b); cerrar la fase (F8-07).

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F8-06b; PR #69, merge `2902ec40`) está integrada y empujada en `dev`.
- [ ] Decisiones rellenas en [`../DECISIONES.md`](../DECISIONES.md): D-F8-13.
- [ ] Docker encendido en el Mac.
- [ ] `go1.26.5` y `golangci-lint v2.12.2` disponibles (`make toolchain`).
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F8-07a del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F8 · conversacion → documentations/reorganizacion-modular/plan/F8-conversacion/
- Bloque: 7a · limpieza tras D-F7-9
- Tareas: T8.41, T8.42 y T8.43 de plan/F8-conversacion/tareas.md, en ese orden, cada una en su propio commit
- Lee ENTERA la sección «Qué se hace (y qué no)» de plan/sesiones/F8-07a-cli-limpieza-d-f7-9.md.
- Te paras cuando: `PutSourceText` y `ComposeAtFlush` no existen en el árbol nuevo e `intake.JobStore` vuelve a cuatro operaciones; el caso adversario del callback del CRM tolera el corte y sigue fallando si el servidor acepta el cuerpo; los siete mutantes de T8.43 muertos; `make ci-local` rc=0 con 0 SKIP; `BINARIO=nuevo make test-procesos` rc=0; PR a `dev` abierto desde la rama de la sesión.
- Decisiones: D-F8-13 en plan/DECISIONES.md y la deuda D-35 de documentations/deuda.md (decidida: se borra). Si falta alguna, PARA y dilo.
- Skills: contrato-tdd, procesos-testcontainers, validar-antes-de-cerrar, describir-pr.

- Base: F8-06b integrada en `dev` (PR #69, merge `2902ec40`): compruébalo con `git log origin/dev` y, si no está, PARA y dilo. Trabaja en TU rama partida de `origin/dev`, nunca en `dev` (regla 6 del `CLAUDE.md`).

Lo que F8-06b te deja dicho: su bloque de ESTADO.md y los hallazgos 52–59 del README de F8.
- T8.41: borra `(*SourceTextComposer).ComposeAtFlush` (source_composer_flush.go y su test), `runtime.SourceTextWriter`, el campo `SourceTextComposer.jobs` y su argumento en `NewSourceTextComposer`, `intake.JobStore.PutSourceText` con sus dos implementaciones y sus tests, los casos `PutSourceText_*` de `intakehelpertest` (y los helpers que solo ellos usen) y la llamada en `internal/arranque/fase5_captacion.go`. `CloseWindow` y `CloseWithSourceText` NO se tocan. Los tests de cableado (`conversacion_cableado_test.go`, `captacion_cableado_test.go`) leen `composer.jobs` por reflexión: esas aserciones desaparecen CON el campo, en el mismo commit, y las demás no se relajan. `store_test.go` fija el número de operaciones del puerto: vuelve a cuatro. Si al borrar aparece un llamante de producción que F8-06b no vio, PARA y dilo.
- T8.42: antes de tocar, LEE el caso y confirma la causa. El arreglo acepta como rechazo válido la respuesta de error o el corte al enviar; si el servidor ACEPTA el cuerpo adversario, el caso sigue fallando. Comprueba con `-run TestP6_CRMBridge -count=10` contra los dos binarios.
- T8.43: los siete mutantes, en serie, por `go test -overlay` sobre copias fuera del repo, nunca editando el checkout. Los corres TÚ (el orquestador) o un sub-agente que no haya escrito ese código. Anota mutante → test que lo mata.
- Método que funcionó (hallazgos 47 y 59): rojo y verde con el mismo sub-agente; los sub-agentes no commitean; nadie lanza `pkill`; `make test-procesos` nunca en paralelo con nada. El lint, también con `--build-tags pendiente,integracion`; un `golangci-lint` a mano necesita `GOTOOLCHAIN=go1.26.5` delante.
- Un cambio de conducta visible desde fuera NO se adapta en los consumidores: se suma a la deuda D-36.

Reloj inyectado; prohibido `time.Sleep` y `t.Skip`; ningún `//nolint` nuevo; nada de `go ... -mod=mod`. Nombres en inglés en lo nuevo (E-11); ≤ 500 líneas por `.go` (E-13). El código viejo no se toca.

Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase; y la deuda D-35 marcada cerrada en deuda.md. Push de TU rama (`git push origin <rama>`, rc sin pipe) y PR a `dev` (`gh pr create --base dev`; «integrar SIN squash»): nunca directo a `dev`. No toques `main`.
No empieces la sesión siguiente (es F8-07b).
```

## Al terminar debe existir

- En [`../F8-conversacion/tareas.md`](../F8-conversacion/tareas.md): T8.41, T8.42 y T8.43 `[x]` con SHA.
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../F8-conversacion/README.md), con la tabla mutante → test de T8.43.
- La deuda D-35 de [`deuda.md`](../../../deuda.md) marcada **cerrada**.
- Un PR con `--base dev` desde la rama de la sesión, con «integrar SIN squash».

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** en `ESTADO.md`, no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
