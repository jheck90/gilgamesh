package secrets

import "testing"

func TestSealOpen(t *testing.T) {
	box, err := NewBox(GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	sealed := box.Seal([]byte("sk-test"), "user:1")
	got, err := box.Open(sealed, "user:1")
	if err != nil || string(got) != "sk-test" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	if _, err := box.Open(sealed, "user:2"); err == nil {
		t.Error("a key sealed for user 1 opened for user 2")
	}
	other, _ := NewBox(GenerateKey())
	if _, err := other.Open(sealed, "user:1"); err == nil {
		t.Error("a key opened with the wrong secret")
	}
	if _, err := NewBox("dG9vIHNob3J0"); err == nil {
		t.Error("accepted a short secret")
	}
}
