package storehelpertest

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Los casos de las líneas de una solicitud: ReplaceIntakeItems y ListIntakeItems sobre
// intake_items.

// requireListMatchesObserver afirma que ListIntakeItems devuelve exactamente lo que hay guardado:
// las mismas líneas, en el mismo orden, con sus siete campos.
func requireListMatchesObserver(t *testing.T, m Montaje, what, intakeID string) {
	t.Helper()
	got, err := m.Store.ListIntakeItems(ctx, intakeID)
	if err != nil {
		t.Fatalf("%s: ListIntakeItems: %v", what, err)
	}
	requireSameItems(t, what+": ListIntakeItems", got, m.IntakeItems(t, intakeID))
}

// caseReplaceItemsMirrors: ReplaceIntakeItems deja las líneas de la solicitud EXACTAMENTE en lo
// que recibe, en su orden y sin acumular: escribir otra foto sustituye la anterior, y escribir dos
// veces la misma deja la misma. Fecha cada línea y le pone su solicitud; no toca la cabecera ni
// las líneas de otra solicitud.
func caseReplaceItemsMirrors(t *testing.T, m Montaje) {
	contact := uuid.NewString()
	seedIntakeWitnesses(t, m, contact)
	in := seedIntake(t, m, m.TenantA, contact, statusOpen)
	header := observedIntake(t, m, m.TenantA, in.ID)
	before := take(t, m)

	t0 := m.Now(t)
	mustReplaceItems(t, m, in.ID, line("CAFE", "Café", 2, 2.5))
	t1 := m.Now(t)
	first := requireLines(t, m, "primera foto", in.ID, line("CAFE", "Café", 2, 2.5))
	requireStamped(t, "primera foto: AddedAt", first[0].AddedAt, t0, t1)
	requireListMatchesObserver(t, m, "primera foto", in.ID)

	m.Advance(t)
	customized := line("CAFE", "Café", 5, 2.5)
	customized.Customization = "sin azúcar"
	second := []IntakeItem{customized, line("TE", "Té verde", 3, 2), line("CAFE", "Café", 1, 2.5)}
	for range 2 {
		mustReplaceItems(t, m, in.ID, second...)
		got := requireLines(t, m, "segunda foto", in.ID, second...)
		for i := range got {
			if !got[i].AddedAt.After(first[0].AddedAt) {
				t.Errorf("la línea %d de la segunda foto lleva AddedAt %v, quería posterior a %v", i, got[i].AddedAt, first[0].AddedAt)
			}
		}
		requireListMatchesObserver(t, m, "segunda foto", in.ID)
	}

	requireSameIntake(t, "la cabecera tras reemplazar líneas", observedIntake(t, m, m.TenantA, in.ID), header)
	requireRestUntouched(t, "ReplaceIntakeItems", before, take(t, m), forgetIntake(m.TenantA, in.ID))
}

// caseReplaceItemsEmpty: una foto vacía —nil o sin líneas— BORRA las líneas de cliente: es la foto
// de un carrito vacío, no un no-op. Solo las de esa solicitud.
func caseReplaceItemsEmpty(t *testing.T, m Montaje) {
	contact := uuid.NewString()
	seedIntakeWitnesses(t, m, contact)
	in := seedIntake(t, m, m.TenantA, contact, statusOpen)
	before := take(t, m)

	for _, empty := range [][]IntakeItem{nil, {}} {
		mustReplaceItems(t, m, in.ID, line("CAFE", "Café", 2, 2.5), line("TE", "Té", 1, 2))
		requireLines(t, m, "con dos líneas", in.ID, line("CAFE", "Café", 2, 2.5), line("TE", "Té", 1, 2))
		if err := m.Store.ReplaceIntakeItems(ctx, in.ID, empty); err != nil {
			t.Fatalf("ReplaceIntakeItems con la foto vacía: %v", err)
		}
		requireLines(t, m, "tras la foto vacía", in.ID)
		requireListMatchesObserver(t, m, "tras la foto vacía", in.ID)
	}
	requireUntouched(t, "ReplaceIntakeItems con la foto vacía", before, take(t, m))
}

// caseReplaceItemsPlatform: las líneas de LA PLATAFORMA (sku con el prefijo reservado "_", hoy la
// de envío) sobreviven a cualquier foto del carrito, intactas y al frente: el carrito escribe lo
// que armó el cliente y no puede tirar lo que puso wApp. Ni siquiera la foto vacía.
func caseReplaceItemsPlatform(t *testing.T, m Montaje) {
	in := seedIntake(t, m, m.TenantA, uuid.NewString(), statusOpen)
	shipping := line(platformSKU, "Envío", 1, 3000)
	mustReplaceItems(t, m, in.ID, shipping, line("CAFE", "Café", 1, 2.5))
	seeded := requireLines(t, m, "siembra", in.ID, shipping, line("CAFE", "Café", 1, 2.5))

	for _, photo := range [][]IntakeItem{
		{line("TE", "Té", 2, 2)},
		// Un sku que CONTIENE el guion bajo pero no empieza por él es de cliente: la foto
		// siguiente se lo lleva.
		{line("FLAN", "Flan", 1, 4), line("TE_VERDE", "_Té verde", 1, 2)},
		{line("TE", "Té", 1, 2)},
		nil,
	} {
		m.Advance(t)
		mustReplaceItems(t, m, in.ID, photo...)
		got := requireLines(t, m, "foto del carrito", in.ID, append([]IntakeItem{shipping}, photo...)...)
		if !got[0].AddedAt.Equal(seeded[0].AddedAt) {
			t.Errorf("la línea de la plataforma cambió de AddedAt: era %v y quedó %v", seeded[0].AddedAt, got[0].AddedAt)
		}
		requireListMatchesObserver(t, m, "foto del carrito", in.ID)
	}
}

