# F3-05 · F3 · cierre local con mTLS · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F3 · edge](../F3-edge/README.md) · bloque F3-05 de [`tareas.md`](../F3-edge/tareas.md) |
| Entorno | 💻 solo local |
| Nivel (E-12) | no aplica: no escribe ficheros de módulo (cierre, suites contra Postgres y procesos) |
| Duración objetivo | 45–90 min |
| Tareas | T3.29–T3.30 y la parte 💻 de T3.27 |
| Depende de | F3-04 |
| Decisiones | D-F9-1 (adelantar F9) y D-F3-6 de [`../DECISIONES.md`](../DECISIONES.md) |
| Se para cuando | e2e de `cmd/server-modular` con mTLS real verde · las 7 suites de puerto con BD verdes contra Postgres con el arnés · `make test-procesos` rc=0 con 0 SKIP contra los dos binarios · `make ci-local` rc=0 · `dev` empujado. |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F3-04) está integrada en `dev` (PR fusionado **sin squash**) y, si hubo, dejó traspaso.
- [ ] Docker encendido en el Mac; `go1.26.5` y `golangci-lint v2.12.2` disponibles.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F3-05 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F3 · edge → documentations/reorganizacion-modular/plan/F3-edge/
- Bloque: F3-05 · cierre local con mTLS
- Tareas: T3.29–T3.30 y la parte 💻 de T3.27 de plan/F3-edge/tareas.md
- Qué: e2e con mTLS real (enrolamiento → Connect → lease → revocación; dos Edge de prueba a la vez), las 7 suites de puerto con BD contra Postgres con el arnés (testcontainers, nunca un Postgres vivo), y el proceso «Enrolamiento de un Edge y su lease» (T9.24 de plan/F9-procesos/tareas.md) contra el binario viejo y el NUEVO.
- Te paras cuando: e2e verde, 7 suites verdes en Postgres, `make test-procesos` rc=0 con 0 SKIP contra los dos binarios, `make ci-local` rc=0.
- Decisiones: si D-F9-1 = no en plan/DECISIONES.md, T3.30 se tacha con ese motivo y la lista pasa a F9 (T9.34).
- Skills: validar-antes-de-cerrar, procesos-testcontainers, traspaso-web-local (solo si recibiste un traspaso).

Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.

Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase.
Si hubo traspaso, ciérralo con «CERRADO <fecha>». `git push origin dev` (rc sin pipe). No toques `main`.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F3-edge/tareas.md`](../F3-edge/tareas.md): T3.27 (parte 💻), T3.29 y T3.30 `[x]` con SHA.
- Un bloque de la sesión en `ESTADO.md` de la reorganización, y F3 «cerrada» con SHA en su [README](../F3-edge/README.md).
- Los hallazgos nuevos en el README de F3.
- `dev` empujado; el traspaso, si lo hubo, con su sección `CERRADO <fecha>`.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en `ESTADO.md` o en el traspaso), no se adapta el proyecto a ella.
- Si el bloque no cabe en ~90 min: para en un punto limpio (tras T3.29), cierra con las tres cosas y se relanza.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
