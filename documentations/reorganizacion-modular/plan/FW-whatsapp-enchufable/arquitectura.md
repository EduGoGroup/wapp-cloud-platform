# FW · Arquitectura

> Vista macro. 🟡 Es la **intención**; el dibujo exacto lo corrige FW-01 (TW.3) con lo que mida. Rutas del Edge sobre
> `edge/wapp-edge-agent` @ `38e5f91`.

## 1 · Hoy

```
WhatsApp ⇄ adapters/whatsmeow ──(puertos de internal/app)──► cola SQLite cifrada ─► despachador ─► adapters/cloudlink ⇄ Cloud
               ▲   Connector · ListenGateway · LiveSender
               └── inyector (ADR-0041): fabrica un *events.Message y lo mete por handleEvent;
                   necesita el Listener del adaptador real ⇒ sin sesión viva, 409
```

Los puertos existen, pero solo tienen **una** implementación de producción, y la aplicación conoce tipos de `whatsmeow`
(`internal/app/sessionmgr`, `internal/domain`).

## 2 · Destino

```
                 ┌─ adapters/whatsmeow   (real; por defecto)
puertos de app ──┤
                 └─ adapters/<falso>     (palanca; grita; todo sale marcado sintético)
                        ├─ entrada: una ruta local hace entrar un mensaje ─► MISMA cola, despachador y CloudLink
                        └─ salida : SendText / SendMedia se anotan en un registro legible desde fuera
```

- **La elección se hace una vez, en el arranque** (`internal/infra/daemon`), según la palanca. Ni la aplicación ni el
  dominio saben cuál de los dos corre.
- **Todo lo demás es el Edge real**: cola cifrada, despachador, lease, CloudLink con mTLS, cliente de inferencia.
  Eso es justo lo que el Edge falso de `test/procesos` no ejercita.

## 3 · Quién conduce la conversación

```
test/procesos (este repo)
   ├─ Postgres            (testcontainers, como hoy)
   ├─ cmd/server-modular  (como hoy; tras el relevo, cmd/server)
   └─ wapp-edge-agent     (NUEVO: el binario real, con WhatsApp falso)
          entrada ◄── el caso inyecta «carrito», «1», «2»…
          salida  ──► el caso lee lo capturado y lo compara
```

El Edge falso del arnés **se queda**: es rápido y cubre el contrato. El modo «Edge real» es un caso (o un puñado) más,
no un sustituto (D-FW-5).

## 4 · Lo que no cambia

| Qué | Por qué |
|---|---|
| El contrato de CloudLink (`cloud/wapp-cloudlink`) | La fase no añade ni un campo al proto. La marca sintética ya viaja en el `WAMessageID` |
| La doble llave (ADR-0007, fuera de este repo: la DEK la custodia el cliente; el lease lo emite y revoca el servidor) | El falso no relaja el lease ni mueve la DEK |
| Sin Redis ni broker en el Edge (ADR-0003) | El registro de envíos es memoria o SQLite; lo decide TW.3 |
| El código de producción de este repo | Aquí solo cambian `test/procesos`, el `Makefile` y la documentación |
| El Edge con la palanca baja | Requisito RW.1.b |

## 5 · Riesgos conocidos

1. **Un transporte falso en un daemon de producción.** Se trata como el inyector: apagado por defecto, la superficie no
   existe con la palanca baja, grita, marca. El ADR de D-FW-1 lo deja escrito.
2. **El arranque sin device pareado.** Hoy la escucha parte de un device cargado del almacén cifrado. Cuánto hay que
   abrir para arrancar sin él está **sin medir** (TW.1).
3. **La inferencia.** El Edge sirve la inferencia contra Ollama. En local, el caso necesita un Ollama o un doble de él:
   lo decide TW.3 (el arnés de F9 ya resuelve esto para el Edge falso; ver [`F9-procesos/diseno.md`](../F9-procesos/diseno.md)).
