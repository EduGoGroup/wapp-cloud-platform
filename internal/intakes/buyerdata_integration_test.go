// buyerdata_integration_test.go es la prueba de que los datos del comprador están
// CIFRADOS EN REPOSO de verdad (Plan 041 · T4.5, D-041.13 / ADR-0017).
//
// Corre contra Postgres porque es la única forma de comprobar lo que hay que
// comprobar: se escribe por el camino real y luego se lee la fila POR SQL DIRECTO,
// como la leería quien tuviera acceso a la base —un volcado, un backup, un
// administrador—, y se exige que el valor no esté ahí. Un doble en memoria no
// puede demostrar eso.
package intakes_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Material de clave de tests: 32B constantes en base64, el mismo patrón que usan
// los tests del KeyProvider. No es secreto de nada — la base es efímera.
const (
	kekDePruebaB64   = "ERERERERERERERERERERERERERERERERERERERERERE="
	indexDePruebaB64 = "RERERERERERERERERERERERERERERERERERERERERES="
	kekIDDePrueba    = "test-kek-1"
)

// cipherDePrueba construye el MISMO stack de cifrado que usa el arranque
// (keyring versionado del Plan 012), y devuelve también el KeyProvider para poder
// comprobar el key_id que se persiste.
func cipherDePrueba(t *testing.T) (*crypto.FieldCipher, crypto.KeyProvider) {
	t.Helper()
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{
		KeyringB64: kekIDDePrueba + ":" + kekDePruebaB64,
		CurrentID:  kekIDDePrueba,
		IndexB64:   indexDePruebaB64,
	})
	if err != nil {
		t.Fatalf("KeyProvider de prueba: %v", err)
	}
	return crypto.NewFieldCipher(kp), kp
}

