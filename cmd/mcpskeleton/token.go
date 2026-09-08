package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/out/auth/local"
	"github.com/bartdelepeleer/mcpskeleton/internal/config"
)

// runToken dispatches the token subcommands.
func runToken(args []string, cfg config.Config, out io.Writer, now func() time.Time) error {
	if len(args) == 0 {
		return errors.New("token: expected issue, list or revoke")
	}

	store, err := local.NewStore(cfg.UsersFile)
	if err != nil {
		return err
	}

	switch args[0] {
	case "issue":
		return tokenIssue(args[1:], store, out, now)
	case "list":
		return tokenList(args[1:], store, out)
	case "revoke":
		return tokenRevoke(args[1:], store, out)
	default:
		return fmt.Errorf("token: unknown subcommand %q", args[0])
	}
}

func tokenIssue(args []string, store *local.Store, out io.Writer, now func() time.Time) error {
	fs := flag.NewFlagSet("token issue", flag.ContinueOnError)
	fs.SetOutput(out)
	user := fs.String("user", "", "subject to issue the token for (required)")
	create := fs.Bool("create", false, "provision the user if they do not exist")
	scopes := fs.String("scopes", "", "comma-separated scopes; empty grants none")
	ttl := fs.String("ttl", "", "how long the token lives, e.g. 90d or 720h; empty never expires")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *user == "" {
		return errors.New("token issue: --user is required")
	}

	opts := local.IssueOptions{
		Scopes:     splitScopes(*scopes),
		CreateUser: *create,
	}
	if *ttl != "" {
		d, err := parseTTL(*ttl)
		if err != nil {
			return fmt.Errorf("token issue: --ttl: %w", err)
		}
		expires := now().UTC().Add(d)
		opts.ExpiresAt = &expires
	}

	issued, err := store.IssueToken(*user, opts)
	if errors.Is(err, local.ErrUserNotFound) {
		// The guard that turns a typo into a failure rather than a second
		// account with a working credential.
		return fmt.Errorf("token issue: no user %q; pass --create to provision them", *user)
	}
	if err != nil {
		return err
	}

	if issued.UserCreated {
		_, _ = fmt.Fprintf(out, "Created user %q.\n", issued.Subject)
	}
	_, _ = fmt.Fprintf(out, "Token id:  %s\n", issued.TokenID)
	_, _ = fmt.Fprintf(out, "Scopes:    %s\n", describeScopes(issued.Scopes))
	_, _ = fmt.Fprintf(out, "Expires:   %s\n", describeExpiry(issued.ExpiresAt))
	_, _ = fmt.Fprintf(out, "\n%s\n\n", issued.Token)
	// The store keeps only a hash, so this really is the only chance.
	_, _ = fmt.Fprintln(out, "This token is shown once and cannot be recovered. Store it now.")
	_, _ = fmt.Fprintf(out, "\nTo use it with Claude Code:\n")
	_, _ = fmt.Fprintf(out, "  claude mcp add --transport http mcpskeleton <base-url>%s \\\n", config.MCPPath)
	_, _ = fmt.Fprintf(out, "    --header \"Authorization: Bearer %s\"\n", issued.Token)

	return nil
}

func tokenList(args []string, store *local.Store, out io.Writer) error {
	fs := flag.NewFlagSet("token list", flag.ContinueOnError)
	fs.SetOutput(out)
	if err := fs.Parse(args); err != nil {
		return err
	}

	users, err := store.Users()
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TOKEN ID\tUSER\tSCOPES\tCREATED\tEXPIRES")

	rows := 0
	for _, u := range users {
		for _, t := range u.Tokens {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
				t.ID, u.Subject, describeScopes(t.Scopes),
				t.CreatedAt.Format(time.RFC3339), describeExpiry(t.ExpiresAt))
			rows++
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if rows == 0 {
		_, _ = fmt.Fprintln(out, "\nNo tokens have been issued.")
	}
	return nil
}

func tokenRevoke(args []string, store *local.Store, out io.Writer) error {
	fs := flag.NewFlagSet("token revoke", flag.ContinueOnError)
	fs.SetOutput(out)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("token revoke: expected exactly one token id")
	}

	id := fs.Arg(0)
	if err := store.RevokeToken(id); err != nil {
		if errors.Is(err, local.ErrTokenNotFound) {
			return fmt.Errorf("token revoke: no token %q; run `token list` to see the ids", id)
		}
		return err
	}

	// Authentication reads the file on every request, so this is already true.
	_, _ = fmt.Fprintf(out, "Revoked %s. It stops working on the next request.\n", id)
	return nil
}

// runUser dispatches the user subcommands.
//
// There is no `user add`: `token issue --create` covers provisioning, and a
// user record is three lines of JSON that can be edited by hand.
func runUser(args []string, cfg config.Config, out io.Writer) error {
	if len(args) == 0 || args[0] != "list" {
		return errors.New("user: expected list")
	}

	store, err := local.NewStore(cfg.UsersFile)
	if err != nil {
		return err
	}
	users, err := store.Users()
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SUBJECT\tID\tENABLED\tTOKENS")
	for _, u := range users {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%t\t%d\n", u.Subject, u.ID, u.Enabled, len(u.Tokens))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(users) == 0 {
		_, _ = fmt.Fprintf(out, "\nNo users in %s.\n", cfg.UsersFile)
	}
	return nil
}

func splitScopes(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	scopes := make([]string, 0, len(parts))
	for _, p := range parts {
		if scope := strings.TrimSpace(p); scope != "" {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}

// parseTTL accepts Go duration syntax plus a days suffix.
//
// Days are not a Go duration unit, but they are the unit credentials are
// actually described in, and writing 2160h for ninety days invites arithmetic
// mistakes in the direction of too long.
func parseTTL(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, fmt.Errorf("%q is not a number of days", s)
		}
		if n <= 0 {
			return 0, fmt.Errorf("%q must be positive", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}

	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration (try 90d or 720h)", s)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%q must be positive", s)
	}
	return d, nil
}

func describeScopes(scopes []string) string {
	if len(scopes) == 0 {
		return "(none)"
	}
	return strings.Join(scopes, ",")
}

func describeExpiry(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.Format(time.RFC3339)
}
