// Package keychain abstracts macOS Keychain access for Claude Code's OAuth
// credentials (generic password, service "Claude Code-credentials").
package keychain

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

const Service = "Claude Code-credentials"

// Credential is what gets stashed per context (0600 JSON file).
type Credential struct {
	Service  string `json:"service"`
	Account  string `json:"account"`
	Password string `json:"password"`
}

// ErrNotFound means no keychain item exists for the service.
var ErrNotFound = errors.New("keychain item not found")

type Backend interface {
	// Read returns the current credential, or ErrNotFound.
	Read() (Credential, error)
	// Write upserts the credential.
	Write(Credential) error
	// Delete removes the item; absent item is not an error.
	Delete() error
}

// Null is used on non-darwin platforms or when keychain handling is disabled.
// Linux needs nothing: ~/.claude/.credentials.json travels with the dir.
type Null struct{}

func (Null) Read() (Credential, error) { return Credential{}, ErrNotFound }
func (Null) Write(Credential) error    { return nil }
func (Null) Delete() error             { return nil }

// Fake is a test double.
type Fake struct {
	Cred *Credential
	// FailOn lets tests inject failures: "read", "write", "delete".
	FailOn map[string]error
}

func (f *Fake) Read() (Credential, error) {
	if err := f.FailOn["read"]; err != nil {
		return Credential{}, err
	}
	if f.Cred == nil {
		return Credential{}, ErrNotFound
	}
	return *f.Cred, nil
}

func (f *Fake) Write(c Credential) error {
	if err := f.FailOn["write"]; err != nil {
		return err
	}
	f.Cred = &c
	return nil
}

func (f *Fake) Delete() error {
	if err := f.FailOn["delete"]; err != nil {
		return err
	}
	f.Cred = nil
	return nil
}

// Mac talks to the real keychain via the `security` CLI.
type Mac struct{}

var acctRe = regexp.MustCompile(`"acct"<blob>="((?:[^"\\]|\\.)*)"`)

func (Mac) Read() (Credential, error) {
	// Two calls: attributes (for the account name), then -w for the secret.
	attrs, err := runSecurity(nil, "find-generic-password", "-s", Service)
	if err != nil {
		if isNotFound(err) {
			return Credential{}, ErrNotFound
		}
		return Credential{}, err
	}
	account := ""
	if m := acctRe.FindStringSubmatch(attrs); m != nil {
		account = m[1]
	}
	secret, err := runSecurity(nil, "find-generic-password", "-s", Service, "-w")
	if err != nil {
		if isNotFound(err) {
			return Credential{}, ErrNotFound
		}
		return Credential{}, err
	}
	return Credential{
		Service:  Service,
		Account:  account,
		Password: strings.TrimSuffix(secret, "\n"),
	}, nil
}

func (Mac) Write(c Credential) error {
	// `security -i` reads commands from stdin, keeping the secret off argv
	// (argv is visible to every process via ps).
	cmd := fmt.Sprintf("add-generic-password -U -s %s -a %s -w %s\n",
		quote(Service), quote(c.Account), quote(c.Password))
	_, err := runSecurity([]byte(cmd), "-i")
	return err
}

func (Mac) Delete() error {
	_, err := runSecurity(nil, "delete-generic-password", "-s", Service)
	if err != nil && !isNotFound(err) {
		return err
	}
	return nil
}

// quote escapes a string for the security(1) interactive command parser.
func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

type securityError struct {
	exitCode int
	stderr   string
}

func (e *securityError) Error() string {
	return fmt.Sprintf("security exited %d: %s", e.exitCode, strings.TrimSpace(e.stderr))
}

// isNotFound detects errSecItemNotFound (exit code 44 / "could not be found").
func isNotFound(err error) bool {
	var se *securityError
	if errors.As(err, &se) {
		return se.exitCode == 44 || strings.Contains(se.stderr, "could not be found")
	}
	return false
}

func runSecurity(stdin []byte, args ...string) (string, error) {
	cmd := exec.Command("security", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", &securityError{exitCode: ee.ExitCode(), stderr: errb.String()}
		}
		return "", err
	}
	return out.String(), nil
}
