package integrations_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// sealedSecret devuelve las tres columnas del sobre que guardaría la base para ese secreto,
// selladas con la KEK kekID.
func sealedSecret(t *testing.T, kekID, secret string) []driver.Value {
	t.Helper()
	enc, dek, gotID, err := newCipher(t, kekID).Encrypt(secret)
	if err != nil || gotID != kekID {
		t.Fatalf("sellar el secreto de prueba: id=%q err=%v", gotID, err)
	}
	return []driver.Value{enc, dek, gotID}
}

// brokenKeyProvider es un KeyProvider que no sabe envolver: hace fallar a FieldCipher.Encrypt.
type brokenKeyProvider struct{ cause error }

func (b brokenKeyProvider) WrapDEK([]byte) ([]byte, string, error)   { return nil, "", b.cause }
func (b brokenKeyProvider) UnwrapDEK([]byte, string) ([]byte, error) { return nil, b.cause }
func (brokenKeyProvider) BlindIndex(string, string) string           { return "" }
func (brokenKeyProvider) CurrentKeyID() string                       { return pgKEKNewID }

// TestPostgres_GetTenantIntegration_MapsTheRow: es UNA consulta, la literal, por el tenant. Un
// endpoint NULL llega vacío, y HasSecret dice si hay blob cifrado SIN que el blob salga.
func TestPostgres_GetTenantIntegration_MapsTheRow(t *testing.T) {
	created := time.Unix(1_700_000_000, 0).UTC()
	updated := created.Add(time.Hour)
	cases := []struct {
		name string
		row  []driver.Value
		want integrations.TenantIntegration
	}{
		{
			"with endpoint and secret",
			[]driver.Value{pgTenant, "http", "webhook", "https://bridge.example/hook", []byte("blob-cifrado"), true, created, updated},
			integrations.TenantIntegration{TenantID: pgTenant, CatalogAdapter: "http", EventsAdapter: "webhook",
				EndpointURL: "https://bridge.example/hook", HasSecret: true, Enabled: true, CreatedAt: created, UpdatedAt: updated},
		},
		{
			"null endpoint and no secret",
			[]driver.Value{pgTenant, "local", "local", nil, nil, false, created, created},
			integrations.TenantIntegration{TenantID: pgTenant, CatalogAdapter: "local", EventsAdapter: "local", CreatedAt: created, UpdatedAt: created},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgres(t)
			fake.script(reply{rows: [][]driver.Value{c.row}})
			ti, found, err := store.GetTenantIntegration(context.Background(), pgTenant)
			if err != nil || !found {
				t.Fatalf("GetTenantIntegration = (found=%v, err=%v), quería la fila", found, err)
			}
			if !ti.CreatedAt.Equal(c.want.CreatedAt) || !ti.UpdatedAt.Equal(c.want.UpdatedAt) {
				t.Errorf("(CreatedAt, UpdatedAt) = (%v, %v), quería (%v, %v)", ti.CreatedAt, ti.UpdatedAt, c.want.CreatedAt, c.want.UpdatedAt)
			}
			ti.CreatedAt, ti.UpdatedAt = c.want.CreatedAt, c.want.UpdatedAt
			if ti != c.want {
				t.Errorf("GetTenantIntegration = %+v, quería %+v", ti, c.want)
			}
			got := only(t, fake, eventQuery, getTenantSQL)
			requireArgs(t, got.args, pgTenant)
		})
	}
}

// TestPostgres_GetTenantIntegration_NoRowAndError: sin fila no es un error (valor cero y false); un
// fallo de la consulta sale con su prefijo, que nombra el tenant.
func TestPostgres_GetTenantIntegration_NoRowAndError(t *testing.T) {
	t.Run("no row", func(t *testing.T) {
		store, _ := newPostgres(t)
		ti, found, err := store.GetTenantIntegration(context.Background(), pgTenant)
		if err != nil || found || ti != (integrations.TenantIntegration{}) {
			t.Errorf("GetTenantIntegration sin fila = (%+v, %v, %v), quería (cero, false, nil)", ti, found, err)
		}
	})
	t.Run("the query fails", func(t *testing.T) {
		store, fake := newPostgres(t)
		fake.script(reply{err: errDB})
		ti, found, err := store.GetTenantIntegration(context.Background(), pgTenant)
		requireWrapped(t, err, errDB, "integrations: leer integración de "+pgTenant+": ")
		if found || ti != (integrations.TenantIntegration{}) {
			t.Errorf("con error devolvió (%+v, %v), quería (cero, false)", ti, found)
		}
	})
}

