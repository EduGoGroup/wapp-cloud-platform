# FW · Requisitos

> Historias y criterios EARS ([`plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §2). Los nombres de palanca,
> ruta y test los fijó FW-01 (TW.3, 2026-10-11) con lo medido: están en [`diseno.md`](diseno.md) §3 y §4. La palanca es
> `WAPP_AGENT_WHATSAPP_FALSO` (nombre efectivo en la máquina). 🟡 Pendientes de la aprobación de Jhoan.

## HW.1 · WhatsApp se quita y se pone

> Como **Jhoan**, quiero arrancar el Edge real con un WhatsApp falso, para probar un flujo entero sin depender de una
> sesión de WhatsApp.

- **RW.1.a** · **DONDE** la palanca del WhatsApp falso está echada, **EL** Edge **DEBERÁ** arrancar y quedar operativo
  **sin** device pareado y **sin** abrir conexión con WhatsApp. — Verifica:
  `TestPalancaWhatsAppFalso_ConLaPalancaECHADAElDaemonArrancaSinDeviceNiRed` (TW.5) y, en campo, el arranque de TW.7 sin
  red hacia WhatsApp.
- **RW.1.b** · **MIENTRAS** la palanca está baja (el valor por defecto), **EL** Edge **DEBERÁ** comportarse como hoy: mismas
  rutas registradas, mismo adaptador. — Verifica: `TestPalancaWhatsAppFalso_ConLaPalancaBAJADANadaCambia` (TW.5; el molde
  es el guardarraíl del inyector) y el gate del Edge rc=0.
- **RW.1.c** · **EL** código de `internal/app/sessionmgr` **NO DEBERÁ** construir el adaptador real ni depender de su tipo
  concreto: el transporte lo recibe ya elegido por el arranque. — Verifica:
  `TestFronteraDelTransporte_SessionmgrNoConstruyeElAdaptadorReal`, sobre el AST (TW.4; alcance: D-FW-4 y
  [`diseno.md`](diseno.md) §2.1). ✎ FW-01: el requisito hablaba de *imports* de `go.mau.fi`; lo medido es que ningún tipo
  de la librería cruza un puerto y que el estorbo es quién **construye** el adaptador.

## HW.2 · Las dos direcciones

> Como **la sesión local**, quiero hacer entrar un mensaje y leer lo que el Edge respondió, para afirmar «el carrito
> contestó esto».

- **RW.2.a** · **CUANDO** se inyecta un entrante en el WhatsApp falso, **EL** Edge **DEBERÁ** tratarlo por el **mismo
  camino** que uno real: las guardas de la puerta, cola durable cifrada, despachador, CloudLink. — Verifica:
  `TestWhatsAppFalso_ElEntranteInyectadoEntraPorLaPuertaRealYCaeEnLaCola` (TW.5); es la lección del ADR-0041 (entrar por
  debajo del listener «mide otro tramo»).
- **RW.2.b** · **CUANDO** el Cloud ordena un envío (`SendText`, `SendMedia`), **EL** WhatsApp falso **DEBERÁ** registrarlo
  —destinatario, texto o media, `command_id`— y devolver un acuse como el real. — Verifica:
  `TestWhatsAppFalso_SendTextYSendMediaQuedanCapturadosConSuCommandID` (TW.6).
- **RW.2.c** · **EL** registro de envíos capturados **DEBERÁ** poder leerse desde fuera del proceso. — Verifica:
  `GET /v1/diag/outbound/captured`, con `TestCapturados_LaRutaDevuelveLoEnviadoYConLaPalancaBAJADANoExiste` (TW.6) y
  leída por el caso de TW.8.

## HW.3 · No es una puerta trasera

> Como **Jhoan**, quiero que el WhatsApp falso no pueda quedar activo por descuido en un Edge de cliente.

- **RW.3.a** · **DONDE** la palanca está echada, **EL** Edge **DEBERÁ** decirlo al arrancar y en cada latido. — Verifica:
  `TestPalancaWhatsAppFalso_ElDaemonLoGRITAYElLatidoLoPublica` (TW.5; molde: `internal/app/latencia/latido.go:75`).
- **RW.3.b** · **EL** entrante que nace del WhatsApp falso **DEBERÁ** viajar marcado como sintético hasta la nube (el
  prefijo `SINTETICO-` del `WAMessageID`, ADR-0041). — Verifica: `TestWhatsAppFalso_TodoEntranteLlevaLasDosMarcas` (TW.5) y el aserto
  del caso de TW.8.
- **RW.3.c** · **SI** el lease está revocado, **ENTONCES EL** Edge con WhatsApp falso **NO DEBERÁ** enviar ni inferir.
  — Verifica: `TestWhatsAppFalso_ConElLeaseRevocadoNoSeCapturaNingunEnvio` (TW.6) y un caso de TW.8 que revoca y comprueba cero
  envíos capturados, con `WAPP_AGENT_CLOUDLINK_LEASE_PUBKEY_PATH` puesta: sin ella el Edge no tiene validador y el caso
  sería verde sin medir nada ([`diseno.md`](diseno.md) §2.5). (La doble llave no tiene excepción de pruebas.)
- **RW.3.d** · **EL** WhatsApp falso **NO DEBERÁ** recibir, persistir ni registrar la DEK más allá de lo que hace el
  adaptador real. — Verifica: `TestWhatsAppFalso_LaDEKNoSeGuardaNiSeRegistra` y revisión en TW.5, con
  `fichero:línea` en el hallazgo. La DEK de la sesión falsa nace en el emparejamiento de siempre, en el cliente.

## HW.4 · Una conversación entera, repetible

> Como **Jhoan**, quiero correr con un comando una conversación de carrito con Cloud y Edge reales.

- **RW.4.a** · **EL** comando de TW.8 **DEBERÁ** lanzar `cmd/server-modular` (tras el relevo, `cmd/server`), Postgres por
  testcontainers y el binario `wapp-edge-agent` con WhatsApp falso, y recorrer «carrito» → categoría → artículo →
  cantidad → resumen → confirmar. — Verifica: `make test-procesos-edge-real` (`TestEdgeReal_CartConversation`):
  `RC=0`, 0 SKIP, 0 FAIL, rc leído sin pipe. El caso no lanza Ollama ni el cajero: el carrito es motor de flujos.
- **RW.4.b** · **LOS** textos que el Edge captura **DEBERÁN** ser los que el mismo caso obtiene con el Edge falso.
  — Verifica: la comparación del caso (el golden de D-38).
- **RW.4.c** · **EL** Edge falso de `test/procesos` **NO DEBERÁ** retirarse: los 1.291 casos siguen corriendo contra él.
  — Verifica: `make test-procesos` con el mismo `PASS` que antes de TW.7.
- **RW.4.d** · **CUANDO** un cliente retoma un carrito interrumpido en una empresa con tamaño de página propio, **EL**
  sistema **DEBERÁ** reanudarlo con la política de esa empresa, y **SI** el arranque pierde `WithResumePolicy`, **ENTONCES
  EL** caso **DEBERÁ** fallar. — Verifica: `TestEdgeReal_CartResumePolicy` (TW.11) y su mutante (deuda D-39).

## HW.5 · La prueba de UAT ya no pide WhatsApp real

> Como **la operación de UAT**, quiero mirar los criterios conversacionales de la ventana sin una sesión de WhatsApp.

- **RW.5.a** · **LA** receta de TW.9 **DEBERÁ** decir, paso a paso, cómo echar la palanca en el Edge de UAT, conducir la
  conversación y **volver a bajarla**. — Verifica: la receta, ejecutada en local en TW.9.
- **RW.5.b** · **CUANDO** la ventana termina, **EL** Edge de UAT **DEBERÁ** quedar con la palanca baja. — Verifica: la línea
  de arranque del Edge en el acta de F10-03.
