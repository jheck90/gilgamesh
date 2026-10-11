package text

import (
	"reflect"
	"testing"
)

func TestSplit(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"One. Two! Three?", []string{"One.", "Two!", "Three?"}},
		{`"Stop!" she yelled. He stopped.`, []string{`"Stop!" she yelled.`, "He stopped."}},
		{"“Run.” Carl ran.", []string{"“Run.”", "Carl ran."}},
		{"Mr. Smith met Dr. Jones. They talked.", []string{"Mr. Smith met Dr. Jones.", "They talked."}},
		{"J. R. R. Tolkien wrote it. Then he slept.", []string{"J. R. R. Tolkien wrote it.", "Then he slept."}},
		{"Wait... what? No.", []string{"Wait... what?", "No."}},
		{"It cost 3.50 dollars. Cheap.", []string{"It cost 3.50 dollars.", "Cheap."}},
		{"Level up!?! Yes.", []string{"Level up!?!", "Yes."}},
		{"No terminal punctuation", []string{"No terminal punctuation"}},
		{"  ", nil},
	}
	for _, c := range cases {
		if got := Split(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Split(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestContainsName(t *testing.T) {
	cases := []struct {
		s, name string
		want    bool
	}{
		{"Princess Donut’s tail twitched.", "princess donut", true},
		{"Carl's boots", "Carl", true},
		{"Carlton arrived", "Carl", false},
		{"the Borant Corporation", "Borant", true},
		{"Mordecai", "Mordecai", true},
		{"nothing here", "", false},
		{"Don't do it", "Don’t", true},
	}
	for _, c := range cases {
		if got := ContainsName(c.s, c.name); got != c.want {
			t.Errorf("ContainsName(%q, %q) = %v, want %v", c.s, c.name, got, c.want)
		}
	}
}

func TestKey(t *testing.T) {
	if Key("the Messenger") != Key("Messenger") || Key("The  ants") != Key("ants") {
		t.Error(`"the X" should key like "X"`)
	}
	if Key("a male") == Key("the male") || Key("Theodore") != "theodore" {
		t.Error(`only a leading "the" is dropped`)
	}
}

func TestNameWords(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{"Doctor Vrie Guyen", "Guyen", "the commander"}, []string{"Vrie", "Guyen"}},
		{[]string{"Guyen-the-fucking-virus", "Lain’s stick"}, []string{"Guyen", "Lain"}},
		{[]string{"The Barrel", "Great Nest", "Al"}, []string{"Barrel", "Nest"}},
	}
	for _, c := range cases {
		if got := NameWords(c.in...); !reflect.DeepEqual(got, c.want) {
			t.Errorf("NameWords(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestContainsWord(t *testing.T) {
	cases := []struct {
		s, word string
		want    bool
	}{
		{"Lain’s stick clattered.", "Lain", true},
		{"It had lain there for years.", "Lain", false},
		{"Guyen-the-virus spoke.", "Guyen", true},
		{"Guyenne", "Guyen", false},
	}
	for _, c := range cases {
		if got := ContainsWord(c.s, c.word); got != c.want {
			t.Errorf("ContainsWord(%q, %q) = %v, want %v", c.s, c.word, got, c.want)
		}
	}
}
