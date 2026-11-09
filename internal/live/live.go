// Package live is the logic of AndRho's live sales (founder/live-sale-spec.md),
// free of Meta and Odoo so it can be tested on its own:
//
//   - Normalize / IsMio: how a comment is read ("Mío!!" == "mio").
//   - Names: a short, unique name per item, what the seller says on the
//     live and what buyers comment ("blusa roja 3").
//   - Order: the suggested selling order.
//   - Match: which item a live comment takes.
//   - Messages: what the buyer gets in Messenger, in plain Spanish.
package live

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

var stripMarks = transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)

// Normalize lowercases, drops accents, emoji and punctuation, and collapses
// spaces: "¡Mío!! 😍" -> "mio", "Blusa  Roja #3" -> "blusa roja 3".
func Normalize(s string) string {
	t, _, err := transform.String(stripMarks, s)
	if err != nil {
		t = s
	}
	t = strings.ToLower(t)
	var b strings.Builder
	space := false
	for _, r := range t {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		default:
			space = true
		}
	}
	return b.String()
}

// DefaultClaimWord is what buyers comment on an unsold-item post to take
// it, unless the seller picks another word (Ajustes).
const DefaultClaimWord = "mío"

// IsClaim says whether a comment on an unsold-item post claims it: only the
// seller's word ("mío" by default), in any case and with or without
// accents, with nothing else but punctuation or emoji.
func IsClaim(comment, word string) bool {
	w := Normalize(word)
	if w == "" {
		w = Normalize(DefaultClaimWord)
	}
	return Normalize(comment) == w
}

// IsMio is IsClaim with the default word.
func IsMio(comment string) bool { return IsClaim(comment, DefaultClaimWord) }

// ValidClaimWord checks a seller's word: one to three words, letters and
// numbers only (emoji and punctuation are dropped when reading comments, so
// they can't be the word), up to 20 characters.
func ValidClaimWord(word string) bool {
	word = strings.TrimSpace(word)
	n := Normalize(word)
	return n != "" && len([]rune(word)) <= 20 && len(strings.Fields(n)) <= 3
}

// Item is one product of a live, as the logic needs it.
type Item struct {
	ID       int     // the Odoo line id
	Name     string  // the product's name
	LiveName string  // the name said on the live
	Price    float64 // quetzales
	Stock    float64 // units on hand (0 = unknown)
}

var nameStop = map[string]bool{"de": true, "la": true, "el": true, "los": true, "las": true, "para": true, "con": true, "y": true, "en": true, "talla": true}

// Names gives each item a short unique live name: the first two meaningful
// words of the product plus its position ("blusa roja 3"). The number
// makes it unique and lets buyers comment just "3".
func Names(items []Item) []Item {
	out := make([]Item, len(items))
	for i, it := range items {
		words := []string{}
		for _, w := range strings.Fields(Normalize(it.Name)) {
			if nameStop[w] || isNumber(w) {
				continue
			}
			words = append(words, w)
			if len(words) == 2 {
				break
			}
		}
		if len(words) == 0 {
			words = []string{"articulo"}
		}
		it.LiveName = strings.Join(words, " ") + " " + strconv.Itoa(i+1)
		out[i] = it
	}
	return out
}

