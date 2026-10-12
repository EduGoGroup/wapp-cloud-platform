# CLAUDE.md — `wapp-cloud-platform`

> **Portal. La verdad vive en [`documentations/`](documentations/README.md).** Este fichero
> solo apunta: no repitas aquí lo de allí, porque se desincroniza.

El **monolito modular en Go** donde la nube de wApp *piensa*: IAM/RBAC multi-empresa, la API
pública `/api/v1`, el Motor de Flujos con sus cuatro módulos (menú · encuesta · carrito ·
media), el gateway gRPC que termina el túnel de cada Edge, y el pipeline LLM **P2→P5** que
convierte una conversación de WhatsApp en un presupuesto. El **Edge** —el equipo del cliente—
despacha WhatsApp 24/7 y custodia sus llaves; **esta pieza arma el payload, gobierna los leases
y guarda el dato de negocio**. Un proceso (`cmd/server`) y **cuatro listeners**: `:8100` HTTP
admin/health · `:8103` HTTP API pública · `:8101` gRPC CloudLink bidi con mTLS estricto ·
`:8102` gRPC enrolamiento. Go **1.26.5**, PostgreSQL con `pgx` y SQL crudo, sin ORM, **sin
frontend**. Tamaño, medido el 2026-10-11 (`.go` de `cmd`, `internal` y `test`, sin `testdata/`):
**139.645 líneas de producción** en 608 ficheros —130 de ellos son suites de contrato, los paquetes
`…helpertest`— más 215.421 de test en 899.

## Las seis reglas innegociables

1. **Zero-knowledge y doble llave.** La nube nunca accede a credenciales ni llaves privadas: la
   **DEK** que descifra el almacén de `whatsmeow` la custodia el cliente y **jamás cruza el
   contrato**; el **Lease** lo emite y revoca este repo, y es el kill-switch anti-clon. Protege
   **llaves**, no el contenido de negocio, que sí sube a la nube a propósito. 🔴 **Homónimo**: la
   `DEK` del código de este repo es la del **envelope de PII de negocio**, otra cosa.
2. **Sin Redis ni broker**, ni aquí ni en el Edge: la concurrencia se resuelve con goroutines y
   canales de Go; la durabilidad, con tablas (`webhook_outbox`, `intake_jobs`).
3. **Copia-adaptación, nunca dependencia.** Prohibido importar un repo `edugo-*` (verificado: cero
   en `go.mod`); **única excepción**: `identity-shared/auth`, el SDK del SSO del grupo. El código
   compartido interno vive en **`wapp-shared`**, con releases por módulo (`<modulo>/vX.Y.Z`).
4. **El texto de los prompts P2–P5 se ajusta POR FICHERO y SIN release**:
   `go run ./cmd/prompts -volcar <dir>` → editas → `WAPP_LLM_PROMPTS_DIR` → **reinicias** (no hay
   recarga en caliente, a propósito). 🔴 En el esquema **no puede haber un valor que su propio
   validador rechace** —el modelo copia el ejemplo; P4 fue 0 de 14 en campo por un
   `"package_size": 0`— y por eso una plantilla inválida **aborta el arranque**. **P1 no vive
   aquí**: lo gobierna el catálogo de intenciones, que se edita por API.
5. **La inferencia la orquesta ESTE repo; el Edge solo la sirve** — lo contrario de lo que decía
   el diseño original. El Cloud construye el prompt y valida la salida; el Edge es *prompt entra
   → JSON sale* y **no interpreta nada**.
6. 🔴 **Ningún código nace en `dev` ni en `main`.** Todo código se escribe en una **rama partida de
   `dev`** y llega a `dev` por **PR** (sin squash). Commitear o hacer `cherry-pick` directamente sobre
   `dev` o `main` solo si Jhoan lo pide **expresamente** en la conversación: que una ficha o un prompt
   de sesión diga «push a `dev`» **no** cuenta. Vale también para los sub-agentes en *worktrees*: sus
   commits se integran en la rama. El PR lo integra Jhoan en GitHub: ninguna sesión fusiona en `dev` en
   local. 🔴 **La documentación no da por fusionado un PR abierto** (Jhoan, 2026-10-11; sustituye a la del 2026-10-10): mientras el PR está en revisión dice «PR #N abierto, en revisión», y **solo justo antes de fusionar** un último commit de la rama la pasa a «PR #N integrado», «fase cerrada» y «siguiente sesión». Sigue sin corregirse nada en `dev` después. Skill `pre-merge-docs`. (Regla de Jhoan, 2026-10-03, tras F1-06, que aterrizó en `dev`.)

## Antes de tocar nada

