# FW · Requisitos

> Historias y criterios EARS ([`plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §2). Los nombres de palanca,
> ruta y test que aquí aparecen como `<…>` los fija FW-01 (TW.3) y se sustituyen entonces: no se inventan antes de medir.

## HW.1 · WhatsApp se quita y se pone

> Como **Jhoan**, quiero arrancar el Edge real con un WhatsApp falso, para probar un flujo entero sin depender de una
> sesión de WhatsApp.

- **RW.1.a** · **DONDE** la palanca del WhatsApp falso está echada, **EL** Edge **DEBERÁ** arrancar y quedar operativo
  **sin** device pareado y **sin** abrir conexión con WhatsApp. — Verifica: test del daemon `<TW.5>` y, en campo, el
  arranque de TW.7 sin red hacia WhatsApp.
- **RW.1.b** · **MIENTRAS** la palanca está baja (el valor por defecto), **EL** Edge **DEBERÁ** comportarse como hoy: mismas
  rutas registradas, mismo adaptador. — Verifica: el test de guardarraíl del daemon (el molde es el del inyector,
  `registrarInyectorEntrantes`) y `make ci-local` del Edge rc=0.
- **RW.1.c** · **EL** código de `internal/app` y de `internal/domain` que elige o usa el adaptador **NO DEBERÁ** importar
  `go.mau.fi/whatsmeow` en lo que el falso necesita para enchufarse. — Verifica: el candado de imports que escriba TW.4
  (alcance: D-FW-4).

## HW.2 · Las dos direcciones

> Como **la sesión local**, quiero hacer entrar un mensaje y leer lo que el Edge respondió, para afirmar «el carrito
> contestó esto».

- **RW.2.a** · **CUANDO** se inyecta un entrante en el WhatsApp falso, **EL** Edge **DEBERÁ** tratarlo por el **mismo
  camino** que uno real: cola durable cifrada, despachador, CloudLink. — Verifica: test `<TW.5>`; es la lección del
  ADR-0041 (entrar por debajo del listener «mide otro tramo»).
- **RW.2.b** · **CUANDO** el Cloud ordena un envío (`SendText`, `SendMedia`), **EL** WhatsApp falso **DEBERÁ** registrarlo
  —destinatario, texto o media, `command_id`— y devolver un acuse como el real. — Verifica: test `<TW.6>`.
- **RW.2.c** · **EL** registro de envíos capturados **DEBERÁ** poder leerse desde fuera del proceso. — Verifica: la ruta o
  fichero que fije TW.3, leída por el caso de TW.8.

## HW.3 · No es una puerta trasera

> Como **Jhoan**, quiero que el WhatsApp falso no pueda quedar activo por descuido en un Edge de cliente.

- **RW.3.a** · **DONDE** la palanca está echada, **EL** Edge **DEBERÁ** decirlo al arrancar y en cada latido. — Verifica:
  test `<TW.5>` (molde: `internal/app/latencia/latido.go:75`).
- **RW.3.b** · **EL** entrante que nace del WhatsApp falso **DEBERÁ** viajar marcado como sintético hasta la nube (el
  prefijo `SINTETICO-` del `WAMessageID`, ADR-0041). — Verifica: test `<TW.5>` y el aserto del caso de TW.8.
- **RW.3.c** · **SI** el lease está revocado, **ENTONCES EL** Edge con WhatsApp falso **NO DEBERÁ** enviar ni inferir.
  — Verifica: un caso de TW.8 que revoca y comprueba cero envíos capturados. (La doble llave no tiene excepción de pruebas.)
- **RW.3.d** · **EL** WhatsApp falso **NO DEBERÁ** recibir, persistir ni registrar la DEK más allá de lo que hace el
  adaptador real. — Verifica: revisión en TW.5, con `fichero:línea` en el hallazgo.

## HW.4 · Una conversación entera, repetible

> Como **Jhoan**, quiero correr con un comando una conversación de carrito con Cloud y Edge reales.

- **RW.4.a** · **EL** comando de TW.8 **DEBERÁ** lanzar `cmd/server-modular` (tras el relevo, `cmd/server`), Postgres por
  testcontainers y el binario `wapp-edge-agent` con WhatsApp falso, y recorrer «carrito» → categoría → artículo →
  cantidad → resumen → confirmar. — Verifica: `RC=0`, 0 SKIP, 0 FAIL, rc leído sin pipe.
- **RW.4.b** · **LOS** textos que el Edge captura **DEBERÁN** ser los que el mismo caso obtiene con el Edge falso.
  — Verifica: la comparación del caso (el golden de D-38).
- **RW.4.c** · **EL** Edge falso de `test/procesos` **NO DEBERÁ** retirarse: los 1.291 casos siguen corriendo contra él.
  — Verifica: `make test-procesos` con el mismo `PASS` que antes de TW.7.

## HW.5 · La prueba de UAT ya no pide WhatsApp real

> Como **la operación de UAT**, quiero mirar los criterios conversacionales de la ventana sin una sesión de WhatsApp.

- **RW.5.a** · **LA** receta de TW.9 **DEBERÁ** decir, paso a paso, cómo echar la palanca en el Edge de UAT, conducir la
  conversación y **volver a bajarla**. — Verifica: la receta, ejecutada en local en TW.9.
- **RW.5.b** · **CUANDO** la ventana termina, **EL** Edge de UAT **DEBERÁ** quedar con la palanca baja. — Verifica: la línea
  de arranque del Edge en el acta de F10-03.