// TestBuyerDataPG_LaFilaNoEsLegiblePorSQLDirecto es el criterio literal de T4.5.
//
// Escribe dos campos por el camino real, y después:
//
//	(a) lee data_enc por SQL directo y exige que el RUT y la dirección NO estén ahí,
//	    ni como texto ni como bytes;
//	(b) exige que sí se puedan recuperar con la llave, para que "ilegible" no se
//	    pueda conseguir tirando el dato;
//	(c) exige que la fila lleve el key_id de la KEK que la envolvió, sin el cual
//	    quedaría fuera de la rotación del Plan 012.
func TestBuyerDataPG_LaFilaNoEsLegiblePorSQLDirecto(t *testing.T) {
	db := openTestDB(t)
	tenant := uuid.NewString()
	id := uuid.NewString()
	seedPG(t, db, tenant, []fixture{{id, intakes.StatusConfirmed, "sess-a", 1}})

	cipher, kp := cipherDePrueba(t)
	store := intakes.NewPostgresBuyerData(db, cipher)
	ctx := context.Background()

	const (
		rut       = "12.345.678-K"
		dirección = "Pasaje Los Aromos 4412, depto 7"
	)
	if err := store.PutBuyerField(ctx, id, "rut", rut); err != nil {
		t.Fatalf("PutBuyerField(rut): %v", err)
	}
	if err := store.PutBuyerField(ctx, id, "direccion", dirección); err != nil {
		t.Fatalf("PutBuyerField(direccion): %v", err)
	}

	// UNA fila por solicitud (PK intake_id): el segundo campo fusiona, no duplica.
	var filas int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.intake_buyer_data WHERE intake_id = $1`, id).Scan(&filas); err != nil {
		t.Fatalf("contando filas: %v", err)
	}
	if filas != 1 {
		t.Fatalf("filas de intake_buyer_data = %d, esperaba 1 (los campos fusionan)", filas)
	}

	var (
		enc, dek []byte
		kekID    string
	)
	if err := db.QueryRowContext(ctx, `
		SELECT data_enc, data_dek, data_kek_id
		FROM public.intake_buyer_data WHERE intake_id = $1
	`, id).Scan(&enc, &dek, &kekID); err != nil {
		t.Fatalf("leyendo la fila por SQL directo: %v", err)
	}

	// (a) El que abra la base NO ve el dato.
	for _, valor := range []string{rut, dirección, "rut", "direccion"} {
		if strings.Contains(string(enc), valor) {
			t.Fatalf("FUGA: data_enc contiene %q en claro (%d bytes)", valor, len(enc))
		}
	}
	if len(enc) == 0 || len(dek) == 0 {
		t.Fatalf("la fila quedó sin material cifrado: enc=%dB dek=%dB", len(enc), len(dek))
	}

	// (b) …y el que tenga la llave, sí. "Ilegible" no puede lograrse perdiendo el dato.
	plain, err := cipher.Decrypt(enc, dek, kekID)
	if err != nil {
		t.Fatalf("descifrando la fila con la llave: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(plain), &got); err != nil {
		t.Fatalf("el blob descifrado no es un objeto JSON: %v", err)
	}
	if got["rut"] != rut || got["direccion"] != dirección {
		t.Fatalf("el checklist descifrado quedó en %+v; esperaba los dos campos", got)
	}

	// (c) La fila sabe con qué KEK se envolvió: sin esto, la rotación del Plan 012
	// no podría re-envolverla y quedaría huérfana en el keyring.
	if kekID != kp.CurrentKeyID() {
		t.Fatalf("data_kek_id = %q, esperaba la KEK current %q", kekID, kp.CurrentKeyID())
	}
}

// TestBuyerDataPG_OtraKEKNoAbreLaFila: el cifrado no es decorativo. Con otro
// keyring —el atacante que copió la base pero no las llaves— la fila no se abre.
func TestBuyerDataPG_OtraKEKNoAbreLaFila(t *testing.T) {
	db := openTestDB(t)
	tenant := uuid.NewString()
	id := uuid.NewString()
	seedPG(t, db, tenant, []fixture{{id, intakes.StatusConfirmed, "sess-a", 1}})

	cipher, _ := cipherDePrueba(t)
	if err := intakes.NewPostgresBuyerData(db, cipher).
		PutBuyerField(context.Background(), id, "rut", "12.345.678-K"); err != nil {
		t.Fatalf("PutBuyerField: %v", err)
	}

	otroKP, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{
		KeyringB64: kekIDDePrueba + ":" + "IiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiI=",
		CurrentID:  kekIDDePrueba,
		IndexB64:   indexDePruebaB64,
	})
	if err != nil {
		t.Fatalf("KeyProvider ajeno: %v", err)
	}

	var (
		enc, dek []byte
		kekID    string
	)
	if err := db.QueryRowContext(context.Background(), `
		SELECT data_enc, data_dek, data_kek_id
		FROM public.intake_buyer_data WHERE intake_id = $1
	`, id).Scan(&enc, &dek, &kekID); err != nil {
		t.Fatalf("leyendo la fila: %v", err)
	}
	if _, err := crypto.NewFieldCipher(otroKP).Decrypt(enc, dek, kekID); err == nil {
		t.Fatalf("la fila se abrió con OTRA KEK: el cifrado no protege nada")
	}
}

// TestBuyerDataPG_ElDetalleSoloDiceQueEstá cierra el circuito por el store real:
// tras escribir el checklist, Get publica el booleano y NADA del contenido.
func TestBuyerDataPG_ElDetalleSoloDiceQueEstá(t *testing.T) {
	db := openTestDB(t)
	tenant := uuid.NewString()
	conDatos, sinDatos := uuid.NewString(), uuid.NewString()
	seedPG(t, db, tenant, []fixture{
		{conDatos, intakes.StatusConfirmed, "sess-a", 1},
		{sinDatos, intakes.StatusConfirmed, "sess-a", 2},
	})

	cipher, _ := cipherDePrueba(t)
	ctx := context.Background()
	if err := intakes.NewPostgresBuyerData(db, cipher).
		PutBuyerField(ctx, conDatos, "rut", "12.345.678-K"); err != nil {
		t.Fatalf("PutBuyerField: %v", err)
	}

	store := intakes.NewPostgres(db)
	for id, quiero := range map[string]bool{conDatos: true, sinDatos: false} {
		detail, err := store.Get(ctx, tenant, id)
		if err != nil {
			t.Fatalf("Get(%s): %v", id, err)
		}
		if detail.BuyerDataPresent != quiero {
			t.Fatalf("BuyerDataPresent de %s = %v, quiero %v", id, detail.BuyerDataPresent, quiero)
		}
		blob, err := json.Marshal(detail)
		if err != nil {
			t.Fatalf("serializando el detalle: %v", err)
		}
		if strings.Contains(string(blob), "12.345.678-K") {
			t.Fatalf("FUGA: el detalle leído de Postgres lleva el dato del comprador:\n%s", blob)
		}
	}
}

// TestBuyerDataPG_SinSolicitudNoSeGuarda: la FK protege la tabla de datos
// personales huérfanos. Sin ella, un intakeID equivocado dejaría una ficha que
// nadie podría relacionar con nada — ni para leerla, ni para borrarla con su
// solicitud.
func TestBuyerDataPG_SinSolicitudNoSeGuarda(t *testing.T) {
	db := openTestDB(t)
	cipher, _ := cipherDePrueba(t)

	err := intakes.NewPostgresBuyerData(db, cipher).
		PutBuyerField(context.Background(), uuid.NewString(), "rut", "12.345.678-K")
	if err == nil {
		t.Fatalf("se guardaron datos del comprador de una solicitud inexistente")
	}
}

// TestBuyerDataPG_SeVaConLaSolicitud: el ON DELETE CASCADE de la migración 0045 es
// lo que hace que borrar un pedido borre de verdad los datos personales de su
// comprador.
//
// 🔴 El motivo original era otro: se apoyaba en un barrido automático por antigüedad
// que iba a llegar y que se DESCARTÓ el 2026-08-20 (D-046.15, ADR-0043). No hay
// barrido que dependa de este CASCADE. Lo que sí hay es el BORRADO
// MANUAL de una solicitud, y ahí el CASCADE es lo único que impide que los datos del
// comprador queden huérfanos en la base. La aserción no cambia; su razón, sí.
func TestBuyerDataPG_SeVaConLaSolicitud(t *testing.T) {
	db := openTestDB(t)
	tenant := uuid.NewString()
	id := uuid.NewString()
	seedPG(t, db, tenant, []fixture{{id, intakes.StatusConfirmed, "sess-a", 1}})

	cipher, _ := cipherDePrueba(t)
	ctx := context.Background()
	if err := intakes.NewPostgresBuyerData(db, cipher).PutBuyerField(ctx, id, "rut", "12.345.678-K"); err != nil {
		t.Fatalf("PutBuyerField: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM public.intakes WHERE id = $1`, id); err != nil {
		t.Fatalf("borrando la solicitud: %v", err)
	}

	var quedan int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.intake_buyer_data WHERE intake_id = $1`, id).Scan(&quedan); err != nil {
		t.Fatalf("contando: %v", err)
	}
	if quedan != 0 {
		t.Fatalf("tras borrar la solicitud quedaron %d fila(s) con datos personales", quedan)
	}
}
