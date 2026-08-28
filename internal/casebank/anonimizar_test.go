package casebank_test

import (
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/casebank"
)

// anonimizar_test.go — los tests del barrido de PII de T5.3.
//
// 🔴 EL PAR QUE HACE QUE ESTO NO SEA DECORADO: los tests de abajo van SIEMPRE en
// dos mitades — «esto se tapa» y «esto NO se toca»—. Un anonimizador que
// devolviera "[TELEFONO]" para cualquier entrada pasaría la primera mitad entera
// y es exactamente lo que rompería el banco de casos: sin cantidades no hay nada
// que evaluar en P4.

// nombresDePrueba es la lista con la que se arman los anonimizadores de este
// fichero. Incluye un nombre acentuado a propósito: el límite de palabra de Go
// (`\b`) es ASCII y se equivocaría en el borde de «Fusión», y por eso el
// anonimizador decodifica la runa en vez de usarlo.
func nombresDePrueba() []string { return []string{"Ambar", "Herminia", "Fusión", "Ana", "Ana María"} }

func anon() casebank.Anonimizador {
	return casebank.NuevoAnonimizador(nombresDePrueba()...)
}

// ---------------------------------------------------------------------------
// JID
// ---------------------------------------------------------------------------

// TestAnonimizar_JID cubre los cinco dominios de WhatsApp que el patrón conoce.
//
// 💥 Mutación: quitar `g.us` de la alternancia de `reJID` ⇒ el caso del grupo
// deja el JID entero en el texto y este test se pone rojo.
func TestAnonimizar_JID(t *testing.T) {
	casos := []struct {
		nombre, entrada, quiero string
	}{
		{"individual", "escribe a 584121234567@s.whatsapp.net ya", "escribe a [JID] ya"},
		{"grupo", "el grupo 120363012345678901@g.us", "el grupo [JID]"},
		{"c.us", "puente: 5491133334444@c.us", "puente: [JID]"},
		{"lid", "oculto: 98765432101234@lid", "oculto: [JID]"},
		{"al final sin espacio", "manda a 584121234567@s.whatsapp.net", "manda a [JID]"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := anon().Anonimizar(c.entrada); got != c.quiero {
				t.Errorf("Anonimizar(%q) = %q; se esperaba %q", c.entrada, got, c.quiero)
			}
		})
	}
}

// TestAnonimizar_UnCorreoNoEsUnJID_YNoSeToca fija el agujero DECLARADO: el patrón
// exige uno de los dominios de WhatsApp, así que un correo pasa entero. Está
// escrito como test para que el día que alguien «mejore» el patrón a un `@`
// genérico se entere de que está cambiando el alcance, no arreglando un fallo.
func TestAnonimizar_UnCorreoNoEsUnJID_YNoSeToca(t *testing.T) {
	const entrada = "mi correo es ambar.perez@gmail.com"
	// El NOMBRE sí cae (está en la lista); el correo, no.
	got := anon().Anonimizar(entrada)
	if !strings.Contains(got, "@gmail.com") {
		t.Fatalf("Anonimizar(%q) = %q; el dominio del correo tenía que sobrevivir: este anonimizador NO cubre correos", entrada, got)
	}
	if strings.Contains(got, casebank.MarcaJID) {
		t.Errorf("Anonimizar(%q) = %q; un correo NO es un JID y no debe marcarse como tal", entrada, got)
	}
}

// ---------------------------------------------------------------------------
// TELÉFONOS — las dos mitades
// ---------------------------------------------------------------------------

// TestAnonimizar_Telefonos es la mitad «esto se tapa»: los formatos con y sin
// `+`, con espacios, guiones y paréntesis.
//
// 💥 Mutación: subir `minDigitosTelefono` a 12 ⇒ los de 10 y 11 dígitos
// sobreviven y este test se pone rojo.
func TestAnonimizar_Telefonos(t *testing.T) {
	casos := []struct {
		nombre, entrada string
	}{
		{"con + y espacios", "llámame al +58 412 123 4567 porfa"},
		{"con guiones", "mi número es 0412-123-4567"},
		{"seguido", "anota 04121234567"},
		{"con paréntesis", "fijo (0212) 555 6677"},
		{"con puntos", "es el 412.123.4567"},
		{"internacional pegado", "+34911223344 es el mío"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			got := anon().Anonimizar(c.entrada)
			if !strings.Contains(got, casebank.MarcaTelefono) {
				t.Fatalf("Anonimizar(%q) = %q; el teléfono no se tapó", c.entrada, got)
			}
			if quedan := digitosDe(got); quedan != 0 {
				t.Errorf("Anonimizar(%q) = %q; quedaron %d dígitos del número", c.entrada, got, quedan)
			}
		})
	}
}

