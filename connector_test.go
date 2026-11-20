package sqlkit

import (
	"net/url"
	"testing"
)

func TestConnectorBuild_EscapesValues(t *testing.T) {
	tests := []struct {
		name     string
		password string
		want     string
	}{
		{"plain unchanged", "secret", "host=h port=5432 user=u password=secret dbname=d sslmode=disable"},
		{"space", "a b", `host=h port=5432 user=u password='a b' dbname=d sslmode=disable`},
		{"quote", "a'b", `host=h port=5432 user=u password='a\'b' dbname=d sslmode=disable`},
		{"backslash", `a\b`, `host=h port=5432 user=u password='a\\b' dbname=d sslmode=disable`},
		{"empty", "", `host=h port=5432 user=u password='' dbname=d sslmode=disable`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewConnector().Host("h").Port(5432).Username("u").Password(tt.password).Database("d")
			if got := c.Build(); got != tt.want {
				t.Fatalf("Build() = %q, want %q", got, tt.want)
			}

			if got := c.BuildMigration(); got != tt.want {
				t.Fatalf("BuildMigration() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildClickhouse_EscapesUserinfo(t *testing.T) {
	c := NewConnector().Host("ch.local").Port(8443).Username("us@er:x").Password("p@ss:w/ord").
		Database("db").ReadTimeout(5).WriteTimeout(7)

	raw := c.BuildClickhouse()

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}

	pass, _ := u.User.Password()
	if u.User.Username() != "us@er:x" || pass != "p@ss:w/ord" {
		t.Fatalf("userinfo = %q:%q (url %s)", u.User.Username(), pass, raw)
	}

	if u.Host != "ch.local:8443" || u.Path != "/db" || u.RawQuery != "read_timeout=5s&write_timeout=7s" {
		t.Fatalf("url = %s", raw)
	}
}

func TestBuildClickhouse_PlainFormatUnchanged(t *testing.T) {
	c := NewConnector().Host("h").Port(1).Username("u").Password("p").Database("d")
	if got, want := c.BuildClickhouse(), "https://u:p@h:1/d?"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
