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
estricto, `:8102` enrolamiento con TLS solo de servidor). No hay frontend aquí: la UI vive en
otros repos.

**Cómo está organizado.** `internal/` tiene seis directorios: `internal/modulos/` (los siete
módulos de negocio: `acceso`, `edge`, `conversacion`, `catalogo`, `captacion`, `inferencia`,
`solicitudes`), `internal/nucleo/` y `internal/platform/` (lo compartido), `internal/apipublica/`
(la cara HTTP pública), `internal/arranque/` (el cableado, en nueve fases) e
`internal/candados/` (los candados que vigilan las fronteras). El detalle, en
[`arquitectura.md`](arquitectura.md).

---

## Tamaño real, dicho con su descomposición

Una sola cifra de líneas engaña. La cuenta, medida el **2026-10-11**:

```bash
# Regla: ficheros .go del repo, sin testdata/ ni .claude/. «Producción» = no acaba en _test.go
# y no está en un paquete *helpertest; «soporte de tests» = los paquetes *helpertest.
find . -name '*.go' -not -path '*/testdata/*' -not -path './.claude/*' \
  -not -name '*_test.go' -not -path '*helpertest/*' | wc -l          # ficheros
find . -name '*.go' -not -path '*/testdata/*' -not -path './.claude/*' \
  -not -name '*_test.go' -not -path '*helpertest/*' -exec cat {} + | wc -l   # líneas
```

| | Ficheros | Líneas |
|---|---|---|
| **Producción** | 478 | **111.124** |
| **Soporte de tests** (paquetes `…helpertest`: suites de contrato y dobles) | 130 | 28.521 |
| **Tests** (`*_test.go`) | 899 | 215.421 |
| **Total** | 1.507 | 355.066 |

Las líneas incluyen comentarios y blancos, y el estilo del repo es *comentario-como-ADR*: los
ficheros explican qué se rechazó, cuándo y por qué. Léelos: casi siempre la respuesta a «¿por
qué está así?» está tres líneas más arriba.

---

## Índice

| Documento | Qué contesta |
|---|---|
| [`constitucion.md`](constitucion.md) | **Empieza aquí.** Los invariantes que no se pueden violar (los del ecosistema que aplican aquí, repetidos, y los propios), la tecnología con sus versiones reales, las convenciones y las **trampas conocidas** de esta pieza. |
| [`arquitectura.md`](arquitectura.md) | Cómo está hecha por dentro: los siete módulos y qué hace cada uno, el núcleo, `platform`, la cara, el arranque en nueve fases, los binarios de `cmd/`, **dónde se rompen las fronteras** hoy (imports y tablas, medido) y los candados que las vigilan. |
| [`contratos.md`](contratos.md) | Todo lo que otros consumen: las rutas HTTP por listener, los rpc, los comandos CLI, las variables de entorno con su default, y los ficheros que escribe. Dice de dónde salió cada lista. |
| [`esquema-postgres.md`](esquema-postgres.md) | Las tablas que toca, cómo se versiona el esquema, el runner full-replay y los vocabularios cerrados. |
| [`operacion.md`](operacion.md) | Cómo se arranca en local, cómo se prueba (los `make` reales y qué valida cada uno), cómo se publica una versión y cómo se depura cuando falla. |
| [`deuda.md`](deuda.md) | La deuda viva con `fichero:línea`, su consecuencia y cómo se cerraría. Incluye el código muerto verificado. |
| [`literal-aviso-sesion-pasiva.md`](literal-aviso-sesion-pasiva.md) | 🔒 **Contrato congelado, no lo edites de paso.** El literal exacto del aviso de sesión pasiva. |
| [`reorganizacion-modular/`](reorganizacion-modular/README.md) | **Hecha: el relevo se cerró el 2026-10-11.** El expediente de la reconstrucción de `internal/` por módulos: el método normativo ([`05`](reorganizacion-modular/05-metodo-contratos-y-tdd.md), que sigue mandando sobre cómo se escribe código nuevo), las decisiones ([`plan/DECISIONES.md`](reorganizacion-modular/plan/DECISIONES.md)) y el estado final ([`ESTADO.md`](reorganizacion-modular/ESTADO.md)). Para saber cómo es el código hoy se lee `arquitectura.md`, no esa carpeta. |

---

## Las cinco cosas que ahorran una tarde

1. **Para saber qué existe, se lee `internal/arranque/orquestador.go`, no un README.** Su lista
   `fases` —las nueve, en una pantalla— y el fichero de cada una son el inventario real.
2. **Las rutas están en DOS sitios**, uno por listener HTTP: las del `:8103` las monta
   `internal/arranque/mudanzas.go` llamando a los montadores de `internal/apipublica/`, y las
   del `:8100` las cablea una sola función en `internal/arranque/rutas_admin.go`. La lista, en
   [`contratos.md`](contratos.md).
3. **P1 no vive aquí.** El prompt del clasificador de intenciones lo gobierna el catálogo de
   intenciones, que se edita por API (`PUT /api/v1/intents`). Aquí solo viven P2–P5.
4. **La `DEK` de este repo NO es la DEK del ecosistema.** Ver la sección de homónimos de
   `constitucion.md` antes de concluir que el zero-knowledge está roto.
5. **Un PR aquí no valida nada**: `.github/workflows/ci.yml` es `workflow_dispatch`. El gate
   real es `make ci-local` en tu máquina, más `make test-procesos` para lo que toca Postgres.
   Ver [`operacion.md`](operacion.md).

---

## Cómo se comprueba que esta documentación dice lo que hay

```bash
scripts/check-docs-tree.sh; echo "rc=$?"      # FALTAN=0 y rc=0
```

Recorre `CLAUDE.md`, los dos `README.md`, los documentos de esta carpeta y las skills del repo, y falla
si algo de lo que nombran entre comillas invertidas no existe en el árbol. Cinco reglas: una **ruta**
del repo (`ls -d`, admite comodines); una cita **`fichero:N`** (el fichero existe y tiene al menos N
líneas); un **símbolo** (un token con la forma paquete.Símbolo, Símbolo() o Test… aparece como palabra en algún
`.go`); una **variable** `WAPP_X` (`X` o `WAPP_X` aparece en el código, el `Makefile`, los scripts o
`deploy/`, porque el prefijo lo compone el loader); y **ninguna ruta de un paquete que ya no existe**.

No comprueba que la línea citada diga lo que se afirma —eso se mira a mano al escribir la cita— ni
entra en `reorganizacion-modular/`. Córrelo antes de dar por bueno un cambio de documentación.