// TestAnonimizar_NoSeComeLasCantidadesDelPedido es LA OTRA MITAD, y es la que de
// verdad decide si este banco de casos sirve para algo: si el anonimizador se
// come «10 o 12 porciones» o «paquete de 30», el dataset ya no puede evaluar a
// P4, que es la etapa que vive de esos números.
//
// 💥 Mutación: bajar `minDigitosTelefono` a 2 ⇒ «10 o 12» y «25 o 30» pasan a
// `[TELEFONO]` y este test se pone rojo. Es la mutación que el test de arriba NO
// caza, y por eso hacen falta los dos.
func TestAnonimizar_NoSeComeLasCantidadesDelPedido(t *testing.T) {
	intactos := []string{
		"de 10 o 12 porciones",
		"de 25 o 30 porciones",
		"un paquete de tequeños congelados de 30",
		"para el 22/07",
		"Serían 2 tortas",
		"5 kg de harina",
		"pedido 887",
	}
	a := anon()
	for _, texto := range intactos {
		t.Run(texto, func(t *testing.T) {
			if got := a.Anonimizar(texto); got != texto {
				t.Errorf("Anonimizar(%q) = %q; una cantidad del pedido NO es un teléfono", texto, got)
			}
			if r := a.Restos(texto); len(r) != 0 {
				t.Errorf("Restos(%q) = %v; el barrido no puede delatar una cantidad del pedido", texto, r)
			}
		})
	}
}

// TestAnonimizar_SeparadoresQueUnaVezSeEscaparon es el test de REGRESIÓN del
// defecto medido el 2026-08-27: con la barra fuera de la clase de separadores,
// `0412/1234567` salía INTACTO de `Anonimizar` y —lo grave— `Restos` devolvía
// `[]` sobre él. Un barrido que dice «limpio» sobre un teléfono completo es peor
// que no tener barrido: `Restos` existe justamente para auditar texto que NO pasó
// por `Anonimizar` (un fixture escrito a mano, un caso pegado desde un ticket).
//
// 🔴 LAS DOS MITADES SON OBLIGATORIAS y por eso están en el mismo test: el
// defecto se manifestaba en las dos y una sola de ellas no lo habría cazado
// entero. Quien añada un separador nuevo a la clase añade su caso AQUÍ.
//
// 💥 Mutación (ejecutada): quitar `/` de la clase de `reCandidatoTelefono` ⇒ los
// dos subtests de la barra caen, uno por `Anonimizar` y otro por `Restos`.
func TestAnonimizar_SeparadoresQueUnaVezSeEscaparon(t *testing.T) {
	casos := []struct {
		nombre, entrada string
	}{
		{"barra", "llamame al 0412/1234567"},
		{"barra con prefijo internacional", "+58/412/123/4567"},
		{"guion bajo", "anota 0412_123_4567"},
		{"salto de linea", "mi numero:\n0412\n1234567"},
		{"retorno de carro", "mi numero:\r\n0412\r\n1234567"},
		{"mezcla de separadores", "el (0212)/555_66-77"},
	}
	a := anon()
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			// (a) LA REDACCIÓN.
			got := a.Anonimizar(c.entrada)
			if !strings.Contains(got, casebank.MarcaTelefono) {
				t.Errorf("Anonimizar(%q) = %q; el teléfono NO se tapó", c.entrada, got)
			}
			if quedan := digitosDe(got); quedan != 0 {
				t.Errorf("Anonimizar(%q) = %q; quedaron %d dígitos del número", c.entrada, got, quedan)
			}
			// (b) EL BARRIDO, que es la mitad que más importa: éste corre sobre
			// texto que nadie redactó, y decir «cero hallazgos» aquí sería
			// afirmar que un teléfono completo no es PII.
			restos := a.Restos(c.entrada)
			if len(restos) == 0 {
				t.Fatalf("Restos(%q) = []; el barrido dice LIMPIO sobre un teléfono entero", c.entrada)
			}
			if restos[0].Clase != casebank.ClaseTelefono {
				t.Errorf("Restos(%q) delató un %q; se esperaba %q",
					c.entrada, restos[0].Clase, casebank.ClaseTelefono)
			}
		})
	}
}

