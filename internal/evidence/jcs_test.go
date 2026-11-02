package evidence

import "testing"

func TestCanonicalJCSSortsAndFormats(t *testing.T) {
	b, err := CanonicalJCS(map[string]any{"b": 1, "a": "x", "c": []int{2, 1}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"a":"x","b":1,"c":[2,1]}`; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestCanonicalJCSEscaping(t *testing.T) {
	b, _ := CanonicalJCS(map[string]any{"k": "a\"b\\c\nd\te\u0001"})
	if got, want := string(b), `{"k":"a\"b\\c\nd\te\u0001"}`; got != want {
		t.Fatalf("got %s", got)
	}
}

func TestCanonicalJCSES6Numbers(t *testing.T) {
	// RFC 8785 numbers are IEEE-754 doubles with ES6 formatting; integers
	// beyond 2^53 lose precision, which is why producers send them as strings.
	b, _ := CanonicalizeJSON([]byte(`{"big":9007199254740993,"f":1.0,"e":1E21,"s":0.1,"m":1e-7}`))
	if got, want := string(b), `{"big":9007199254740992,"e":1e+21,"f":1,"m":1e-7,"s":0.1}`; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestCanonicalJCSUTF16Order(t *testing.T) {
	// U+1D11E (surrogate pair, first unit 0xD834) sorts before U+FF21 in
	// UTF-16 order although its code point is larger.
	b, _ := CanonicalJCS(map[string]any{"\U0001D11E": 1, "Ａ": 2})
	if got, want := string(b), "{\"\U0001D11E\":1,\"Ａ\":2}"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
