//go:build pendiente

package fleet

// La otra mitad de los tests del núcleo (E-13: se parte por tema): List con filas y sus errores,
// y el modo de fallo BLANDO del descifrado del self_pn al servir Get y List.

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// TestPostgresRepository_List_Rows: las filas vuelven en el orden en que las da la sentencia,
// cada una con su número descifrado, y sin avisos.
func TestPostgresRepository_List_Rows(t *testing.T) {
	f := newFixture(t)
	f.fake.script(reply{rows: [][]driver.Value{
		bareRow("edge-a", "s-1").withEnvelope(t, f.cipher, "34600111222").values(),
		bareRow("edge-a", "s-2").values(),
		bareRow("edge-b", "s-1").withEnvelope(t, f.cipher, "5491122334455").values(),
	}})
	got, err := f.repo.List(context.Background(), pgTenant)
	if err != nil {
		t.Fatalf("List: error inesperado %v", err)
	}
	want := []string{"edge-a/s-1/34600111222", "edge-a/s-2/", "edge-b/s-1/5491122334455"}
	if len(got) != len(want) {
		t.Fatalf("List devolvió %d sesiones, quería %d: %+v", len(got), len(want), got)
	}
	for i, s := range got {
		if key := s.EdgeID + "/" + s.SessionID + "/" + s.SelfPn; key != want[i] {
			t.Errorf("sesión %d = %q, quería %q", i, key, want[i])
		}
		if s.TenantID != pgTenant {
			t.Errorf("sesión %d: tenant %q, quería %q", i, s.TenantID, pgTenant)
		}
	}
	requireStatements(t, f.fake, statement{sqlList, []driver.Value{pgTenant}})
	if got := f.log.seen(); len(got) != 0 {
		t.Errorf("un listado que descifra bien dejó avisos: %+v", got)
	}
}

// TestPostgresRepository_List_Errors: cada fallo vuelve con su prefijo y SIN lista parcial.
//
// El cierre de las filas falla DESPUÉS de una iteración completa, y database/sql lo entrega por
// rows.Err(): por eso sale con el prefijo de la iteración y no con «fleet: cerrar filas: ».
func TestPostgresRepository_List_Errors(t *testing.T) {
	good := bareRow(pgEdge, pgSession).values()
	// La columna 15 (last_event_age_s) es un entero: un texto ahí no se puede escanear.
	bad := bareRow(pgEdge, "s-2").values()
	bad[14] = "no-es-un-entero"
	cases := []struct {
		name   string
		reply  reply
		prefix string
		cause  error
	}{
		{"query fails", reply{err: errBoom}, "fleet: listar sesiones: ", errBoom},
		{"iteration fails", reply{rows: [][]driver.Value{good}, endErr: errBoom}, "fleet: iterar sesiones: ", errBoom},
		{"close fails", reply{rows: [][]driver.Value{good}, closeErr: errBoom}, "fleet: iterar sesiones: ", errBoom},
		{"row does not scan", reply{rows: [][]driver.Value{good, bad}}, "fleet: escanear sesión: ", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, c.reply)
			got, err := f.repo.List(context.Background(), pgTenant)
			if got != nil {
				t.Errorf("List con error devolvió %d sesiones, quería nil", len(got))
			}
			if err == nil || !strings.HasPrefix(err.Error(), c.prefix) {
				t.Fatalf("err = %v, quería el prefijo %q", err, c.prefix)
			}
			if c.cause != nil && !errors.Is(err, c.cause) {
				t.Errorf("err = %q no envuelve la causa %q", err, c.cause)
			}
		})
	}
}

// TestPostgresRepository_List_BrokenBreakdownIsHard: un desglose de motivos que no es JSON tumba
// el listado (no es el fallo blando del self_pn).
func TestPostgresRepository_List_BrokenBreakdownIsHard(t *testing.T) {
	row := bareRow(pgEdge, pgSession)
	row.omitted = []byte("no-es-json")
	f := newFixture(t, reply{rows: [][]driver.Value{row.values()}})
	got, err := f.repo.List(context.Background(), pgTenant)
	const prefix = "fleet: escanear sesión: fleet: deserializar desglose de motivos: "
	if got != nil || err == nil || !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("List = (%+v, %v), quería (nil, error con el prefijo %q)", got, err, prefix)
	}
}

