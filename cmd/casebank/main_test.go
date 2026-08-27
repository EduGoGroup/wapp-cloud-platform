package main

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/casebank"
)

// main_test.go — LA VÍA DE SIEMBRA REAL, BAJO TEST.
//
// ════════════════════════════════════════════════════════════════════════════
// POR QUÉ EXISTE ESTE FICHERO (auditoría del 2026-08-27)
// ════════════════════════════════════════════════════════════════════════════
//
// Este binario es la ÚNICA vía por la que un caso entra al banco, y no tenía ni
// un test. La auditoría metió dos mutaciones de UNA LÍNEA cada una y no las cazó
// nadie: `vet` en 0 y la suite entera verde.
//
//	💥 M-A: construir el anonimizador SIN `casebank.NombresDelCaso()`
//	        ⇒ los nombres propios se siembran EN CLARO (PII en la nube, ADR-0034).
//	💥 M-B: `caso.Consented = true` fijo, ignorando el flag `-consentido`
//	        ⇒ el consentimiento deja de significar nada.
//
// Las dos atacan justo la frase que da sentido al binario —«la siembra es un ACTO
// DE OPERADOR, y ese acto ES el registro del consentimiento»—, así que aquí hay
// un test por mutación y cada uno cae SOLO con la suya. No hace falta red ni base:
// `preparar` no toca ninguna de las dos, que es la razón de haberla extraído.

// storeQueCuenta es el doble mínimo del puerto: lo único que hace falta afirmar es
// si la escritura LLEGÓ o no. No valida nada, para no probar al doble.
type storeQueCuenta struct {
	insertados []casebank.Caso
	consultas  int
}

func (s *storeQueCuenta) Insertar(_ context.Context, c casebank.Caso) (int64, error) {
	s.insertados = append(s.insertados, c)
	return int64(len(s.insertados)), nil
}

func (s *storeQueCuenta) Existe(context.Context, string, string) (bool, error) {
	s.consultas++
	return false, nil
}

// ---------------------------------------------------------------------------
// M-A: el anonimizador tiene que llevar la lista de nombres
// ---------------------------------------------------------------------------

// TestPreparar_ElAnonimizadorLlevaLaListaDeNombres es el candado de M-A.
//
// 🔴 SE AFIRMA POR CONDUCTA Y NO SOLO POR LA LISTA. `Nombres()` compara
// configuración —útil para que el fallo diga qué falta—, pero lo que de verdad
// importa es que un nombre propio SALGA TAPADO, y eso solo lo dice pasarle un
// texto. Un anonimizador construido con la lista pero con la pasada de nombres
// rota pasaría la primera mitad y fallaría la segunda.
//
// 💥 Mutación (ejecutada): `casebank.NuevoAnonimizador()` sin argumentos en
// `preparar` ⇒ caen las dos mitades.
func TestPreparar_ElAnonimizadorLlevaLaListaDeNombres(t *testing.T) {
	_, anon, err := preparar(peticion{tenant: "t-siembra", consentido: true})
	if err != nil {
		t.Fatalf("preparar: %v", err)
	}

	// (a) la configuración: la lista es LA del caso, no una inventada aquí.
	//
	// ⚠️ SE COMPARA COMO CONJUNTO, no en orden: `Nombres()` devuelve el orden de
	// la alternancia (más largo primero, que es obligatorio para que «Ana María»
	// gane a «Ana»), no el orden en que se pasaron. Un `reflect.DeepEqual` aquí
	// falla por un detalle de implementación en vez de por un nombre que falte —
	// se comprobó al escribir este test, que cayó justo por eso.
	got, quiero := append([]string(nil), anon.Nombres()...), casebank.NombresDelCaso()
	sort.Strings(got)
	sort.Strings(quiero)
	if !reflect.DeepEqual(got, quiero) {
		t.Errorf("el anonimizador conoce %v; se esperaba %v — sin la lista, los nombres se siembran EN CLARO", got, quiero)
	}

	// (b) la conducta, que es la que no se puede fingir.
	for _, nombre := range casebank.NombresDelCaso() {
		entrada := "habló con " + nombre + " ayer"
		got := anon.Anonimizar(entrada)
		if strings.Contains(got, nombre) {
			t.Errorf("Anonimizar(%q) = %q; el nombre %q llegaría a la base EN CLARO", entrada, got, nombre)
		}
		if !strings.Contains(got, casebank.MarcaNombre) {
			t.Errorf("Anonimizar(%q) = %q; falta la marca %q", entrada, got, casebank.MarcaNombre)
		}
	}
}

// ---------------------------------------------------------------------------
// M-B: el flag `-consentido` es EL consentimiento
// ---------------------------------------------------------------------------

