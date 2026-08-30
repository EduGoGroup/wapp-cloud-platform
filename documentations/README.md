# `wapp-cloud-platform` — portal de la pieza

**Qué es.** El **monolito modular en Go** donde vive todo lo que la nube de wApp *piensa*:
IAM/RBAC multi-empresa, la API pública `/api/v1`, el Motor de Flujos con sus cuatro módulos
(menú · encuesta · carrito · media), el gateway gRPC que termina el túnel de cada Edge, y el
pipeline LLM **P2→P5** que convierte una conversación de WhatsApp en un presupuesto.

**Para qué existe.** En wApp el Edge —el equipo del cliente— despacha WhatsApp y custodia sus
llaves; **esta pieza arma el payload, gobierna los leases (kill-switch) y guarda el dato de
negocio**. Es la pieza 03 (plataforma cloud) más la 05 (motor de flujos) del ecosistema.

**Un proceso, cuatro listeners.** `cmd/server` levanta en el mismo binario dos HTTP
(`:8100` admin/health, `:8103` API pública) y dos gRPC (`:8101` CloudLink bidi con mTLS
estricto, `:8102` enrolamiento con TLS solo de servidor). No hay frontend aquí: cero
plantillas Go, cero JS. La UI vive en otros repos.

---

## Tamaño real, dicho con su descomposición

Decir «244k líneas» engaña. La cuenta honesta, medida el 2026-08-30 con
`find . -name '*.go' | wc -l` y `… -exec cat {} + | wc -l`:

| | Ficheros | Líneas |
|---|---|---|
| **Producción** | 333 | **92.451** (de ellas ~43k, el **46,8 %**, son comentario) |
| **Tests** | 528 | 151.891 |
| **Total** | 861 | 244.342 |

La lógica de producción real ronda las **49k líneas**. El estilo del repo es
*comentario-como-ADR*: los ficheros explican qué se rechazó, cuándo y por qué. Léelos: casi
siempre la respuesta a «¿por qué está así?» está tres líneas más arriba.

---

## Índice

| Documento | Qué contesta |
|---|---|
| [`constitucion.md`](constitucion.md) | **Empieza aquí.** Los invariantes que no se pueden violar (los del ecosistema que aplican aquí, repetidos, y los propios), la tecnología con sus versiones reales, las convenciones y las **trampas conocidas** de esta pieza. |
| [`arquitectura.md`](arquitectura.md) | Cómo está hecha por dentro **por dominios** (IAM, gateway, flujos, intakes/pipeline LLM, catálogo, telemetría, entitlements, CRM, publicapi, casebank, degradación, reanálisis), dónde están las fronteras y **dónde se rompen**, con diagramas. |
| [`contratos.md`](contratos.md) | Todo lo que otros consumen: las **95 rutas HTTP** agrupadas por dominio, los 2 rpc, los 4 binarios CLI, las variables de entorno con su default, y los ficheros que escribe. Dice de dónde salió cada lista. |
| [`esquema-postgres.md`](esquema-postgres.md) | Las **47 tablas** que toca, cómo se versiona el esquema (hoy **0.48.0**), el runner full-replay y los vocabularios cerrados. |
| [`operacion.md`](operacion.md) | Cómo se arranca en local, cómo se prueba (los `make` reales y qué valida cada uno), cómo se publica una versión y cómo se depura cuando falla. |
| [`deuda.md`](deuda.md) | La deuda viva con `fichero:línea`, su consecuencia y cómo se cerraría. Incluye el código muerto verificado. |
| [`literal-aviso-sesion-pasiva.md`](literal-aviso-sesion-pasiva.md) | 🔒 **Contrato congelado, no lo edites de paso.** El literal exacto del aviso de sesión pasiva. |

---

## Las cinco cosas que ahorran una tarde

1. **Para saber qué existe, se lee `internal/bootstrap/bootstrap.go`, no el README.** Las
   991 líneas de `Run` (`internal/bootstrap/bootstrap.go:106`) son el inventario real.
2. **Las rutas están en TRES sitios**, no en uno: `internal/publicapi/`,
   `internal/bootstrap/http.go` (las que un token **sin empresa** puede atravesar) y
   `internal/bootstrap/bootstrap.go` (el listener admin).
3. **P1 no vive aquí.** El prompt del clasificador de intenciones lo gobierna el catálogo de
   intenciones, que se edita por API (`PUT /api/v1/intents`). Aquí solo viven P2–P5.
4. **La `DEK` de este repo NO es la DEK del ecosistema.** Ver la sección de homónimos de
   `constitucion.md` antes de concluir que el zero-knowledge está roto.
5. **Un PR aquí no valida nada**: `.github/workflows/ci.yml` es `workflow_dispatch`. El gate
   real es `make ci-local` en tu máquina. Ver [`operacion.md`](operacion.md).
