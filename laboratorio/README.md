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
| `-frame` | `script` | Quién contesta la inferencia: `script` (el guion de JSON fijos del caso Ámbar, determinista), `ollama` (el modelo de verdad) o `hybrid` (por etapa, ver abajo) |
| `-script-stages` | `p2,p3,p4` | Con `-frame hybrid`, las etapas que contesta el **guion**; el resto va a Ollama |
| `-model` | `gemma4:e2b-it-qat` | Modelo de Ollama. Es el del laboratorio, **no** el del producto |
| `-ollama` | `http://127.0.0.1:11434` | URL de Ollama |
| `-v` | apagado | Saca el log del cloud por stderr |

### El frame híbrido (`-frame hybrid`)

Con Gemma, el P2 real tumba 12 de 14 frases del banco (E10), y con él muere todo lo que viene
detrás. Eso **no se arregla aquí**: queda como deuda y expediente de análisis aparte. Para poder
seguir estudiando el flujo, `HybridFrame` (`common/frame_hybrid.go`) enruta **por etapa**
(`common.StageOf`): las de `-script-stages` las contesta el guion y el resto el modelo.

⚠️ **Es un andamio, no un arreglo.** Cada llamada se narra con quién contestó y, si fue el guion,
por qué —`⚠️ P2 por GUION: Gemma falla aquí, ver deuda`—, y el escalón cierra con el recuento
(`HybridFrame.Tally`). Medido el 2026-10-14 en E11: con `-script-stages p2`, Gemma hace P3 y P4 de la
ráfaga Ámbar y el borrador sale con sus 3 líneas casadas. Para usar el modelo todo lo posible, esa es
la combinación.

## Qué hay

🔤 El **código** va en inglés, como el resto del repo (ficheros, tipos, funciones, banderas); los
comentarios y lo que el narrador imprime, en español. Solo el directorio `laboratorio/` y la etiqueta
`laboratorio` se quedan en español.

| Ruta | Qué es |
|---|---|
| `common/` | El andamio (paquete `common`): `ScriptFrame`, `OllamaFrame` y `HybridFrame` (tres `local.Frame`), `SpyFrame`, `NewSelector` (el `llmvia.Selector` real en vía local), `RegisterFlags`/`FrameFromFlags`, `Clock` manual, `NewCipher` (el de la PII, real y en memoria), el caso Ámbar (`AmberCatalog`, `AmberBurst`, `AmberIntents`, `AmberP1Input`…), el banco (`LoadPhrases`, `Compare`), las cinco etapas armadas como el arranque (`NewIntakeStages`), la bandeja (`NewInbox`, con la costura `IntakeTable`) y el `Narrator` |
| `data/` | Los datos que leen los escalones: `phrases.json`, el banco de frases de E03 (14 casos) |
| `e00-smoke/` | Una P1 (o una P2 con `-stage p2`; otra frase con `-phrase`) por el selector real. Si sale, el andamio está sano |
| `e09-window/` | **E09** · la ventana de captación sola: silencio, techo, segunda ventana, sin inferencia |
| `e10-pipeline/` | **E10** · P2 → P3 → P4 → Match → Draft con texto directo, frente al banco (`-all`) |
| `e11-window-to-draft/` | **E11** · ventana → compositor → worker → borrador `pending_approval` |
| `e12-ahead/` | **E12** · el adelanto P1 (`intakeahead`): cuándo cierra la ventana antes de tiempo |
| `e13-inbox/` | **E13** · la bandeja sin LLM: corregir, pedir información, aprobar, rechazar, descartar y exportar, con `intakes.Service` real |
| `e14-quote-and-reanalysis/` | **E14** · P5 (`quotetext`, con el verificador de precios) y el re-análisis (`reanalisis`) recorrido por el worker |

🔴 **Las costuras que no existen en producción**, y cada escalón las dice en pantalla: E11 copia la fila
cerrada de la cola de la ventana a la máquina del worker (dos dobles de la misma `intake_jobs`), y E13
y E14 usan `common.IntakeTable` para que el borrador y la bandeja vean la misma fila de `intakes`
(además responde `ReanalysisTargetOf`, que `intakes.MemoryStore` no implementa).

La costura es `local.Frame` (`internal/modulos/inferencia/llmvia/local/local.go`): con ella se usan los
prompts, los techos por etapa, el plazo heredado y el parseo **reales**, y solo se sustituye el viaje
al Edge.

## Dónde está la guía

Las fichas del alumno (qué aprendes, qué es real y qué es doble, «Rómpelo», y lo ejecutado) viven en
la documentación de la raíz del ecosistema wApp, en `documentations/operacion/laboratorio/`, y el plan
en `documentations/planes/abiertos/058-laboratorio-de-flujos/`. Este repo solo guarda el código.
