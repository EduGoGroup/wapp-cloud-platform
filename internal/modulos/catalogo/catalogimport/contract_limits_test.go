package catalogimport

// Parte de los tests de contract.go: el auxiliar normalized. Va en el paquete, no en
// catalogimport_test, porque desde fuera la regla solo se ve a través de ReadLimited
// y de Validate (donde también se prueba, validator_test.go y
// validator_catalog_test.go), y un fallo aquí se diagnosticaría allí como un techo
// que no corta. Nace en el verde (05 E-4): lleva una regla de negocio —y de
// seguridad—, la de que la configuración nunca DESACTIVA un tope por accidente.

import "testing"

// TestLimits_NonPositiveValuesFallBackToTheDefault: cada tope cae a SU default por
// separado, y un valor positivo —aunque sea 1— se respeta.
func TestLimits_NonPositiveValuesFallBackToTheDefault(t *testing.T) {
	cases := map[string]struct {
		in   Limits
		want Limits
	}{
		"zero value":     {Limits{}, Limits{MaxJSONBytes: DefaultMaxJSONBytes, MaxItems: DefaultMaxItems}},
		"negative":       {Limits{MaxJSONBytes: -1, MaxItems: -1}, Limits{MaxJSONBytes: DefaultMaxJSONBytes, MaxItems: DefaultMaxItems}},
		"only bytes set": {Limits{MaxJSONBytes: 64}, Limits{MaxJSONBytes: 64, MaxItems: DefaultMaxItems}},
		"only items set": {Limits{MaxItems: 3}, Limits{MaxJSONBytes: DefaultMaxJSONBytes, MaxItems: 3}},
		"smallest ones":  {Limits{MaxJSONBytes: 1, MaxItems: 1}, Limits{MaxJSONBytes: 1, MaxItems: 1}},
		"above defaults": {Limits{MaxJSONBytes: 4 << 20, MaxItems: 9000}, Limits{MaxJSONBytes: 4 << 20, MaxItems: 9000}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := c.in.normalized(); got != c.want {
				t.Errorf("%+v.normalized() = %+v; se esperaba %+v", c.in, got, c.want)
			}
		})
	}
}
