// Package inferencia declara el TIPO que la telemetría de inferencia del Edge
// entrega a internal/platform/metrics para publicarlo en /metrics: el agregado de
// la flota. Solo el tipo; ni Prometheus ni estado.
//
// # Por qué un paquete hoja y no platform/metrics (F0 · D-F0-3)
//
// platform no debe importar dominio, así que el tipo se declara aquí e
// internal/modulos/edge/inferstats lo re-exporta como ALIAS
// (`inferstats.Aggregate = inferencia.Agregado`). Declararlo en platform/metrics
// obligaría a inferstats a arrastrar Prometheus, contra su propia doc («No
// conoce Prometheus»).
package inferencia

// Agregado es la suma de la flota, listo para publicar.
type Agregado struct {
	// PorRegimen, PorClase y OmitidasPorMotivo suman los acumulados de todos los
	// Edges vivos, clave a clave.
	PorRegimen        map[string]int64
	PorClase          map[string]int64
	OmitidasPorMotivo map[string]int64
	// MuestrasPrefill y MuestrasGeneracion suman el `n` de los Edges QUE LO REPORTAN.
	// nil cuando no lo reporta ninguno — que es «no medible», no «cero muestras».
	MuestrasPrefill    *int64
	MuestrasGeneracion *int64
	// Edges es cuántos Edges sostienen el agregado. Se publica porque una suma de la
	// flota sin saber sobre cuántos se hizo es la misma trampa que un cuantil sin su
	// `n`: dice poco y parece decir mucho.
	Edges int
}
