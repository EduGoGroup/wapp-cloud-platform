# F8-08 · F8 · barrida tras el cierre · 💻 CLI

> Sesión **no prevista**: nació el 2026-10-11 del repaso, punto por punto, de los pendientes del cierre de F8 con Jhoan.
> No tiene prompt: se hizo en la misma conversación que cerró F8. Queda aquí como ficha de lo que cubrió.

| | |
|---|---|
| Fase · bloque | [F8 · conversacion](../F8-conversacion/README.md) · 8 · barrida tras el cierre |
| Entorno | 💻 local; Docker (procesos) |
| Tareas | T8.44–T8.46 |
| Rama · PR | `reorg/f8-08-coverage-sweep-and-mutants` · PR #73 (integrado en `dev`, merge `abbfb926`) |
| Depende de | F8-07 (PR #72) |

## Qué cubrió

1. **Los mutantes que el cierre dejó sin repetir** (`admin`, cara HTTP): hallazgo 72 del README de F8.
2. **La barrida de las 43 guardas de «identidad sin tenant»**, que salió del único superviviente: hallazgo 73.
3. **La cobertura combinada** (`make cobertura-combinada`): unitarios + suites contra Postgres + flujos: hallazgo 74.
4. **Las dos promesas sin test de `events_summary.go`**: hallazgo 75.
5. **La regla de documentación antes de fusionar** (skill `pre-merge-docs`), pedida por Jhoan en la sesión.

## Lo que dejó para otras sesiones

- **T9.36** (F9-05): clasificar los bloques que siguen sin cubrir.
- Las decisiones del repaso (fase FW, D-36, D-39, D-F10-8, D-F10-9) viajaron en el PR #72.
