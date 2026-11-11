package postgres

import "testing"

func TestSchemaName(t *testing.T) {
	cases := map[string]string{
		"/registry":       "registry",
		"registry":        "registry",
		"/test-ab12":      "test_ab12",
		"/Solas/Registry": "solas_registry",
		"/":               "registry",
		"":                "registry",
	}
	for prefix, want := range cases {
		if got := SchemaName(prefix); got != want {
			t.Errorf("SchemaName(%q) = %q, want %q", prefix, got, want)
		}
	}
}
