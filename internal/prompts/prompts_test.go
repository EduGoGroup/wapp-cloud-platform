package prompts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/prompts"
	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/stretchr/testify/require"
)

// TestVolcarYCargar_DevuelveEXACTAMENTELaPlantillaCompilada es el test que
// sostiene todo el ciclo: volcar → cargar tiene que dar, byte a byte, la misma
// plantilla que corre sin directorio.
//
// Si no fuera exacto, encender WAPP_PROMPTS_DIR cambiaría los prompts SIN que
// nadie hubiera editado nada — un cambio de conducta invisible en el diff, que es
// la peor forma de introducir uno. Cubre de paso que Serializar y Parsear son
// inversas, que el hueco {{version}} va y vuelve, y que la normalización de
// espacios coincide con los bordes de las plantillas compiladas.
func TestVolcarYCargar_DevuelveEXACTAMENTELaPlantillaCompilada(t *testing.T) {
	dir := t.TempDir()
	rutas, err := prompts.Volcar(dir)
	require.NoError(t, err)
	require.Len(t, rutas, len(llm.EtapasAjustables))

	cargadas, err := prompts.Cargar(dir)
	require.NoError(t, err)

	for _, e := range llm.EtapasAjustables {
		t.Run(string(e), func(t *testing.T) {
			esperada, ok := llm.PlantillaPorDefecto(e)
			require.True(t, ok)
			require.Equal(t, esperada, cargadas.Plantillas[e],
				"volcar y cargar cambió el prompt de %s: encender el directorio alteraría la conducta "+
					"sin que nadie hubiera editado nada", e)
			require.NotEqual(t, prompts.OrigenCompilado, cargadas.Origen[e],
				"con fichero presente, el origen tiene que ser el fichero")
		})
	}
}

// TestCargar_SinDirectorioDaLasCompiladas fija el caso normal de producción: no
// hay directorio y no debe haberlo. Que eso NO sea un error es deliberado.
func TestCargar_SinDirectorioDaLasCompiladas(t *testing.T) {
	cargadas, err := prompts.Cargar("")
	require.NoError(t, err)
	require.Len(t, cargadas.Plantillas, len(llm.EtapasAjustables))
	for _, e := range llm.EtapasAjustables {
		esperada, _ := llm.PlantillaPorDefecto(e)
		require.Equal(t, esperada, cargadas.Plantillas[e])
		require.Equal(t, prompts.OrigenCompilado, cargadas.Origen[e])
	}
}

// TestCargar_UnaEtapaSuelta comprueba la mezcla, que es el uso real: se ajusta UN
// prompt y los otros tres siguen con el texto compilado. Un cargador que exigiera
// los cuatro ficheros obligaría a copiar tres que nadie quiere tocar, y esos tres
// se quedarían viejos en silencio.
func TestCargar_UnaEtapaSuelta(t *testing.T) {
	dir := t.TempDir()
	p4, _ := llm.PlantillaPorDefecto(llm.EtapaP4)
	escribir(t, dir, "p4-lo-que-sea.tmpl", prompts.Serializar(llm.EtapaP4, p4))

	cargadas, err := prompts.Cargar(dir)
	require.NoError(t, err)
	require.Contains(t, cargadas.Origen[llm.EtapaP4], "p4-lo-que-sea.tmpl",
		"el sufijo del nombre es libre: sólo el prefijo p4- es contrato")
	require.Equal(t, prompts.OrigenCompilado, cargadas.Origen[llm.EtapaP2])
	require.Equal(t, prompts.OrigenCompilado, cargadas.Origen[llm.EtapaP5])
}

