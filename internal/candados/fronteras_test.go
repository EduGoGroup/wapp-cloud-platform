//go:build pendiente

package candados

import "testing"

const moduloWapp = "github.com/EduGoGroup/wapp-cloud-platform"

// dirsFronteras: el candado recorre internal/ y cmd/, producción y tests.
var dirsFronteras = []string{"internal", "cmd"}

// mapaFronteras: "internal/flujos/store" es más largo que "internal/flujos": gana él.
var mapaFronteras = map[string]string{
	"internal/iam":          "acceso",
	"internal/flujos":       "conversacion",
	"internal/flujos/store": "solicitudes",
}

// TestFronterasMuerde: cada una de las seis reglas dispara sobre su fichero, nombrando la
// arista, y cada import prohibido da UNA violación.
func TestFronterasMuerde(t *testing.T) {
	fuentes := recorrerCaso(t, "testdata/fronteras/muerde", dirsFronteras, true)
	r := Reglas{
		Capas:      map[string][]string{"edge": {"acceso"}, "acceso": {}, "catalogo": {}},
		Mapa:       mapaFronteras,
		Conmutados: []string{"solicitudes"},
		Puentes: []Puente{
			{Desde: "internal/modulos/acceso/muerto", Hacia: "internal/intentcfg", Motivo: "nadie lo usa", Nace: "F1", Muere: "F8"},
			{Desde: "internal/modulos/catalogo/menu", Hacia: "internal/intentcfg", Motivo: "caducado", Nace: "F0", Muere: "F0"},
		},
		FasesCerradas: []string{"F0"},
	}
	vs := Fronteras(moduloWapp, fuentes, r)

	casos := []struct {
		nombre  string
		fichero string
		trozos  []string
	}{
		{"regla 1: módulo → módulo fuera de Capas", "internal/modulos/edge/lease/lease.go",
			[]string{"regla 1: ", "internal/modulos/edge/lease → internal/modulos/captacion/pipeline"}},
		{"regla 2: nuevo → viejo sin puente", "internal/modulos/acceso/sesion/sesion.go",
			[]string{"regla 2: ", "internal/modulos/acceso/sesion → internal/iam/app"}},
		{"regla 2: también en los tests nuevos", "internal/modulos/acceso/sesion/sesion_test.go",
			[]string{"regla 2: ", "internal/modulos/acceso/sesion → internal/gateway/fleet"}},
		{"regla 3: arranque → viejo de un módulo conmutado (prefijo más largo)", "internal/arranque/cableado.go",
			[]string{"regla 3: ", "internal/arranque → internal/flujos/store"}},
		{"regla 4: apipublica → cualquier viejo", "internal/apipublica/contactos/contactos.go",
			[]string{"regla 4: ", "internal/apipublica/contactos → internal/publicapi"}},
		{"regla 5: producción vieja → nuevo", "internal/iam/app/usa_nucleo.go",
			[]string{"regla 5: ", "internal/iam/app → internal/nucleo/contact"}},
		{"regla 5: test viejo que no es huella_vieja_test.go → huellatest", "internal/bootstrap/arranque/otro_test.go",
			[]string{"regla 5: ", "internal/bootstrap/arranque → internal/arranque/huellatest"}},
		{"regla 6: puente muerto", "internal/modulos/acceso/muerto",
			[]string{"regla 6: ", "internal/modulos/acceso/muerto → internal/intentcfg"}},
		{"regla 6: puente cuyo Muere es una fase cerrada", "internal/modulos/catalogo/menu",
			[]string{"regla 6: ", "internal/modulos/catalogo/menu → internal/intentcfg", "F0"}},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			exigeViolacion(t, vs, c.fichero, c.trozos...)
		})
	}
	if len(vs) != len(casos) {
		t.Errorf("se esperaban %d violaciones (una por arista prohibida); hay %d: %v", len(casos), len(vs), vs)
	}
	exigeNingunaEn(t, vs, "internal/modulos/catalogo/menu/menu.go") // su import lo cubre un puente
	exigeOrdenadas(t, vs)
}

// TestFronterasPasa: Capas, platform/nucleo/pendiente, stdlib y terceros, puentes vivos
// (Desde como prefijo), el arranque sobre módulos no conmutados, huella_vieja_test.go y
// viejo → viejo no son violaciones.
func TestFronterasPasa(t *testing.T) {
	fuentes := recorrerCaso(t, "testdata/fronteras/pasa", dirsFronteras, true)
	r := Reglas{
		Capas:      map[string][]string{"edge": {"acceso"}, "acceso": {}, "captacion": {"conversacion"}},
		Mapa:       mapaFronteras,
		Conmutados: []string{"solicitudes"},
		Puentes: []Puente{
			{Desde: "internal/modulos/captacion", Hacia: "internal/flujos/store", Motivo: "almacén viejo", Nace: "F7", Muere: "F8"},
		},
		FasesCerradas: []string{"F0"},
	}
	exigeCero(t, Fronteras(moduloWapp, fuentes, r))
}
