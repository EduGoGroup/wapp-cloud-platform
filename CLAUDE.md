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
frontend**. Tamaño con su descomposición («244k líneas» engaña): **92.451 líneas de producción**
en 333 ficheros —46,8 % comentario— más 151.891 de test en 528.

## Las cinco reglas innegociables

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

## Antes de tocar nada

- **Para saber qué existe se lee `internal/bootstrap/arranque/orquestador.go`**, no el
  `README.md`: la lista `fases` (nueve, en una pantalla) y el fichero de cada una son el
  inventario real. Hasta el 2026-09-04 eran las 991 líneas de `bootstrap.Run`, hoy una fachada.
  El `README.md` tiene afirmaciones caducadas, listadas en `documentations/deuda.md` §6.
- **Un PR aquí no valida nada** (`ci.yml` es `workflow_dispatch`): el gate es `make ci-local`.
  Y un `rc=0` cuenta un `--- SKIP` igual que un `--- PASS`: **cuenta los SKIP**, porque los 97
  ficheros de integración se saltan solos sin `WAPP_TEST_DB_DSN`.
- **Trabaja dentro del módulo** de `internal/` que corresponda: modular por capacidad, no
  hexagonal global (la única zona hexagonal es `internal/iam/`). 🔒 Y no toques
  `documentations/literal-aviso-sesion-pasiva.md`, que es un contrato congelado.
## 🔧 Reorganización modular en curso (desde el 2026-09-27)

`internal/` se está **reconstruyendo** en `internal/modulos/<módulo>/`, **no moviendo**: cada
fichero nuevo nace con su **contrato sin lógica** y un **test que lo cubre**, en rojo, antes que la
lógica. El código viejo **no se toca**: es la referencia y lo que corre en UAT. Conviven dos
arranques (`cmd/server` viejo, `cmd/server-modular` nuevo), y los tests de integración se
reescriben **por proceso con testcontainers**, nunca contra un Postgres vivo.

- **La norma**: [`documentations/reorganizacion-modular/05-metodo-contratos-y-tdd.md`](documentations/reorganizacion-modular/05-metodo-contratos-y-tdd.md). Manda sobre los documentos 01–04 de esa carpeta.
- **El plan ejecutable**: [`documentations/reorganizacion-modular/plan/`](documentations/reorganizacion-modular/plan/README.md) — una *spec* por fase, [`DECISIONES.md`](documentations/reorganizacion-modular/plan/DECISIONES.md) y [`sesiones/`](documentations/reorganizacion-modular/plan/sesiones/README.md). **Si te arrancaron con el prompt de una sesión, su protocolo (`plan/sesiones/PROTOCOLO-WEB.md` o `PROTOCOLO-CLI.md`) te dice qué leer; no leas el plan entero.**
- 🔤 **Idioma (E-11)**: en lo **nuevo**, los nombres (ficheros, tipos, funciones, variables, tests) van en
  **inglés** y solo los comentarios y la documentación en español. Lo ya escrito no se renombra; los textos
  observables se copian literales. Detalle en `05` E-11.
- 🔄 **D-10**: la cara HTTP es **única y nueva**, `internal/apipublica`, construida por olas delante del `publicapi` viejo ([`plan/FX-cara-http/`](documentations/reorganizacion-modular/plan/FX-cara-http/README.md)).
- **Las skills del repo**: `contrato-tdd` (un fichero) · `reconstruir-modulo` (una fase) ·
  `validar-antes-de-cerrar` (los gates) · `traspaso-web-local` (web ↔ local) ·
  `procesos-testcontainers` (F9).
- **F1 es un piloto con parada**: después de `nucleo/contact`, no se sigue sin decisión de Jhoan.

## Índice de `documentations/`

| Fichero | Qué contesta |
|---|---|
| [`README.md`](documentations/README.md) | Portal de la pieza |
| [`constitucion.md`](documentations/constitucion.md) | **Empieza aquí.** Invariantes, homónimos, tecnología, convenciones, 12 trampas |
| [`arquitectura.md`](documentations/arquitectura.md) | Dominios, los 4 binarios y **dónde se rompen las fronteras** |
| [`contratos.md`](documentations/contratos.md) | Las 95 rutas HTTP, 2 rpc, los CLI y las 70 variables de entorno |
| [`esquema-postgres.md`](documentations/esquema-postgres.md) | Las 47 tablas, el esquema **0.48.0** y el runner full-replay |
| [`operacion.md`](documentations/operacion.md) | Arranque local, `make` targets, release y depuración |
| [`deuda.md`](documentations/deuda.md) | Deuda viva con `fichero:línea` y el código muerto verificado |
| [`reorganizacion-modular/`](documentations/reorganizacion-modular/README.md) | La reconstrucción por módulos: análisis, árbol destino, **método normativo (`05`)** y entorno web |