// caseReplaceItemsSecondShipping: una solicitud tiene A LO SUMO una línea de envío. Escribirle una
// segunda —sobre la que ya tenía, o dos en la misma escritura, por ReplaceIntakeItems o por
// CloseIntake— se rechaza y no cambia NADA: ni las líneas que había ni la cabecera, y el cierre no
// cierra ni crea solicitud. Solo se afirma que hay error: su texto es de cada adaptador.
func caseReplaceItemsSecondShipping(t *testing.T, m Montaje) {
	contact, shipping := uuid.NewString(), line(platformSKU, "Envío", 1, 3000)
	in := seedIntake(t, m, m.TenantA, contact, statusOpen)
	bare := seedIntake(t, m, m.TenantA, uuid.NewString(), statusOpen)
	mustReplaceItems(t, m, in.ID, shipping, line("CAFE", "Café", 1, 2.5))
	mustReplaceItems(t, m, bare.ID, line("FLAN", "Flan", 1, 4))
	m.Advance(t)
	before := take(t, m)

	again := []IntakeItem{line(platformSKU, "Envío exprés", 1, 5000), line("TE", "Té", 2, 2)}
	if err := m.Store.ReplaceIntakeItems(ctx, in.ID, again); err == nil {
		t.Error("ReplaceIntakeItems aceptó una segunda línea de envío")
	}
	requireUntouched(t, "segunda línea de envío", before, take(t, m))

	if err := m.Store.ReplaceIntakeItems(ctx, bare.ID, []IntakeItem{shipping, line("TE", "Té", 1, 2), shipping}); err == nil {
		t.Error("ReplaceIntakeItems aceptó dos líneas de envío en la misma escritura")
	}
	requireUntouched(t, "dos líneas de envío en la misma escritura", before, take(t, m))

	closedID, err := m.Store.CloseIntake(ctx, store.IntakeClose{
		TenantID: m.TenantA, ContactID: contact, Total: 5004, CustomerNote: "no debe quedar",
		EventID: in.EventID, Items: again,
	})
	if err == nil || closedID != "" {
		t.Errorf("CloseIntake con una segunda línea de envío = (%q, %v), quería (\"\", un error)", closedID, err)
	}
	requireUntouched(t, "cierre con una segunda línea de envío", before, take(t, m))

	closedID, err = m.Store.CloseIntake(ctx, store.IntakeClose{
		TenantID: m.TenantA, ContactID: uuid.NewString(), Total: 6000,
		EventID: m.NewEvent(t, m.TenantA), Items: []IntakeItem{shipping, shipping},
	})
	if err == nil || closedID != "" {
		t.Errorf("CloseIntake sin abierta y con dos líneas de envío = (%q, %v), quería (\"\", un error)", closedID, err)
	}
	requireUntouched(t, "cierre sin abierta con dos líneas de envío", before, take(t, m))
}

// caseListItems: una solicitud sin líneas —o que no existe— da la lista vacía SIN error; un id que
// no es un UUID es un ERROR, no «sin líneas», para que el hueco típico (pasar la cadena vacía sin
// comprobar el found de quien resolvió el id) se vea en vez de disfrazarse de pedido vacío.
func caseListItems(t *testing.T, m Montaje) {
	in := seedIntake(t, m, m.TenantA, uuid.NewString(), statusOpen)
	for _, id := range []string{in.ID, uuid.NewString()} {
		if got, err := m.Store.ListIntakeItems(ctx, id); err != nil || len(got) != 0 {
			t.Errorf("ListIntakeItems(%s) sin líneas = (%+v, %v), quería vacía sin error", id, got, err)
		}
	}
	for _, id := range []string{"", "no-soy-un-uuid"} {
		got, err := m.Store.ListIntakeItems(ctx, id)
		wantPrefix := `store: listar líneas de solicitud: id "` + id + `" inválido: `
		if err == nil || !strings.HasPrefix(err.Error(), wantPrefix) {
			t.Errorf("ListIntakeItems(%q): err = %v, quería un error que empezara por %q", id, err, wantPrefix)
		}
		if got != nil {
			t.Errorf("ListIntakeItems(%q) devolvió %+v junto al error", id, got)
		}
	}
}
