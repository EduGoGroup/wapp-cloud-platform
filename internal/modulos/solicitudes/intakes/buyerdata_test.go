package intakes

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// TestErrBuyerFieldEmpty_Text: el texto del centinela, byte a byte, y que es un centinela
// comparable con errors.Is también cuando llega envuelto.
func TestErrBuyerFieldEmpty_Text(t *testing.T) {
	t.Parallel()
	if got, want := ErrBuyerFieldEmpty.Error(), "intakes: campo del comprador sin clave"; got != want {
		t.Errorf("ErrBuyerFieldEmpty = %q, quería %q", got, want)
	}
	if !errors.Is(fmt.Errorf("proyector: %w", ErrBuyerFieldEmpty), ErrBuyerFieldEmpty) {
		t.Errorf("ErrBuyerFieldEmpty envuelto no se reconoce con errors.Is")
	}
}

// TestBuyerData_IsAPlainFieldMap: clave del campo → valor, y lo que se cifra es su forma JSON: un
// OBJETO plano de cadenas, con las claves en orden. El adaptador depende de las dos cosas —
// descifra a este tipo y rechaza lo que no sea un objeto—.
func TestBuyerData_IsAPlainFieldMap(t *testing.T) {
	t.Parallel()
	data := BuyerData{}
	data["rut"] = "11.222.333-Q"
	data["direccion"] = "Camino del Alba 909, casa 3"
	data["rut"] = "otro" // reescribir una clave corrige ESA y deja la otra en paz

	blob, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("serializando BuyerData: %v", err)
	}
	if got, want := string(blob), `{"direccion":"Camino del Alba 909, casa 3","rut":"otro"}`; got != want {
		t.Errorf("JSON de BuyerData = %s, quería %s", got, want)
	}

	var back BuyerData
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatalf("leyendo el JSON de BuyerData: %v", err)
	}
	if len(back) != 2 || back["rut"] != "otro" || back["direccion"] != data["direccion"] {
		t.Errorf("BuyerData tras ida y vuelta = %+v", back)
	}
	if err := json.Unmarshal([]byte(`["rut"]`), &back); err == nil {
		t.Errorf("un JSON que no es un objeto se leyó como BuyerData")
	}
}
