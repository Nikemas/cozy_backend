package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// createOwnerCmd is the subcommand that bootstraps (or recovers) an owner
// account: `server create-owner --phone +996XXXXXXXXX [--name "…"]`, with
// the password read from the first line of stdin. Runs inside the backend
// container (docs/deployment.md "Первый владелец"), which already has
// DATABASE_URL — it needs nothing else from the server's config.
const createOwnerCmd = "create-owner"

// maxPasswordLineBytes bounds how much of stdin is read for the password
// line (bcrypt itself accepts at most 72 bytes; validation reports that).
const maxPasswordLineBytes = 1024

const createOwnerTimeout = 30 * time.Second

type createOwnerOpts struct {
	phone string
	name  string
}

func parseCreateOwnerArgs(args []string, stderr io.Writer) (createOwnerOpts, error) {
	fs := flag.NewFlagSet(createOwnerCmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o createOwnerOpts
	fs.StringVar(&o.phone, "phone", "", "owner's phone, e.g. +996700123456 (required)")
	fs.StringVar(&o.name, "name", "", "display name for a NEW account (default \""+staff.DefaultOwnerName+"\")")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: server %s --phone +996XXXXXXXXX [--name NAME] < password-on-stdin\n\n"+
			"Creates an active owner, or resets the password of the existing owner with that phone.\n"+
			"The password is read from the first line of stdin (never from a flag, so it\n"+
			"stays out of `ps` and shell history). Needs DATABASE_URL in the environment.\n\n", createOwnerCmd)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return createOwnerOpts{}, err
	}
	if fs.NArg() > 0 {
		return createOwnerOpts{}, fmt.Errorf("unexpected arguments: %q (the password is read from stdin, not from arguments)", fs.Args())
	}
	if strings.TrimSpace(o.phone) == "" {
		fs.Usage()
		return createOwnerOpts{}, errors.New("--phone is required")
	}
	return o, nil
}

// readPasswordLine returns the first line of r without its line ending.
func readPasswordLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(io.LimitReader(r, maxPasswordLineBytes)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", errors.New("no password on stdin (pipe it in, see docs/deployment.md)")
	}
	return line, nil
}

// createOwnerMain runs the subcommand and returns the process exit code.
// Neither the password nor its hash is ever printed.
func createOwnerMain(args []string, stdin io.Reader, stdout, stderr io.Writer, databaseURL string) int {
	opts, err := parseCreateOwnerArgs(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "create-owner:", err)
		return 2
	}
	if databaseURL == "" {
		_, _ = fmt.Fprintln(stderr, "create-owner: DATABASE_URL is not set")
		return 2
	}
	if isTerminal(stdin) {
		_, _ = fmt.Fprint(stderr, "password (typed text is VISIBLE — prefer piping it in): ")
	}
	password, err := readPasswordLine(stdin)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "create-owner:", err)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), createOwnerTimeout)
	defer cancel()
	res, err := ensureOwner(ctx, databaseURL, staff.EnsureOwnerInput{Phone: opts.phone, Password: password, Name: opts.name})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "create-owner:", describeErr(err))
		return 1
	}
	if res.Created {
		_, _ = fmt.Fprintf(stdout, "create-owner: created owner %s (phone %s)\n", res.StaffID, res.Phone)
	} else {
		_, _ = fmt.Fprintf(stdout, "create-owner: reset password of owner %s (phone %s), account active, old sessions revoked\n", res.StaffID, res.Phone)
	}
	return 0
}

func ensureOwner(ctx context.Context, databaseURL string, in staff.EnsureOwnerInput) (staff.EnsureOwnerResult, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return staff.EnsureOwnerResult{}, err
	}
	defer func() { _ = db.Close() }()
	if err := db.PingContext(ctx); err != nil {
		return staff.EnsureOwnerResult{}, fmt.Errorf("connect to the database: %w", err)
	}
	return staff.EnsureOwner(ctx, db, in)
}

// describeErr prints an *apperr.AppError as "code: message" (the message is
// meant for people), anything else as is.
func describeErr(err error) string {
	var ae *apperr.AppError
	if errors.As(err, &ae) {
		return ae.Code + ": " + ae.Message
	}
	return err.Error()
}

func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