// TestCargar_LoQueTieneQueFallarEnElARRANQUE recorre las configuraciones que un
// operador produce con facilidad. Todas comparten el mismo criterio: es mejor no
// arrancar que servir un prompt que no es el que alguien creyó dejar puesto.
//
// El caso del prefijo desconocido es el importante y el que más cuesta defender:
// lo cómodo sería ignorar el fichero. Ignorarlo produce el peor síntoma posible
// —«edité el prompt, reinicié y no cambió nada»— y no deja ni una línea de log
// donde buscarlo.
func TestCargar_LoQueTieneQueFallarEnElARRANQUE(t *testing.T) {
	p4, _ := llm.PlantillaPorDefecto(llm.EtapaP4)
	buenoP4 := prompts.Serializar(llm.EtapaP4, p4)

	casos := []struct {
		nombre   string
		ficheros map[string]string
		enError  string
	}{
		{
			nombre:   "un prefijo que no es una etapa",
			ficheros: map[string]string{"p6-inventada.tmpl": buenoP4},
			enError:  "no empieza por ninguna etapa conocida",
		},
		{
			nombre:   "dos ficheros para la misma etapa",
			ficheros: map[string]string{"p4-uno.tmpl": buenoP4, "p4-dos.tmpl": buenoP4},
			enError:  "reclaman la etapa",
		},
		{
			nombre:   "sin el marcador de la instrucción",
			ficheros: map[string]string{"p4-x.tmpl": "--- ESQUEMA ---\n{\"version\": 1}\n"},
			enError:  "falta el marcador",
		},
		{
			nombre:   "las secciones al revés",
			ficheros: map[string]string{"p4-x.tmpl": "--- ESQUEMA ---\nx\n--- INSTRUCCION ---\ny\n"},
			enError:  "el orden de las secciones",
		},
		{
			nombre: "un esquema que su propio validador rechaza",
			ficheros: map[string]string{
				"p4-x.tmpl": strings.Replace(buenoP4, `"package_size": 30`, `"package_size": 0`, 1),
			},
			enError: "lo rechaza su propio validador",
		},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			dir := t.TempDir()
			for n, contenido := range c.ficheros {
				escribir(t, dir, n, contenido)
			}
			_, err := prompts.Cargar(dir)
			require.Error(t, err, "esta configuración tiene que impedir el arranque")
			require.ErrorIs(t, err, prompts.ErrPromptsDir)
			require.Contains(t, err.Error(), c.enError,
				"el error tiene que decir QUÉ pasa, no sólo que algo pasa")
		})
	}
}

// TestCargar_IgnoraLoQueNoEsPlantilla protege la vía de escape del error anterior:
// se pueden dejar notas al lado mientras no lleven la extensión.
func TestCargar_IgnoraLoQueNoEsPlantilla(t *testing.T) {
	dir := t.TempDir()
	escribir(t, dir, "NOTAS.md", "esto no es una plantilla y no debe romper nada")
	escribir(t, dir, "p4-viejo.tmpl.bak", "ni esto")

	cargadas, err := prompts.Cargar(dir)
	require.NoError(t, err)
	require.Equal(t, prompts.OrigenCompilado, cargadas.Origen[llm.EtapaP4])
}

// TestVolcar_NoPisaLoQueYaEstaba: perderle a alguien sus ajustes por un comando
// de más no es un intercambio aceptable.
func TestVolcar_NoPisaLoQueYaEstaba(t *testing.T) {
	dir := t.TempDir()
	_, err := prompts.Volcar(dir)
	require.NoError(t, err)

	_, err = prompts.Volcar(dir)
	require.ErrorIs(t, err, prompts.ErrPromptsDir)
	require.Contains(t, err.Error(), "ya existe y NO se sobrescribe")
}

// TestParsear_ElPreambuloNoViajaAlModelo fija que lo escrito antes del primer
// marcador es documentación: es lo que hace que los ficheros se puedan explicar a
// sí mismos sin ensuciar el prompt.
func TestParsear_ElPreambuloNoViajaAlModelo(t *testing.T) {
	p, err := prompts.Parsear(
		"NOTA PARA EL QUE VENGA: esto no se manda.\n\n" +
			"--- INSTRUCCION ---\nHaz esto.\n\n--- ESQUEMA ---\nEsquema de la respuesta:\n{\"version\": {{version}}}\n")
	require.NoError(t, err)
	require.NotContains(t, p.Instruccion, "NOTA PARA EL QUE VENGA")
	require.NotContains(t, p.Esquema, "NOTA PARA EL QUE VENGA")
	require.Contains(t, p.Instruccion, "Haz esto.")
	require.Contains(t, p.Esquema, `"version": 1`, "{{version}} se sustituye al cargar")
}

func escribir(t *testing.T, dir, nombre, contenido string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, nombre), []byte(contenido), 0o600))
}
