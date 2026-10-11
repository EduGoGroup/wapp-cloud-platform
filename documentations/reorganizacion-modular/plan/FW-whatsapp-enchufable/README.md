# FW · WhatsApp enchufable — el Edge real conversa sin WhatsApp

> **Estado**: ⏳ sin empezar (spec escrita el 2026-10-11, por decisión de Jhoan al cerrar F8; resuelve la deuda **D-38** de
> [`deuda.md`](../../../deuda.md)). Medida sobre `edge/wapp-edge-agent` @ `38e5f91` (rama `main`; su rama por defecto es `dev`).
> **Forma**: [`plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md). 🟡 Spec **corta a propósito**: lo que no se ha
> medido está marcado «sin medir» y lo fija la primera sesión (FW-01), que es de solo lectura.
> 🔴 **El código de esta fase NO vive en este repo**: vive en `edge/wapp-edge-agent`. Aquí viven la spec, las sesiones y
> el caso de proceso que conduce la conversación. `05` (contratos y TDD de `internal/modulos`) **no** rige el código del
> Edge: rige la `documentations/` del Edge.

## Objetivo en tres líneas

Que WhatsApp sea en el Edge **una pieza de quita y pon**: el mismo binario `wapp-edge-agent` arranca con el adaptador real
(`whatsmeow`) o con uno **falso** que hace entrar mensajes y **captura** lo que el Edge habría enviado. Con eso se corre
una conversación completa —Cloud real + Edge real + modelo real— sin sesión de WhatsApp: el carrito, un presupuesto por
P2→P5 o cualquier flujo, las veces que haga falta. Sirve para cerrar la migración y, después, para probar y mejorar flujos.

## Por qué hace falta (medido el 2026-10-11)

| Hecho | Cómo se midió |
|---|---|
| Los 1.291 casos de `test/procesos` corren contra un **Edge falso**: no ejercitan la cola SQLite cifrada, el lease ni el envío del Edge real | [`F9-procesos/diseno.md`](../F9-procesos/diseno.md); deuda D-38 |
| Probar el Edge real exige hoy una sesión de WhatsApp viva; las de local y del VPS estaban caducadas el 2026-10-10 | deuda D-38 |
| El Edge **ya tiene** puertos para WhatsApp: `Connector` (`internal/app/pair.go:67`), `ListenGateway` (`internal/app/listen.go:80`), `LiveSender` (`:91`) | `grep -rn 'interface {' --include='*.go' internal/app` |
| El Edge **ya tiene** un inyector de entrantes (ADR-0041, fuera de este repo: «vía **permanente** y apagada por defecto»): `POST /v1/diag/inbound/inject`, palanca `WAPP_AGENT_INYECTOR_ENTRANTES` | `internal/adapters/control/diag/inyector.go:70` |
| 🔴 El inyector **no sustituye** a WhatsApp: vive dentro del adaptador `whatsmeow`, fabrica `*events.Message` y necesita un `Listener` publicado; sin él responde `409` (`ErrSinEscuchaViva`) | `internal/adapters/whatsmeow/inyector.go:60`, `internal/app/sessionmgr/inyector.go:96` |
| 🔴 Solo cubre la **entrada**. Un emisor falso que capture la salida: **no se encontró** (búsqueda no exhaustiva) | — |
| `whatsmeow` se importa fuera de su adaptador y del almacén: `internal/app/sessionmgr` (2 ficheros), `internal/domain` (1), `internal/adapters/cloudlink` (1), `internal/infra/edgemigrate` (1) | `grep -rl 'go.mau.fi/whatsmeow' --include='*.go' . \| grep -v _test.go`, por directorio |
| 🟡 Contradicción: el ADR-0041 dice «permanente»; la cabecera de `control/diag/inyector.go` dice que el paquete «se BORRA cuando la pregunta esté respondida» | los dos textos |

**Sin medir** (lo fija FW-01): si con la sesión caducada el `Listener` llega a publicarse; qué impide hoy arrancar el
daemon sin un device pareado; cuánto de `sessionmgr` depende de tipos de `whatsmeow`; si el arnés de `test/procesos`
puede lanzar el binario del Edge.

## Entradas (tiene que ser cierto para empezar)

| Condición | Cómo se comprueba |
|---|---|
| F8 cerrada (el Cloud nuevo arranca solo) | bloque F8-07 de [`ESTADO.md`](../../ESTADO.md) |
| La sesión trabaja **dentro** de `edge/wapp-edge-agent` y cargó su `documentations/` (`constitucion.md` primero) | regla del `CLAUDE.md` de la raíz de wApp, §2 |
| D-FW-1 y D-FW-2 decididas (para FW-02; FW-01 no las necesita) | [`../DECISIONES.md`](../DECISIONES.md) §8 |

FW **no depende de F9-05 ni de F10-01/F10-02**: avanza en paralelo.

## Salidas (es cierto al cerrar)

- El Edge arranca con WhatsApp **falso** por una palanca apagada por defecto, que **grita** al arrancar y marca todo lo
  que entra como sintético (el patrón del ADR-0041). Con la palanca baja, el binario se comporta **igual que hoy**.
- El WhatsApp falso cubre las **dos direcciones**: entran mensajes y se puede leer lo que el Edge envió.
- Una conversación de carrito recorre Cloud real + Edge real sin WhatsApp, repetible con un comando, en local.
- Existe la receta para hacer lo mismo en el Edge de UAT (la usa F10-03).
- La **doble llave** queda intacta: la DEK sigue en el cliente y no cruza el contrato; el lease lo sigue emitiendo y
  revocando el Cloud, **también** con WhatsApp falso. Sin Redis ni broker en el Edge.
- La deuda D-38 cerrada; el ADR que lo ampara, aprobado por Jhoan.

## Las pruebas que se mudan aquí (decisión de Jhoan, 2026-10-11)

| Prueba | Dónde estaba | Dónde queda |
|---|---|---|
| El golden con un **Edge real** | Deuda D-38, «a decidir en F10-01» | **TW.8** (sesión FW-03) |
| «Un mensaje real entra y se contesta» y «los textos no cambian», con **WhatsApp real** | F10 [`diseno.md`](../F10-relevo/diseno.md) §2.4 (filas 1 y 6), R10.1.e, D-F10-1 | Se hacen con el **WhatsApp falso** de esta fase: en local en TW.8 y, durante la ventana de UAT, con la receta de TW.9. **F10-03 pasa a depender de FW** |

Ya no hace falta renovar una sesión de WhatsApp para el relevo. 🟡 Lo que el WhatsApp falso **no** prueba —el protocolo
de WhatsApp de verdad: emparejar, recibir y enviar por la red— ya estaba probado en campo y **no lo toca** la
reconstrucción del Cloud.

## Orden de lectura

[`requisitos.md`](requisitos.md) → [`arquitectura.md`](arquitectura.md) → [`diseno.md`](diseno.md) →
[`reglas.md`](reglas.md) → [`tareas.md`](tareas.md).

## Bloques de sesión

| Sesión | Bloque | Tareas | Dónde trabaja | Para cuando |
|---|---|---|---|---|
| [FW-01](../sesiones/FW-01-cli-inventario-y-adr.md) | **A · inventario y ADR** | TW.1–TW.3 | Edge (solo lectura) y la raíz de wApp (el ADR) | Los «sin medir» de arriba, medidos; el borrador del ADR; diseño de este directorio corregido con lo medido. **Jhoan aprueba antes de escribir código** |
| [FW-02](../sesiones/FW-02-cli-adaptador-falso.md) | **B · el adaptador falso** | TW.4–TW.6 | Edge | El Edge arranca con WhatsApp falso: entra un mensaje y se lee la respuesta capturada; con la palanca baja, nada cambia |
| [FW-03](../sesiones/FW-03-cli-conversacion-edge-real.md) | **C · la conversación con Edge real** | TW.7–TW.8 | este repo y Edge · **Docker** | Un comando corre el carrito con Cloud nuevo + Edge real, `RC=0`, 0 SKIP |
| [FW-04](../sesiones/FW-04-cli-cierre-y-receta-uat.md) | **D · cierre y receta de UAT** | TW.9–TW.10 | este repo, Edge y la raíz de wApp | Receta para UAT escrita y probada en local; D-38 cerrada; documentación de las dos piezas al día |

## Decisiones que necesita (Jhoan)

| # | Pregunta | Recomendación |
|---|---|---|
| **D-FW-1** | ¿ADR nuevo o enmienda del ADR-0041? | **ADR nuevo**. El 0041 decide un *inyector* sobre el adaptador real; esto decide un *adaptador sustituto*. Lo escribe FW-01, lo apruebas tú |
| **D-FW-2** | ¿El WhatsApp falso va en el **mismo binario** tras una palanca, o en un binario / *build tag* aparte? | **Mismo binario, palanca apagada por defecto**. El ADR-0041 ya rechazó el binario aparte: «un instrumento que solo existe en un binario que no se despliega no mide el sistema desplegado» |
| **D-FW-3** | ¿El relevo (F10-04) espera a esta fase? | **Sí** (así queda escrito): F10-03 depende de FW y F10-04 de F10-03. 🟡 **Asumido** de «que se haga con la fase nueva»; si prefieres relevar antes, se quita esa dependencia y las filas 1 y 6 de §2.4 se miran después del relevo |
| **D-FW-4** | ¿Se limpia toda la fuga de `whatsmeow` fuera de su adaptador? | **Solo lo que impida enchufar el falso**. El resto, deuda del Edge con número. Se decide con el inventario de TW.1 |
| **D-FW-5** | ¿Quién conduce la conversación? | **El arnés de `test/procesos`** de este repo, con un modo «Edge real» junto al Edge falso (que no se retira). 🟡 Sin medir si puede lanzar el binario del Edge: lo dice TW.1 |
| **D-FW-6** | La contradicción ADR-0041 («permanente») ↔ comentario de `control/diag` («se borra») | Manda el ADR; el comentario se corrige en TW.4 |

## Hallazgos

*(vacío: los numera cada sesión al cerrar)*
