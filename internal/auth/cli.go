package auth

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"retail-prices/internal/db"
)

const adminUsage = `usage: retail admin <subcommand> [flags]

subcommands:
  login-link       print a single-use sign-in link for an administrator
  revoke-sessions  sign every administrator out everywhere`

// RunCLI implements `retail admin`.
func RunCLI(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, adminUsage)
		return fmt.Errorf("missing subcommand")
	}
	switch args[0] {
	case "login-link":
		return loginLink(ctx, args[1:])
	case "revoke-sessions":
		pool, err := db.Connect(ctx)
		if err != nil {
			return err
		}
		defer pool.Close()
		n, err := RevokeAllSessions(ctx, pool)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "revoked %d session(s)\n", n)
		return nil
	default:
		fmt.Fprintln(os.Stderr, adminUsage)
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func loginLink(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("admin login-link", flag.ContinueOnError)
	name := fs.String("name", "", "administrator name, recorded on every decision they make (required)")
	baseURL := fs.String("base-url", "http://localhost:8080", "public URL the app is served at")
	ttl := fs.Duration("ttl", LoginTokenTTL, "how long the link stays valid")
	if err := fs.Parse(args); err != nil {
		return err
	}
	*name = strings.TrimSpace(*name)
	if *name == "" || len(*name) > 40 {
		return fmt.Errorf("--name is required (up to 40 characters)")
	}

	pool, err := db.Connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	raw, err := CreateLoginToken(ctx, pool, *name, *ttl)
	if err != nil {
		return err
	}
	fmt.Printf("%s/admin/login?token=%s\n", strings.TrimRight(*baseURL, "/"), url.QueryEscape(raw))
	fmt.Fprintf(os.Stderr, "single use, valid for %s; signs you in as %q for %d days\n", *ttl, *name, int(SessionTTL.Hours()/24))
	return nil
}
