# F9-04 · F9 · B2 · procesos de negocio · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F9 · procesos (testcontainers)](../F9-procesos/README.md) · B2 · procesos de negocio, y cierre de B1 + B2 (sesión **completa**) |
| Entorno | 💻 solo local: testcontainers y los dos binarios |
| Nivel (E-12) | No aplica: F9 no reconstruye un módulo y no lleva inventario E-12. Son tests de proceso (caja negra) |
| Duración objetivo | 45–90 min. Seis procesos pueden no caber: para en un punto limpio tras un proceso cerrado y se relanza |
| Tareas | T9.17–T9.21 🕐 (P4–P8, el guion de inferencia y el CRM falso), T9.35 (P10; D-F9-4 = sí) y T9.22 (`nucleo`, F1) |
| Depende de | [F9-03](F9-03-cli-procesos-plataforma-y-acceso.md) |
| Decisiones | Las de [`../DECISIONES.md`](../DECISIONES.md) que bloquean «F9 bloque B2» |
| Se para cuando | P4–P8 y P10 dan `RC=0` contra el viejo; **toda la suite B1 + B2** da `RC=0` contra el viejo **y** el nuevo, con 0 SKIP y 0 FAIL; T9.22 hecha (la suite de `contact` contra Postgres y la suite entera contra el nuevo) |

> De esta sesión depende **F2-01**.

## Antes de pegar el prompt (Jhoan)

- [ ] F9-03 está cerrada y en `dev` (T9.13–T9.16 `[x]`, el mutante `maxTxAttempts = 1` cae).
- [ ] Las decisiones que bloquean «F9 bloque B2» en [`../DECISIONES.md`](../DECISIONES.md) están rellenas.
- [ ] Docker encendido en el Mac; `make toolchain` da `TOOLCHAIN=OK`.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F9-04 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md (es una sesión COMPLETA: escribes código)

Tu encargo (y solo este):
- Fase: F9 · procesos (testcontainers) → documentations/reorganizacion-modular/plan/F9-procesos/
- Bloque: B2 · procesos de negocio, y el cierre de B1 + B2
- Tareas: T9.17–T9.21 (P4–P8, con guion_test.go y el CRM falso), T9.35 (P10, D-F9-4 = sí) y T9.22 (`nucleo`) de
  plan/F9-procesos/tareas.md.
- Entrada: F9-03 cerrada.
- Cierre de B1 y B2: `make test-procesos` contra viejo Y nuevo con todos los procesos (P0–P10), RC=0 por binario
  leído del log, 0 SKIP, 0 FAIL.
- T9.22: la suite de contrato de contact contra Postgres y la suite entera contra el nuevo; comprueba que el test
  de cableado de `nucleo` está completo (hallazgo 39 de F1: los procesos no ven un cableado al paquete viejo).
- Las tablas de casos llevan casos adversarios (reglas.md §2).
- Te paras cuando: P4–P8 y P10 dan RC=0 contra el viejo; la suite B1 + B2 entera da RC=0 contra viejo y nuevo; y
  T9.22 está hecha.
- Decisiones: las que en plan/DECISIONES.md bloquean «F9 bloque B2» deben estar rellenas. Si falta alguna, PARA y dilo.
- Skills: procesos-testcontainers, validar-antes-de-cerrar.

No aplica nivel de ceremonia E-12 (F9 no reconstruye un módulo). Sin umbral de cobertura: los procesos son parte
de lo que lo sustituye.
Un proceso por commit (`procesos(<proceso>): …`). Un rojo solo contra el nuevo es un hallazgo: no ajustes el test.
Si no cabe en 45–90 min, para en un punto limpio tras un proceso cerrado y se relanza.
Orquesta con sub-agentes (uno por proceso) y protege tu contexto. Los worktrees de sub-agentes nacen de
origin/main: ponlos en el SHA de dev antes de medir.
Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase.
`git push origin dev` (rc sin pipe). No toques `main`. No empieces la sesión siguiente.
```

## Al terminar debe existir

- Commits `procesos(<proceso>): …` (uno por proceso) y uno de documentación, en `dev`, empujados.
- En [`../F9-procesos/tareas.md`](../F9-procesos/tareas.md): T9.17–T9.21, T9.35 y T9.22 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque en `ESTADO.md`: `RC`, `--- PASS` y `--- SKIP` por binario de la suite entera.
- Los hallazgos nuevos en el [README de F9](../F9-procesos/README.md).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Un proceso rojo **solo contra el nuevo**: es un hallazgo (R9.4.c); se anota y **no** se ajusta el test.
- Una limitación del entorno (sin Docker, red, toolchain): se **anota** en `ESTADO.md`, no se adapta el proyecto a ella.
- Si el bloque no cabe en ~90 min: para en un punto limpio tras un proceso cerrado, cierra con las tres cosas y se relanza.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