// TestPostgres_GetTenantSecret_DecryptsWithTheKEKOfTheRow: es UNA consulta, la literal, y el sobre
// se abre con la KEK que lo envolvió (su secret_kek_id), aunque ya no sea la current: tras una
// rotación a medias conviven filas de las dos.
func TestPostgres_GetTenantSecret_DecryptsWithTheKEKOfTheRow(t *testing.T) {
	for _, kekID := range []string{pgKEKNewID, pgKEKOldID} {
		t.Run(kekID, func(t *testing.T) {
			store, fake := newPostgres(t) // la current del adaptador es SIEMPRE la nueva
			fake.script(reply{rows: [][]driver.Value{sealedSecret(t, kekID, pgSecret)}})
			secret, found, err := store.GetTenantSecret(context.Background(), pgTenant)
			if err != nil || !found || secret != pgSecret {
				t.Errorf("GetTenantSecret = (found=%v, err=%v, igual=%v), quería el secreto sellado", found, err, secret == pgSecret)
			}
			got := only(t, fake, eventQuery, readEnvelopeSQL)
			requireArgs(t, got.args, pgTenant)
		})
	}
}

// TestPostgres_GetTenantSecret_NotFound: sin fila, o con CUALQUIERA de las tres columnas del sobre
// a NULL, no hay secreto (y no es un error): un sobre a medias no se intenta abrir.
func TestPostgres_GetTenantSecret_NotFound(t *testing.T) {
	full := sealedSecret(t, pgKEKNewID, pgSecret)
	without := func(i int) []driver.Value {
		row := append([]driver.Value(nil), full...)
		row[i] = nil
		return row
	}
	cases := []struct {
		name string
		rows [][]driver.Value
	}{
		{"no row", nil},
		{"all three null", [][]driver.Value{{nil, nil, nil}}},
		{"secret_enc null", [][]driver.Value{without(0)}},
		{"secret_dek null", [][]driver.Value{without(1)}},
		{"secret_kek_id null", [][]driver.Value{without(2)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgres(t)
			fake.script(reply{rows: c.rows})
			secret, found, err := store.GetTenantSecret(context.Background(), pgTenant)
			if err != nil || found || secret != "" {
				t.Errorf("GetTenantSecret = (found=%v, err=%v, vacío=%v), quería (false, nil, vacío)", found, err, secret == "")
			}
		})
	}
}

// TestPostgres_GetTenantSecret_Errors: el fallo de la consulta y el del sobre que no abre salen
// cada uno con su prefijo, sin secreto y sin citar nada del sobre.
func TestPostgres_GetTenantSecret_Errors(t *testing.T) {
	t.Run("the query fails", func(t *testing.T) {
		store, fake := newPostgres(t)
		fake.script(reply{err: errDB})
		secret, found, err := store.GetTenantSecret(context.Background(), pgTenant)
		requireWrapped(t, err, errDB, "integrations: leer secreto de "+pgTenant+": ")
		if found || secret != "" {
			t.Errorf("con error devolvió (found=%v, vacío=%v), quería (false, vacío)", found, secret == "")
		}
	})
	t.Run("the envelope does not open", func(t *testing.T) {
		store, fake := newPostgres(t)
		row := sealedSecret(t, pgKEKNewID, pgSecret)
		row[2] = "kek-que-no-esta-en-el-keyring"
		fake.script(reply{rows: [][]driver.Value{row}})
		secret, found, err := store.GetTenantSecret(context.Background(), pgTenant)
		if err == nil || !strings.HasPrefix(err.Error(), "integrations: descifrar secreto de "+pgTenant+": ") {
			t.Fatalf("err = %v, quería el prefijo «integrations: descifrar secreto de %s: »", err, pgTenant)
		}
		if found || secret != "" {
			t.Errorf("con error devolvió (found=%v, vacío=%v), quería (false, vacío)", found, secret == "")
		}
		if strings.Contains(err.Error(), pgSecret) {
			t.Error("FUGA: el error cita el secreto")
		}
	})
}

