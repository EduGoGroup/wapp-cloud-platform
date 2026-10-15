//go:build laboratorio

// Package common es el andamio que comparten los escalones del laboratorio de flujos (Plan 058).
//
// # Qué es el laboratorio, y qué NO es
//
// Un laboratorio PEDAGÓGICO: cada escalón (`laboratorio/eNN-<nombre>/main.go`) estudia UNA pieza del
// cloud dejándola real y doblando lo que la rodea, y cuenta por pantalla qué pasó y por qué. NO es un
// test de integración: no corre solo, no afirma nada y puede doblar cifrado, llaves o transporte si
// estorban a lo que se estudia.
//
// Por eso TODO fichero de `laboratorio/` empieza por `//go:build laboratorio`: `go build ./...`,
// `go vet ./...`, `go test ./...`, `make ci-local`, `make ci-docker`, golangci-lint y los candados de
// `internal/candados` no lo ven. Se compila y se corre a mano, siempre desde la raíz del repo:
//
//	GOTOOLCHAIN=go1.26.5 GOWORK=off go build -tags laboratorio ./laboratorio/...
//	GOTOOLCHAIN=go1.26.5 GOWORK=off go run -tags laboratorio ./laboratorio/e00-smoke -frame script
//
// ⚠️ Precio conocido: como CI no lo compila, un cambio del código puede romper un escalón sin que nadie
// se entere. El primer paso de cada sesión es el `go build -tags laboratorio` de arriba.
//
// # Lo que hay aquí
//
//   - ScriptFrame y OllamaFrame: dos implementaciones de local.Frame, la costura por la que la vía local
//     manda una inferencia al Edge. Con ellas los escalones usan los prompts y el parseo REALES
//     (wapp-shared/llm) y solo se sustituye el viaje al Edge. SpyFrame envuelve a cualquiera de las
//     dos para enseñar qué viajó por el cable.
//   - NewSelector: el llmvia.Selector real sobre la configuración en memoria (sin fila ⇒ vía local).
//   - Flags: -frame script|ollama, -model, -ollama, comunes a todos los escalones.
//   - Clock: un reloj manual para las opciones WithClock (todas son func() time.Time).
//   - NewCipher: el cifrador REAL de la PII de negocio, con una KEK aleatoria en memoria.
//   - El caso Ámbar: catálogo, ráfaga e intenciones, los mismos del arnés de test/procesos.
//   - Narrator: imprime los pasos numerados con «qué pasó y por qué».
package common
