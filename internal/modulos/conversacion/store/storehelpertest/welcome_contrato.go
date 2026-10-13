package storehelpertest

import (
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Los casos de WelcomeStore: TouchContact y MarkWelcomed sobre conversation_welcomes. Los
// instantes los pone el LLAMANTE (el reloj del runtime, no el de la base): la suite pasa los
// suyos, de agosto de 2026 y en segundos enteros. La carrera está en concurrency_contrato.go.

// mustTouch registra que el contacto escribió en `now` y devuelve lo que TouchContact devuelve.
func mustTouch(t *testing.T, m Montaje, k store.Key, now time.Time) WelcomeMark {
	t.Helper()
	got, err := m.Store.TouchContact(ctx, k, now)
	if err != nil {
		t.Fatalf("TouchContact(%s, %v): %v", k, now, err)
	}
	return got
}

// requireMark afirma el resultado de un MarkWelcomed: lo que devuelve y que no dé error.
func requireMark(t *testing.T, m Montaje, what string, k store.Key, witness WelcomeMark, now time.Time, want bool) {
	t.Helper()
	got, err := m.Store.MarkWelcomed(ctx, k, witness, now)
	if err != nil || got != want {
		t.Errorf("%s: MarkWelcomed = (%v, %v), quería (%v, nil)", what, got, err, want)
	}
}

// requireRow afirma la fila guardada de la bienvenida de una clave.
func requireRow(t *testing.T, m Montaje, what string, k store.Key, lastIncoming, welcomed time.Time) {
	t.Helper()
	requireSameWelcome(t, what, m.Welcome(t, k.TenantID, k.SessionID, k.ContactID),
		WelcomeMark{LastIncomingAt: lastIncoming, WelcomedAt: welcomed})
}

// touchAll toca las cuatro claves de la fixture, cada una en un instante distinto, y devuelve el de
// cada una.
func touchAll(t *testing.T, fx fixture) map[store.Key]time.Time {
	t.Helper()
	touched := make(map[store.Key]time.Time, 4)
	for i, k := range append([]store.Key{fx.key}, fx.witnesses()...) {
		touched[k] = at(10, 8+i)
		mustTouch(t, fx.m, k, touched[k])
	}
	return touched
}

// caseTouchFirstTime: el primer mensaje de un contacto por una sesión devuelve la marca CERO
// («nunca habló, nunca se le saludó») y crea su fila, con ese instante y sin bienvenida. Aunque
// las tres vecinas —otro contacto, otra sesión, otro tenant— ya tengan fila y bienvenida: el
// estado previo es el de ESA clave, y ninguna vecina se mueve.
func caseTouchFirstTime(t *testing.T, m Montaje) {
	fx := newFixture(m)
	for i, k := range fx.witnesses() {
		requireMark(t, m, "sello de una vecina", k, mustTouch(t, m, k, at(10, 8+i)), at(11, 8+i), true)
	}
	before := fx.take(t)

	got := mustTouch(t, m, fx.key, at(25, 9))
	requireSameWelcome(t, "primer TouchContact", got, WelcomeMark{})
	requireRow(t, m, "fila tras el primer toque", fx.key, at(25, 9), time.Time{})
	requireRestUntouched(t, "primer TouchContact", before, fx.take(t), forgetWelcome(fx.key))
}

// caseTouchPrevious: TouchContact devuelve el estado que había ANTES del toque, no el que deja.
// Es toda la función: el silencio se mide contra el mensaje ANTERIOR del contacto, y si devolviera
// el estado ya tocado valdría siempre cero y la bienvenida no volvería nunca tras la primera.
func caseTouchPrevious(t *testing.T, m Montaje) {
	fx := newFixture(m)
	t1, t2, t3 := at(25, 9), at(25, 9).Add(90*time.Second), at(26, 15)

	mustTouch(t, m, fx.key, t1)
	got := mustTouch(t, m, fx.key, t2)
	requireSameWelcome(t, "segundo TouchContact", got, WelcomeMark{LastIncomingAt: t1})
	requireRow(t, m, "fila tras el segundo toque", fx.key, t2, time.Time{})

	got = mustTouch(t, m, fx.key, t3)
	requireSameWelcome(t, "tercer TouchContact", got, WelcomeMark{LastIncomingAt: t2})
	requireRow(t, m, "fila tras el tercer toque", fx.key, t3, time.Time{})

	// El instante es el que pasa el llamante, también si es ANTERIOR al guardado: no hay máximo.
	got = mustTouch(t, m, fx.key, t1)
	requireSameWelcome(t, "TouchContact con un instante anterior", got, WelcomeMark{LastIncomingAt: t3})
	requireRow(t, m, "fila tras el toque con un instante anterior", fx.key, t1, time.Time{})
}

// caseTouchKeepsWelcomed: el toque mueve last_incoming_at y NUNCA welcomed_at: una bienvenida ya
// sellada sobrevive a todos los mensajes posteriores, y TouchContact la devuelve como testigo.
func caseTouchKeepsWelcomed(t *testing.T, m Montaje) {
	fx := newFixture(m)
	t1, t2, t3 := at(25, 9), at(25, 10), at(26, 16)

	witness := mustTouch(t, m, fx.key, t1)
	requireMark(t, m, "sello", fx.key, witness, t2, true)

	got := mustTouch(t, m, fx.key, t3)
	requireSameWelcome(t, "TouchContact tras el sello", got, WelcomeMark{LastIncomingAt: t1, WelcomedAt: t2})
	requireRow(t, m, "fila tras tocar una conversación ya saludada", fx.key, t3, t2)
}

// caseTouchKeyIsolation: la clave es (tenant, sesión, contacto) entera. Con las cuatro filas
// sembradas —la tocada y tres que difieren de ella en UNA componente—, tocar una mueve esa y solo
// esa, y devuelve SU estado previo, no el de una vecina.
func caseTouchKeyIsolation(t *testing.T, m Montaje) {
	fx := newFixture(m)
	touched := touchAll(t, fx)
	requireMark(t, m, "sello del testigo de otro contacto", fx.otherContact, WelcomeMark{}, at(11, 0), true)
	before := fx.take(t)

	got := mustTouch(t, m, fx.key, at(20, 12))
	requireSameWelcome(t, "TouchContact", got, WelcomeMark{LastIncomingAt: touched[fx.key]})
	requireRow(t, m, "fila tocada", fx.key, at(20, 12), time.Time{})
	requireRestUntouched(t, "TouchContact", before, fx.take(t), forgetWelcome(fx.key))
}

// caseMarkMatching: con el testigo que TouchContact devolvió, MarkWelcomed sella la bienvenida:
// devuelve true y escribe welcomed_at con el instante dado. Solo eso: last_incoming_at no se
// mueve, y del testigo solo cuenta su WelcomedAt.
func caseMarkMatching(t *testing.T, m Montaje) {
	fx := newFixture(m)
	t1, t2 := at(25, 9), at(25, 10)
	witness := mustTouch(t, m, fx.key, t1)
	before := fx.take(t)

	// El LastIncomingAt del testigo no participa en la comparación.
	witness.LastIncomingAt = at(1, 1)
	requireMark(t, m, "primera bienvenida", fx.key, witness, t2, true)
	requireRow(t, m, "fila tras la primera bienvenida", fx.key, t1, t2)
	requireRestUntouched(t, "MarkWelcomed", before, fx.take(t), forgetWelcome(fx.key))
}

// caseMarkStale: si welcomed_at ya no es el del testigo —otro turno ganó la carrera—, MarkWelcomed
// devuelve false SIN error y no escribe nada. Vale para el testigo cero sobre una fila ya
// saludada, para un testigo con otra fecha y para un testigo con fecha sobre una fila sin saludar.
func caseMarkStale(t *testing.T, m Montaje) {
	fx := newFixture(m)
	t1, t2, t3 := at(25, 12), at(26, 13), at(27, 14)

	mustTouch(t, m, fx.key, t1)
	before := fx.take(t)
	requireMark(t, m, "testigo con fecha sobre una fila sin saludar", fx.key, WelcomeMark{WelcomedAt: t1}, t2, false)
	requireUntouched(t, "testigo con fecha sobre una fila sin saludar", before, fx.take(t))

	requireMark(t, m, "primera bienvenida", fx.key, WelcomeMark{}, t1, true)
	before = fx.take(t)
	requireMark(t, m, "testigo cero rancio", fx.key, WelcomeMark{}, t2, false)
	requireMark(t, m, "testigo con otra fecha", fx.key, WelcomeMark{LastIncomingAt: t1, WelcomedAt: t3}, t2, false)
	requireUntouched(t, "testigo rancio", before, fx.take(t))
	requireRow(t, m, "fila tras los testigos rancios", fx.key, t1, t1)
}

// caseMarkSecond: la marca vuelve a ponerse cada vez que el contacto reaparece tras el silencio,
// así que el centinela compara contra la marca VIGENTE, no contra «nunca saludado»: con el testigo
// de la bienvenida anterior, la segunda —y la tercera— se sellan.
func caseMarkSecond(t *testing.T, m Montaje) {
	fx := newFixture(m)
	t1, t2, t3 := at(25, 12), at(26, 13), at(27, 14)

	requireMark(t, m, "primera bienvenida", fx.key, mustTouch(t, m, fx.key, t1), t1, true)
	witness := mustTouch(t, m, fx.key, t2)
	requireSameWelcome(t, "testigo de la segunda", witness, WelcomeMark{LastIncomingAt: t1, WelcomedAt: t1})
	requireMark(t, m, "segunda bienvenida", fx.key, witness, t2, true)
	requireRow(t, m, "fila tras la segunda bienvenida", fx.key, t2, t2)

	// El testigo que acaba de servir ya es rancio; el nuevo sí vale.
	requireMark(t, m, "el testigo de la segunda, otra vez", fx.key, witness, t3, false)
	requireMark(t, m, "tercera bienvenida", fx.key, mustTouch(t, m, fx.key, t3), t3, true)
	requireRow(t, m, "fila tras la tercera bienvenida", fx.key, t3, t3)
}

// caseMarkNoRow: sin fila, MarkWelcomed devuelve false sin error y NO la crea: solo puede llegar
// después de un TouchContact, y fabricarla taparía un orden de llamadas equivocado. Que las tres
// vecinas tengan fila no cambia nada.
func caseMarkNoRow(t *testing.T, m Montaje) {
	fx := newFixture(m)
	for i, k := range fx.witnesses() {
		mustTouch(t, m, k, at(10, 8+i))
	}
	before := fx.take(t)
	requireMark(t, m, "sin fila", fx.key, WelcomeMark{}, at(25, 12), false)
	requireMark(t, m, "sin fila, con un testigo con fecha", fx.key, WelcomeMark{WelcomedAt: at(10, 8)}, at(25, 12), false)
	requireUntouched(t, "MarkWelcomed sin fila", before, fx.take(t))
	requireRow(t, m, "MarkWelcomed sin fila", fx.key, time.Time{}, time.Time{})
}

// caseMarkKeyIsolation: con las cuatro filas sembradas y sin saludar —o sea, las cuatro casarían
// con el mismo testigo—, sellar una sella esa y solo esa.
func caseMarkKeyIsolation(t *testing.T, m Montaje) {
	fx := newFixture(m)
	touched := touchAll(t, fx)
	before := fx.take(t)

	requireMark(t, m, "sello", fx.key, WelcomeMark{}, at(25, 12), true)
	requireRow(t, m, "fila sellada", fx.key, touched[fx.key], at(25, 12))
	requireRestUntouched(t, "MarkWelcomed", before, fx.take(t), forgetWelcome(fx.key))
}
