# F10-03 · F10 · B · la prueba en UAT en sustitución · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F10 · relevo](../F10-relevo/README.md) · B · la prueba en UAT en sustitución |
| Entorno | 💻 solo local; necesita **UAT por SSH** (`ssh wapp-vps`) |
| Nivel (E-12) | No aplica: F10 no reconstruye un módulo y no lleva inventario E-12 |
| Duración objetivo | 45–90 min por tramo: abrir la ventana (T10.4) y, pasadas ≥ 24 h, cerrarla (T10.5–T10.6). La espera no es sesión |
| Tareas | T10.4–T10.6 |
| Depende de | F10-02 |
| Decisiones | D-F10-1 (ventana y fecha), D-F10-2 |
| Se para cuando | el acta tiene las salidas de `diseno.md` §2.3, la tabla de §2.4 con números y el veredicto de Jhoan («sigue» o «vuelta atrás»). |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F10-02) cerró con sus tres cosas y está empujada a `dev`.
- [ ] Las decisiones de la fila «Decisiones» están rellenas en [`../DECISIONES.md`](../DECISIONES.md) §6.
- [ ] Acceso SSH a UAT y la ventana acordada (D-F10-1).
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F10-03 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md
En F10 todo es local: no hay sesión web que cerrar, ni rama que integrar, ni traspaso que leer. Trabajas directo en `dev`.

Tu encargo (y solo este):
- Fase: F10 · relevo → documentations/reorganizacion-modular/plan/F10-relevo/
- Bloque(s): B · la prueba en UAT en sustitución
- Tareas: T10.4–T10.6 de plan/F10-relevo/tareas.md
- Entrada: T10.3; D-F10-1 decidida (ventana y fecha)
- Te paras cuando: el acta tiene las salidas de `diseno.md` §2.3, la tabla de §2.4 con números y el veredicto de Jhoan («sigue» o «vuelta atrás»).
- Decisiones: D-F10-1 y D-F10-2 deben estar rellenas en plan/DECISIONES.md. Si falta alguna, PARA y dilo.
- Skills: validar-antes-de-cerrar.

La prueba en UAT en sustitución (D-9, D-F10-1, D-F10-2): con acta y vuelta atrás preparada. Sin secretos en la documentación.
Primer tramo: T10.4 y PARA (la ventana corre sola). Segundo tramo, al relanzarte tras la ventana: T10.5 y T10.6. Si salta un criterio de corte, vuelta atrás en < 5 min (`diseno.md` §2.5) y PARA.
Nivel de ceremonia: no hay inventario E-12 (F10 no reconstruye un módulo). Sin umbral de cobertura: `make cobertura-ficheros` es un informe.
Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. `git push origin dev` (rc sin pipe). No toques `main`.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F10-relevo/tareas.md`](../F10-relevo/tareas.md): T10.4–T10.6 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../F10-relevo/README.md).
- El acta de la prueba en `traspasos/TRASPASO-F10-relevo.md`, con el veredicto de Jhoan.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (sin Docker, sin SSH a UAT, red): se **anota** en el acta (`traspasos/TRASPASO-F10-relevo.md`), no se adapta el proyecto a ella.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir. Solo en ese caso se escribe un traspaso.
- Un criterio de corte de `diseno.md` §2.4: vuelta atrás al binario viejo, hallazgo con la fase culpable, y F10 vuelve a su entrada.
