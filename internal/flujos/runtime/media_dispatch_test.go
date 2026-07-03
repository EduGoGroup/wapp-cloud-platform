package runtime_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/media"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

const testMediaFlow = "envio-pdf"

// fakePresigner es un doble del puerto runtime.Presigner: captura la key firmada
// y devuelve una URL fija (o un error inyectado). No golpea R2.
type fakePresigner struct {
	url    string
	err    error
	gotKey string
}

func (f *fakePresigner) GenerateDownloadURL(_ context.Context, key string) (string, time.Time, error) {
	f.gotKey = key
	if f.err != nil {
		return "", time.Time{}, f.err
	}
	return f.url, time.Now().Add(15 * time.Minute), nil
}

// mediaFlow arma un flujo de UN nodo "media" no interactivo que emite el adjunto y
// termina (Next nil → NodeTerminal). El descriptor viaja INLINE en node.Content
// (fuente static, §9.B): el módulo media lo parsea a un MediaRef.
func mediaFlow() model.Flow {
	return model.Flow{
		FlowID:  testMediaFlow,
		Initial: "doc",
		Nodes: map[string]model.Node{
			"doc": {
				Type: media.NodeTypeMedia,
				Content: &model.ContentRef{
					Source:   "static",
					Key:      "wapp/media/lista-precios.pdf",
					Filename: "Lista de precios.pdf",
					Mime:     "application/pdf",
					Kind:     media.KindDocument,
					Caption:  "Acá va la lista de precios 📄",
				},
			},
		},
	}
}

// newMediaRuntime arma un runtime con un engine que maneja "media", el flujo ya
// publicado y el Presigner dado (nil = sin WithPresignClient, para el caso de
// error de configuración).
func newMediaRuntime(t *testing.T, p runtime.Presigner) (*runtime.Runtime, *fakeSender) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, mediaFlow()); err != nil {
		t.Fatalf("sembrar definición media: %v", err)
	}
	reg := modules.NewRegistry()
	reg.Register(media.New())
	eng := engine.New(reg)
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	opts := []runtime.Option{}
	if p != nil {
		opts = append(opts, runtime.WithPresignClient(p))
	}
	rt := runtime.New(repo, eng, sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(), opts...)
	return rt, sender
}

// TestStart_MediaNode_PresignaYDespachaSendMedia: un nodo media produce EXACTAMENTE
// una llamada GenerateDownloadURL (con la key del descriptor) + una SendMedia con
// la URL prefirmada y los metadatos correctos, y CERO SendText (§9.C/§9.I).
func TestStart_MediaNode_PresignaYDespachaSendMedia(t *testing.T) {
	const wantURL = "https://r2.example/presigned-get?sig=abc"
	ps := &fakePresigner{url: wantURL}
	rt, sender := newMediaRuntime(t, ps)

	if _, err := rt.Start(context.Background(), testTenant, testMediaFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if ps.gotKey != "wapp/media/lista-precios.pdf" {
		t.Errorf("presigner recibió key %q, want la del descriptor", ps.gotKey)
	}
	if sender.count() != 0 {
		t.Errorf("un nodo media NO debe emitir SendText: got %d", sender.count())
	}
	ms := sender.mediaSent()
	if len(ms) != 1 {
		t.Fatalf("debería haber EXACTAMENTE 1 SendMedia, got %d", len(ms))
	}
	got := ms[0]
	want := sentMedia{
		sessionID: testSession,
		to:        testContact,
		url:       wantURL,
		filename:  "Lista de precios.pdf",
		mime:      "application/pdf",
		caption:   "Acá va la lista de precios 📄",
		kind:      media.KindDocument,
	}
	if got != want {
		t.Fatalf("SendMedia:\n got %+v\nwant %+v", got, want)
	}
}

// TestMedia_PresignError_SeSurface: si el presign falla, Start devuelve error y NO
// se despacha ningún SendMedia (el estado ya está persistido antes del envío, así
// que no se corrompe; el error se surface para logging).
func TestMedia_PresignError_SeSurface(t *testing.T) {
	ps := &fakePresigner{err: errors.New("r2 caído")}
	rt, sender := newMediaRuntime(t, ps)

	_, err := rt.Start(context.Background(), testTenant, testMediaFlow, testSession, phoneRef(t, testContact))
	if err == nil {
		t.Fatal("Start debería devolver error cuando el presign falla")
	}
	if len(sender.mediaSent()) != 0 {
		t.Errorf("no debe despacharse SendMedia si el presign falla")
	}
}

// TestMedia_SendError_SeSurface: si el Sender.SendMedia falla, Start devuelve error
// (mismo contrato que SendText).
func TestMedia_SendError_SeSurface(t *testing.T) {
	rt, sender := newMediaRuntime(t, &fakePresigner{url: "https://r2.example/x"})
	sender.mediaErr = errors.New("sesión offline")

	_, err := rt.Start(context.Background(), testTenant, testMediaFlow, testSession, phoneRef(t, testContact))
	if err == nil {
		t.Fatal("Start debería devolver error cuando SendMedia falla")
	}
}

// TestMedia_SinPresignClient_ErrorControlado: un nodo media sin Presigner cableado
// es un error de configuración CONTROLADO (no un pánico) y no despacha nada.
func TestMedia_SinPresignClient_ErrorControlado(t *testing.T) {
	rt, sender := newMediaRuntime(t, nil) // sin WithPresignClient

	_, err := rt.Start(context.Background(), testTenant, testMediaFlow, testSession, phoneRef(t, testContact))
	if err == nil {
		t.Fatal("Start debería devolver error controlado si no hay PresignClient")
	}
	if len(sender.mediaSent()) != 0 {
		t.Errorf("no debe despacharse SendMedia sin PresignClient")
	}
}

// TestStart_MenuSiguePorSendText_NoRegresion: un flujo de menú (sin media) sigue
// despachando por SendText y NUNCA por SendMedia, aunque el runtime tenga un
// Presigner cableado (no-regresión del camino de texto).
func TestStart_MenuSiguePorSendText_NoRegresion(t *testing.T) {
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición menú: %v", err)
	}
	sender := &fakeSender{}
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant},
		contact.NewMemoryResolver(repo), discardLogger(),
		runtime.WithPresignClient(&fakePresigner{url: "https://r2.example/x"}))

	if _, err := rt.Start(context.Background(), testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if sender.count() != 1 {
		t.Errorf("el menú debe emitir 1 SendText, got %d", sender.count())
	}
	if len(sender.mediaSent()) != 0 {
		t.Errorf("el menú NO debe emitir SendMedia, got %d", len(sender.mediaSent()))
	}
}
