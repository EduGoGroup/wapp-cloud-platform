# FW · Arquitectura

> Vista macro, **corregida por FW-01 (TW.3, 2026-10-11) con lo medido** ([`diseno.md`](diseno.md) §2). Rutas del Edge
> sobre `edge/wapp-edge-agent` @ `38e5f91`. 🟡 Pendiente de la aprobación de Jhoan.

## 1 · Hoy

```
WhatsApp ⇄ adapters/whatsmeow ─► puerta (Listener: 5 guardas, marca, Enqueue) ─► cola SQLite cifrada ─► despachador ─► adapters/cloudlink ⇄ Cloud
               ▲   lo CONSTRUYE app/sessionmgr (listen.go:110, pairing.go:55), no el arranque
               └── inyector (ADR-0041): fabrica un *events.Message y lo mete por handleEvent;
                   necesita el Listener del adaptador real ⇒ sin sesión viva, 409
```

Los puertos existen y sus firmas están **limpias** de tipos de `whatsmeow` (`internal/domain` no importa la librería).
El problema es otro: `internal/app/sessionmgr` **construye el adaptador concreto** y usa diez métodos suyos que no están
en ningún puerto. La costura (`m.newListener`, `m.newPairer`) existe, pero es privada.

## 2 · Destino

```
                        ┌─ adapters/whatsmeow       (real; por defecto)
transporte de sesión ───┤
(interfaz de sessionmgr)└─ adapters/whatsappfalso   (WAPP_AGENT_WHATSAPP_FALSO; grita; todo sale marcado sintético)
                               ├─ alta   : el emparejamiento de siempre, con un Connector falso ─► la DEK nace en el cliente
                               ├─ entrada: POST /v1/diag/inbound/inject ─► la MISMA puerta (Listener sin cliente) ─► cola, despachador, CloudLink
                               └─ salida : SendText / SendMedia se anotan en memoria ─► GET /v1/diag/outbound/captured
```

Se sustituye el **transporte** (el cliente de `whatsmeow`: socket, device, almacén), no la puerta de entrantes, que se
reutiliza tal cual.

- **La elección se hace una vez, en el arranque** (`internal/infra/daemon/daemon.go:226-227`), según la palanca. Ni la aplicación ni el
  dominio saben cuál de los dos corre.
- **Todo lo demás es el Edge real**: cola cifrada, despachador, lease, CloudLink con mTLS, cliente de inferencia.
  Eso es justo lo que el Edge falso de `test/procesos` no ejercita.

## 3 · Quién conduce la conversación

```
test/procesos (este repo)
   ├─ Postgres            (testcontainers, como hoy)
   ├─ cmd/server-modular  (como hoy; tras el relevo, cmd/server)
   └─ agent serve         (NUEVO: el binario real del Edge, con WhatsApp falso; sin wapp-ctl, sin cajero, sin Ollama)
          antes   :   agent enroll <código> contra el :8102 del arnés (enrolamiento real, no interactivo)
          entrada ◄── el caso inyecta «carrito», «1», «2»… por el socket unix de control
          salida  ──► el caso lee lo capturado y lo compara
```

El Edge falso del arnés **se queda**: es rápido y cubre el contrato. El modo «Edge real» es un caso (o un puñado) más,
no un sustituto (D-FW-5).

## 4 · Lo que no cambia

| Qué | Por qué |
|---|---|
| El contrato de CloudLink (`cloud/wapp-cloudlink`) | La fase no añade ni un campo al proto. La marca sintética ya viaja en el `WAMessageID` |
| La doble llave (ADR-0007, fuera de este repo: la DEK la custodia el cliente; el lease lo emite y revoca el servidor) | El falso no relaja el lease ni mueve la DEK |
| Sin Redis ni broker en el Edge (ADR-0003) | El registro de envíos es **memoria**, con tope ([`diseno.md`](diseno.md) §3) |
| El código de producción de este repo | Aquí solo cambian `test/procesos`, el `Makefile` y la documentación |
| El Edge con la palanca baja | Requisito RW.1.b |

## 5 · Riesgos conocidos

1. **Un transporte falso en un daemon de producción.** Se trata como el inyector: apagado por defecto, la superficie no
   existe con la palanca baja, grita, marca. El ADR de D-FW-1 lo deja escrito.
2. **El arranque sin device pareado.** ✅ Medido: el daemon arranca con cero sesiones; lo que exige device es `Listen`
   del adaptador real ([`diseno.md`](diseno.md) §2.2). La sesión falsa nace por el emparejamiento de siempre.
3. **La inferencia.** ✅ Medido: el carrito no la necesita; el Edge real sin cajero contesta `OLLAMA_DOWN`, que es lo que
   el caso ya espera ([`diseno.md`](diseno.md) §2.6).
4. **La DEK no es opcional aunque el falso no descifre nada**: la cola de entrantes sella con ella. Y en macOS su custodia
   es el Keychain, fijado en compilación: por **D-FW-7** (decidida el 2026-10-11) el Edge de la prueba corre en un
   **contenedor Linux** y la custodia no se toca. El coste lo paga el arnés ([`diseno.md`](diseno.md) §2.5, punto 7).
5. **El lease sin validador, o en modo sombra.** Sin `WAPP_AGENT_CLOUDLINK_LEASE_PUBKEY_PATH`, o con
   `WAPP_AGENT_CLOUDLINK_LEASE_SHADOW_MODE` encendida, el Edge envía con el lease revocado: el caso pone la primera y
   afirma que la segunda está apagada, o RW.3.c no mide nada.