// TestPostgresRepository_Get_UndecryptableSelfPnIsSoft: un sobre que no abre NO tumba la lectura:
// la sesión sale entera, con SelfPn vacío, y queda UN aviso con la identidad opaca, el key_id que
// falta y la causa.
func TestPostgresRepository_Get_UndecryptableSelfPnIsSoft(t *testing.T) {
	f := newFixture(t)
	row := bareRow(pgEdge, pgSession).withEnvelope(t, foreignCipher(t), "34600111222")
	row.whatsappState = "connected"
	f.fake.script(reply{rows: [][]driver.Value{row.values()}})

	s, found, err := f.repo.Get(context.Background(), pgTenant, pgEdge, pgSession)
	if err != nil || !found {
		t.Fatalf("Get = (found %v, err %v), quería la sesión sin error", found, err)
	}
	if s.SelfPn != "" || s.WhatsappState != "connected" || s.SessionID != pgSession {
		t.Errorf("sesión = %+v, quería SelfPn vacío y el resto de la fila intacto", s)
	}
	cause := requireWarning(t, f.log, 1, pgEdge, pgSession, "Z")
	requireWrapped(t, cause, crypto.ErrKEKNotInKeyring, "fleet: descifrar self_pn: ")
}

// TestPostgresRepository_Get_IncompleteEnvelopeIsSoft: un sobre a medias también se sirve vacío y
// se avisa, con el error literal del sobre incompleto y sin key_id que mostrar.
func TestPostgresRepository_Get_IncompleteEnvelopeIsSoft(t *testing.T) {
	f := newFixture(t)
	row := bareRow(pgEdge, pgSession).withEnvelope(t, f.cipher, "34600111222")
	row.kekID = nil
	f.fake.script(reply{rows: [][]driver.Value{row.values()}})

	s, found, err := f.repo.Get(context.Background(), pgTenant, pgEdge, pgSession)
	if err != nil || !found || s.SelfPn != "" {
		t.Fatalf("Get = (SelfPn %q, %v, %v), quería la sesión con SelfPn vacío y sin error", s.SelfPn, found, err)
	}
	cause := requireWarning(t, f.log, 1, pgEdge, pgSession, "")
	if got, want := cause.Error(), "fleet: sobre de self_pn incompleto (enc/dek/kek_id no viajan juntos)"; got != want {
		t.Errorf("causa del aviso = %q, quería %q", got, want)
	}
}

// TestPostgresRepository_List_UndecryptableSelfPnIsOneWarning: con varias filas ilegibles el
// listado se sirve ENTERO y el aviso es UNO, con el conteo y la muestra de la PRIMERA que falló
// (no de la última, ni una línea por fila). Y el aviso no lleva el número.
func TestPostgresRepository_List_UndecryptableSelfPnIsOneWarning(t *testing.T) {
	f := newFixture(t)
	other := crypto.NewFieldCipher(keyringKP(t, "Y:"+key32(0x55), "Y"))
	f.fake.script(reply{rows: [][]driver.Value{
		bareRow("edge-a", "s-1").withEnvelope(t, f.cipher, "34600111222").values(),
		bareRow("edge-b", "s-2").withEnvelope(t, foreignCipher(t), "34600999888").values(),
		bareRow("edge-c", "s-3").withEnvelope(t, other, "34600777666").values(),
		bareRow("edge-d", "s-4").values(),
	}})
	got, err := f.repo.List(context.Background(), pgTenant)
	if err != nil {
		t.Fatalf("List: error inesperado %v (un sobre ilegible no tumba el listado)", err)
	}
	if len(got) != 4 {
		t.Fatalf("List devolvió %d sesiones, quería las 4", len(got))
	}
	for i, want := range []string{"34600111222", "", "", ""} {
		if got[i].SelfPn != want {
			t.Errorf("sesión %d: SelfPn = %q, quería %q", i, got[i].SelfPn, want)
		}
	}
	cause := requireWarning(t, f.log, 2, "edge-b", "s-2", "Z")
	requireWrapped(t, cause, crypto.ErrKEKNotInKeyring, "fleet: descifrar self_pn: ")
	if text := fmt.Sprint(f.log.seen()); strings.Contains(text, "34600999888") || strings.Contains(text, "34600777666") {
		t.Errorf("el aviso lleva un número de teléfono: %s", text)
	}
}

// TestPostgresRepository_List_WarnsEvenWhenItFails: si el listado se corta a media iteración, las
// filas que ya fallaron al descifrar se avisan igual.
func TestPostgresRepository_List_WarnsEvenWhenItFails(t *testing.T) {
	f := newFixture(t)
	f.fake.script(reply{
		rows:   [][]driver.Value{bareRow(pgEdge, pgSession).withEnvelope(t, foreignCipher(t), "34600111222").values()},
		endErr: errBoom,
	})
	if _, err := f.repo.List(context.Background(), pgTenant); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, quería el corte de la iteración", err)
	}
	cause := requireWarning(t, f.log, 1, pgEdge, pgSession, "Z")
	requireWrapped(t, cause, crypto.ErrKEKNotInKeyring, "fleet: descifrar self_pn: ")
}
