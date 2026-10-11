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
