package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestParseCreateOwnerArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    createOwnerOpts
		wantErr string
	}{
		{name: "phone only", args: []string{"--phone", "+996700123456"}, want: createOwnerOpts{phone: "+996700123456"}},
		{name: "phone and name", args: []string{"--phone=0700123456", "--name", "Айбек"}, want: createOwnerOpts{phone: "0700123456", name: "Айбек"}},
		{name: "missing phone", args: nil, wantErr: "--phone is required"},
		{name: "password as positional arg refused", args: []string{"--phone", "+996700123456", "hunter2hunter2"}, wantErr: "unexpected arguments"},
		{name: "password flag does not exist", args: []string{"--phone", "+996700123456", "--password", "x"}, wantErr: "not defined"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseCreateOwnerArgs(c.args, io.Discard)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestReadPasswordLine(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "s3cret-pass\n", want: "s3cret-pass"},
		{in: "s3cret-pass\r\nsecond line\n", want: "s3cret-pass"},
		{in: "no-newline-at-eof", want: "no-newline-at-eof"},
		{in: "  spaces kept  \n", want: "  spaces kept  "},
		{in: "", wantErr: true},
		{in: "\n", wantErr: true},
	}
	for _, c := range cases {
		got, err := readPasswordLine(strings.NewReader(c.in))
		if c.wantErr {
			if err == nil {
				t.Errorf("readPasswordLine(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("readPasswordLine(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

func TestCreateOwnerMainValidatesBeforeTouchingTheDatabase(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		stdin string
		dbURL string
		want  string
	}{
		{name: "no DATABASE_URL", args: []string{"--phone", "+996700123456"}, stdin: "password123\n", want: "DATABASE_URL is not set"},
		{name: "empty stdin", args: []string{"--phone", "+996700123456"}, stdin: "", dbURL: "postgres://unused", want: "no password on stdin"},
		{name: "missing phone", args: nil, stdin: "password123\n", dbURL: "postgres://unused", want: "--phone is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := createOwnerMain(c.args, strings.NewReader(c.stdin), &stdout, &stderr, c.dbURL)
			if code != 2 {
				t.Fatalf("exit code = %d, want 2 (stderr: %s)", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), c.want) {
				t.Fatalf("stderr = %q, want it to contain %q", stderr.String(), c.want)
			}
			if strings.Contains(stdout.String()+stderr.String(), "password123") {
				t.Fatal("the password was echoed to the output")
			}
		})
	}
}

func TestCreateOwnerMainHelpExitsZero(t *testing.T) {
	var stderr bytes.Buffer
	if code := createOwnerMain([]string{"-h"}, strings.NewReader(""), io.Discard, &stderr, ""); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stderr.String(), "usage: server create-owner") {
		t.Fatalf("usage not printed: %q", stderr.String())
	}
}