func isNumber(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

// Order is the suggested selling order. Lives keep people watching when
// they open strong and close with the best piece, so:
//
//  1. the second most expensive item opens (a strong start without spending
//     the best one),
//  2. the rest alternate cheap and expensive, so there's always something
//     for every budget,
//  3. the most expensive closes.
//
// Ties keep the seller's order. Items marked out of stock (Stock < 0)
// go last.
func Order(items []Item) []Item {
	if len(items) <= 2 {
		out := append([]Item(nil), items...)
		sort.SliceStable(out, func(i, j int) bool { return out[i].Price < out[j].Price })
		return out
	}
	var inStock, out []Item
	var noStock []Item
	for _, it := range items {
		if it.Stock < 0 {
			noStock = append(noStock, it)
			continue
		}
		inStock = append(inStock, it)
	}
	byPrice := append([]Item(nil), inStock...)
	sort.SliceStable(byPrice, func(i, j int) bool { return byPrice[i].Price > byPrice[j].Price })
	if len(byPrice) < 3 {
		return append(byPrice, noStock...)
	}
	closer, opener := byPrice[0], byPrice[1]
	rest := byPrice[2:] // descending
	out = append(out, opener)
	lo, hi := len(rest)-1, 0
	for turn := 0; lo >= hi; turn++ {
		if turn%2 == 0 {
			out = append(out, rest[lo])
			lo--
		} else {
			out = append(out, rest[hi])
			hi++
		}
	}
	out = append(out, closer)
	return append(out, noStock...)
}

var numberOnly = regexp.MustCompile(`^(?:(?:la|el|numero|num|no|n)\s+)?(\d{1,3})$`)

// Match is the item a live comment takes, among the ones still on sale.
// In order:
//
//  1. the comment is exactly an item's live name ("blusa roja 3"),
//  2. it is just the item's number ("3", "la 3", "#3"),
//  3. it contains one item's live name ("yo quiero blusa roja 3").
//
// When the comment fits more than one item (rule 3), ok is false: AndRho
// doesn't guess, the seller decides.
func Match(comment string, available []Item) (Item, bool) {
	c := Normalize(comment)
	if c == "" {
		return Item{}, false
	}
	for _, it := range available {
		if Normalize(it.LiveName) == c {
			return it, true
		}
	}
	if m := numberOnly.FindStringSubmatch(c); m != nil {
		for _, it := range available {
			if liveNumber(it.LiveName) == m[1] {
				return it, true
			}
		}
		return Item{}, false
	}
	var found []Item
	padded := " " + c + " "
	for _, it := range available {
		n := Normalize(it.LiveName)
		if n != "" && strings.Contains(padded, " "+n+" ") {
			found = append(found, it)
		}
	}
	if len(found) == 1 {
		return found[0], true
	}
	return Item{}, false
}

func liveNumber(liveName string) string {
	f := strings.Fields(Normalize(liveName))
	if len(f) == 0 {
		return ""
	}
	if last := f[len(f)-1]; isNumber(last) {
		return strings.TrimLeft(last, "0")
	}
	return ""
}

// FirstName is the first word of a name, capitalized ("ana maría" -> "Ana").
func FirstName(full string) string {
	f := strings.Fields(full)
	if len(f) == 0 {
		return ""
	}
	r := []rune(strings.ToLower(f[0]))
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// Money formats quetzales: Q85, Q1,250.50.
func Money(q float64) string {
	whole := int64(q)
	cents := int64((q-float64(whole))*100 + 0.5)
	if cents == 100 {
		whole, cents = whole+1, 0
	}
	s := strconv.FormatInt(whole, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if cents > 0 {
		return fmt.Sprintf("Q%s.%02d", s, cents)
	}
	return "Q" + s
}

// Messages the buyer gets (Messenger). Payloads carry the claim id.
const (
	PayloadYes = "LS_YES:"
	PayloadNo  = "LS_NO:"
)

// ConfirmText is the question in the private reply, with the Sí/No buttons.
func ConfirmText(liveName string, price float64) string {
	return fmt.Sprintf("¿Confirmas la compra de %s por %s?", liveName, Money(price))
}

// AfterYes is the answer to "Sí". With known shipping data it shows it and
// asks only if it changed; otherwise it asks for the phone first.
func AfterYes(firstName string, discountPct float64, address, zone string) string {
	greet := "¡Listo"
	if firstName != "" {
		greet += ", " + firstName
	}
	msg := greet + "! Tu pedido quedó apartado."
	if discountPct > 0 {
		msg += fmt.Sprintf(" Como cliente frecuente tienes %g%% de descuento.", discountPct)
	}
	if address != "" {
		where := address
		if zone != "" {
			where += ", " + zone
		}
		return msg + " Lo enviamos a " + where + ". Si cambió, escríbenos tu nueva dirección."
	}
	return msg + " Para tu envío, escríbenos tu número de teléfono."
}

// AskAddress, Done, Declined and Expired are the rest of the conversation.
const (
	AskAddress = "Gracias. Ahora escribe tu dirección y zona (por ejemplo: 5a calle 1-23, zona 1)."
	Done       = "¡Gracias! Tu pedido está confirmado. Te avisamos cuando salga tu envío."
	Declined   = "Entendido, lo liberamos para alguien más. ¡Gracias por acompañarnos!"
	Expired    = "No recibimos tu confirmación a tiempo y el artículo pasó a la siguiente persona. ¡Te esperamos en el próximo live!"
	BadPhone   = "No pudimos leer ese número. Escríbelo con sus 8 dígitos, por ejemplo 5555 1234."
)

var phoneDigits = regexp.MustCompile(`\d`)

// Phone reads a Guatemalan phone (8 digits, optional +502) from a message.
func Phone(text string) (string, bool) {
	d := strings.Join(phoneDigits.FindAllString(text, -1), "")
	d = strings.TrimPrefix(d, "502")
	if len(d) != 8 {
		return "", false
	}
	return d[:4] + "-" + d[4:], true
}

var zoneRe = regexp.MustCompile(`(?i)\bzona\s*\d{1,2}\b`)

// Address splits "5a calle 1-23, zona 1, Mixco" into address and zone:
// "zona N" when it says so, otherwise the last comma-separated part.
func Address(text string) (address, zone string) {
	text = strings.TrimSpace(text)
	if m := zoneRe.FindString(text); m != "" {
		zone = strings.ToLower(strings.Join(strings.Fields(m), " "))
		address = strings.Trim(strings.TrimSpace(strings.Replace(text, m, "", 1)), ", ")
		address = strings.Join(strings.Fields(strings.ReplaceAll(address, ",,", ",")), " ")
		return address, zone
	}
	if i := strings.LastIndex(text, ","); i > 0 {
		return strings.TrimSpace(text[:i]), strings.TrimSpace(text[i+1:])
	}
	return text, ""
}

// PostCaption is the text of an unsold item's post: price, the optional
// frequent-buyer note and how to buy. No product name, on purpose: the
// photo speaks, and "mío" is the only word that counts.
func PostCaption(price float64, description string, loyalty bool, word string) string {
	word = strings.TrimSpace(word)
	if word == "" {
		word = DefaultClaimWord
	}
	r := []rune(word)
	r[0] = unicode.ToUpper(r[0])
	var b strings.Builder
	b.WriteString(Money(price))
	if d := strings.TrimSpace(description); d != "" {
		b.WriteString("\n" + d)
	}
	if loyalty {
		b.WriteString("\nAplica descuento para clientes frecuentes.")
	}
	b.WriteString("\n\nComenta «" + string(r) + "» si lo quieres. La primera persona en comentar se lo lleva y le escribimos por Messenger para confirmar.")
	return b.String()
}
