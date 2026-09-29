# F10-03 · F10 · B · la prueba en UAT en sustitución · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F10 · relevo](../F10-relevo/README.md) · B · la prueba en UAT en sustitución (💻) |
| Tareas | T10.4–T10.6 |
| Depende de | F10-02 |
| Decisiones | Las de [`../DECISIONES.md`](../DECISIONES.md) que bloquean «F10 bloque B» |
| Se para cuando | acta con veredicto. |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión web que cierra (F10-02) terminó y dejó PR y, si aplica, traspaso.
- [ ] Docker encendido en el Mac; `go1.26.5` y `golangci-lint v2.12.2` disponibles.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F10-03 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F10 · relevo → documentations/reorganizacion-modular/plan/F10-relevo/
- Bloque(s): B · la prueba en UAT en sustitución
- Tareas: T10.4–T10.6 de plan/F10-relevo/tareas.md
- Entrada: T10.3; D-F10-1 decidida (ventana y fecha)
- Te paras cuando: acta con veredicto.
- Decisiones: las que en plan/DECISIONES.md bloquean «F10 bloque B» deben estar rellenas. Si falta alguna, PARA y dilo.
- Skills: validar-antes-de-cerrar, traspaso-web-local.

La prueba en UAT en sustitución (D-9, D-F10-1, D-F10-2): con acta y vuelta atrás preparada. Sin secretos en la documentación.

Al terminar: traspaso con «CERRADO <fecha>», tareas [x] con SHA, ESTADO.md, `git push origin dev` (rc sin pipe). No toques `main`.
```

## Al terminar debe existir

- La rama de la web integrada en `dev` **sin squash** y `dev` empujado.
- En [`../F10-relevo/tareas.md`](../F10-relevo/tareas.md): las tareas 💻 y 🌐→💻 de T10.4–T10.6 `[x]` con SHA.
- El traspaso con su sección `CERRADO <fecha>` (qué se refutó de su §7).
- `ESTADO.md` de la reorganización al día.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el traspaso o en `06-entorno-web.md` §5), no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
