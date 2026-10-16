# `laboratorio/` — el laboratorio de flujos del cloud

Programas para **estudiar una pieza** de `wapp-cloud-platform` de una en una (la ventana de captación,
el pipeline P2→P4, el carrito…): la pieza bajo estudio es **real**, lo que la rodea son **dobles**, y
cada programa cuenta por pantalla qué pasa y por qué. Es el Plan 058 del ecosistema.

## Por qué CI no lo ve

🔒 **No es un test de integración.** No corre solo, no afirma nada y puede doblar cifrado, llaves o
transporte si estorban a lo que se estudia. Por eso **todo `.go` de aquí empieza por
`//go:build laboratorio`**, y sin esa etiqueta no lo compila nadie: ni `go build ./...`, ni
`go vet ./...`, ni `go test ./...`, ni `make ci-local`, ni `make ci-docker`, ni golangci-lint, ni los
candados de `internal/candados`. Tampoco entra en el binario del servidor.

⚠️ El precio: un cambio del código puede romper un escalón sin que nadie se entere. **Lo primero de
cada sesión** es compilarlo.

## Cómo se corre

Siempre desde la raíz del repo, con la toolchain del `Makefile`:

```bash
GOTOOLCHAIN=go1.26.5 GOWORK=off go build -tags laboratorio ./laboratorio/...
GOTOOLCHAIN=go1.26.5 GOWORK=off go run -tags laboratorio ./laboratorio/e00-smoke -frame script
GOTOOLCHAIN=go1.26.5 GOWORK=off go run -tags laboratorio ./laboratorio/e00-smoke -frame ollama
```

Banderas comunes a todos los escalones (`common.RegisterFlags`):

| Bandera | Defecto | Qué hace |
|---|---|---|
| `-frame` | `script` | Quién contesta la inferencia: `script` (el guion de JSON fijos del caso Ámbar, determinista) u `ollama` (el modelo de verdad) |
| `-model` | `gemma4:e2b-it-qat` | Modelo de Ollama. Es el del laboratorio, **no** el del producto |
| `-ollama` | `http://127.0.0.1:11434` | URL de Ollama |
| `-v` | apagado | Saca el log del cloud por stderr |

## Qué hay

🔤 El **código** va en inglés, como el resto del repo (ficheros, tipos, funciones, banderas); los
comentarios y lo que el narrador imprime, en español. Solo el directorio `laboratorio/` y la etiqueta
`laboratorio` se quedan en español.

| Ruta | Qué es |
|---|---|
| `common/` | El andamio (paquete `common`): `ScriptFrame` y `OllamaFrame` (dos `local.Frame`), `SpyFrame`, `NewSelector` (el `llmvia.Selector` real en vía local), `RegisterFlags`/`FrameFromFlags`, `Clock` manual, `NewCipher` (el de la PII, real y en memoria), el caso Ámbar (`AmberCatalog`, `AmberBurst`, `AmberIntents`, `AmberP1Input`…) y el `Narrator` |
| `e00-smoke/` | Una P1 (o una P2 con `-stage p2`; otra frase con `-phrase`) por el selector real. Si sale, el andamio está sano |
| `eNN-<nombre>/` | Un escalón por pieza (llegan en las olas siguientes del plan) |

La costura es `local.Frame` (`internal/modulos/inferencia/llmvia/local/local.go`): con ella se usan los
prompts, los techos por etapa, el plazo heredado y el parseo **reales**, y solo se sustituye el viaje
al Edge.

## Dónde está la guía

Las fichas del alumno (qué aprendes, qué es real y qué es doble, «Rómpelo», y lo ejecutado) viven en
la documentación de la raíz del ecosistema wApp, en `documentations/operacion/laboratorio/`, y el plan
en `documentations/planes/abiertos/058-laboratorio-de-flujos/`. Este repo solo guarda el código.