// TestAnonimizar_FalsoPositivoConocido_UnaFechaLargaSeTapa deja escrito en forma
// de test el falso positivo que la cabecera del anonimizador declara: una fecha
// con separadores admitidos suma 8 dígitos y se redacta.
//
// Se acepta y se fija: en un barrido de PII, tapar una fecha es barato y dejar un
// número no lo es. Si alguien lo «arregla», este test se lo dirá y podrá decidir
// a sabiendas en vez de descubrirlo en el dataset.
func TestAnonimizar_FalsoPositivoConocido_UnaFechaLargaSeTapa(t *testing.T) {
	// 🆕 El segundo caso NACIÓ el 2026-08-27 al meter la barra en la clase de
	// separadores para cerrar el pase de `0412/1234567`. Es una pérdida real —una
	// fecha de entrega es dato del pedido— y se acepta: el intercambio es «tapo
	// alguna fecha» contra «publico algún teléfono», y no está empatado.
	fechas := []string{
		"el 2026 08 27 a las 10", // separadores de siempre
		"para el 22/07/2026",     // 🆕 la barra: 8 dígitos
		"del 01-02-2026",         // el guion ya estaba, mismo efecto
	}
	for _, entrada := range fechas {
		t.Run(entrada, func(t *testing.T) {
			got := anon().Anonimizar(entrada)
			if !strings.Contains(got, casebank.MarcaTelefono) {
				t.Fatalf("Anonimizar(%q) = %q; se esperaba el falso positivo DECLARADO (8 dígitos)", entrada, got)
			}
		})
	}
}

