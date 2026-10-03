# F9-03 · F9 · B1 · procesos de plataforma y acceso · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F9 · procesos (testcontainers)](../F9-procesos/README.md) · B1 · procesos de plataforma y acceso (sesión **completa**: escribe, corre y cierra) |
| Entorno | 💻 solo local: testcontainers y los dos binarios |
| Nivel (E-12) | No aplica: F9 no reconstruye un módulo y no lleva inventario E-12. Son tests de proceso (caja negra) |
| Duración objetivo | 45–90 min. Cuatro procesos pueden no caber: para en un punto limpio tras un proceso cerrado y se relanza |
| Tareas | T9.13–T9.16 🕐 (P1, P2, P3 con el reintento de `WithTx`, P9) |
| Depende de | [F1-06](F1-06-cli-ajustes-previos-a-f2.md) (ajustes previos a F2) |
| Decisiones | Las de [`../DECISIONES.md`](../DECISIONES.md) que bloquean «F9 bloque B1» |
| Se para cuando | P1, P2, P3 y P9 dan `RC=0` contra el **viejo**, con 0 SKIP y 0 FAIL, y están corridos contra el nuevo (un rojo solo contra el nuevo es hallazgo); **y** el mutante `maxTxAttempts = 1` hace **caer** `TestP3_TxRetryOnSerializationFailure` |

## Antes de pegar el prompt (Jhoan)

- [ ] F1-06 está hecha y en `dev`: la marca de `Estado` reforzada y el comando de R9.4.d dentro de un gate.
- [ ] Las decisiones que bloquean «F9 bloque B1» en [`../DECISIONES.md`](../DECISIONES.md) están rellenas.
- [ ] Docker encendido en el Mac; `make toolchain` da `TOOLCHAIN=OK`.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F9-03 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md (es una sesión COMPLETA: escribes código)

Tu encargo (y solo este):
- Fase: F9 · procesos (testcontainers) → documentations/reorganizacion-modular/plan/F9-procesos/
- Bloque: B1 · procesos de plataforma y acceso
- Tareas: T9.13–T9.16 de plan/F9-procesos/tareas.md (P1, P2, P3, P9), contra el binario viejo; después se corren
  también contra el nuevo.
- Entrada: bloque A `CERRADO` y F1-06 hecha.
- 🔴 T9.15 (P3) incluye el reintento de postgres.WithTx con ejecución real (R9.6.e, paso 9 de diseno.md §4):
  provoca contra Postgres un conflicto de serialización o un deadlock y afirma que la operación termina bien.
  Después aplica el mutante `maxTxAttempts = 1` en una copia desechable y comprueba que el test CAE. Si el mutante
  sobrevive, T9.15 no se cierra: déjala [~], anota qué probaste y PARA.
- Las tablas de casos llevan casos adversarios (reglas.md §2): `@` repetidos, dígitos no ASCII, espacios Unicode.
- Te paras cuando: P1, P2, P3 y P9 dan RC=0 contra el viejo (0 SKIP, 0 FAIL), están corridos contra el nuevo, y
  el mutante `maxTxAttempts = 1` cae.
- Decisiones: las que en plan/DECISIONES.md bloquean «F9 bloque B1» deben estar rellenas. Si falta alguna, PARA y dilo.
- Skills: procesos-testcontainers, validar-antes-de-cerrar.

No aplica nivel de ceremonia E-12 (F9 no reconstruye un módulo). Sin umbral de cobertura: los procesos son parte
de lo que lo sustituye.
Un proceso por commit (`procesos(<proceso>): …`). Un rojo solo contra el nuevo es un hallazgo: no ajustes el test.
Si los cuatro procesos no caben en 45–90 min, para en un punto limpio tras un proceso cerrado y se relanza.
Orquesta con sub-agentes (uno por proceso) y protege tu contexto. Los worktrees de sub-agentes nacen de
origin/main: ponlos en el SHA de dev antes de medir.
Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase.
Push de TU rama (`git push origin <rama>`, rc sin pipe) y PR a `dev` (`gh pr create --base dev`, o el PR ya abierto de esa rama; «integrar SIN squash»): nunca directo a `dev` (regla 6 del `CLAUDE.md`). No toques `main`. No empieces la sesión siguiente.
```

## Al terminar debe existir

- Commits `procesos(<proceso>): …` (uno por proceso) y uno de documentación, en `dev`, empujados.
- En [`../F9-procesos/tareas.md`](../F9-procesos/tareas.md): T9.13–T9.16 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque en `ESTADO.md`: `RC`, `--- PASS` y `--- SKIP` por binario, y el test que cae con el mutante `maxTxAttempts = 1`.
- Los hallazgos nuevos en el [README de F9](../F9-procesos/README.md).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Un proceso rojo **solo contra el nuevo**: es un hallazgo (R9.4.c); se anota y **no** se ajusta el test.
- Una limitación del entorno (sin Docker, red, toolchain): se **anota** en `ESTADO.md`, no se adapta el proyecto a ella.
- Si el bloque no cabe en ~90 min: para en un punto limpio tras un proceso cerrado, cierra con las tres cosas y se relanza.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
