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

- **Para saber qué existe se lee `internal/bootstrap/bootstrap.go`**, no el `README.md`: las 991
  líneas de `Run` (`:106`) son el inventario real. El `README.md` tiene afirmaciones caducadas,
  listadas en `documentations/deuda.md` §6.
- **Un PR aquí no valida nada** (`ci.yml` es `workflow_dispatch`): el gate es `make ci-local`.
  Y un `rc=0` cuenta un `--- SKIP` igual que un `--- PASS`: **cuenta los SKIP**, porque los 97
  ficheros de integración se saltan solos sin `WAPP_TEST_DB_DSN`.
- **Trabaja dentro del módulo** de `internal/` que corresponda: modular por capacidad, no
  hexagonal global (la única zona hexagonal es `internal/iam/`). 🔒 Y no toques
  `documentations/literal-aviso-sesion-pasiva.md`, que es un contrato congelado.
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
