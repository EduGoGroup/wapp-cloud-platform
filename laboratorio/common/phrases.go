//go:build laboratorio

package common

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
)

// El banco de frases (Plan 058 · E03): qué es «acertar». Cada caso dice qué debería salir de una
// frase —o de una ráfaga de mensajes— al pasar por la captación: la intención de P1, las líneas
// casadas con el catálogo Ámbar, cuántas quedan sin casar y la fecha de entrega. Lo leen E10 y E11.
//
// 🔴 Las expectativas son las de UNA PERSONA, no las del código. Si el pipeline dice otra cosa, eso
// no se arregla tocando el banco: es un hallazgo y se anota.

// DefaultPhrasesPath es la ruta del banco, relativa a la raíz del repo (los escalones se corren
// siempre desde ahí).
const DefaultPhrasesPath = "laboratorio/data/phrases.json"

// PhraseBankVersion es la única versión del formato que este loader entiende.
const PhraseBankVersion = 1

// Phrase es un caso del banco.
type Phrase struct {
	// ID es el nombre corto del caso, único en el banco (-phrase <id>).
	ID string `json:"id"`
	// Category es easy, ambiguous, no_order, package, relative_date o short.
	Category string `json:"category"`
	// Messages son los mensajes del cliente, en orden: uno es una frase, varios son una ráfaga.
	Messages []string `json:"messages"`
	// Intent es la intención que P1 debería dar (la reservada «desconocido» si ninguna).
	Intent string `json:"intent"`
	// Lines son las líneas que el match debería CASAR con el catálogo.
	Lines []ExpectedLine `json:"lines"`
	// Unmatched es cuántas líneas deberían quedar SIN casar (las que el dueño precifica).
	Unmatched int `json:"unmatched"`
	// Date es la fecha de entrega, RELATIVA al día de la prueba (ver ResolveExpectedDate); nil
	// si el cliente no dijo cuándo.
	Date *string `json:"date"`
	// Why explica por qué el caso está en el banco.
	Why string `json:"why"`
}

// ExpectedLine es una línea casada que se espera: el SKU BASE del artículo (sin la variante, que
// el match escribe como «SKU#código») y la cantidad. Qty 0 significa «la cantidad no se mira».
type ExpectedLine struct {
	SKU string `json:"sku"`
	Qty int    `json:"qty"`
}

// Text es la frase entera: los mensajes unidos por saltos de línea.
func (p Phrase) Text() string { return strings.Join(p.Messages, "\n") }

// DateToken es la fecha esperada tal como está escrita en el banco («» si no hay).
func (p Phrase) DateToken() string {
	if p.Date == nil {
		return ""
	}
	return *p.Date
}

type phraseBank struct {
	Version int      `json:"version"`
	Catalog string   `json:"catalog"`
	Note    string   `json:"note"`
	Phrases []Phrase `json:"phrases"`
}

// LoadPhrases lee el banco de path ("" ⇒ DefaultPhrasesPath) y lo valida: versión conocida, al
// menos un caso, ids únicos y no vacíos, al menos un mensaje no vacío por caso, líneas con SKU y
// qty ≥ 0, y fechas que ResolveExpectedDate entiende. Un banco mal escrito se rechaza entero: un
// caso roto que se saltara en silencio daría una tabla de aciertos mentirosa.
func LoadPhrases(path string) ([]Phrase, error) {
	if path == "" {
		path = DefaultPhrasesPath
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("banco de frases: %w (¿corres desde la raíz del repo?)", err)
	}
	var bank phraseBank
	if err := json.Unmarshal(raw, &bank); err != nil {
		return nil, fmt.Errorf("banco de frases %s: JSON ilegible: %w", path, err)
	}
	if bank.Version != PhraseBankVersion {
		return nil, fmt.Errorf("banco de frases %s: versión %d, este loader entiende la %d", path, bank.Version, PhraseBankVersion)
	}
	if len(bank.Phrases) == 0 {
		return nil, fmt.Errorf("banco de frases %s: no trae ningún caso", path)
	}
	seen := make(map[string]bool, len(bank.Phrases))
	for i, p := range bank.Phrases {
		if err := validatePhrase(p, seen); err != nil {
			return nil, fmt.Errorf("banco de frases %s, caso %d: %w", path, i, err)
		}
		seen[p.ID] = true
	}
	return bank.Phrases, nil
}

