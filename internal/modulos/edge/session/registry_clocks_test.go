package session

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

// Los DOS RELOJES de Push y de BoundedSend: el plazo propio (ErrPushTimeout) y el ctx del
// llamante (ErrPushAbandoned). Los dobles (recordingSender, blockingSender) y los contextos de
// ayuda viven en registry_test.go.

// R-S2: un Edge que no lee su stream → ErrPushTimeout dentro del sendTimeout DEL REGISTRY, sin
// ningún error de ctx (el timer es propio: no es un context.WithTimeout derivado).
func TestPushTimeoutWhenTheEdgeDoesNotRead(t *testing.T) {
	t.Parallel()
	reg := NewRegistry(WithSendTimeout(20 * time.Millisecond))
	reg.Register("s1", newBlockingSender(t))

	start := time.Now()
	err := reg.Push(context.Background(), "s1", newSendText("hola"))
	if !errors.Is(err, ErrPushTimeout) {
		t.Fatalf("Push a un Edge bloqueado devolvió %v, quiero ErrPushTimeout", err)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.Is(err, ErrPushAbandoned) {
		t.Fatalf("el vencimiento del sendTimeout NO debe envolver un error de ctx ni el de abandono: %v", err)
	}
	if want := `timeout empujando comando al Edge: "s1"`; err.Error() != want {
		t.Fatalf("texto = %q, quiero %q", err.Error(), want)
	}
	if elapsed := time.Since(start); elapsed > watchdog {
		t.Fatalf("Push tardó %v: no usó el sendTimeout del Registry (20ms)", elapsed)
	}
}

// R-S3: el llamante se rinde antes de que el Send conteste → ErrPushAbandoned junto a ctx.Err(),
// NO ErrPushTimeout. El sendTimeout va alto a propósito: sin el brazo de ctx.Done() el test no
// fallaría por aserción sino por tardar un minuto.
func TestPushAbandonedWhenTheCallerGivesUp(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		ctx      func(t *testing.T) context.Context
		wantCtx  error
		wantText string
	}{
		{
			name:     "cancelled before sending",
			ctx:      func(*testing.T) context.Context { return cancelledContext() },
			wantCtx:  context.Canceled,
			wantText: `el llamante se rindió empujando el comando al Edge: "s1": context canceled`,
		},
		{
			name:     "deadline already expired",
			ctx:      expiredContext,
			wantCtx:  context.DeadlineExceeded,
			wantText: `el llamante se rindió empujando el comando al Edge: "s1": context deadline exceeded`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			reg := NewRegistry(WithSendTimeout(time.Minute))
			reg.Register("s1", newBlockingSender(t))

			start := time.Now()
			err := reg.Push(c.ctx(t), "s1", newSendText("hola"))
			if !errors.Is(err, ErrPushAbandoned) {
				t.Fatalf("Push devolvió %v, quiero que envuelva ErrPushAbandoned", err)
			}
			if !errors.Is(err, c.wantCtx) {
				t.Fatalf("Push devolvió %v, quiero que envuelva %v", err, c.wantCtx)
			}
			if errors.Is(err, ErrPushTimeout) {
				t.Fatalf("un abandono del llamante NO debe envolver ErrPushTimeout: %v", err)
			}
			if err.Error() != c.wantText {
				t.Fatalf("texto = %q, quiero %q", err.Error(), c.wantText)
			}
			if elapsed := time.Since(start); elapsed > watchdog {
				t.Fatalf("Push tardó %v con el ctx ya terminado: no salió por ctx.Done()", elapsed)
			}
		})
	}
}

// R-S3, con el Send YA en vuelo: la cancelación llega mientras el Edge no lee. Push sale con el
// de abandono; el Send sigue bloqueado (cancelar el ctx no lo desbloquea, y no se promete).
func TestPushAbandonedWhileTheSendIsInFlight(t *testing.T) {
	t.Parallel()
	reg := NewRegistry(WithSendTimeout(time.Minute))
	sender := newBlockingSender(t)
	reg.Register("s1", sender)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- reg.Push(ctx, "s1", newSendText("hola")) }()

	<-sender.entered // el Send ya está bloqueado en el «stream».
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, ErrPushAbandoned) || !errors.Is(err, context.Canceled) || errors.Is(err, ErrPushTimeout) {
			t.Fatalf("Push devolvió %v, quiero ErrPushAbandoned + context.Canceled y no ErrPushTimeout", err)
		}
	case <-time.After(watchdog):
		t.Fatal("Push no volvió tras cancelar el ctx con el Send en vuelo")
	}
}

