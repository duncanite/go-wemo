package terminal_test

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/duncanite/go-wemo/internal/terminal"
)

// TestText: what a terminal only draws is left as it is; what it obeys,
// and what is no text, is written out.
func TestText(t *testing.T) {
	t.Parallel()

	for _, row := range []struct{ name, text, want string }{
		{"a name", "Kitchen Lamp", "Kitchen Lamp"},
		{"nothing", "", ""},
		{"accents and other scripts", "Séjour 台所 лампа", "Séjour 台所 лампа"}, //nolint:gosmopolitan // text in other scripts is the case
		{"a picture", "Lamp 💡", "Lamp 💡"},
		{"a backslash", `a\b`, `a\b`},
		{"an escape sequence", "Lamp\x1b[2J", `Lamp\x1b[2J`},
		{"a line's end", "Lamp\nNo changes.", `Lamp\nNo changes.`},
		{"a carriage return", "Lamp\rNo changes.", `Lamp\rNo changes.`},
		{"a tab", "a\tb", `a\tb`},
		{"a bell and a NUL", "a\x07\x00b", `a\a\x00b`},
		{"a delete", "a\x7fb", `a\x7fb`},
		{"a control above ASCII", "a\u009b2Jb", `a\u009b2Jb`},
		{"a direction override", "abc\u202edef", `abc\u202edef`},
		{"a width of nothing", "a\u200bb", `a\u200bb`},
		{"a space that is not the plain one", "a\u00a0b\u2028c", `a\u00a0b\u2028c`},
		{"a byte that is no text", "a\xffb", `a\xffb`},
		{"the mark of what is no text, which is drawn", "a\ufffdb", "a\ufffdb"},
		{"no text after text that is plain", "plain\xc3", `plain\xc3`},
	} {
		if got := terminal.Text(row.text); got != row.want {
			t.Errorf("%s: Text(%q) = %q, want %q", row.name, row.text, got, row.want)
		}
	}
}

// errCarriage is an error whose text a terminal would obey.
var errCarriage = errors.New("d\re")

// named is text of a type of its own, as an identity is.
type named string

// worded is what prints as text without being any.
type worded struct{ text string }

func (w *worded) String() string { return w.text }

// TestPrintfOfWhatHoldsText: text inside a list, a map or a structure is
// written out as text alone is, and what is nothing at all prints as fmt
// prints it.
func TestPrintfOfWhatHoldsText(t *testing.T) {
	t.Parallel()

	var (
		out     bytes.Buffer
		nowhere *worded
		nothing error
	)

	terminal.Printf(&out, "%v|%v|%v|%v|%v|%s\n",
		[]string{"a\x1bb"}, map[string]string{"k\n": "v\r"}, struct{ Name string }{"s\x07"},
		nowhere, nothing, []byte("b\x1b"))

	if want := `[a\x1bb]|map[k\n:v\r]|{s\a}|<nil>|<nil>|[98 27]` + "\n"; out.String() != want {
		t.Fatalf("Printf wrote %q, want %q", out.String(), want)
	}
}

// TestPrintf: every argument that is text, or prints as text, is written
// out; the format, and what is no text, are as they were given.
func TestPrintf(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	terminal.Printf(&out, "%-6s|%s|%s|%s|%s|%d|%v\n",
		"a\nb", named("c\x1b"), errCarriage, &worded{text: "f\x1bg"}, 90*time.Second, 7, true)

	if want := `a\nb  |c\x1b|d\re|f\x1bg|1m30s|7|true` + "\n"; out.String() != want {
		t.Fatalf("Printf wrote %q, want %q", out.String(), want)
	}
}