func validatePhrase(p Phrase, seen map[string]bool) error {
	switch {
	case p.ID == "":
		return errors.New("sin id")
	case seen[p.ID]:
		return fmt.Errorf("id %q repetido", p.ID)
	case len(p.Messages) == 0:
		return fmt.Errorf("%q no trae mensajes", p.ID)
	case p.Intent == "":
		return fmt.Errorf("%q no dice qué intención espera", p.ID)
	case p.Unmatched < 0:
		return fmt.Errorf("%q: unmatched negativo", p.ID)
	}
	for _, m := range p.Messages {
		if strings.TrimSpace(m) == "" {
			return fmt.Errorf("%q trae un mensaje vacío", p.ID)
		}
	}
	for _, l := range p.Lines {
		if l.SKU == "" || l.Qty < 0 {
			return fmt.Errorf("%q: línea esperada sin sku o con qty negativa", p.ID)
		}
	}
	if _, err := ResolveExpectedDate(p.DateToken(), AmberStart); err != nil {
		return fmt.Errorf("%q: %w", p.ID, err)
	}
	return nil
}

// FindPhrase devuelve el caso de id dado.
func FindPhrase(phrases []Phrase, id string) (Phrase, bool) {
	for _, p := range phrases {
		if p.ID == id {
			return p, true
		}
	}
	return Phrase{}, false
}

var weekdaysEN = map[string]time.Weekday{
	"monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday,
	"thursday": time.Thursday, "friday": time.Friday, "saturday": time.Saturday, "sunday": time.Sunday,
}

// ResolveExpectedDate convierte la fecha RELATIVA del banco en una absoluta («AAAA-MM-DD») contra
// base, que es el día de la prueba. «» ⇒ «» (sin fecha esperada). Los tres formatos:
//
//   - «+Nd»: N días después de base («+0d» es el mismo día);
//   - «next:<día>»: la PRÓXIMA aparición estricta de ese día de la semana, nunca el mismo día;
//   - «nextweek:<día>»: ese día de la semana ISO (de lunes a domingo) siguiente a la de base.
//
// 🔴 Es una aritmética PROPIA, escrita a propósito sin llamar a stages.ResolveDate: si las dos
// coincidieran por construcción, la comparación no mediría nada.
func ResolveExpectedDate(token string, base time.Time) (string, error) {
	if token == "" {
		return "", nil
	}
	day := time.Date(base.Year(), base.Month(), base.Day(), 12, 0, 0, 0, base.Location())
	switch {
	case strings.HasPrefix(token, "+") && strings.HasSuffix(token, "d"):
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(token, "+"), "d"))
		if err != nil || n < 0 {
			return "", fmt.Errorf("fecha %q: «+Nd» con N entero ≥ 0", token)
		}
		return day.AddDate(0, 0, n).Format(time.DateOnly), nil
	case strings.HasPrefix(token, "next:"):
		wd, ok := weekdaysEN[strings.TrimPrefix(token, "next:")]
		if !ok {
			return "", fmt.Errorf("fecha %q: día de la semana desconocido", token)
		}
		delta := (int(wd) - int(day.Weekday()) + 7) % 7
		if delta == 0 {
			delta = 7
		}
		return day.AddDate(0, 0, delta).Format(time.DateOnly), nil
	case strings.HasPrefix(token, "nextweek:"):
		wd, ok := weekdaysEN[strings.TrimPrefix(token, "nextweek:")]
		if !ok {
			return "", fmt.Errorf("fecha %q: día de la semana desconocido", token)
		}
		monday := day.AddDate(0, 0, -isoOffset(day.Weekday())+7)
		return monday.AddDate(0, 0, isoOffset(wd)).Format(time.DateOnly), nil
	}
	return "", fmt.Errorf("fecha %q: vale null, «+Nd», «next:<día>» o «nextweek:<día>»", token)
}

// isoOffset es cuántos días van del lunes a d (lunes 0 … domingo 6).
func isoOffset(d time.Weekday) int { return (int(d) + 6) % 7 }

