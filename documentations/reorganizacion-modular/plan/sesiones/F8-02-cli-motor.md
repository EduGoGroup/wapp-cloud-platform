# F8-02 · F8 · el motor · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F8 · conversacion](../F8-conversacion/README.md) · 2 · el motor |
| Entorno | 💻 solo local |
| Nivel (E-12) | medio · `menu` y `media` simple (el del inventario aprobado) |
| Duración objetivo | 45–90 min |
| Tareas | T8.9–T8.11, T8.14, T8.23 |
| Depende de | F8-01 |
| Decisiones | ninguna pendiente |
| Se para cuando | `engine`·`menu`·`survey`·`media`·`turnoacotado` verdes (9 ficheros); `pendiente` del módulo = 0; `make ci-local` rc=0 con 0 SKIP; `dev` empujado |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F8-01) está integrada y empujada en `dev`.
- [ ] Decisiones rellenas en [`../DECISIONES.md`](../DECISIONES.md): ninguna pendiente.
- [ ] `go1.26.5` y `golangci-lint v2.12.2` disponibles (`make toolchain`).
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F8-02 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F8 · conversacion → documentations/reorganizacion-modular/plan/F8-conversacion/
- Bloque: 2 · el motor
- Tareas: T8.9–T8.11, T8.23 y T8.14 de plan/F8-conversacion/tareas.md
- Te paras cuando: `engine`, `menu`, `survey`, `media` y `turnoacotado` están verdes (9 ficheros); `pendiente` del módulo = 0; `make ci-local` rc=0 con 0 SKIP; `dev` empujado.
- Decisiones: ninguna pendiente en plan/DECISIONES.md. Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo, contrato-tdd, validar-antes-de-cerrar.

No hay rama web que integrar ni traspaso que cerrar: trabajas sobre `dev`.
`turnoacotado` importa `modulos/inferencia/llmvia`, no el viejo: añade la arista `conversacion → inferencia` a la lista blanca de `internal/modulos/fronteras_test.go`.

Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.

Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. `git push origin dev` (rc sin pipe). No toques `main`.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F8-conversacion/tareas.md`](../F8-conversacion/tareas.md): T8.9–T8.11, T8.14, T8.23 `[x]` con SHA.
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../F8-conversacion/README.md).
- `dev` empujado (`git push origin dev`, rc sin pipe).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** en `ESTADO.md`, no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir. Solo entonces se escribe un traspaso.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
