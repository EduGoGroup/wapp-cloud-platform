# F0-06 · F0 · E · la cara nueva vacía, los ✎ de `platform` y la deriva + F · el cierre local · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F0 · Andamiaje](../F0-andamiaje/README.md) · E · la cara nueva vacía, los ✎ de `platform` y la deriva + F · el cierre local (🌐→💻, 💻) |
| Tareas | T0.16–T0.21 + T0.22–T0.25 |
| Depende de | F0-05 |
| Decisiones | Las de [`../DECISIONES.md`](../DECISIONES.md) que bloquean «F0 bloque E/F» |
| Se para cuando | los cinco criterios de salida de [`README.md`](../F0-andamiaje/README.md) se cumplen y `dev` contiene F0 entera. |

> Cierra la parte 💻 de los bloques E y F.

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión web que cierra (F0-05) terminó y dejó PR y, si aplica, traspaso.
- [ ] Docker encendido en el Mac; `go1.26.5` y `golangci-lint v2.12.2` disponibles.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F0-06 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F0 · Andamiaje → documentations/reorganizacion-modular/plan/F0-andamiaje/
- Bloque(s): E · la cara nueva vacía, los ✎ de `platform` y la deriva + F · el cierre local
- Tareas: T0.16–T0.21 + T0.22–T0.25 de plan/F0-andamiaje/tareas.md
- Te paras cuando: los cinco criterios de salida de plan/F0-andamiaje/README.md se cumplen y `dev` contiene F0 entera.
- Decisiones: las que en plan/DECISIONES.md bloquean «F0 bloque E/F» deben estar rellenas. Si falta alguna, PARA y dilo.
- Skills: validar-antes-de-cerrar, traspaso-web-local.

Cierras F0 entero: la parte 💻 del bloque E (integración vieja con `WAPP_TEST_REQUIRE_DB=1` tras los ✎ de `platform`, SKIP contados con `-v`) y el bloque F.

Al terminar: traspaso con «CERRADO <fecha>», tareas [x] con SHA, ESTADO.md, `git push origin dev` (rc sin pipe). No toques `main`.
```

## Al terminar debe existir

- La rama de la web integrada en `dev` **sin squash** y `dev` empujado.
- En [`../F0-andamiaje/tareas.md`](../F0-andamiaje/tareas.md): las tareas 💻 y 🌐→💻 de T0.16–T0.21 + T0.22–T0.25 `[x]` con SHA.
- El traspaso con su sección `CERRADO <fecha>` (qué se refutó de su §7).
- `ESTADO.md` de la reorganización al día.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el traspaso o en `06-entorno-web.md` §5), no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
