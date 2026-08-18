package fleet_test

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
)

// i64 devuelve un puntero al valor dado (azúcar para armar snapshots).
func i64(v int64) *int64 { return &v }

// requirePunteroInt64 exige que el puntero esté PRESENTE y valga lo esperado. Las dos
// mitades van siempre juntas a propósito: distinguir «no lo sé» (nil) de un valor
// MEDIDO es el corazón de T4.3, y comprobar solo una de ellas dejaría pasar justo el
// fallo que duele (un 0 medido degradado a NULL, o un NULL leído como 0). El mensaje
// lo pone el llamante para no perder el porqué de cada aserción.
func requirePunteroInt64(t *testing.T, got *int64, want int64, format string, args ...any) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf(format, args...)
	}
}

// TestMemorySaveHealthWorkerUnknownIsNotZero es el test central de la regla de
// lectura del Plan 051 · T4.3: un snapshot que NO SABE el bloque del worker
// (taskset vacío, p50 nil, mapa nil, contadores nil) se persiste como DESCONOCIDO
// y no como cero. Si alguien "simplifica" los punteros a int64, este test cae.
func TestMemorySaveHealthWorkerUnknownIsNotZero(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()
	if err := repo.MarkOnline(ctx, "t", "e", "s1"); err != nil {
		t.Fatalf("MarkOnline: %v", err)
	}

	// Edge que no sabe nada del worker: solo salud del socket. El mapa va NIL a
	// propósito (Edge viejo): ni la escritura ni la lectura pueden romperse por él.
	if err := repo.SaveHealth(ctx, "t", "e", "s1", fleet.HealthSnapshot{
		WhatsappState: "connected", BinaryVersion: "v0.12.0",
	}); err != nil {
		t.Fatalf("SaveHealth sin bloque de worker: %v", err)
	}

	s := getSession(t, repo, "t", "e", "s1")
	if s.WorkerTaskset != "" {
		t.Fatalf("taskset desconocido debe quedar vacío, nunca 'disjunta': %q", s.WorkerTaskset)
	}
	if s.IntentP50Ms != nil {
		t.Fatalf("p50 no medible debe quedar nil, nunca 0 ms: %v", *s.IntentP50Ms)
	}
	if s.IntentOmittedByReason != nil {
		t.Fatalf("desglose no reportado debe quedar nil: %v", s.IntentOmittedByReason)
	}
	for name, got := range map[string]*int64{
		"stuck_heads": s.StuckHeads, "stuck_head_polls": s.StuckHeadPolls,
		"failed_seal_dispatch": s.FailedSealDispatch, "failed_seal_budget": s.FailedSealBudget,
	} {
		if got != nil {
			t.Fatalf("%s sin reportar debe quedar nil, nunca 0: %v", name, *got)
		}
	}

	// Leer e ITERAR un mapa nil no puede romper nada (regla 4 del contrato).
	if n := s.IntentOmittedByReason["breaker"]; n != 0 {
		t.Fatalf("leer un mapa nil debe dar el cero: %d", n)
	}
	vueltas := 0
	for range s.IntentOmittedByReason {
		vueltas++
	}
	if vueltas != 0 {
		t.Fatalf("iterar un mapa nil debe dar cero vueltas: %d", vueltas)
	}
}

