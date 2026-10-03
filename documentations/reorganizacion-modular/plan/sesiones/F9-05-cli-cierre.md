# F9-05 · F9 · D · cierre · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F9 · procesos (testcontainers)](../F9-procesos/README.md) · D · cierre |
| Entorno | 💻 solo local: testcontainers y los dos binarios |
| Nivel (E-12) | No aplica: F9 no reconstruye un módulo y no lleva inventario E-12. Son tests de proceso (caja negra) |
| Duración objetivo | 45–90 min |
| Tareas | T9.30–T9.33 |
| Depende de | F8-07 (F8 conmutada y cerrada en local; T9.29 `[x]`) |
| Decisiones | Las de [`../DECISIONES.md`](../DECISIONES.md) que bloquean «F9 bloque D» |
| Se para cuando | `CUENTA=3 make test-procesos` da `RC=0` contra viejo y nuevo, 0 SKIP, 0 FAIL, sin intermitencias; las 22 filas de `diseno.md` §5 tienen su `--- PASS`; el recuento de T9.31 está limpio; la condición del relevo queda escrita en `ESTADO.md` |

## Antes de pegar el prompt (Jhoan)

- [ ] F8 está conmutada y cerrada en `dev` (F8-07); T9.22–T9.29 `[x]`; la lista de puentes (imports) de `internal/modulos/fronteras_test.go` está vacía.
- [ ] Las decisiones que bloquean «F9 bloque D» en [`../DECISIONES.md`](../DECISIONES.md) están rellenas.
- [ ] Docker encendido en el Mac; `make toolchain` da `TOOLCHAIN=OK`.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F9-05 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F9 · procesos (testcontainers) → documentations/reorganizacion-modular/plan/F9-procesos/
- Bloque: D · cierre
- Tareas: T9.30–T9.33 de plan/F9-procesos/tareas.md
- Entrada: F8 conmutada, T9.29 [x], puentes (imports) = 0.
- T9.30: `CUENTA=3 make test-procesos` contra los dos binarios, sin intermitencias; lee las notas de T9.30 sobre
  la carrera de `sin_errores` (contradicción 19, D-F9-10) antes de interpretar un rojo o un verde.
- T9.31: recuento contra el código. El comando de R9.4.d se verifica contra el gate que crea F1-06 (sigue en él y
  el gate lo corre) y, a mano, lo que el comando no ve.
- T9.32–T9.33: documentación y la condición del relevo escrita.
- Te paras cuando: la suite entera da RC=0 ×3 contra viejo y nuevo con 0 SKIP y 0 FAIL, las 22 suites de
  diseno.md §5 tienen su PASS, el recuento está limpio y la condición del relevo está en ESTADO.md.
- Decisiones: las que en plan/DECISIONES.md bloquean «F9 bloque D» deben estar rellenas. Si falta alguna, PARA y dilo.
- Skills: validar-antes-de-cerrar, procesos-testcontainers.

No aplica nivel de ceremonia E-12. Sin umbral de cobertura: los procesos son parte de lo que lo sustituye.
Si no cabe en ~90 min, para en un punto limpio y se relanza.
Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase.
`git push origin dev` (rc sin pipe). No toques `main`. No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F9-procesos/tareas.md`](../F9-procesos/tareas.md): T9.30–T9.33 `[x]` con SHA.
- Un bloque en `ESTADO.md` con los dos logs (`RC`, PASS, SKIP por binario, duración) y `F9 CERRADA <fecha>`: es la condición del relevo que lee F10.
- El [README de F9](../F9-procesos/README.md) con el estado y los números medidos, y los hallazgos nuevos.
- `dev` empujado.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Un proceso rojo **solo contra el nuevo**: es un hallazgo (R9.4.c); se anota y **no** se ajusta el test.
- Una limitación del entorno (sin Docker, red, toolchain): se **anota** en `ESTADO.md`, no se adapta el proyecto a ella.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
