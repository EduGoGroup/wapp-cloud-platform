# Estado de la reorganización modular — punto de retoma

> **Última actualización: 2026-09-27**, al cerrar la sesión de análisis. Este fichero es para
> **retomar**: qué se hizo, qué está decidido, qué falta decidir y cuál es el siguiente paso. El
> detalle vive en los documentos 01–06; aquí solo el mapa.

## Dónde estamos

**Fase: análisis cerrado · plan de trabajo por escribir.** No hay ni una línea de código de la
reconstrucción. Lo que existe es la norma (`05`), el árbol destino (`04`), las skills del repo y el
entorno web documentado (`06`).

**Siguiente paso: escribir el plan de trabajo ejecutable** — convertir las fases de `05` §6 en olas
con tareas, criterios de cierre y prompts para Claude Code en la web, empezando por **F0
(andamiaje)** y **F1 (piloto `nucleo/contact`, con parada)**. Antes conviene cerrar las decisiones
abiertas que afectan a F0 (abajo).

## Qué se hizo el 2026-09-27

| # | Qué | Dónde |
|---|---|---|
| 1 | Análisis de factibilidad: grafo de dependencias medido, paquetes mal ubicados (catálogo dentro del carrito, contacto dentro de flujos, `platform` dependiendo de dominios), dos ciclos entre módulos, contratos externos que no se tocan | `01`, `02`, `03` |
| 2 | La rama local `refactor/arranque-por-fases` (las nueve fases del arranque) y el trabajo sin commitear se llevaron a `dev`, con gates: `ci-local` rc=0 y **integración con Postgres real: 4.318 PASS · 0 SKIP · 0 FAIL**. `main` se alineó con `dev` en `2da10b4` | git |
| 3 | `intake` (la cola que produce el borrador) frente a `intakes` (la solicitud y su vida) | `02` §4.1 |
| 4 | El árbol final, fichero a fichero, generado por script de la lista real: 7 módulos (`acceso`, `edge`, `conversacion`, `catalogo`, `captacion`, `inferencia`, `solicitudes`) + `arranque`, `nucleo`, `platform`, `publicapi` | `04` |
| 5 | 🔒 El **método**: reconstruir por contratos y TDD, con arranque paralelo | `05` |
| 6 | Cinco skills en `.claude/skills/`, el script de setup del entorno web y `CLAUDE.md` apuntando a la reconstrucción | `06`, `.claude/skills/` |

## 🔒 Decisiones de Jhoan (cerradas)

1. **No es un movimiento mecánico: es una reconstrucción.** Cada fichero nuevo nace con su
   **contrato sin lógica** (`panic(pendiente.Implementar(...))`) y un **`x_test.go` escrito desde
   ese contrato**, en rojo tras `//go:build pendiente`; después, la lógica portada del viejo. El
   código viejo no se toca.
2. **Arranque paralelo**: `cmd/server` (viejo, el oráculo, lo que corre en UAT) y
   `cmd/server-modular` → `internal/arranque` (nuevo), para ir de a poco.
3. **Los tests viejos no se portan**, se consultan. Los ficheros nacen cubiertos desde el contrato.
4. **La integración no se porta**: se escribe **de cero, por proceso**, en caja negra, contra los
   dos binarios (F9), y es condición del relevo (F10).
5. **Los tests de proceso usan testcontainers**: una instancia de Postgres compartida por corrida y
   una base clonada por proceso. **Nunca un Postgres vivo.** Los corre Claude Code en local.
6. **Toda la documentación de este trabajo se commitea y pushea a `dev`.** `main` solo se mueve a
   petición de Jhoan.
7. **Quien implementa es Claude Code en la web**, que solo ve este repo. Por eso todo vive aquí.

## Decisiones abiertas (de `03` §3)

Los documentos asumen la recomendación de cada una; ninguna está confirmada.

| | Pregunta | Recomendación | ¿Bloquea F0/F1? |
|---|---|---|---|
| D-1 | Alcance | Mover + corregir ubicaciones | No |
| D-2 | Nivel `internal/modulos/` | Sí | **Sí** (F0 crea la estructura) |
| D-3 | Aplanar subcarpetas | Sí | No (F2+) |
| D-4 | Renombrar paquetes | No, salvo `indice`; textos observables intactos | No |
| D-5 | Módulos y bordes (¿`acceso`+`operador` fusionados? ¿dónde van P5, `reanalisis`, `tenantvars`…?) | La candidata de `02` §4 con `acceso` fusionado | No (F2+) |
| D-7 | Ciclo de negocio | Congelarlo con puentes | No |
| D-9 | Nombre del `cmd` temporal | `cmd/server-modular` | **Sí** (F0) |
| D-10 | `publicapi` | Repartirlo en `modulos/<m>/http/` | No para F1; **sí antes de F2** |
| D-11 | Etiquetas | `pendiente` e `integracion`, sin `t.Skip` | **Sí** (F0) |
| D-12 | Umbral de cobertura por fichero | 80 %, recalibrar tras F1 | **Sí** (F0 crea el candado) |
| D-13 | Lista de procesos de F9 | La de `05` §7.4 | No (F9) |

(D-6 y D-8 quedaron superadas: D-6 por D-10; D-8, el plan vive en este repo.)

## Pendientes y obstáculos conocidos

- 🔴 **El servidor completo exige R2**: `internal/bootstrap/arranque/flows.go:75` construye siempre
  el cliente de presign y hace `HeadBucket` (virtual-hosted); si falla, no arranca. Los tests de
  proceso de F9 necesitan un doble de S3. Puede requerir una opción **solo en el arranque nuevo**:
  decisión de Jhoan cuando llegue F9.
- **12 de 20** paquetes con adaptador Postgres no tienen gemelo en memoria: se crea un doble por
  cada uno en la pasada de contrato (`05` E-6).
- **Sin verificar**: que la red del entorno web deje instalar el lint y la toolchain (`06` §3), y
  que los hooks de `.claude/settings.json` corran en la web (por eso no se usan).
- `make test-integration` usa `postgres:16`; UAT corre `postgres:17-alpine`.
- Deriva documental menor sin corregir (`03` §2.2): rutas de `README.md` y de `constitucion.md`
  que aún dicen `internal/bootstrap/…` en vez de `internal/bootstrap/arranque/…`.
- Fuera de este repo (lo hará una sesión local al final): 2.565 menciones de rutas `internal/` en la
  documentación del ecosistema, la regla de conteo de ADR-0010 y 5 comentarios en repos hermanos.

## Estado de git al cerrar

- `origin/dev` = `9faca6e` · `origin/main` = `2da10b4` (`dev` va 6 commits por delante, todos de
  documentación y skills).
- La rama local `refactor/arranque-por-fases` sigue existiendo en la máquina de Jhoan, ya fusionada.

## Para retomar

1. Lee [`README.md`](README.md) y [`05-metodo-contratos-y-tdd.md`](05-metodo-contratos-y-tdd.md)
   (la norma). El árbol, en [`04`](04-estructura-final.md) §3.
2. Cierra con Jhoan las decisiones marcadas «Sí» arriba.
3. Escribe el plan de trabajo (F0 y F1 primero) en esta carpeta.
4. Skills disponibles: `reconstruir-modulo`, `contrato-tdd`, `validar-antes-de-cerrar`,
   `traspaso-web-local`, `procesos-testcontainers`.
