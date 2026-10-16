//go:build laboratorio

package common

import (
	"sync"
	"time"
)

// Clock es un reloj manual: solo se mueve cuando el escalón lo dice. Su método Now es un
// func() time.Time, que es lo que esperan TODAS las opciones de reloj del cloud
// (runtime.WithClock y WithAggregatorClock, pipeline.WithClock, stages.WithClock, events.WithClock,
// intakes.WithClock, llmvia.WithClock…): se pasa como `clock.Now`, sin paréntesis.
//
// Es seguro entre goroutines.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// AmberStart es un instante fijo y legible para empezar: lunes 13 de julio de 2026, 9:55 en UTC (el
// message_ts del caso Ámbar en los tests de P4).
var AmberStart = time.Date(2026, 7, 13, 9, 55, 0, 0, time.UTC)

// NewClock crea un reloj parado en start (cero ⇒ AmberStart).
func NewClock(start time.Time) *Clock {
	if start.IsZero() {
		start = AmberStart
	}
	return &Clock{now: start}
}

// Now devuelve la hora del reloj.
func (r *Clock) Now() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.now
}

// Advance mueve el reloj d hacia delante y devuelve la hora nueva.
func (r *Clock) Advance(d time.Duration) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = r.now.Add(d)
	return r.now
}

// Set pone el reloj en t.
func (r *Clock) Set(t time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = t
}
