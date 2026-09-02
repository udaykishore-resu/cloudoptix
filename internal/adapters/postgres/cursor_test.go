package postgres

import "testing"

func TestCursorRoundTrip(t *testing.T) {
	cases := [][]string{
		nil,
		{"a"},
		{"2024-01-02T15:04:05Z", "rec_01j9x2m4qk_8f21c0de"},
		{"with spaces", "unicode-✓", ""},
	}
	for _, vals := range cases {
		enc := encodeCursor(vals...)
		if len(vals) == 0 && enc == "" {
			// encodeCursor with zero args still produces a token; only the
			// empty string itself means "no cursor" to decodeCursor.
			t.Fatalf("encodeCursor() of empty slice should not produce empty string")
		}
		got, err := decodeCursor(enc)
		if err != nil {
			t.Fatalf("decodeCursor(%q) error: %v", enc, err)
		}
		if len(got) != len(vals) {
			t.Fatalf("round trip length mismatch: got %v want %v", got, vals)
		}
		for i := range vals {
			if got[i] != vals[i] {
				t.Errorf("round trip[%d] = %q want %q", i, got[i], vals[i])
			}
		}
	}
}

func TestDecodeCursorEmptyString(t *testing.T) {
	vals, err := decodeCursor("")
	if err != nil {
		t.Fatalf("decodeCursor(\"\") error: %v", err)
	}
	if vals != nil {
		t.Fatalf("decodeCursor(\"\") = %v, want nil (first page)", vals)
	}
}

func TestDecodeCursorMalformed(t *testing.T) {
	cases := []string{
		"not-base64-!!!",
		"aGVsbG8", // valid base64, but not our JSON envelope
	}
	for _, c := range cases {
		if _, err := decodeCursor(c); err == nil {
			t.Errorf("decodeCursor(%q) expected an error, got nil", c)
		}
	}
}

func TestExpectCursorArity(t *testing.T) {
	tok := encodeCursor("a", "b")
	if _, err := expectCursor(tok, 2); err != nil {
		t.Fatalf("expectCursor with matching arity: %v", err)
	}
	if _, err := expectCursor(tok, 3); err == nil {
		t.Fatalf("expectCursor with mismatched arity: expected an error")
	}
	vals, err := expectCursor("", 5)
	if err != nil || vals != nil {
		t.Fatalf("expectCursor(\"\", 5) = %v, %v; want nil, nil", vals, err)
	}
}

func TestEncodeCursorNoValueCollision(t *testing.T) {
	// Two different value sets must not collide onto the same token — a
	// pagination bug here silently serves the wrong page.
	a := encodeCursor("x", "1")
	b := encodeCursor("x1")
	if a == b {
		t.Fatalf("distinct cursors encoded identically: %q", a)
	}
}