// BoundedSend, las tres ramas, sin Registry (es el camino in-band del gateway): el `target` va
// al mensaje y el plazo es el que se le pasa.
func TestBoundedSend(t *testing.T) {
	t.Parallel()
	sendErr := errors.New("stream roto")

	t.Run("delivers and returns nil", func(t *testing.T) {
		t.Parallel()
		s := &recordingSender{}
		msg := newSendText("hola")
		if err := BoundedSend(context.Background(), s, msg, time.Minute, "edge-1"); err != nil {
			t.Fatalf("BoundedSend devolvió %v, quiero nil", err)
		}
		if s.count() != 1 || s.sent[0] != msg {
			t.Fatalf("el sender recibió %d mensajes (o no el mismo puntero)", s.count())
		}
	})

	t.Run("returns the sender error unwrapped", func(t *testing.T) {
		t.Parallel()
		err := BoundedSend(context.Background(), &recordingSender{err: sendErr}, newSendText("hola"), time.Minute, "edge-1")
		if err != sendErr { //nolint:errorlint // se afirma que llega SIN envolver
			t.Fatalf("BoundedSend devolvió %v, quiero el error del Sender tal cual", err)
		}
	})

	t.Run("own timeout", func(t *testing.T) {
		t.Parallel()
		start := time.Now()
		err := BoundedSend(context.Background(), newBlockingSender(t), newSendText("hola"), 20*time.Millisecond, "edge-1")
		if !errors.Is(err, ErrPushTimeout) || errors.Is(err, ErrPushAbandoned) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("BoundedSend devolvió %v, quiero solo ErrPushTimeout", err)
		}
		if want := `timeout empujando comando al Edge: "edge-1"`; err.Error() != want {
			t.Fatalf("texto = %q, quiero %q", err.Error(), want)
		}
		if elapsed := time.Since(start); elapsed > watchdog {
			t.Fatalf("BoundedSend tardó %v: no respetó su plazo (20ms)", elapsed)
		}
	})

	t.Run("caller gave up", func(t *testing.T) {
		t.Parallel()
		err := BoundedSend(cancelledContext(), newBlockingSender(t), newSendText("hola"), time.Minute, "edge-1")
		if !errors.Is(err, ErrPushAbandoned) || !errors.Is(err, context.Canceled) || errors.Is(err, ErrPushTimeout) {
			t.Fatalf("BoundedSend devolvió %v, quiero ErrPushAbandoned + context.Canceled", err)
		}
		if want := `el llamante se rindió empujando el comando al Edge: "edge-1": context canceled`; err.Error() != want {
			t.Fatalf("texto = %q, quiero %q", err.Error(), want)
		}
	})
}

// El canal por el que la goroutine del Send deja su resultado es BUFFERIZADO (cap 1): cuando el
// llamante ya se fue —por ctx o por timer— y el Edge por fin lee, esa goroutine tiene que poder
// dejar el resultado y MORIR. Sin el buffer se quedaría bloqueada para siempre: una goroutine
// fugada por cada envío abandonado, que con el presupuesto de la petición es el desenlace
// normal de un envío saturado.
//
// La burbuja de synctest lo hace determinista y sin reloj real: si al terminar queda una
// goroutine bloqueada dentro, synctest.Test hace fallar el test (deadlock).
func TestBoundedSendDoesNotLeakTheSendGoroutine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ctx  func() context.Context
		want error
	}{
		{"after the caller gave up", cancelledContext, ErrPushAbandoned},
		{"after the own timeout", context.Background, ErrPushTimeout},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				// Los canales nacen DENTRO de la burbuja: solo así cuentan como bloqueo duradero.
				sender := &blockingSender{entered: make(chan struct{}, 1), release: make(chan struct{})}

				err := BoundedSend(c.ctx(), sender, newSendText("hola"), time.Second, "edge-1")
				if !errors.Is(err, c.want) {
					t.Fatalf("BoundedSend devolvió %v, quiero %v", err, c.want)
				}

				<-sender.entered      // el Send está en vuelo…
				close(sender.release) // …y el Edge por fin lee, con el llamante ya fuera.
				synctest.Wait()
			})
		})
	}
}
