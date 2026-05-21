package main

import "testing"

// TestShipCommandWiring pins that the `ship` command tree is
// registered and reachable. A regression here (e.g. someone drops the
// rootCmd.AddCommand(shipCmd) line) would otherwise only surface as a
// confusing "unknown command" at runtime.
func TestShipCommandWiring(t *testing.T) {
	t.Run("ship command is registered on root", func(t *testing.T) {
		if _, _, err := rootCmd.Find([]string{"ship"}); err != nil {
			t.Fatalf("expected `ship` command to exist on root: %v", err)
		}
	})

	t.Run("ship introspect-pipelines subcommand exists", func(t *testing.T) {
		c, _, err := rootCmd.Find([]string{"ship", "introspect-pipelines"})
		if err != nil {
			t.Fatalf("expected `ship introspect-pipelines` subcommand: %v", err)
		}
		if c.Name() != "introspect-pipelines" {
			t.Fatalf("resolved to wrong command: %q", c.Name())
		}
	})

	t.Run("introspect-pipelines declares output + full-id flags", func(t *testing.T) {
		c, _, err := rootCmd.Find([]string{"ship", "introspect-pipelines"})
		if err != nil {
			t.Fatalf("find subcommand: %v", err)
		}
		if c.Flags().Lookup("output") == nil {
			t.Error("expected --output flag")
		}
		if c.Flags().Lookup("full-id") == nil {
			t.Error("expected --full-id flag")
		}
	})
}

// TestShortRepo covers the table-display trimming helper.
func TestShortRepo(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://github.com/prellr/multica", "prellr/multica"},
		{"https://github.com/imjenaro/safra-360", "imjenaro/safra-360"},
		// Non-github / unparseable values pass through untouched.
		{"git@github.com:prellr/multica.git", "git@github.com:prellr/multica.git"},
		{"", ""},
		// Exactly the prefix with nothing after → untouched (len guard).
		{"https://github.com/", "https://github.com/"},
	}
	for _, c := range cases {
		if got := shortRepo(c.in); got != c.want {
			t.Errorf("shortRepo(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
