//go:build integracion

package procesos

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// El parser de la exposición de Prometheus que usa P0 para leer /metrics: los tipos de cada familia
// y las muestras con sus etiquetas. Solo usa la biblioteca estándar: otros procesos lo pueden usar.
// Sale de p0_arranque_test.go (D-F9-11: solo se movieron declaraciones).

// p0Muestra es una línea de muestra de la exposición de Prometheus: nombre, etiquetas y valor.
type p0Muestra struct {
	Nombre    string
	Etiquetas map[string]string
	Valor     float64
}

// p0Exposicion es el cuerpo de /metrics ya interpretado: el tipo de cada familia (de sus líneas
// «# TYPE nombre tipo») y todas las muestras, en el orden del cuerpo.
type p0Exposicion struct {
	Tipos    map[string]string
	Muestras []p0Muestra
}

// p0ParsearExposicion recibe el cuerpo de /metrics y devuelve su contenido interpretado: las líneas
// «# TYPE» (las «# HELP» y cualquier otro comentario se ignoran) y las muestras «nombre{etiquetas}
// valor». Devuelve error si un «# TYPE» no tiene nombre y tipo, o si una muestra no tiene
// nombre, etiquetas bien cerradas o un valor numérico. Solo usa la biblioteca estándar.
func p0ParsearExposicion(cuerpo string) (p0Exposicion, error) {
	exp := p0Exposicion{Tipos: map[string]string{}}
	for linea := range strings.SplitSeq(cuerpo, "\n") {
		linea = strings.TrimSpace(linea)
		switch {
		case linea == "":
		case strings.HasPrefix(linea, "# TYPE "):
			campos := strings.Fields(linea)
			if len(campos) != 4 {
				return exp, fmt.Errorf("línea TYPE mal formada: %q", linea)
			}
			exp.Tipos[campos[2]] = campos[3]
		case strings.HasPrefix(linea, "#"):
		default:
			m, err := p0ParsearMuestra(linea)
			if err != nil {
				return exp, err
			}
			exp.Muestras = append(exp.Muestras, m)
		}
	}
	return exp, nil
}

// p0ParsearMuestra recibe una línea de muestra («nombre valor» o «nombre{k="v",…} valor», con una
// marca de tiempo opcional) y devuelve la muestra. Devuelve error si no tiene nombre, si sus
// etiquetas están mal cerradas o si el valor no es un número (acepta +Inf, -Inf y NaN).
func p0ParsearMuestra(linea string) (p0Muestra, error) {
	fin := strings.IndexAny(linea, "{ ")
	if fin <= 0 {
		return p0Muestra{}, fmt.Errorf("muestra sin nombre o sin valor: %q", linea)
	}
	m := p0Muestra{Nombre: linea[:fin], Etiquetas: map[string]string{}}
	resto := linea[fin:]
	if strings.HasPrefix(resto, "{") {
		var err error
		if m.Etiquetas, resto, err = p0ParsearEtiquetas(resto); err != nil {
			return p0Muestra{}, fmt.Errorf("muestra %q: %w", linea, err)
		}
	}
	campos := strings.Fields(resto)
	if len(campos) == 0 || len(campos) > 2 {
		return p0Muestra{}, fmt.Errorf("muestra sin valor (o con basura detrás): %q", linea)
	}
	valor, err := strconv.ParseFloat(campos[0], 64)
	if err != nil {
		return p0Muestra{}, fmt.Errorf("muestra %q: el valor no es un número: %w", linea, err)
	}
	m.Valor = valor
	return m, nil
}

// p0ParsearEtiquetas recibe el texto que empieza en «{» y devuelve las etiquetas k="v" hasta el «}»
// que las cierra, y lo que queda detrás. Respeta las comillas: un valor puede contener «{», «}»
// y «,» (la ruta /api/v1/flows/{id} es una etiqueta real). Devuelve error si falta un «="», una
// comilla de cierre o el «}».
func p0ParsearEtiquetas(texto string) (etiquetas map[string]string, resto string, err error) {
	etiquetas = map[string]string{}
	resto = strings.TrimPrefix(texto, "{")
	for !strings.HasPrefix(resto, "}") {
		clave, tras, hay := strings.Cut(resto, `="`)
		if !hay {
			return nil, "", errors.New(`etiqueta sin «="» o sin «}» de cierre`)
		}
		var valor string
		if valor, resto, err = p0LeerValorEtiqueta(tras); err != nil {
			return nil, "", err
		}
		etiquetas[strings.TrimSpace(clave)] = valor
		resto = strings.TrimPrefix(resto, ",")
	}
	return etiquetas, strings.TrimPrefix(resto, "}"), nil
}

// p0LeerValorEtiqueta recibe el texto que sigue a la comilla de apertura de un valor de etiqueta y
// devuelve el valor (con \\, \" y \n ya deshechos) y lo que queda tras la comilla de cierre.
// Devuelve error si el texto se acaba sin comilla de cierre.
func p0LeerValorEtiqueta(texto string) (valor, resto string, err error) {
	var sb strings.Builder
	for i := 0; i < len(texto); i++ {
		switch c := texto[i]; c {
		case '"':
			return sb.String(), texto[i+1:], nil
		case '\\':
			i++
			if i >= len(texto) {
				return "", "", errors.New("valor de etiqueta que acaba en «\\»")
			}
			if texto[i] == 'n' {
				sb.WriteByte('\n')
			} else {
				sb.WriteByte(texto[i])
			}
		default:
			sb.WriteByte(c)
		}
	}
	return "", "", errors.New("valor de etiqueta sin comilla de cierre")
}
