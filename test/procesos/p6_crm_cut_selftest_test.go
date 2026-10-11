//go:build integracion

package procesos

import (
	"bytes"
	"errors"
	"net"
	"testing"
)

// TestP6CutWhileSending prueba el clasificador con el que (*p6World).try decide qué error de
// transporte se reintenta: reconoce el corte REAL de una conexión cuyo cuerpo el servidor no leyó, y
// no confunde con él ni la falta de error ni un servidor que no está. Existe porque en los procesos
// ese corte es una carrera que casi nunca se pierde (T8.42): sin este test, la rama no la ejercitaría
// nadie.
func TestP6CutWhileSending(t *testing.T) {
	// Un servidor que acepta, lee UN byte y cierra con RST dejando el resto del cuerpo sin leer: lo
	// mismo que hace net/http cuando el handler contesta sin leer el cuerpo, pero siempre.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("escuchar: %v", err)
	}
	// reset corta una conexión aceptada; lo que falle al hacerlo se afirma al final, en el test.
	reset := func(conn net.Conn) error {
		if _, err := conn.Read(make([]byte, 1)); err != nil {
			return errors.Join(err, conn.Close())
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			if err := tcp.SetLinger(0); err != nil {
				return errors.Join(err, conn.Close())
			}
		}
		return conn.Close()
	}
	failures := make(chan error, 1)
	go func() {
		var failed error
		defer func() { failures <- failed }()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return // el listener se cerró: fin del servidor
			}
			failed = errors.Join(failed, reset(conn))
		}
	}()
	addr := "http://" + listener.Addr().String()

	// 4 MiB no caben en los búferes del socket: el cliente sigue escribiendo cuando llega el corte.
	cb := crmFakeCallback{Body: bytes.Repeat([]byte(" "), 4<<20)}
	if _, err := cb.send(t.Context(), addr); !p6CutWhileSending(err) {
		t.Errorf("el corte con el cuerpo a medio enviar no se reconoce: %v", err)
	}

	if p6CutWhileSending(nil) {
		t.Error("la falta de error se toma por un corte")
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("cerrar el servidor: %v", err)
	}
	if err := <-failures; err != nil {
		t.Fatalf("el servidor de la prueba no pudo cortar la conexión: %v", err)
	}
	// Sin nadie escuchando el error es «connection refused»: un servidor muerto NO es un corte.
	if _, err := cb.send(t.Context(), addr); err == nil || p6CutWhileSending(err) {
		t.Errorf("un servidor que no está se toma por un corte (o no da error): %v", err)
	}
}
