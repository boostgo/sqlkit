package sqlkit

import "testing"

func TestArguments_AddManyFormat(t *testing.T) {
	a := NewArguments("x")

	if got := a.AddMany(1, 2, 3); got != "($2, $3, $4)" {
		t.Fatalf("got %q", got)
	}

	if got := a.AddMany(); got != "" {
		t.Fatalf("empty AddMany = %q", got)
	}

	if got := a.AddMany(9); got != "($5)" {
		t.Fatalf("got %q", got)
	}

	if len(a.Args()) != 5 {
		t.Fatalf("args = %v", a.Args())
	}
}
