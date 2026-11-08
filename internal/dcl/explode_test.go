package dcl

import "testing"

// Test vector from zlib contrib/blast/test.pk.
func TestExplodeVector(t *testing.T) {
	out, err := Explode([]byte{0x00, 0x04, 0x82, 0x24, 0x25, 0x8f, 0x80, 0x7f}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "AIAIAIAIAIAIA" {
		t.Fatalf("got %q", out)
	}
}

func TestExplodeErrors(t *testing.T) {
	if _, err := Explode([]byte{2, 4}, 0); err != ErrLiteralFlag {
		t.Fatal(err)
	}
	if _, err := Explode([]byte{0, 9}, 0); err != ErrDictSize {
		t.Fatal(err)
	}
	if _, err := Explode([]byte{0, 4, 0x82}, 0); err != ErrTruncated {
		t.Fatal(err)
	}
}
