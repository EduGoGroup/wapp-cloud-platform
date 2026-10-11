# FW · WhatsApp enchufable — el Edge real conversa sin WhatsApp

> **Estado**: 🟡 **bloque C (FW-03) hecho el 2026-10-11: el carrito conversa con Cloud y Edge reales; PR #78 de este repo
> integrado. Siguiente sesión: FW-04.** Bloque B (FW-02): el transporte falso, en el Edge (PR #12 de `wapp-edge-agent` y PR #77 de este repo, integrados). Bloque A (FW-01), PR #76 integrado. D-FW-1, 2, 4, 6, 7 y 8
> decididas y **ADR-0049 aprobado** (vigente; sin commitear en la raíz de wApp). D-FW-5 decidida al arrancar FW-03 (spec escrita el 2026-10-11, por decisión de Jhoan al cerrar F8; resuelve la deuda **D-38** de
> [`deuda.md`](../../../deuda.md)). Medida sobre `edge/wapp-edge-agent` @ `38e5f91` (rama `main`; su rama por defecto es `dev`).
> **Forma**: [`plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md). Spec **corta a propósito**; lo que faltaba por
> medir lo midió FW-01 (solo lectura) y está en [`diseno.md`](diseno.md) §2, con lo que solo se sabe ejecutando en su §6.
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
| `whatsmeow` se importa fuera de su adaptador y del almacén en **3 ficheros**: `internal/app/sessionmgr` (2) e `internal/infra/edgemigrate` (1), los tres solo por `types.ParseJID`. ✎ FW-01: esta fila contaba también `internal/domain` y `internal/adapters/cloudlink`, que solo **mencionan** la librería en un comentario (hallazgo 1) | `grep -rn 'go.mau.fi/' --include='*.go' . \| grep -v _test.go`, contando ficheros con `import` |
| 🟡 Contradicción: el ADR-0041 dice «permanente»; la cabecera de `control/diag/inyector.go` dice que el paquete «se BORRA cuando la pregunta esté respondida» | los dos textos |

**Lo que faltaba por medir, medido por FW-01** ([`diseno.md`](diseno.md) §2): con la sesión caducada el `Listener` solo
sigue publicado mientras el proceso vive, y la sesión ya salió del mux (no sirve de base); el daemon **arranca** sin
device pareado; `sessionmgr` no depende de tipos de `whatsmeow` en sus firmas, pero **construye** el adaptador concreto; y
el arnés de `test/procesos` **puede** lanzar y enrolar el binario del Edge, con siete cosas por montar.

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
- Las deudas D-38 y D-39 cerradas; el ADR que lo ampara, aprobado por Jhoan.

## Las pruebas que se mudan aquí (decisión de Jhoan, 2026-10-11)

| Prueba | Dónde estaba | Dónde queda |
|---|---|---|
| El golden con un **Edge real** | Deuda D-38, «a decidir en F10-01» | **TW.8** (sesión FW-03) |
| Un proceso que note **desde fuera** que falta la política de reanudación del carrito | Deuda D-39, «para después del relevo» | **TW.11** (sesión FW-03), como segunda conversación |
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
| [FW-01](../sesiones/FW-01-cli-inventario-y-adr.md) | **A · inventario y ADR** | TW.1–TW.3 | Edge (solo lectura) y la raíz de wApp (el ADR) | Lo que faltaba por medir, medido; el borrador del ADR; diseño de este directorio corregido con lo medido. **Jhoan aprueba antes de escribir código** |
| [FW-02](../sesiones/FW-02-cli-adaptador-falso.md) | **B · el adaptador falso** | TW.4–TW.6 | Edge | El Edge arranca con WhatsApp falso: entra un mensaje y se lee la respuesta capturada; con la palanca baja, nada cambia |
| [FW-03](../sesiones/FW-03-cli-conversacion-edge-real.md) | **C · la conversación con Edge real** | TW.7–TW.8, TW.11 | este repo y Edge · **Docker** | Un comando corre el carrito con Cloud nuevo + Edge real, `RC=0`, 0 SKIP; y un carrito interrumpido se reanuda con la política de la empresa (D-39) |
| [FW-04](../sesiones/FW-04-cli-cierre-y-receta-uat.md) | **D · cierre y receta de UAT** | TW.9–TW.10 | este repo, Edge y la raíz de wApp | Receta para UAT escrita y probada en local; D-38 cerrada; documentación de las dos piezas al día |

## Decisiones que necesita (Jhoan)

| # | Pregunta | Recomendación |
|---|---|---|
| **D-FW-1** | ¿ADR nuevo o enmienda del ADR-0041? | ✅ **Decidida: ADR nuevo** (Jhoan, 2026-10-11): el ADR-0049, **aprobado** el mismo día. El 0041 decide un *inyector* sobre el adaptador real; esto decide un *adaptador sustituto*. Lo escribe FW-01, lo apruebas tú |
| **D-FW-2** | ¿El WhatsApp falso va en el **mismo binario** tras una palanca, o en un binario / *build tag* aparte? | ✅ **Decidida: mismo binario tras palanca** (Jhoan, 2026-10-11) |
| **D-FW-3** | ¿El relevo (F10-04) espera a esta fase? | ✅ **Decidida: sí** (Jhoan, 2026-10-11). F10-03 depende de FW y F10-04 de F10-03: el código viejo se borra solo cuando el flujo nuevo está visto funcionando, o le quedan detalles menores que se arreglan sin el viejo delante |
| **D-FW-4** | ¿Se limpia toda la fuga de `whatsmeow` fuera de su adaptador? | ✅ **Decidida: solo lo que estorba** (Jhoan, 2026-10-11). El resto es deuda **del Edge** (A-5 de su `deuda.md`), y la frontera se diseñó para crecer |
| **D-FW-5** | ¿Quién conduce la conversación? | ✅ **Decidida: el arnés de `test/procesos`** (Jhoan, 2026-10-11, al arrancar FW-03), con un modo «Edge real» junto al Edge falso, que no se retira. Descartado un script fuera del arnés |
| **D-FW-6** | La contradicción ADR-0041 («permanente») ↔ comentario de `control/diag` («se borra») | ✅ **Decidida: permanente, por mérito y no por jerarquía** (Jhoan, 2026-10-11): el inyector es la única puerta de entrada del transporte falso. Lo decide el ADR-0049 como decisión propia |
| **D-FW-7** | ✎ FW-01. La custodia de la DEK en macOS es el Keychain, fijado en compilación: cada binario nuevo abre un diálogo. ¿Cómo corre el caso sin nadie delante? | ✅ **Decidida: Edge en contenedor Linux; la custodia no se toca** (Jhoan, 2026-10-11). Descartado el archivo en macOS tras la palanca, que era lo recomendado |
| **D-FW-8** | ✎ FW-01. ¿El falso reutiliza la puerta de entrantes del adaptador real, o se escribe sin importar nada de `whatsmeow`? | ✅ **Decidida: reutiliza la puerta real** (Jhoan, 2026-10-11), pensando en más transportes |

## Hallazgos

### FW-01 (2026-10-11) · inventario de solo lectura sobre `edge/wapp-edge-agent` @ `38e5f91`

1. **La spec contaba menciones, no imports.** Decía que `internal/domain` e `internal/adapters/cloudlink` importaban
   `whatsmeow`: solo lo nombran en un comentario (`internal/domain/jid.go:7`, `internal/adapters/cloudlink/adapter.go:628`).
   Regla 6 del `CLAUDE.md` de la raíz: se dice con qué regla se contó.
2. **El estorbo no son los tipos, es quién construye.** Ningún tipo de `whatsmeow` cruza un puerto. Lo que impide
   enchufar el falso es que `sessionmgr` construye el adaptador concreto (`internal/app/sessionmgr/listen.go:110`,
   `pairing.go:55`) y usa diez métodos suyos fuera de los puertos (`listen.go:113-201`). RW.1.c se reescribió con eso.
3. **`LiveSender` es un puerto sin consumidor de producción** (solo una aserción de test,
   `internal/adapters/whatsmeow/listen_gateway_test.go:11`); el envío real va por *closures*. Lo mismo `LiveLogout`. La spec
   lo daba por el puerto del envío.
4. **La «vertical muerta» de envío ya no existe**: `internal/app/send.go` se borró en `e5f149b`. La trampa 10 de la
   constitución del Edge está caducada. Se corrige en la documentación del Edge en TW.10, no aquí.
5. 🔴 **La DEK hace falta aunque el falso no descifre nada**: la cola de entrantes sella con ella
   (`internal/infra/wiring/cloudlink.go:147-163`). Por eso la sesión falsa nace por el emparejamiento de siempre, que la
   genera en el cliente (`internal/app/pair.go:143`, `:175`).
6. 🔴 **Sin `WAPP_AGENT_CLOUDLINK_LEASE_PUBKEY_PATH` el Edge no tiene validador y envía con el lease revocado**
   (`internal/infra/wiring/cloudlink.go:291-293`). El caso de RW.3.c la pone siempre o es verde sin medir. Además, la
   constitución del Edge dice que **no hay ningún test** que afirme «sin lease no se despacha» (INV-2): el de TW.6 será el primero.
7. **La custodia de la DEK en macOS es el Keychain, fijado en compilación** (`internal/adapters/keycustody/file.go:1`).
   No hay variable. Abre la decisión **D-FW-7**.
8. **El inyector se reutiliza**: su contrato es una *closure* sin tipos de la librería
   (`internal/app/sessionmgr/session.go:194`). Y la puerta de entrantes (`NewListener`, `listener.go:331`) no necesita
   cliente. Abre la decisión **D-FW-8**.
9. **El arnés no tiene precedente de repo hermano ni de binario por variable**, y exige `GOWORK=off`
   (`test/procesos/main_test.go:127-142`). La propuesta: `WAPP_PROCESOS_EDGE_DIR`, etiqueta `edgereal` y objetivo aparte.
10. **El carrito no necesita Ollama ni el cajero**: es motor de flujos y la única inferencia ya se espera caída
    (`test/procesos/p3_entrante_cart_test.go:238-248`).
11. **Varios comentarios del Edge citan líneas ya desplazadas** (p. ej. `session.go:182` dice `listen.go:169` y el cable
    está en `listen.go:187`). Las líneas de [`diseno.md`](diseno.md) son las reales a `38e5f91`.
12. **La raíz de wApp no tiene remoto ni rama `dev`**: solo `main`, con cambios de Jhoan sin commitear. El borrador del
    ADR-0049 queda como fichero nuevo **sin commitear**; cómo entra lo decide Jhoan. `make check-docs` de la raíz dio
    `rc=0` con el gate de diagramas **saltado** (falta `mmdc`): un salto no es un verde, y el ADR no trae diagramas.
13. ✎ D-FW-1 la decidió Jhoan en la misma sesión (ADR nuevo). **Las decisiones D-FW-2, D-FW-4, D-FW-5 y D-FW-6 siguen vacías** en [`../DECISIONES.md`](../DECISIONES.md) §8.
    FW-01 no las necesitaba; FW-02 sí (salvo D-FW-5, que es de FW-03), junto con D-FW-7 y D-FW-8.

### FW-02 (2026-10-11) · el transporte falso, en `edge/wapp-edge-agent` (rama `feat/fw-02-whatsapp-falso`)

14. 🔴 **`make ci-local` del Edge falla en la Mac sin tocar nada.** En `dev` @ `38e5f91` cae en el lint: el
    `golangci-lint` del sistema (2.14.0, sobre Go 1.27.1) marca 3 `SA1019` en `cmd/wapp-ctl/proxy.go` e
    `internal/auth/keystore.go`, y los tests no llegan a correr. El `Makefile` del Edge **no fija la toolchain** como el
    de este repo. Con la fijada (`make ci-docker`: Go 1.26.5, lint v2.12.2) da `GATE_RC=0`. El punto 1 de
    [`reglas.md`](reglas.md) §4 se midió con `ci-docker`, antes y después. Los 3 avisos no se arreglaron: no son de FW.
15. **Corrección a FW-01 (hallazgo 7 y D-FW-7):** el archivo `0600` no es «la custodia de Linux». Linux usa Secret
    Service y Windows DPAPI (`internal/adapters/keycustody/secretservice_linux.go`, `dpapi_windows.go`); el archivo
    en claro es el último recurso, y ya es la deuda **S-6** del Edge. Con eso Jhoan cambió D-FW-7 a contenedor Linux.
16. 🔴 **Hay una segunda forma de que RW.3.c salga verde sin medir**: `WAPP_AGENT_CLOUDLINK_LEASE_SHADOW_MODE`. Con
    ella, un lease revocado solo avisa y el envío sale (`internal/adapters/cloudlink/adapter.go:133-139`). El default es
    apagada; el caso de TW.8 tiene que afirmarlo, igual que la pública del lease (hallazgo 6).
17. **El test de «sin lease no se despacha» ya existe y tiene control positivo**:
    `TestWhatsAppFalso_ConElLeaseRevocadoNoSeCapturaNingunEnvio` monta el adaptador de CloudLink de verdad con el
    transporte falso detrás; con lease válido captura, tras revocar no. El transporte falso no sabe nada del lease: el
    gate está antes.
18. **La frontera no cupo en `internal/app`**: la interfaz nombra tipos de `health` y `latencia`, que importan `app`.
    Vive en `internal/app/sessionmgr/transporte.go` y la cola con cada adaptador en `internal/infra/wiring/transporte.go`.
    Coste: los tests de cableado de `sessionmgr` llevan un calco de esa cola (no pueden importar `wiring`).
19. **Reutilizar la puerta costó un fichero en el adaptador real**, `whatsmeow/sin_transporte.go`: `handleEvent` es
    privado. Es código del adaptador real que solo alcanza el transporte falso.
20. **`sessionmgr` sigue importando el paquete del adaptador real**, por `whatsmeow.Backoff`. El candado de RW.1.c
    (`TestFronteraDelTransporte_SessionmgrNoConstruyeElAdaptadorReal`, sobre el AST) permite ese nombre y ninguno más;
    probado con un mutante. La lista solo encoge.
21. **Medido lo que FW-01 no pudo**: desvincular una sesión que nunca tuvo almacén de `whatsmeow` no falla.
22. 🟡 **Para FW-03, sin medir** ([`diseno.md`](diseno.md) §6): cómo alcanza el arnés el socket unix del Edge dentro
    del contenedor y con qué nombre de certificado llega el contenedor al Cloud; si el Cloud acepta el número propio
    de una sesión falsa (`0000` + 8 cifras), que viaja en cada latido; y quién concede `edge.diag.inbound.inject`, que
    es también el recurso de la ruta de lectura. 🔴 Lo del socket **no** se lo dije a Jhoan al decidir D-FW-7: es un
    coste del contenedor que entonces no estaba medido.
23. **El acuse `delivered` sale 50 ms después del envío**, por la puerta real. Es para no fabricar el orden contrario
    al de WhatsApp (primero el `Ack`, luego el acuse); no garantiza el orden.
24. **TW.5 y TW.6 van en un solo commit** (`94e29be`): la escucha falsa recibe y envía en el mismo tipo.
25. 🟡 **«Con la palanca baja, igual que hoy» tiene dos excepciones visibles, las dos en el log**: la línea del latido
    gana el campo `whatsapp=real` en **todos** los Edge (un `real` explícito es un dato; es el mismo criterio del campo
    `inyector`), y el aviso de «sesión sin escucha» nombra `WithEscucha` en vez de `WithWhatsmeowListen`. Ni rutas, ni
    proto, ni conducta. Si algo en campo parsea esa línea por posición, lo nota.
26. **El ADR-0049 está aprobado (Jhoan, 2026-10-11) y sigue sin commitear** en la raíz de wApp, con las cinco decisiones y su fila en el índice. Los contadores de ADR de `documentations/README.md` y `ACTIVE.md` de la raíz ya iban atrasados (dicen 46) y no se tocaron. `make check-docs`
    de la raíz: rc=0, 0 enlaces rotos, gate de diagramas **saltado** (falta `mmdc`).

### FW-03 (2026-10-11) · la conversación con Cloud y Edge reales, en `test/procesos` (rama `reorg/fw-03-conversacion-edge-real`)

Medido contra `edge/wapp-edge-agent` @ `0675d55` (su `dev`), en un contenedor `linux/arm64`. En el Edge, ni un commit.

27. **Cómo llega el contenedor al servidor del arnés** (lo que el hallazgo 22 dejó sin medir). El servidor escucha en el
    loopback del anfitrión; el contenedor lo alcanza por el túnel de testcontainers (`WithHostPortAccess`, un segundo
    contenedor `testcontainers/sshd`). El certificado del arnés **no se tocó**: el Edge verifica `localhost` con
    `WAPP_AGENT_CLOUDLINK_SERVER_NAME`. 🔴 El túnel se monta **después** de arrancar el contenedor y `agent enroll` no
    reintenta: con dos casos en paralelo el enrolamiento moría con «connection refused». El contenedor espera al
    puerto antes de enrolar (`edgeRealScript`).
28. **Cómo se alcanza el plano de control**: `curl --unix-socket` lanzado dentro del contenedor, una llamada por
    petición. El Edge no tiene cliente de línea de comandos para su plano de control (`wapp-ctl` es un supervisor con
    web), y el mensaje de `internal/adapters/control/diag/inyector.go:328` cita un `wapp-ctl sessions` que no existe.
29. **No hay imagen del Edge**: el binario se compila en el anfitrión para Linux (es Go puro, sin CGO) y se copia a
    una imagen de terceros con `curl` (`curlimages/curl`, versión fijada). El Edge no tiene Dockerfile de ejecución ni
    `.dockerignore`, y su árbol de trabajo guarda datos de sesiones reales: construir una imagen con el repo como
    contexto los habría metido dentro. La corrida descarga dos imágenes públicas la primera vez.
30. **Quién concede los permisos del plano de control**: nadie aparte. El operador es una persona con el rol de
    administradora de la empresa; entra por `POST /v1/auth/login` del Edge, que relaya a la nube, y su token trae los
    permisos. Hasta que el Edge recibe el JWKS de la nube solo deja leer (`403 degraded_read_only`): la escena reintenta.
31. **La nube acepta el número propio de una sesión falsa** (`0000` + 8 cifras): le manda el aviso de sesión pasiva y
    marca `greeted_at` cuando el Edge lo acusa. El aviso queda capturado, como cualquier envío.
32. **Con la *readiness* sin afirmar, la nube sí pide la inferencia de «abc»**; el Edge, sin cajero, contesta
    `ollama_down` y el carrito repregunta con el mismo texto que con el Edge falso.
33. 🟡 **RW.4.b se cumple por constantes compartidas, no por función compartida.** `TestEdgeReal_CartConversation` es un
    recorrido paralelo al de `TestP3_CartConversation`: mismas constantes de texto, mismo catálogo, mismas consultas
    de estado. No es la misma función porque el caso viejo afirma cosas que solo existen en el doble (cuántas
    inferencias le bajaron, sus errores internos) y sus `wa_message_id` son otros (los del Edge real llevan la marca:
    `SINTETICO-CART-<n>-0000`). **No repite** del caso viejo: el hilo cifrado, el detalle de `conversation_events`, la
    revisión de la solicitud ni el mensaje posterior al cierre. Si un texto cambia en un caso y no en el otro, lo
    notan las constantes; si cambia el guion, no.
34. **Con el lease revocado, lo que entra sigue subiendo y lo que sale se corta en el Edge.** El servidor ordena los
    dos envíos (bienvenida y categorías) y el Edge los rechaza en su gate con «lease no vigente», antes del
    transporte: cero capturados. El caso lo afirma por las dos caras (la línea del servidor y las dos del Edge), sin
    esperas a ciegas. 🟡 Dos cosas que **no** mide: la inferencia con el lease revocado (el recorrido no llega a
    pedirla), y que el servidor **sigue despachando** a un Edge revocado: la conversación de ese cliente queda con un
    carrito abierto cuyas pantallas no recibió nadie. Es conducta heredada, igual con el binario viejo; no se tocó.
35. **TW.11: la interrupción es un reinicio del servidor a mitad del carrito.** El Edge real reconecta solo y entrega
    el mensaje siguiente; la lista sale paginada con el `page_size` de la empresa. **Mutante**: sin
    `flowruntime.WithResumePolicy` en `internal/arranque/fase7_flujos.go` (worktree desechable, binario nuevo), el caso
    cae —salen los dos artículos juntos— y `TestEdgeReal_CartConversation` **sigue pasando**, que es lo que decía D-39.
    🟡 De paso: la **primera** pantalla del carrito no usa el `page_size` de la empresa (la pinta el arranque del
    flujo, antes de sembrar los ajustes). Heredado, igual en el viejo; es deuda para después del relevo.
36. **La custodia en el contenedor, vista**: la DEK cayó sola en su fichero (modo `600`, 32 bytes), sin tocar la
    custodia (D-FW-7). El caso mira el fichero, no su contenido.
37. ✎ **Desvíos del diseño, decididos por la sesión**: (a) `WAPP_PROCESOS_EDGE_DIR` tiene valor por defecto en el
    `Makefile` (el repo hermano), para que sea de verdad un comando; si la ruta no existe o no es el Edge, el caso
    **falla** diciéndolo (probado, también desde un worktree, donde la ruta por defecto no resuelve). (b) Hay un
    tercer caso, `TestEdgeReal_BootsTheRealBinary`, que es el de TW.7. (c) El objetivo corre contra los **dos**
    binarios del servidor, como `test-procesos`.
38. **La línea base no era 1.291**: `make test-procesos` da hoy **1.302** `PASS` por binario, medido en `dev` antes de
    tocar nada. Es el número que se conserva.
39. **Un `//nolint:gosec` nuevo** (`edge_real_test.go`, el `go build` del Edge, con ruta y revisión calculadas por el
    arnés), con el mismo motivo que los tres que ya tiene el arnés. Entra en la revisión de D-31.
40. **El primer intento de gate salió rojo y lo cazó un candado**: el `go build` del Edge copiaba el entorno del shell
    (`os.Environ`), que `TestSinBDViva` prohíbe en `test/procesos`. Arreglado sin tocar el candado.

