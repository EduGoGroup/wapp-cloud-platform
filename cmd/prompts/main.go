// Command prompts vuelca a disco los prompts AJUSTABLES de las etapas P2–P5 con el
// texto que corre HOY, y comprueba un directorio ya editado.
//
// Es la herramienta de la palanca `WAPP_LLM_PROMPTS_DIR`. Dos usos, y nada más:
//
//	prompts -volcar /etc/wapp/prompts     escribe los CUATRO ficheros de partida
//	prompts -comprobar /etc/wapp/prompts  valida el directorio SIN arrancar el cloud
//
// 🔴 VOLCAR ES LA ÚNICA FORMA CORRECTA DE EMPEZAR. Escribir los ficheros a mano
// copiándolos de la documentación o de otro entorno produce plantillas que nacen
// viejas y cambian el prompt sin que nadie lo haya querido — y el cambio no se ve
// en ningún diff, porque el fichero es nuevo. Lo que vuelca este comando sale del
// código compilado, así que el diff contra lo que edites es exactamente lo que
// cambiaste.
//
// 🔴 COMPROBAR EXISTE PARA NO APRENDERLO EN EL REINICIO. El cloud aborta el
// arranque ante un directorio inválido, que es la política correcta, pero
// enterarse de una llave mal cerrada cuando el servicio ya está abajo es caro.
// Esto corre exactamente las mismas comprobaciones —el mismo prompts.Cargar— sin
// tocar nada.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/prompts"
	"github.com/EduGoGroup/wapp-shared/llm"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "prompts: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	volcar := flag.String("volcar", "", "directorio donde escribir los ficheros de partida")
	comprobar := flag.String("comprobar", "", "directorio a validar, sin escribir nada")
	flag.Parse()

	switch {
	case *volcar != "" && *comprobar != "":
		return errors.New("-volcar y -comprobar son excluyentes: elige uno")
	case *volcar != "":
		return ejecutarVolcado(*volcar)
	case *comprobar != "":
		return ejecutarComprobacion(*comprobar)
	default:
		flag.Usage()
		return errors.New("hace falta -volcar o -comprobar")
	}
}

func ejecutarVolcado(dir string) error {
	rutas, err := prompts.Volcar(dir)
	if err != nil {
		return err
	}
	for _, r := range rutas {
		fmt.Println("escrito", r)
	}
	fmt.Printf("\nAhora edita lo que quieras y arranca el cloud con WAPP_LLM_PROMPTS_DIR=%s\n", dir)
	fmt.Println("Un cambio se aplica al REINICIAR: no hay recarga en caliente.")
	return nil
}

func ejecutarComprobacion(dir string) error {
	cargadas, err := prompts.Cargar(dir)
	if err != nil {
		return err
	}
	// Se imprime el origen de LAS CUATRO, no solo el de las que traían fichero: la
	// pregunta que trae aquí a alguien suele ser «¿por qué mi cambio no se aplica?»,
	// y la respuesta casi siempre es que esa etapa salió compilada.
	for _, e := range llm.EtapasAjustables {
		fmt.Printf("%-3s %s\n", e, cargadas.Origen[e])
	}
	fmt.Println("\nOK: el cloud arrancaría con este directorio.")
	return nil
}
