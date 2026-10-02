//go:build integracion

package procesos

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
)

// Los puertos del servidor de prueba: la reserva de cuatro direcciones libres que no se repiten
// dentro de la corrida, y el punto por el que arrancar las pide.
// Sale de servidor_test.go (D-F9-11: solo se movieron declaraciones).

var (
	// puertosMu protege puertosEntregados.
	puertosMu sync.Mutex
	// puertosEntregados son los puertos que reservarPuertos ya ha entregado en esta corrida: dos
	// servidores en paralelo del mismo proceso de test nunca reciben el mismo, aunque el kernel
	// reciclara uno que el primero ya liberó (la carrera con OTROS procesos la cubre el reintento).
	puertosEntregados = map[int]struct{}{}

	// elegirPuertos es el punto por el que arrancar pide sus cuatro direcciones. Es una variable
	// solo para que TestArnes_ReintentoPuertoOcupado fuerce una colisión; en el resto vale
	// reservarPuertos.
	elegirPuertos = reservarPuertos
)

// reservarPuertos recibe cuántas direcciones quiere y devuelve ese número de "127.0.0.1:<puerto>"
// libres y DISTINTAS entre sí y de las ya entregadas en esta corrida. Abre n listeners en el puerto
// 0, lee los puertos que dio el kernel y los cierra todos al final: entre ese cierre y que el
// servidor enlace hay una carrera con otros procesos de la máquina, que el reintento de arrancar
// absorbe. Devuelve error si el kernel no da puertos o no se consiguen n distintos.
func reservarPuertos(n int) (direcciones []string, err error) {
	puertosMu.Lock()
	defer puertosMu.Unlock()

	var escuchas []net.Listener
	defer func() {
		if errCierre := cerrarEscuchas(escuchas); errCierre != nil && err == nil {
			direcciones, err = nil, errCierre
		}
	}()
	// Los listeners repetidos se mantienen abiertos hasta el final para que el kernel no los
	// vuelva a ofrecer en este mismo bucle.
	for intentos := 0; len(direcciones) < n; intentos++ {
		if intentos >= 20*n {
			return nil, fmt.Errorf("no se consiguieron %d puertos libres distintos tras %d intentos", n, intentos)
		}
		escucha, errEscucha := net.Listen("tcp", "127.0.0.1:0")
		if errEscucha != nil {
			return nil, fmt.Errorf("reservar un puerto libre: %w", errEscucha)
		}
		escuchas = append(escuchas, escucha)
		tcp, ok := escucha.Addr().(*net.TCPAddr)
		if !ok {
			return nil, fmt.Errorf("la dirección reservada %v no es TCP", escucha.Addr())
		}
		if _, repetido := puertosEntregados[tcp.Port]; repetido {
			continue
		}
		puertosEntregados[tcp.Port] = struct{}{}
		direcciones = append(direcciones, net.JoinHostPort("127.0.0.1", strconv.Itoa(tcp.Port)))
	}
	return direcciones, nil
}

// cerrarEscuchas cierra todos los listeners y devuelve los errores de cierre unidos (nil si
// ninguno falló).
func cerrarEscuchas(escuchas []net.Listener) error {
	var errs []error
	for _, e := range escuchas {
		if err := e.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
