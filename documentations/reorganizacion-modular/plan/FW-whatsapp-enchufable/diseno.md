# FW · Diseño

> 🟡 **Esqueleto.** El contrato de cada fichero no se puede escribir sin el inventario de TW.1; FW-01 rellena este
> documento (TW.3) y Jhoan lo aprueba antes de FW-02. Lo que sigue es lo ya medido y las preguntas que TW.1 contesta.

## 1 · Lo medido (2026-10-11, `edge/wapp-edge-agent` @ `38e5f91`)

| Pieza | Dónde | Qué promete hoy |
|---|---|---|
| `Connector` | `internal/app/pair.go:67` | Emparejar: `StartConnection(ctx, dek)` devuelve señales (QR, Connected, Error) |
| `ListenGateway` | `internal/app/listen.go:80` | `Listen(ctx, dek)`: conecta, anota los entrantes en la cola durable y bloquea hasta cancelar |
| `LiveSender` | `internal/app/listen.go:91` | Envía por el cliente vivo: texto, texto con `command_id`, media |
| Adaptador real | `internal/adapters/whatsmeow/` (9 ficheros de producción) | Cumple los tres puertos |
| Inyector | `internal/adapters/whatsmeow/inyector.go` (283 líneas), `internal/app/sessionmgr/inyector.go`, `internal/adapters/control/diag/inyector.go` (405) | Ruta `/v1/diag/inbound/inject`, grant `edge.diag.inbound.inject`, tope de 500 por petición, `409` sin escucha viva |
| Registro condicional | `internal/infra/daemon/daemon.go:476-496` | Con la palanca baja la ruta **no se registra** |

## 2 · Lo que TW.1 tiene que contestar

1. ¿Qué tipos de `whatsmeow` cruzan los puertos o viven en `internal/domain` y en `internal/app/sessionmgr`? Lista con
   `fichero:línea`, y cuáles estorban al falso.
2. ¿Qué exige el arranque para dar una sesión por «escuchando»: device pareado, almacén cifrado, DEK?
3. ¿El inyector actual se puede **reutilizar** sobre el falso (misma ruta, misma marca) o le hace falta un `Listener` de
   `whatsmeow`? De esto depende si la entrada es código nuevo o el existente con otro respaldo.
4. ¿Existe ya algún doble de `LiveSender` en los tests del Edge que se pueda promover?
5. ¿Qué necesita el Edge para arrancar contra un Cloud local: certificados mTLS, enrolamiento (`:8102`), configuración?
   ¿El arnés de `test/procesos` ya fabrica algo de eso para su Edge falso?
6. ¿Cómo se resuelve la inferencia en el caso de TW.8 (riesgo 3 de [`arquitectura.md`](arquitectura.md) §5)?

## 3 · Lo que se decide en TW.3 (y aprueba Jhoan)

| Pregunta | Opciones |
|---|---|
| Nombre de la palanca y del paquete del falso | El nombre efectivo en la máquina lleva el prefijo que compone el loader (`WAPP_AGENT_…`) |
| Entrada | Reutilizar `/v1/diag/inbound/inject` · una ruta propia |
| Salida | Ruta de lectura bajo `/v1/diag/` · fichero · los dos |
| Dónde se guarda lo capturado | Memoria (se pierde al reiniciar) · SQLite del Edge |
| Acuses | Qué acuse devuelve el falso y cuándo (entregado, leído) |

## 4 · Tests

- **En el Edge**: un test por promesa del falso (RW.1–RW.3) y el guardarraíl de la palanca baja. El molde son los 19 tests
  del inyector (`inyector_test.go`, `diag/inyector_test.go`), con su aserto de ausencia «un entrante real no se marca».
- **En este repo**: el caso de proceso de TW.8, con la etiqueta de build que fije TW.3, **fuera** de `make test-procesos`
  por defecto si necesita el repo del Edge al lado (este repo se compila solo; `GOWORK=off`).
