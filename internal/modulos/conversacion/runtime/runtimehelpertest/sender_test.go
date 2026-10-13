package runtimehelpertest_test

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
)

// TestSender_RecordsTextAndMediaInOneOrderedList: textos y adjuntos quedan en UNA lista, en el
// orden en que salieron, cada uno con sus campos y con el AckedCommandId que se devolvió.
func TestSender_RecordsTextAndMediaInOneOrderedList(t *testing.T) {
	sender := runtimehelpertest.NewSender()
	ctx := t.Context()

	first, err := sender.SendText(ctx, "sess", "573001112233", "hola")
	if err != nil || !first.GetOk() || first.GetAckedCommandId() != "cmd-1" {
		t.Fatalf("SendText = (%v, %v), quería un Ack correcto con cmd-1", first, err)
	}
	second, err := sender.SendMedia(ctx, "sess", "573001112233", "https://firmada", "lista.pdf", "application/pdf", "la lista", "document")
	if err != nil || !second.GetOk() || second.GetAckedCommandId() != "cmd-2" {
		t.Fatalf("SendMedia = (%v, %v), quería un Ack correcto con cmd-2", second, err)
	}
	if _, err := sender.SendText(ctx, "sess", "573001112233", "adiós"); err != nil {
		t.Fatalf("SendText: %v", err)
	}

	want := []runtimehelpertest.Send{
		{SessionID: "sess", To: "573001112233", Text: "hola", CommandID: "cmd-1"},
		{Media: true, SessionID: "sess", To: "573001112233", URL: "https://firmada", Filename: "lista.pdf", Mime: "application/pdf", Caption: "la lista", Kind: "document", CommandID: "cmd-2"},
		{SessionID: "sess", To: "573001112233", Text: "adiós", CommandID: "cmd-3"},
	}
	if got := sender.Sends(); !slices.Equal(got, want) {
		t.Errorf("Sends = %+v, quería %+v", got, want)
	}
	if got := sender.Texts(); !slices.Equal(got, []string{"hola", "adiós"}) {
		t.Errorf("Texts = %v", got)
	}
	if got := sender.Media(); !slices.Equal(got, want[1:2]) {
		t.Errorf("Media = %+v, quería %+v", got, want[1:2])
	}
}

// TestSender_InjectedErrors_FailEachDoorOnItsOwn: FailText y FailMedia fallan cada puerta por
// separado, con Ack nil, y con nil la puerta vuelve a abrirse.
func TestSender_InjectedErrors_FailEachDoorOnItsOwn(t *testing.T) {
	sender := runtimehelpertest.NewSender()
	ctx := t.Context()
	textDown, mediaDown := errors.New("texto caído"), errors.New("adjunto caído")

	sender.FailText(textDown)
	if ack, err := sender.SendText(ctx, "s", "to", "uno"); ack != nil || !errors.Is(err, textDown) {
		t.Errorf("SendText con error inyectado = (%v, %v)", ack, err)
	}
	if _, err := sender.SendMedia(ctx, "s", "to", "url", "f", "m", "c", "image"); err != nil {
		t.Errorf("FailText rompió SendMedia: %v", err)
	}
	sender.FailText(nil)
	sender.FailMedia(mediaDown)
	if ack, err := sender.SendMedia(ctx, "s", "to", "url", "f", "m", "c", "image"); ack != nil || !errors.Is(err, mediaDown) {
		t.Errorf("SendMedia con error inyectado = (%v, %v)", ack, err)
	}
	if _, err := sender.SendText(ctx, "s", "to", "dos"); err != nil {
		t.Errorf("SendText tras retirar su error: %v", err)
	}
}

