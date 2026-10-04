package enrollhelpertest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/enroll"
	"github.com/google/uuid"
)

// Los dobles cumplen sus puertos.
var (
	_ enroll.CodeStore          = (*MemoriaCodeStore)(nil)
	_ enroll.EdgeCertRepository = (*MemoriaEdgeCertRepository)(nil)
)

// TestMemoriaCodeStore_Contrato corre la suite del puerto contra el doble.
func TestMemoriaCodeStore_Contrato(t *testing.T) {
	ContratoCodeStore(t, func(t *testing.T) MontajeCodeStore {
		t.Helper()
		store := NewMemoriaCodeStore()
		return MontajeCodeStore{
			Store: store,
			SeedCode: func(_ *testing.T, code string, expiresAt time.Time) string {
				tenant := uuid.NewString() // el doble no sabe de tenants: basta un UUID nuevo
				store.Add(code, tenant, expiresAt)
				return tenant
			},
		}
	})
}

// TestMemoriaEdgeCertRepository_Contrato corre la suite del puerto contra el doble.
func TestMemoriaEdgeCertRepository_Contrato(t *testing.T) {
	ContratoEdgeCertRepository(t, func(t *testing.T) MontajeEdgeCertRepository {
		t.Helper()
		repo := NewMemoriaEdgeCertRepository()
		return MontajeEdgeCertRepository{
			Repository: repo,
			SeedTenant: func(*testing.T) string { return uuid.NewString() },
			Records:    func(*testing.T) []enroll.EdgeCertRecord { return repo.Records() },
		}
	})
}

// TestMemoriaCodeStore_DistinguishesTheCause fija la diferencia documentada con Postgres: el
// doble dice POR QUÉ no se consume (desconocido, vencido, usado), en ese orden de comprobación.
// Postgres devuelve siempre ErrCodeInvalid; por eso la suite no afirma el centinela.
func TestMemoriaCodeStore_DistinguishesTheCause(t *testing.T) {
	store := NewMemoriaCodeStore()
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	ctx := context.Background()

	store.Add("vigente", "tenant-a", now.Add(time.Minute))
	store.Add("vencido", "tenant-a", now.Add(-time.Nanosecond))
	store.Add("en-el-limite", "tenant-a", now) // vence EN now: todavía vale (After es estricto)

	if _, err := store.Consume(ctx, "desconocido"); !errors.Is(err, enroll.ErrCodeNotFound) {
		t.Errorf("código desconocido: error %v, quería ErrCodeNotFound", err)
	}
	if _, err := store.Consume(ctx, "vencido"); !errors.Is(err, enroll.ErrCodeExpired) {
		t.Errorf("código vencido: error %v, quería ErrCodeExpired", err)
	}
	if tenant, err := store.Consume(ctx, "en-el-limite"); err != nil || tenant != "tenant-a" {
		t.Errorf("código que vence justo ahora = (%q, %v), quería (tenant-a, nil)", tenant, err)
	}
	if tenant, err := store.Consume(ctx, "vigente"); err != nil || tenant != "tenant-a" {
		t.Errorf("código vigente = (%q, %v), quería (tenant-a, nil)", tenant, err)
	}
	if _, err := store.Consume(ctx, "vigente"); !errors.Is(err, enroll.ErrCodeUsed) {
		t.Errorf("segundo consumo: error %v, quería ErrCodeUsed", err)
	}
	// Un código usado que además venció dice «vencido»: el TTL se mira antes que el uso.
	now = now.Add(time.Hour)
	if _, err := store.Consume(ctx, "vigente"); !errors.Is(err, enroll.ErrCodeExpired) {
		t.Errorf("usado y vencido: error %v, quería ErrCodeExpired", err)
	}
}

// TestMemoriaCodeStore_AddOverwrites: sembrar otra vez un código lo deja sin usar, con el tenant
// y el vencimiento nuevos.
func TestMemoriaCodeStore_AddOverwrites(t *testing.T) {
	store := NewMemoriaCodeStore()
	ctx := context.Background()
	store.Add("c", "tenant-a", time.Now().Add(time.Hour))
	if _, err := store.Consume(ctx, "c"); err != nil {
		t.Fatalf("Consume: error inesperado %v", err)
	}
	store.Add("c", "tenant-b", time.Now().Add(time.Hour))
	if tenant, err := store.Consume(ctx, "c"); err != nil || tenant != "tenant-b" {
		t.Errorf("tras volver a sembrar = (%q, %v), quería (tenant-b, nil)", tenant, err)
	}
}

// TestMemoriaEdgeCertRepository_RecordsIsACopy: Records devuelve una copia en orden de alta;
// tocarla no cambia lo guardado.
func TestMemoriaEdgeCertRepository_RecordsIsACopy(t *testing.T) {
	repo := NewMemoriaEdgeCertRepository()
	ctx := context.Background()
	for _, cn := range []string{"primero", "segundo"} {
		if err := repo.Create(ctx, enroll.EdgeCertRecord{SubjectCN: cn}); err != nil {
			t.Fatalf("Create: error inesperado %v", err)
		}
	}
	got := repo.Records()
	if len(got) != 2 || got[0].SubjectCN != "primero" || got[1].SubjectCN != "segundo" {
		t.Fatalf("Records() = %+v, quería primero y segundo, en ese orden", got)
	}
	got[0].SubjectCN = "manipulado"
	if again := repo.Records(); again[0].SubjectCN != "primero" {
		t.Errorf("mutar el resultado de Records cambió lo guardado: %q", again[0].SubjectCN)
	}
}

// TestAdversarialCodes_AreAllDifferentFromTheCode: ninguna variante es el código, y están las
// que el contrato nombra (espacio delante y detrás, U+200B, U+FEFF, otra capitalización, vacío).
func TestAdversarialCodes_AreAllDifferentFromTheCode(t *testing.T) {
	const code = "Abc-123"
	variants := AdversarialCodes(code)
	want := map[string]string{
		"leading space":             " Abc-123",
		"trailing space":            "Abc-123 ",
		"leading zero width U+200B": "\u200bAbc-123",
		"leading BOM U+FEFF":        "\ufeffAbc-123",
		"other case":                "aBC-123",
		"empty":                     "",
	}
	for name, v := range want {
		if variants[name] != v {
			t.Errorf("variante %q = %q, quería %q", name, variants[name], v)
		}
	}
	for name, v := range variants {
		if v == code {
			t.Errorf("la variante %q es el propio código", name)
		}
	}
}