// TestAnonimizar_LaFechaCORTADelPedidoSobrevive acota el falso positivo de arriba:
// lo que se pierde son las fechas de OCHO dígitos, no toda fecha. `22/07` —la
// forma en que la fecha aparece en el caso Ambar y en la mayoría de los hilos—
// tiene 4 y sigue intacta. Sin esta mitad, «tapamos alguna fecha» podría degenerar
// en «tapamos todas» sin que nadie se enterara.
func TestAnonimizar_LaFechaCORTADelPedidoSobrevive(t *testing.T) {
	cortas := []string{"para el 22/07", "el 3/8", "entre el 10 y el 12"}
	a := anon()
	for _, entrada := range cortas {
		t.Run(entrada, func(t *testing.T) {
			if got := a.Anonimizar(entrada); got != entrada {
				t.Errorf("Anonimizar(%q) = %q; una fecha corta del pedido NO es un teléfono", entrada, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// NOMBRES — las dos mitades
// ---------------------------------------------------------------------------

// TestAnonimizar_Nombres cubre lo que la lista sí conoce, incluida la adyacencia
// —dos nombres pegados— que un patrón que CONSUMA el separador dejaría a medias.
//
// 💥 Mutación: implementar el límite consumiendo el separador
// (`(^|[^\p{L}\p{N}])(nombre)($|[^\p{L}\p{N}])`) ⇒ el caso «dos nombres seguidos»
// solo tapa el primero y este test se pone rojo. Es la razón de que
// `buscarConLimites` decodifique runas en vez de usar un patrón con bordes.
func TestAnonimizar_Nombres(t *testing.T) {
	casos := []struct {
		nombre, entrada, quiero string
	}{
		{"simple", "habló con Ambar", "habló con [NOMBRE]"},
		{"minúsculas", "habló con ambar", "habló con [NOMBRE]"},
		{"mayúsculas", "habló con AMBAR", "habló con [NOMBRE]"},
		{"acentuado", "el negocio Fusión cerró", "el negocio [NOMBRE] cerró"},
		{"dos nombres seguidos", "Ambar Herminia hablaron", "[NOMBRE] [NOMBRE] hablaron"},
		{"el más largo gana", "pregunta por Ana María", "pregunta por [NOMBRE]"},
		{"al principio", "Ambar pidió tortas", "[NOMBRE] pidió tortas"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := anon().Anonimizar(c.entrada); got != c.quiero {
				t.Errorf("Anonimizar(%q) = %q; se esperaba %q", c.entrada, got, c.quiero)
			}
		})
	}
}

// TestAnonimizar_UnNombreDentroDeOtraPalabraNoSeToca es la otra mitad: sin límite
// de palabra, «ambarina» y «Anacleto» se convertirían en «[NOMBRE]ina» y
// «[NOMBRE]cleto», que además de absurdo destruiría el texto del pedido.
func TestAnonimizar_UnNombreDentroDeOtraPalabraNoSeToca(t *testing.T) {
	intactos := []string{"una torta ambarina", "Anacleto trajo el pedido", "herminiana"}
	for _, texto := range intactos {
		t.Run(texto, func(t *testing.T) {
			if got := anon().Anonimizar(texto); got != texto {
				t.Errorf("Anonimizar(%q) = %q; el nombre está DENTRO de otra palabra", texto, got)
			}
		})
	}
}

// TestAnonimizar_SinListaDeNombres_NoTapaNingunNombre fija la limitación grande y
// declarada: no hay reconocimiento de entidades. Un nombre que nadie declaró pasa
// entero, y las otras dos mitades siguen funcionando.
func TestAnonimizar_SinListaDeNombres_NoTapaNingunNombre(t *testing.T) {
	vacio := casebank.NuevoAnonimizador()
	const entrada = "Ambar escribió desde 584121234567@s.whatsapp.net"
	got := vacio.Anonimizar(entrada)
	if !strings.Contains(got, "Ambar") {
		t.Errorf("Anonimizar(%q) = %q; SIN lista no hay NER: el nombre tenía que pasar entero", entrada, got)
	}
	if !strings.Contains(got, casebank.MarcaJID) {
		t.Errorf("Anonimizar(%q) = %q; el JID sí tenía que taparse", entrada, got)
	}
	if len(vacio.Nombres()) != 0 {
		t.Errorf("Nombres() = %v; se esperaba vacío", vacio.Nombres())
	}
}

// ---------------------------------------------------------------------------
// EL ORDEN DE LAS PASADAS
// ---------------------------------------------------------------------------

// TestAnonimizar_ElJIDSeTapaANTESQueElTelefonoQueLleva es una aserción sobre el
// ORDEN, que es parte de la corrección y no una preferencia: un JID lleva el
// número dentro, así que la pasada de teléfonos, si corriera primero, lo partiría
// en `[TELEFONO]@s.whatsapp.net` — perdiendo la marca buena y DEJANDO EL DOMINIO,
// que ya dice que ese contacto es de WhatsApp.
//
// 💥 Mutación: intercambiar las dos primeras líneas de `Anonimizar` ⇒ rojo.
func TestAnonimizar_ElJIDSeTapaANTESQueElTelefonoQueLleva(t *testing.T) {
	const entrada = "desde 584121234567@s.whatsapp.net"
	const quiero = "desde [JID]"
	got := anon().Anonimizar(entrada)
	if got != quiero {
		t.Fatalf("Anonimizar(%q) = %q; se esperaba %q", entrada, got, quiero)
	}
	if strings.Contains(got, "s.whatsapp.net") {
		t.Error("el dominio del JID sobrevivió: la pasada de teléfonos corrió antes que la del JID")
	}
}

// ---------------------------------------------------------------------------
// EL BARRIDO — con su control negativo
// ---------------------------------------------------------------------------

// TestRestos_DelataLasTresClases es EL CONTROL NEGATIVO sin el cual «la semilla
// pasa el barrido» no probaría nada: un barrido que no mirase devolvería cero
// hallazgos siempre y satisfaría igual aquel test.
//
// 💥 Mutación: hacer que `Restos` devuelva `nil` ⇒ rojo aquí, y el test de la
// semilla seguiría VERDE. Ese contraste es el motivo de que este test exista.
func TestRestos_DelataLasTresClases(t *testing.T) {
	const sucio = "Ambar escribió al +58 412 123 4567 desde 584121234567@s.whatsapp.net"
	restos := anon().Restos(sucio)
	if len(restos) != 3 {
		t.Fatalf("Restos(%q) devolvió %d hallazgos, se esperaban 3: %v", sucio, len(restos), restos)
	}
	quiero := []casebank.Clase{casebank.ClaseNombre, casebank.ClaseTelefono, casebank.ClaseJID}
	for i, c := range quiero {
		if restos[i].Clase != c {
			t.Errorf("hallazgo %d: clase %q; se esperaba %q (van en ORDEN DE APARICIÓN)", i, restos[i].Clase, c)
		}
		if restos[i].Texto == "" {
			t.Errorf("hallazgo %d: sin texto; quien cura necesita ver QUÉ tapar", i)
		}
	}
}

// TestRestos_TextoLimpio_CeroHallazgos es la mitad complementaria.
func TestRestos_TextoLimpio_CeroHallazgos(t *testing.T) {
	const limpio = "quiero una torta de 10 o 12 porciones para el miércoles"
	if r := anon().Restos(limpio); len(r) != 0 {
		t.Errorf("Restos(%q) = %v; se esperaba vacío", limpio, r)
	}
}

// digitosDe cuenta los dígitos que quedan en el texto ya anonimizado. Se escribe
// aquí y no se reusa el helper interno: un test que llamara a la MISMA función
// que el código bajo prueba usa para decidir no estaría comprobando nada.
func digitosDe(s string) int {
	n := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n++
		}
	}
	return n
}
