// Package terminal prints text a device chose so that a terminal shows it
// and obeys none of it. A plug's name, an event's value, a network's name
// are whatever the device, or whoever answers for it, wrote: a line's end
// in one forges a line of the program's output, and a control or a
// direction mark moves, hides or reorders what is on the screen.
package terminal

import (
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Text is text with everything a terminal would not simply show written
// out as Go writes it in a string: a control or a format character, a
// line's end and a tab among them, a space that is not the plain one, and
// a byte that is no text. What is left is shown as it is, accents and all.
func Text(text string) string {
	plain := true

	for _, char := range text {
		if !shown(char) {
			plain = false

			break
		}
	}

	if plain && utf8.ValidString(text) {
		return text
	}

	var out strings.Builder

	for index := 0; index < len(text); {
		char, size := utf8.DecodeRuneInString(text[index:])

		switch {
		case char == utf8.RuneError && size == 1:
			out.WriteString(`\x` + strconv.FormatUint(uint64(text[index]), 16))
		case shown(char):
			out.WriteRune(char)
		default:
			quoted := strconv.QuoteRune(char)
			out.WriteString(quoted[1 : len(quoted)-1])
		}

		index += size
	}

	return out.String()
}

// shown says a character is one a terminal draws and does nothing else
// with.
func shown(char rune) bool {
	return unicode.IsPrint(char)
}

// Printf formats as fmt.Fprintf does, with every argument but a number
// and a truth value printed first, as fmt prints it by default, and passed
// through Text: the format is the program's own, and its arguments may be
// a device's, alone or inside a list, a map or a structure. A number that
// prints as text, a duration, is text too.
func Printf(out io.Writer, format string, args ...any) {
	safe := make([]any, 0, len(args))

	for _, arg := range args {
		if plain(arg) {
			safe = append(safe, arg)
		} else {
			safe = append(safe, Text(fmt.Sprint(arg)))
		}
	}

	fmt.Fprintf(out, format, safe...)
}

// plain says an argument is a number or a truth value and nothing that
// prints as text: what no device's text is in.
func plain(arg any) bool {
	switch arg.(type) {
	case error, fmt.Stringer:
		return false
	}

	switch reflect.ValueOf(arg).Kind() { //nolint:exhaustive // every other kind may hold text
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}