// TestPostgres_UpsertTenantIntegration_WithoutSecret: sin secreto se emite el upsert que NO nombra
// las columnas del sobre (por eso las conserva), con las cuatro columnas de configuración; un
// endpoint vacío viaja como NULL. HasSecret y las fechas del llamante no viajan.
func TestPostgres_UpsertTenantIntegration_WithoutSecret(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		want     driver.Value
	}{
		{"with endpoint", "https://bridge.example/hook", "https://bridge.example/hook"},
		{"empty endpoint is NULL", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgres(t)
			ti := integrations.TenantIntegration{
				TenantID: pgTenant, CatalogAdapter: "http", EventsAdapter: "webhook", EndpointURL: c.endpoint, Enabled: true,
				HasSecret: true, CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(2, 0),
			}
			if err := store.UpsertTenantIntegration(context.Background(), ti, ""); err != nil {
				t.Fatalf("UpsertTenantIntegration: error inesperado %v", err)
			}
			got := only(t, fake, eventExec, upsertConfigOnlySQL)
			requireArgs(t, got.args, pgTenant, "http", "webhook", c.want, true)
		})
	}
}

// TestPostgres_UpsertTenantIntegration_WithSecret: con secreto se emite el upsert que escribe las
// tres columnas del sobre, con lo que da el cifrador: un sobre que se abre de vuelta al secreto,
// sellado con la KEK current. El secreto en claro NUNCA viaja a la base.
func TestPostgres_UpsertTenantIntegration_WithSecret(t *testing.T) {
	store, fake := newPostgres(t)
	ti := integrations.TenantIntegration{TenantID: pgTenant, CatalogAdapter: "local", EventsAdapter: "webhook", Enabled: true}
	if err := store.UpsertTenantIntegration(context.Background(), ti, pgSecret); err != nil {
		t.Fatalf("UpsertTenantIntegration: error inesperado %v", err)
	}
	got := only(t, fake, eventExec, upsertWithEnvelopeSQL)
	if len(got.args) != 8 {
		t.Fatalf("la sentencia lleva %d argumentos, quería 8: %v", len(got.args), got.args)
	}
	requireArgs(t, got.args[:4], pgTenant, "local", "webhook", nil)
	if got.args[7] != true {
		t.Errorf("argumento $8 (enabled) = %v, quería true", got.args[7])
	}
	requireSealedEnvelope(t, got.args[4], got.args[5], got.args[6])
	requireNoPlaintext(t, got.args, pgSecret)
}

// requireSealedEnvelope afirma que las tres columnas del sobre que viajaron a la base son un sobre
// de verdad: dos blobs no vacíos y el id de la KEK current, que se abren de vuelta al secreto.
func requireSealedEnvelope(t *testing.T, encArg, dekArg, kekArg driver.Value) {
	t.Helper()
	enc, okEnc := encArg.([]byte)
	dek, okDEK := dekArg.([]byte)
	kekID, okID := kekArg.(string)
	if !okEnc || !okDEK || !okID {
		t.Fatalf("el sobre viajó como (%T, %T, %T), quería dos []byte y el id de la KEK", encArg, dekArg, kekArg)
	}
	if len(enc) == 0 || len(dek) == 0 {
		t.Fatalf("el sobre viajó vacío (enc=%dB, dek=%dB)", len(enc), len(dek))
	}
	if kekID != pgKEKNewID {
		t.Errorf("secret_kek_id = %q, quería la KEK current (%s)", kekID, pgKEKNewID)
	}
	plain, err := newCipher(t, pgKEKOldID).Decrypt(enc, dek, kekID)
	if err != nil {
		t.Fatalf("el sobre que viajó no se abre: %v", err)
	}
	if plain != pgSecret {
		t.Error("el sobre que viajó no se abre de vuelta al secreto")
	}
}

