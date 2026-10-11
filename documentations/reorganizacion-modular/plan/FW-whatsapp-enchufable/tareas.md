# FW · Tareas

> Todas 💻. Las tareas marcadas **Edge** se hacen dentro de `edge/wapp-edge-agent`, con su gate y su flujo de ramas; las
> marcadas **Cloud**, en este repo (rama desde `dev`, PR sin squash). Cada sesión cierra con las tres cosas: tareas `[x]`
> con SHA (y el repo del SHA), bloque en `ESTADO.md`, hallazgos en el [README](README.md) de la fase.
> Gate de este repo: `GOWORK=off make ci-local > /tmp/g.log 2>&1; echo "GATE_RC=$?" >> /tmp/g.log; tail -1 /tmp/g.log`.
> Gate del Edge: el que diga su `documentations/operacion.md`, con el `rc` leído sin pipe.

## Bloque A · inventario y ADR · sesión FW-01 · TW.1–TW.3

Para cuando: [`diseno.md`](diseno.md) sin «sin medir» y el ADR en borrador. **Jhoan aprueba antes de FW-02.**

- [x] **TW.1 · Inventario del Edge** · Edge, solo lectura · cumple RW.1.c · `f9c7c309` (este repo; en el Edge, ni un commit: medido a `38e5f91`)
  - **Ficheros**: [`diseno.md`](diseno.md) §2 (las seis preguntas, contestadas con `fichero:línea`)
  - **Hecho cuando**: cada pregunta tiene respuesta medida o un «no se pudo medir, porque…»; la fuga de `whatsmeow`
    fuera del adaptador, listada y partida en «estorba al falso» / «no estorba» (D-FW-4)
- [x] **TW.2 · El ADR** · raíz de wApp (`documentations/adrs/`) · D-FW-1, D-FW-2, D-FW-6 · borrador escrito el 2026-10-11:
  `0049-el-transporte-de-whatsapp-del-edge-es-enchufable.md`, **sin commitear** (la raíz no tiene remoto ni `dev`: hallazgo 12);
  `make check-docs` rc=0 con el gate de diagramas saltado. D-FW-1 decidida el 2026-10-11 (ADR nuevo). ✎ FW-02 lo actualizó con D-FW-2, 4, 6, 7 y 8 (once piezas, y la sección «Pensado para más de dos transportes»). ✅ **Texto aprobado por Jhoan el 2026-10-11** (tras FW-02): el ADR-0049 está vigente y dado de alta en el índice de ADR de la raíz. ✎ Commiteado en `main` de la raíz el 2026-10-11, tras FW-03, por orden de Jhoan: `1abd9fe` (raíz de wApp). `make check-docs` de la raíz, con el índice: rc=0, diagramas saltados
  - **Hecho cuando**: borrador con la decisión, su tabla de alternativas descartadas y la relación con el ADR-0041 y el
    ADR-0007; `make check-docs` de la raíz verde; **aprobado por Jhoan**
- [x] **TW.3 · El diseño, con lo medido** · Cloud (documentación) · dep. TW.1 · `f9c7c309` (este repo) · ✅ decidido por Jhoan el 2026-10-11, al arrancar FW-02 ([`diseno.md`](diseno.md) §3 y §5)
  - **Ficheros**: [`diseno.md`](diseno.md) §3 y §4, [`requisitos.md`](requisitos.md) (los `<…>` sustituidos), [`arquitectura.md`](arquitectura.md)
  - **Hecho cuando**: palanca, paquete, entrada, salida y acuses tienen nombre; cada requisito nombra su test
  - **Commit**: `docs(reorganizacion-modular): FW, inventario del Edge y diseño`

## Bloque B · el adaptador falso · sesión FW-02 · TW.4–TW.6 · Edge

Entrada: TW.3 aprobado; D-FW-1 y D-FW-2 decididas. Para cuando: definición de hecho 1–4 ([`reglas.md`](reglas.md) §4).
✎ FW-02: el gate del Edge que vale es **`make ci-docker`**; `make ci-local` falla en la Mac por el lint del sistema, sin este cambio (hallazgo 14).

