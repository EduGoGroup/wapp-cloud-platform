# F10-02 · F10 · A · preparación y dorada · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F10 · relevo](../F10-relevo/README.md) · A · preparación y dorada |
| Entorno | 💻 solo local; no necesita Docker ni UAT |
| Nivel (E-12) | No aplica: F10 no reconstruye un módulo y no lleva inventario E-12 |
| Duración objetivo | 45–90 min |
| Tareas | T10.1–T10.3 |
| Depende de | F9-05 y F10-01 |
| Decisiones | D-F10-1 (fecha de la ventana, para escribir el SHA a desplegar) |
| Se para cuando | la dorada de la huella está commiteada y verificada contra el viejo en ejecución, y el acta dice el SHA que se desplegará en UAT. |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F9-05 y F10-01) cerró con sus tres cosas y está empujada a `dev`.
- [ ] Las decisiones de la fila «Decisiones» están rellenas en [`../DECISIONES.md`](../DECISIONES.md) §6.
- [ ] F9 `CERRADO`, con el resultado del mutante `maxTxAttempts = 1` de T9.15 escrito.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F10-02 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md
En F10 todo es local: no hay sesión web que cerrar, ni rama que integrar, ni traspaso que leer. Trabajas directo en `dev`.

Tu encargo (y solo este):
- Fase: F10 · relevo → documentations/reorganizacion-modular/plan/F10-relevo/
- Bloque(s): A · preparación y dorada
- Tareas: T10.1–T10.3 de plan/F10-relevo/tareas.md
- Entrada: F9 `CERRADO`; las condiciones de «Entradas» del README de la fase
- Te paras cuando: la dorada de la huella está commiteada y verificada contra el viejo en ejecución, y el acta dice el SHA que se desplegará en UAT.
- Decisiones: D-F10-1 debe estar rellena en plan/DECISIONES.md. Si falta, PARA y dilo.
- Skills: validar-antes-de-cerrar.

Empieza por la verdad de campo (T10.1): adaptadores `bridge_*.go` = 0, puentes (import) = 0, `Conmutados` completo, pendientes = 0, y el resultado de T9.15 sobre el mutante `maxTxAttempts = 1` (hallazgo 38 de F1). Si alguna falla, PARA y dilo: el relevo no procede.
Nivel de ceremonia: no hay inventario E-12 (F10 no reconstruye un módulo). Sin umbral de cobertura: `make cobertura-ficheros` es un informe.
Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. Push de TU rama (`git push origin <rama>`, rc sin pipe) y PR a `dev` (`gh pr create --base dev`, o el PR ya abierto de esa rama; «integrar SIN squash»): nunca directo a `dev` (regla 6 del `CLAUDE.md`). No toques `main`.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F10-relevo/tareas.md`](../F10-relevo/tareas.md): T10.1–T10.3 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../F10-relevo/README.md).
- `internal/arranque/testdata/huella-vieja.golden` commiteada; el acta (`traspasos/TRASPASO-F10-relevo.md`) con la verdad de campo y el SHA a desplegar.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (sin Docker, sin SSH a UAT, red): se **anota** en el acta (`traspasos/TRASPASO-F10-relevo.md`), no se adapta el proyecto a ella.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir. Solo en ese caso se escribe un traspaso.