// TestSender_FailedAttempts_RecordedApartFromSends: un intento fallido queda en Attempts, con su
// error y sin número de comando, y NO en Sends, Texts ni Media; tampoco gasta número de comando.
func TestSender_FailedAttempts_RecordedApartFromSends(t *testing.T) {
	sender := runtimehelpertest.NewSender()
	ctx := t.Context()
	down := errors.New("caído")

	sender.FailText(down)
	_, textErr := sender.SendText(ctx, "s", "to", "perdido")
	sender.FailText(nil)
	_, sentErr := sender.SendText(ctx, "s", "to", "llegó")
	sender.FailMedia(down)
	_, mediaErr := sender.SendMedia(ctx, "s", "to", "url", "f", "m", "c", "image")
	if !errors.Is(textErr, down) || sentErr != nil || !errors.Is(mediaErr, down) {
		t.Fatalf("errores = (%v, %v, %v), quería (caído, nil, caído)", textErr, sentErr, mediaErr)
	}

	wantAttempts := []runtimehelpertest.Send{
		{SessionID: "s", To: "to", Text: "perdido", Err: down},
		{SessionID: "s", To: "to", Text: "llegó", CommandID: "cmd-1"},
		{Media: true, SessionID: "s", To: "to", URL: "url", Filename: "f", Mime: "m", Caption: "c", Kind: "image", Err: down},
	}
	if got := sender.Attempts(); !slices.Equal(got, wantAttempts) {
		t.Errorf("Attempts = %+v, quería %+v", got, wantAttempts)
	}
	if got := sender.Sends(); !slices.Equal(got, wantAttempts[1:2]) {
		t.Errorf("Sends = %+v, quería solo el despachado", got)
	}
	if got := sender.Texts(); !slices.Equal(got, []string{"llegó"}) {
		t.Errorf("Texts = %v, quería solo el despachado", got)
	}
	if got := sender.Media(); len(got) != 0 {
		t.Errorf("Media = %+v, quería ninguno", got)
	}
}

// TestSender_OnSend_RunsInsideTheCall: el observador corre en cada intento, antes de que la
// llamada devuelva, con el envío ya numerado, y puede consultar al propio Sender; nil lo retira.
func TestSender_OnSend_RunsInsideTheCall(t *testing.T) {
	sender := runtimehelpertest.NewSender()
	var seen []runtimehelpertest.Send
	var recordedSoFar []int
	sender.OnSend(func(send runtimehelpertest.Send) {
		seen = append(seen, send)
		recordedSoFar = append(recordedSoFar, len(sender.Attempts()))
	})

	if _, err := sender.SendText(t.Context(), "s", "to", "uno"); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if len(seen) != 1 || seen[0].Text != "uno" || seen[0].CommandID != "cmd-1" || recordedSoFar[0] != 1 {
		t.Fatalf("el observador vio %+v (intentos apuntados: %v), quería el envío cmd-1 ya apuntado", seen, recordedSoFar)
	}
	sender.OnSend(nil)
	if _, err := sender.SendText(t.Context(), "s", "to", "dos"); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if len(seen) != 1 {
		t.Errorf("el observador retirado siguió recibiendo: %+v", seen)
	}
}

// TestSender_ConcurrentSends_NoneLost: con envíos simultáneos no se pierde ninguno ni se repite un
// número de comando.
func TestSender_ConcurrentSends_NoneLost(t *testing.T) {
	sender := runtimehelpertest.NewSender()
	const n = 50
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			if _, err := sender.SendText(t.Context(), "s", "to", "x"); err != nil {
				t.Errorf("SendText: %v", err)
			}
		})
	}
	wg.Wait()

	commands := make(map[string]bool)
	for _, send := range sender.Sends() {
		commands[send.CommandID] = true
	}
	if len(commands) != n {
		t.Errorf("hay %d comandos distintos tras %d envíos", len(commands), n)
	}
}

// TestPresigner_FixedAnswerKeysAndInjectedError: contesta la URL y la caducidad con que se
// construyó (no mira el reloj), apunta cada clave pedida y, con un error inyectado, devuelve
// vacíos y el error; nil lo retira.
func TestPresigner_FixedAnswerKeysAndInjectedError(t *testing.T) {
	expires := time.Date(2026, 10, 10, 12, 15, 0, 0, time.UTC)
	presigner := runtimehelpertest.NewPresigner("https://firmada", expires)
	down := errors.New("almacén caído")

	url, at, err := presigner.GenerateDownloadURL(t.Context(), "wapp/media/a.pdf")
	if err != nil || url != "https://firmada" || !at.Equal(expires) {
		t.Fatalf("GenerateDownloadURL = (%q, %v, %v), quería la URL y la caducidad fijas", url, at, err)
	}
	presigner.Fail(down)
	if url, at, err := presigner.GenerateDownloadURL(t.Context(), "wapp/media/b.pdf"); url != "" || !at.IsZero() || !errors.Is(err, down) {
		t.Errorf("con error inyectado = (%q, %v, %v), quería vacíos y el error", url, at, err)
	}
	presigner.Fail(nil)
	if _, _, err := presigner.GenerateDownloadURL(t.Context(), "wapp/media/c.pdf"); err != nil {
		t.Errorf("tras retirar el error: %v", err)
	}
	if got, want := presigner.Keys(), []string{"wapp/media/a.pdf", "wapp/media/b.pdf", "wapp/media/c.pdf"}; !slices.Equal(got, want) {
		t.Errorf("Keys = %v, quería %v", got, want)
	}
}
