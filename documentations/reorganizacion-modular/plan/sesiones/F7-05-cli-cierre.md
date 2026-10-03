# F7-05 · F7 · cierre local · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F7 · captacion](../F7-captacion/README.md) · E · cierre local |
| Entorno | 💻 solo local |
| Nivel (E-12) | no aplica (no se escribe código de producción) |
| Duración objetivo | 45–90 min |
| Tareas | T7.27–T7.29 (T7.27 = T9.28) |
| Depende de | F7-04 |
| Decisiones | D-F9-1 (adelantar F9) |
| Se para cuando | las suites de `intake`, `casebank` e `intentcfg` pasan contra Postgres con el arnés · P4 y P8 verdes contra `viejo` y `nuevo` · `make test-procesos` y `make ci-local` rc=0 con 0 SKIP · `dev` empujado |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F7-04) terminó y dejó PR (y traspaso, si corrió en la web).
- [ ] Docker encendido en el Mac; `go1.26.5` y `golangci-lint v2.12.2` disponibles.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F7-05 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F7 · captacion → documentations/reorganizacion-modular/plan/F7-captacion/
- Bloque: E · cierre local
- Tareas: T7.27–T7.29 de plan/F7-captacion/tareas.md (T7.27 = T9.28 de plan/F9-procesos/tareas.md)
- Te paras cuando: las suites de `intake`, `casebank` e `intentcfg` pasan contra Postgres con el arnés, P4 y P8 están verdes contra `viejo` y `nuevo`, `make test-procesos` y `make ci-local` dan rc=0 con 0 SKIP, y `dev` está empujado.
- Decisiones: D-F9-1 debe estar rellena en plan/DECISIONES.md. Si falta, PARA y dilo.
- Skills: validar-antes-de-cerrar, procesos-testcontainers, traspaso-web-local (solo si hay traspaso que cerrar).

Integra la rama de F7-04 en `dev` sin squash. Corre las suites de contrato de los puertos con BD contra Postgres con el mismo `Montaje` que en memoria (P4) y los procesos del módulo (P4 mensaje a borrador, P8 re-análisis) contra el binario NUEVO y el viejo. Nunca contra un Postgres vivo: testcontainers.

Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.

Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. Si hubo traspaso, ciérralo con «CERRADO <fecha>». `git push origin dev` (rc sin pipe). No toques `main`.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- La rama de F7-04 integrada en `dev` **sin squash** y `dev` empujado.
- En [`../F7-captacion/tareas.md`](../F7-captacion/tareas.md): T7.27–T7.29 `[x]` con SHA.
- Un bloque de la sesión en `ESTADO.md` de la reorganización, con F7 «cerrada».
- Los hallazgos nuevos en el [README de la fase](../F7-captacion/README.md) y su estado «cerrada» con SHA.
- Si hubo traspaso: su sección `CERRADO <fecha>` (qué se refutó de su §7).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en `ESTADO.md` o en el traspaso), no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
