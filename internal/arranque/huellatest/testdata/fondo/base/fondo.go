// Package fondo es un paquete de prueba de huellatest.Goroutines y huellatest.Hooks: sus
// sentencias go y sus selectores de ganchos son lo que el contrato debe ver.
package fondo

import (
	"net/http"
	"runtime"
)

// Metricas imita *metrics.Metrics: sus métodos son los ganchos.
type Metricas struct{}

// InstrumentHTTP es un gancho.
func (*Metricas) InstrumentHTTP(h http.Handler) http.Handler { return h }

// RegisterDBStats es un gancho.
func (*Metricas) RegisterDBStats() {}

// Worker tiene un Run de puntero y un Lanzar de valor.
type Worker struct{}

// Run se lanza con go desde Arrancar.
func (w *Worker) Run() {}

// Lanzar lanza un literal de función desde un método.
func (w Worker) Lanzar() {
	go func() {}()
}

func tarea(int) {}

// Arrancar lanza cada forma de sentencia go que Goroutines normaliza.
func Arrancar(m *Metricas, srv *http.Server) {
	w := &Worker{}
	go w.Run()
	go tarea(1)
	go tarea(2)
	go func() {
		go func() { m.RegisterDBStats() }()
	}()
	go srv.ListenAndServe()
	go runtime.GC()
	_ = m.InstrumentHTTP(http.NotFoundHandler())
	_ = m.InstrumentHTTP(nil)
	w.Lanzar()
}