// requireNoPlaintext afirma que ningún argumento de la sentencia lleva el secreto en claro.
func requireNoPlaintext(t *testing.T, args []driver.Value, secret string) {
	t.Helper()
	for i, arg := range args {
		var text string
		switch v := arg.(type) {
		case []byte:
			text = string(v)
		case string:
			text = v
		}
		if strings.Contains(text, secret) {
			t.Errorf("FUGA: el argumento $%d lleva el secreto en claro", i+1)
		}
	}
}

// TestPostgres_UpsertTenantIntegration_Errors: cada sentencia falla con SU prefijo (el de sin
// secreto lo dice), y si falla el cifrador no se emite nada. Ningún error cita el secreto.
func TestPostgres_UpsertTenantIntegration_Errors(t *testing.T) {
	ti := integrations.TenantIntegration{TenantID: pgTenant, CatalogAdapter: "local", EventsAdapter: "webhook"}
	t.Run("without secret", func(t *testing.T) {
		store, fake := newPostgres(t)
		fake.script(reply{err: errDB})
		err := store.UpsertTenantIntegration(context.Background(), ti, "")
		requireWrapped(t, err, errDB, "integrations: upsert de "+pgTenant+" (sin tocar el secreto): ")
	})
	t.Run("with secret", func(t *testing.T) {
		store, fake := newPostgres(t)
		fake.script(reply{err: errDB})
		err := store.UpsertTenantIntegration(context.Background(), ti, pgSecret)
		requireWrapped(t, err, errDB, "integrations: upsert de "+pgTenant+": ")
	})
	t.Run("the cipher fails", func(t *testing.T) {
		fake := &fakeDB{}
		cause := errors.New("KMS caído")
		store := integrations.NewPostgres(fake.open(t), crypto.NewFieldCipher(brokenKeyProvider{cause: cause}))
		err := store.UpsertTenantIntegration(context.Background(), ti, pgSecret)
		if !errors.Is(err, cause) || !strings.HasPrefix(err.Error(), "integrations: cifrar el secreto de "+pgTenant+": ") {
			t.Fatalf("err = %v, quería el prefijo «integrations: cifrar el secreto de %s: » envolviendo la causa", err, pgTenant)
		}
		if strings.Contains(err.Error(), pgSecret) {
			t.Error("FUGA: el error cita el secreto")
		}
		if seen := fake.seen(); len(seen) != 0 {
			t.Errorf("con el cifrador caído se emitieron %d sentencias, quería 0", len(seen))
		}
	})
}

// TestPostgres_DeleteTenantIntegration: es UN DELETE, el literal, por el tenant; que no borre nada
// no es un error, y un fallo sale con su prefijo.
func TestPostgres_DeleteTenantIntegration(t *testing.T) {
	t.Run("emits the delete", func(t *testing.T) {
		store, fake := newPostgres(t)
		fake.script(touched(0))
		if err := store.DeleteTenantIntegration(context.Background(), pgTenant); err != nil {
			t.Fatalf("DeleteTenantIntegration sin fila que borrar: error inesperado %v", err)
		}
		got := only(t, fake, eventExec, deleteTenantSQL)
		requireArgs(t, got.args, pgTenant)
	})
	t.Run("the statement fails", func(t *testing.T) {
		store, fake := newPostgres(t)
		fake.script(reply{err: errDB})
		err := store.DeleteTenantIntegration(context.Background(), pgTenant)
		requireWrapped(t, err, errDB, "integrations: borrar integración de "+pgTenant+": ")
	})
}