- [x] **TW.4 · La frontera** · Edge · dep. TW.3 · cumple RW.1.b, RW.1.c · `94269e0` (`edge/wapp-edge-agent`, rama `feat/fw-02-whatsapp-falso`, PR #12 de ese repo **integrado**)
  - **Hecho cuando**: lo listado como «estorba al falso» ya no importa `whatsmeow`; un candado de imports lo vigila; el
    comentario de `control/diag/inyector.go` ya no contradice al ADR-0041 (D-FW-6); con la palanca baja, el gate del Edge rc=0
- [x] **TW.5 · El falso: arranque y entrada** · Edge · dep. TW.4 · cumple RW.1.a, RW.2.a, RW.3.a, RW.3.b, RW.3.d · `94e29be` (`edge/wapp-edge-agent`, mismo PR #12; ✎ va en el mismo commit que TW.6: la escucha falsa recibe y envía en el mismo tipo)
  - **Hecho cuando**: con la palanca echada el daemon arranca sin device; un entrante inyectado recorre cola →
    despachador → CloudLink con la marca sintética; grita al arrancar y en el latido; el guardarraíl de la palanca baja en verde
- [x] **TW.6 · El falso: salida capturada** · Edge · dep. TW.5 · cumple RW.2.b, RW.2.c, RW.3.c · `94e29be` (`edge/wapp-edge-agent`, mismo PR #12)
  - **Hecho cuando**: un `SendText` y un `SendMedia` quedan en el registro con su `command_id` y se leen desde fuera; el
    acuse vuelve al Cloud como uno real

## Bloque C · la conversación con Edge real · sesión FW-03 · TW.7–TW.8 y TW.11 · necesita Docker

Entrada: bloque B integrado en el `dev` del Edge. Para cuando: definición de hecho 5.
✎ D-FW-7 (2026-10-11): el Edge real corre en un **contenedor Linux**; TW.7 monta eso ([`diseno.md`](diseno.md) §2.5, puntos 7 y 8, y §6).
✎ FW-03 (2026-10-11): D-FW-5 decidida al arrancar (el arnés conduce). Los tres casos viven tras las etiquetas `integracion` y `edgereal`; se corren con `make test-procesos-edge-real`. Todo en este repo, rama `reorg/fw-03-conversacion-edge-real`, PR #78 **integrado**. En el Edge, ni un commit.

- [x] **TW.7 · El arnés lanza el Edge real** · Cloud (`test/procesos`, `Makefile`) · dep. TW.6 · cumple RW.4.a, RW.4.c · `8f558749` y `b89bbbec` (este repo; ✎ el segundo añade la comprobación de la custodia y quita un `os.Environ` que el candado `TestSinBDViva` rechazaba). Trae su propio caso, `TestEdgeReal_BootsTheRealBinary`
  - **Hecho cuando**: un objetivo de `make` construye y lanza `wapp-edge-agent` con WhatsApp falso contra el Cloud del
    arnés, y el caso afirma **qué binario** corre; `make test-procesos` da el mismo `PASS` que antes
  - **Commit**: `procesos(edge-real): el arnés lanza el Edge real con WhatsApp falso`
- [x] **TW.8 · El golden con Edge real (cierra D-38)** · Cloud · dep. TW.7 · cumple RW.4.a, RW.4.b, RW.3.b, RW.3.c · `1ffbf3a0` (este repo) · `TestEdgeReal_CartConversation`. ✎ Es un recorrido paralelo al de `TestP3_CartConversation` con sus mismas constantes de texto, no la misma función (hallazgo 33)
  - **Hecho cuando**: el carrito («carrito» → categoría → artículo → cantidad → resumen → confirmar) pasa con Edge real;
    los textos capturados son los del mismo caso con el Edge falso; tras revocar el lease, cero envíos; `RC=0`, 0 SKIP, 0 FAIL
  - **Commit**: `procesos(edge-real): el carrito con Cloud y Edge reales`
- [x] **TW.11 · La reanudación del carrito, vista desde fuera (cierra D-39)** · Cloud · dep. TW.8 · cumple RW.4.d · añadida el 2026-10-11 por decisión de Jhoan · `b82fb189` (este repo) · `TestEdgeReal_CartResumePolicy`; mutante corrido (hallazgo 35). ✎ No alarga `make test-procesos`: va en el objetivo aparte
  - **Hecho cuando**: una empresa con `page_size` propio (o con checklist de comprador) en `tenant_settings`; el carrito
    se interrumpe y se reanuda; el caso pasa con `flowruntime.WithResumePolicy` cableada y **cae** al quitarla de
    `internal/arranque/fase7_flujos.go` (mutante, en un *worktree* desechable); anotado cuánto alarga la suite
  - **Commit**: `procesos(edge-real): el carrito interrumpido se reanuda con la política de la empresa`

## Bloque D · cierre y receta de UAT · sesión FW-04 · TW.9–TW.10

Para cuando: definición de hecho 6–8. Tras este bloque, F10-03 puede arrancar.

- [ ] **TW.9 · La receta para UAT** · Cloud (documentación) y raíz de wApp · dep. TW.8 · cumple RW.5.a, RW.5.b
  - **Ficheros**: [`../F10-relevo/diseno.md`](../F10-relevo/diseno.md) §2.4 (las filas 1 y 6, con los comandos), el runbook del ecosistema que corresponda
  - **Hecho cuando**: la receta —echar la palanca, conducir la conversación, leer lo capturado, **bajar la palanca**—
    ejecutada en local de principio a fin, con sus salidas
- [ ] **TW.10 · Cerrar FW** · dep. TW.9
  - **Ficheros**: [`deuda.md`](../../../deuda.md) (D-38 y D-39 cerradas), `ESTADO.md`, este README, `documentations/` del Edge (contratos y operación: la palanca y las rutas nuevas)
  - **Hecho cuando**: los 8 puntos de [`reglas.md`](reglas.md) §4, cada uno con su evidencia
  - **Commit**: `docs(reorganizacion-modular): FW cerrada`