// TestMemorySaveHealthWorkerKnownRoundTrip verifica que el bloque del worker viaja
// entero, que un CERO MEDIDO se distingue de un desconocido (puntero presente con
// valor 0) y que los motivos se guardan uno a uno, SIN sumarse (INV-051.3).
func TestMemorySaveHealthWorkerKnownRoundTrip(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()
	if err := repo.MarkOnline(ctx, "t", "e", "s1"); err != nil {
		t.Fatalf("MarkOnline: %v", err)
	}

	razones := map[string]int64{"fastlane": 7, "presupuesto": 2, "breaker": 1}
	// FailedSealDispatch va con un cero MEDIDO: el puntero está presente, así que
	// «no ocurrió» sigue siendo distinguible de «no lo sé».
	if err := repo.SaveHealth(ctx, "t", "e", "s1", fleet.HealthSnapshot{
		WhatsappState: "connected", IntentCircuit: "open",
		WorkerTaskset: "solapada", IntentP50Ms: i64(1450),
		IntentOmittedByReason: razones, StuckHeads: i64(3),
		StuckHeadPolls: i64(11), FailedSealDispatch: i64(0), FailedSealBudget: i64(4),
	}); err != nil {
		t.Fatalf("SaveHealth con bloque de worker: %v", err)
	}

	s := getSession(t, repo, "t", "e", "s1")
	if s.WorkerTaskset != "solapada" {
		t.Fatalf("taskset: got %q, want solapada", s.WorkerTaskset)
	}
	if s.IntentCircuit != "open" {
		t.Fatalf("el breaker abierto debe llegar entero: got %q", s.IntentCircuit)
	}
	requirePunteroInt64(t, s.IntentP50Ms, 1450, "p50: %v, want 1450", s.IntentP50Ms)
	requirePunteroInt64(t, s.FailedSealDispatch, 0,
		"un 0 MEDIDO debe llegar como puntero a 0, no como nil: %v", s.FailedSealDispatch)
	requirePunteroInt64(t, s.FailedSealBudget, 4,
		"failed_seal_budget: %v, want 4", s.FailedSealBudget)
	// Los dos sellos van SEPARADOS (T3.12): solo el de DESPACHO implica duplicados,
	// así que el 0 de uno y el 4 del otro tienen que seguir siendo distinguibles.
	if *s.FailedSealDispatch == *s.FailedSealBudget {
		t.Fatal("failed_seal_dispatch y failed_seal_budget no pueden colapsar en un valor (T3.12)")
	}
	// Los motivos, uno a uno. Sumarlos borraría que 'fastlane' es el camino SANO.
	for k, want := range razones {
		if got := s.IntentOmittedByReason[k]; got != want {
			t.Fatalf("motivo %q: got %d, want %d", k, got, want)
		}
	}
	if len(s.IntentOmittedByReason) != len(razones) {
		t.Fatalf("el desglose no puede ganar ni perder claves: %v", s.IntentOmittedByReason)
	}

	// Copia DEFENSIVA: mutar el mapa del llamante no puede cambiar lo persistido.
	razones["breaker"] = 999
	if s2 := getSession(t, repo, "t", "e", "s1"); s2.IntentOmittedByReason["breaker"] != 1 {
		t.Fatalf("el repositorio no debe compartir respaldo con el llamante: %v", s2.IntentOmittedByReason)
	}
}

// TestMemorySaveHealthWorkerRancidClearsPrevious cubre la regla de RANCIDEZ del
// Edge: cuando el parte del worker lleva más de 90 s sin refrescarse, el Edge manda
// las tres señales a su cero A PROPÓSITO. El Cloud debe BORRAR el valor anterior,
// no conservarlo: mantener un "disjunta" viejo es publicar una salud inventada.
func TestMemorySaveHealthWorkerRancidClearsPrevious(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()
	if err := repo.MarkOnline(ctx, "t", "e", "s1"); err != nil {
		t.Fatalf("MarkOnline: %v", err)
	}

	if err := repo.SaveHealth(ctx, "t", "e", "s1", fleet.HealthSnapshot{
		WhatsappState: "connected", WorkerTaskset: "disjunta", IntentP50Ms: i64(820),
		IntentOmittedByReason: map[string]int64{"fastlane": 5},
	}); err != nil {
		t.Fatalf("SaveHealth fresco: %v", err)
	}
	if s := getSession(t, repo, "t", "e", "s1"); s.WorkerTaskset != "disjunta" {
		t.Fatalf("precondición: el taskset fresco debe estar puesto: %q", s.WorkerTaskset)
	}

	// Parte rancio: las tres señales llegan a su cero.
	if err := repo.SaveHealth(ctx, "t", "e", "s1", fleet.HealthSnapshot{
		WhatsappState: "connected",
	}); err != nil {
		t.Fatalf("SaveHealth rancio: %v", err)
	}
	s := getSession(t, repo, "t", "e", "s1")
	if s.WorkerTaskset != "" || s.IntentP50Ms != nil || s.IntentOmittedByReason != nil {
		t.Fatalf("un parte rancio debe dejar el bloque del worker en DESCONOCIDO: %+v", s)
	}
}