// TestPreparar_ElFlagGobiernaElConsentimiento es el candado de M-B, en las dos
// direcciones: sin flag NO hay consentimiento, con flag SÍ. Una sola dirección no
// bastaría — «siempre false» pasaría la primera y «siempre true» (que es la
// mutación) pasaría la segunda.
//
// 💥 Mutación (ejecutada): `caso.Consented = true` fijo en `preparar` ⇒ cae el
// subtest «sin flag».
func TestPreparar_ElFlagGobiernaElConsentimiento(t *testing.T) {
	casos := []struct {
		nombre     string
		consentido bool
	}{
		{"sin flag", false},
		{"con flag", true},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			caso, _, err := preparar(peticion{tenant: "t-siembra", consentido: c.consentido})
			if err != nil {
				t.Fatalf("preparar: %v", err)
			}
			if caso.Consented != c.consentido {
				t.Errorf("caso.Consented = %t con -consentido=%t; el flag ES el consentimiento, no una sugerencia",
					caso.Consented, c.consentido)
			}
		})
	}
}

// TestPreparar_SinFlag_LaSiembraNoEscribeNADA es la misma mutación vista desde el
// otro extremo: no basta con que el campo valga `false`, hace falta que ese
// `false` DETENGA la escritura. Se monta el servicio real con un store que cuenta
// —lo único falso es la persistencia— y se comprueba que no llega ni la consulta.
//
// 🔴 Es lo más cerca de la siembra de verdad que se puede llegar sin una base
// delante: el caso, el anonimizador y el servicio son los MISMOS objetos que
// `run` construye.
func TestPreparar_SinFlag_LaSiembraNoEscribeNADA(t *testing.T) {
	caso, anon, err := preparar(peticion{tenant: "t-siembra", consentido: false})
	if err != nil {
		t.Fatalf("preparar: %v", err)
	}
	espia := &storeQueCuenta{}
	svc, err := casebank.NewServicio(espia, anon)
	if err != nil {
		t.Fatalf("NewServicio: %v", err)
	}

	_, sembro, err := svc.Sembrar(context.Background(), caso)
	if !errors.Is(err, casebank.ErrSinConsentimiento) {
		t.Fatalf("Sembrar devolvió %v; se esperaba ErrSinConsentimiento", err)
	}
	if sembro {
		t.Error("Sembrar dice que sembró sin consentimiento")
	}
	if len(espia.insertados) != 0 || espia.consultas != 0 {
		t.Errorf("el store recibió %d escrituras y %d consultas; se esperaba cero de las dos",
			len(espia.insertados), espia.consultas)
	}
}

// TestPreparar_ConFlag_SiembraElCasoDeAmbar es el hermano positivo, sin el cual
// los dos de arriba los pasaría también un `preparar` que no preparara nada.
func TestPreparar_ConFlag_SiembraElCasoDeAmbar(t *testing.T) {
	caso, anon, err := preparar(peticion{tenant: "t-siembra", consentido: true})
	if err != nil {
		t.Fatalf("preparar: %v", err)
	}
	espia := &storeQueCuenta{}
	svc, err := casebank.NewServicio(espia, anon)
	if err != nil {
		t.Fatalf("NewServicio: %v", err)
	}

	id, sembro, err := svc.Sembrar(context.Background(), caso)
	if err != nil || !sembro || id == 0 {
		t.Fatalf("Sembrar devolvió (%d, %t, %v); se esperaba haber sembrado", id, sembro, err)
	}
	if len(espia.insertados) != 1 {
		t.Fatalf("el store recibió %d escrituras; se esperaba 1", len(espia.insertados))
	}
	got := espia.insertados[0]
	if got.TenantID != "t-siembra" {
		t.Errorf("tenant_id = %q; el caso tiene que sembrarse para el tenant que pidió el operador", got.TenantID)
	}
	if got.SourceText != casebank.TextoCasoAmbar {
		t.Error("el texto sembrado NO es el del caso Ambar")
	}
	if !strings.Contains(string(got.Expected), `"_procedencia"`) {
		t.Error("la fila sembrada no declara su procedencia: quien la lea mañana la tomará por material real")
	}
}

// TestPreparar_SinTenant_FallaSinTocarNada: el único argumento obligatorio, y su
// rechazo es nombrable con errors.Is en vez de por el texto del mensaje.
func TestPreparar_SinTenant_FallaSinTocarNada(t *testing.T) {
	caso, anon, err := preparar(peticion{tenant: "", consentido: true})
	if !errors.Is(err, errSinTenant) {
		t.Fatalf("preparar sin tenant devolvió %v; se esperaba errSinTenant", err)
	}
	if caso.TenantID != "" || caso.SourceText != "" {
		t.Errorf("devolvió un caso a medias (%+v); con error tiene que salir el cero-valor", caso)
	}
	if len(anon.Nombres()) != 0 {
		t.Errorf("devolvió un anonimizador armado (%v) junto a un error", anon.Nombres())
	}
}