// Observed es lo que la captación produjo para una frase, ya reducido a lo que el banco compara.
type Observed struct {
	// Intent es la intención que dio P1 («» si P1 no se pidió o falló).
	Intent string
	// Confidence es la confianza de P1.
	Confidence float64
	// Lines son las líneas del artefacto del match (stages.Line), con su Kind.
	Lines []stages.Line
	// Date es la fecha de entrega que dejó P4 («AAAA-MM-DD», «» si ninguna).
	Date string
	// Failed es el error de la etapa que cortó la cadena («» si llegó al final).
	Failed string
}

// Verdict es la comparación de un caso: un booleano por eje y, en Notes, por qué falló cada uno.
type Verdict struct {
	Intent bool
	Lines  bool
	Date   bool
	Notes  []string
}

// OK dice si el caso acierta en los tres ejes.
func (v Verdict) OK() bool { return v.Intent && v.Lines && v.Date }

// Compare enfrenta lo esperado con lo obtenido. Reglas:
//
//   - intención: igualdad exacta del nombre;
//   - líneas: cada esperada casa con UNA línea `matched` de SKU igual al suyo o que empieza por
//     «SKU#» (la variante), y con su qty si la esperada no es 0. Una línea casada que sobra es
//     fallo. Las `unmatched` se cuentan contra Unmatched. La de envío (`shipping`) no se mira;
//   - fecha: igualdad exacta de «AAAA-MM-DD», con la esperada resuelta contra base;
//   - si la cadena se cortó (got.Failed), líneas y fecha FALLAN: no hay borrador que mirar.
func Compare(p Phrase, got Observed, base time.Time) Verdict {
	v := Verdict{Intent: got.Intent == p.Intent}
	if !v.Intent {
		v.Notes = append(v.Notes, fmt.Sprintf("intención: esperada %q, salió %q", p.Intent, got.Intent))
	}
	// Una cadena cortada no deja borrador: no es «un borrador vacío», es un job que el worker mataría.
	// Sin borrador no hay líneas ni fecha que dar por buenas, aunque el banco no esperara ninguna.
	if got.Failed != "" {
		v.Notes = append(v.Notes, "sin borrador: la cadena se cortó, no hay líneas ni fecha que comparar")
		return v
	}
	var lineNotes []string
	v.Lines, lineNotes = compareLines(p, got.Lines)
	v.Notes = append(v.Notes, lineNotes...)

	want, err := ResolveExpectedDate(p.DateToken(), base)
	if err != nil {
		v.Notes = append(v.Notes, err.Error())
	}
	v.Date = err == nil && want == got.Date
	if !v.Date {
		v.Notes = append(v.Notes, fmt.Sprintf("fecha: esperada %s, salió %s", orNone(want), orNone(got.Date)))
	}
	return v
}

func compareLines(p Phrase, lines []stages.Line) (bool, []string) {
	var notes []string
	used := make([]bool, len(lines))
	for _, e := range p.Lines {
		found := false
		for i, l := range lines {
			if used[i] || l.Kind != stages.KindMatched || !sameArticle(l.SKU, e.SKU) {
				continue
			}
			used[i], found = true, true
			if e.Qty > 0 && l.Qty != e.Qty {
				notes = append(notes, fmt.Sprintf("línea %s: qty esperada %d, salió %d", e.SKU, e.Qty, l.Qty))
			}
			break
		}
		if !found {
			notes = append(notes, fmt.Sprintf("línea %s: no salió casada", e.SKU))
		}
	}
	unmatched := 0
	for i, l := range lines {
		switch {
		case l.Kind == stages.KindUnmatched:
			unmatched++
		case l.Kind == stages.KindMatched && !used[i]:
			notes = append(notes, fmt.Sprintf("línea de más: %s (%s) × %d", l.SKU, l.Label, l.Qty))
		}
	}
	if unmatched != p.Unmatched {
		notes = append(notes, fmt.Sprintf("sin casar: esperadas %d, salieron %d", p.Unmatched, unmatched))
	}
	return len(notes) == 0, notes
}

// sameArticle dice si el SKU de una línea es el del artículo esperado o una variante suya.
func sameArticle(lineSKU, base string) bool {
	return lineSKU == base || strings.HasPrefix(lineSKU, base+"#")
}

func orNone(s string) string {
	if s == "" {
		return "(ninguna)"
	}
	return s
}

// Mark es la marca de un eje en una tabla: ✓ o ✗.
func Mark(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}