- **Para saber qué existe se lee `internal/arranque/orquestador.go`**, no el `README.md`: la lista
  `fases` (nueve, en una pantalla) y el fichero de cada una (`fase1_infraestructura.go` …
  `fase9_fondo.go`) son el inventario real.
- **Un PR aquí no valida nada** (`ci.yml` es `workflow_dispatch`): el gate es `make ci-local`, que
  corre **sin Postgres**. Lo que pide base de datos son los procesos de `test/procesos/`
  (`make test-procesos`, con testcontainers: necesita Docker), y van aparte. Un `rc=0` cuenta un
  `--- SKIP` igual que un `--- PASS`: **cuenta los SKIP**; aquí deben ser cero.
- **Trabaja dentro del módulo** de `internal/modulos/` que corresponda (`acceso`, `captacion`,
  `catalogo`, `conversacion`, `edge`, `inferencia`, `solicitudes`): modular por capacidad, no
  hexagonal global (la única zona hexagonal es `internal/modulos/acceso/iam/`). Las fronteras entre
  módulos las vigila `internal/modulos/fronteras_test.go`. 🔒 Y no toques
  `documentations/literal-aviso-sesion-pasiva.md`, que es un contrato congelado.
- 🔴 **Nunca un test contra un Postgres vivo**: la única base válida es la del contenedor de la
  corrida (`test/procesos/sin_bd_viva_test.go` lo hace fallar en el gate).

## ✅ Reorganización modular hecha (2026-09-27 → 2026-10-11)

`internal/` se **reconstruyó** en `internal/modulos/<módulo>/` y el código anterior se **borró**: hay
un solo arranque (`cmd/server` sobre `internal/arranque`) y una sola cara HTTP pública
(`internal/apipublica`). El relevo corre en UAT desde el 2026-10-11. No queda nada «viejo» en el
árbol: si un documento o un comentario nombra una ruta que no existe, el que está mal es él.

Lo que la reconstrucción dejó como **norma del código**:

- 🔤 **Idioma**: los nombres nuevos (ficheros, tipos, funciones, variables, tests) van en **inglés**;
  los comentarios y la documentación, en español. Lo ya escrito no se renombra; los textos
  observables no se tocan.
- 📏 **Tamaño**: ≤ 500 líneas por `.go`, tolerancia hasta 600; por encima se parte por tema, solo
  moviendo declaraciones, con el sufijo del origen. Lo vigila `internal/modulos/file_size_test.go`.
- 🔒 **Candados**: `internal/candados` y los tests de `internal/modulos/` (fronteras, un fichero un
  test, exportados cubiertos, tamaño, cero pendientes) corren dentro de `make ci-local`.
- 📊 **La cobertura no bloquea**: `make cobertura-ficheros` es un informe (rc=0 con ficheros por debajo).
- **Las skills del repo**: `validar-antes-de-cerrar` (los gates) · `procesos-testcontainers` (los
  tests contra Postgres) · `pre-merge-docs` (qué dice la documentación mientras el PR está abierto y
  qué al fusionarlo) · `describir-pr` (el cuerpo de **todo** PR: objetivo, dónde encaja, resultado).
- **Las normas en detalle** (qué va en inglés, cómo se parte un fichero, las excepciones de «un
  fichero un test», las listas cerradas y cómo se añade un módulo):
  [`constitucion.md`](documentations/constitucion.md) §5.1 y §5.2.
- **El expediente** de la reconstrucción (método, decisiones, fases y sesiones) **ya no está en este
  repo**: se archivó en la documentación de wApp. Qué fue y dónde está:
  [`modular-reorganization.md`](documentations/modular-reorganization.md). 🔴 Describe código que ya no
  existe: **no es fuente para saber qué hay hoy**.

## Índice de `documentations/`

| Fichero | Qué contesta |
|---|---|
| [`README.md`](documentations/README.md) | Portal de la pieza |
| [`constitucion.md`](documentations/constitucion.md) | **Empieza aquí.** Invariantes, homónimos, tecnología, convenciones, 12 trampas |
| [`arquitectura.md`](documentations/arquitectura.md) | Módulos, los seis `main` de `cmd/` y **dónde se rompen las fronteras** |
| [`contratos.md`](documentations/contratos.md) | Las 95 rutas HTTP, 2 rpc, los 6 CLI y las 71 variables de entorno |
| [`esquema-postgres.md`](documentations/esquema-postgres.md) | Las 45 tablas, el esquema **0.48.0** y el runner full-replay |
| [`operacion.md`](documentations/operacion.md) | Arranque local, `make` targets, release y depuración |
| [`deuda.md`](documentations/deuda.md) | Deuda viva con `fichero:línea` y el código muerto verificado |
| [`modular-reorganization.md`](documentations/modular-reorganization.md) | Ficha de la reconstrucción por módulos, ya hecha: qué fue y dónde está archivado su expediente |