// TestPostgres_SecretFingerprint: devuelve SOLO la huella del secreto guardado —los ocho hex de
// Fingerprint, aquí el vector del test viejo del CRUD—, leyéndolo con la consulta de
// GetTenantSecret; sin secreto no hay huella; y un error es el de GetTenantSecret, tal cual.
func TestPostgres_SecretFingerprint(t *testing.T) {
	t.Run("fingerprint of the stored secret", func(t *testing.T) {
		store, fake := newPostgres(t)
		fake.script(reply{rows: [][]driver.Value{sealedSecret(t, pgKEKOldID, pgSecret)}})
		fp, found, err := store.SecretFingerprint(context.Background(), pgTenant)
		if err != nil || !found || fp != "e5c47775" {
			t.Errorf("SecretFingerprint = (%q, %v, %v), quería (e5c47775, true, nil)", fp, found, err)
		}
		if fp != integrations.Fingerprint(pgSecret) {
			t.Errorf("la huella (%q) no es la de Fingerprint", fp)
		}
		got := only(t, fake, eventQuery, readEnvelopeSQL)
		requireArgs(t, got.args, pgTenant)
	})
	t.Run("no row", func(t *testing.T) {
		store, _ := newPostgres(t)
		if fp, found, err := store.SecretFingerprint(context.Background(), pgTenant); err != nil || found || fp != "" {
			t.Errorf("SecretFingerprint sin fila = (%q, %v, %v), quería (\"\", false, nil)", fp, found, err)
		}
	})
	t.Run("row without secret", func(t *testing.T) {
		store, fake := newPostgres(t)
		fake.script(reply{rows: [][]driver.Value{{nil, nil, nil}}})
		if fp, found, err := store.SecretFingerprint(context.Background(), pgTenant); err != nil || found || fp != "" {
			t.Errorf("SecretFingerprint sin secreto = (%q, %v, %v), quería (\"\", false, nil)", fp, found, err)
		}
	})
	t.Run("the query fails", func(t *testing.T) {
		store, fake := newPostgres(t)
		fake.script(reply{err: errDB})
		fp, found, err := store.SecretFingerprint(context.Background(), pgTenant)
		requireWrapped(t, err, errDB, "integrations: leer secreto de "+pgTenant+": ")
		if found || fp != "" {
			t.Errorf("con error devolvió (%q, %v), quería (\"\", false)", fp, found)
		}
	})
}

// TestPostgres_CountOutbox: es UNA consulta, la literal, con el tenant y los cuatro estados en
// orden; mapea los cuatro contadores y la antigüedad, y un MIN sin filas (NULL) es el instante
// cero: no hay «la más vieja de ninguna».
func TestPostgres_CountOutbox(t *testing.T) {
	oldest := time.Unix(1_700_000_000, 0).UTC()
	cases := []struct {
		name string
		row  []driver.Value
		want integrations.OutboxCounts
	}{
		{"queue with work", []driver.Value{int64(3), int64(1), int64(40), int64(2), oldest},
			integrations.OutboxCounts{Pending: 3, Delivering: 1, Delivered: 40, Dead: 2, OldestPendingAt: oldest}},
		{"nothing pending", []driver.Value{int64(0), int64(1), int64(5), int64(0), nil},
			integrations.OutboxCounts{Delivering: 1, Delivered: 5}},
		{"tenant without rows", []driver.Value{int64(0), int64(0), int64(0), int64(0), nil}, integrations.OutboxCounts{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgres(t)
			fake.script(reply{rows: [][]driver.Value{c.row}})
			counts, err := store.CountOutbox(context.Background(), pgTenant)
			if err != nil {
				t.Fatalf("CountOutbox: error inesperado %v", err)
			}
			if !counts.OldestPendingAt.Equal(c.want.OldestPendingAt) || counts.OldestPendingAt.IsZero() != c.want.OldestPendingAt.IsZero() {
				t.Errorf("OldestPendingAt = %v, quería %v", counts.OldestPendingAt, c.want.OldestPendingAt)
			}
			counts.OldestPendingAt = c.want.OldestPendingAt
			if counts != c.want {
				t.Errorf("CountOutbox = %+v, quería %+v", counts, c.want)
			}
			got := only(t, fake, eventQuery, countSQL)
			requireArgs(t, got.args, pgTenant, "pending", "delivering", "delivered", "dead")
		})
	}
	t.Run("the query fails", func(t *testing.T) {
		store, fake := newPostgres(t)
		fake.script(reply{err: errDB})
		counts, err := store.CountOutbox(context.Background(), pgTenant)
		requireWrapped(t, err, errDB, "integrations: contar la cola de entregas: ")
		if counts != (integrations.OutboxCounts{}) {
			t.Errorf("con error devolvió %+v, quería el valor cero", counts)
		}
	})
}
