package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Party-line is the message bus Claude sessions use to talk to each other.
// It is multi-tenant: a machine enrolls once with its owner's sign-in (the same
// token `work login` holds) and gets its own node token, so a person's laptop
// and box share a bus with each other and with nobody else.

func partyLineURL() string {
	if u := os.Getenv("WORK_PARTY_LINE_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "https://party-line.jumpstartlab.com"
}

// partyLineDir is where the party-line client looks for a machine's token.
func partyLineDir() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "party-line")
}

// What party-line's enrollment error codes mean to the person running this.
var enrollErrors = map[string]string{
	"missing_token":             "sign in first with `work login <your email>`",
	"invalid_token":             "your sign-in isn't valid here; run `work login <your email>` again",
	"expired":                   "your sign-in expired; run `work login <your email>` again",
	"wrong_audience":            "this sign-in isn't for party-line; update work and sign in again",
	"wrong_issuer":              "this sign-in isn't for party-line; update work and sign in again",
	"wrong_client":              "this sign-in isn't for party-line; update work and sign in again",
	"not_entitled":              "your account isn't on party-line yet; ask Jumpstart Lab",
	"email_not_verified":        "your email isn't verified; ask Jumpstart Lab",
	"archived":                  "your party-line account is archived; ask Jumpstart Lab",
	"env_provisioned":           "this machine's party-line token is managed for you; nothing to do",
	"machine_taken":             "another machine already uses that name; pick another: `work bus enroll <name>`",
	"invalid_machine":           "that machine name isn't allowed; use lowercase letters, digits and dashes",
	"enrollment_not_configured": "party-line isn't open for new machines yet; try again later",
}

var machineName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// defaultMachine is this computer's short host name, lowercased.
func defaultMachine() string {
	h, _ := os.Hostname()
	h = strings.ToLower(strings.SplitN(h, ".", 2)[0])
	return strings.Trim(regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(h, "-"), "-")
}

// busEnroll enrolls this machine on party-line as the signed-in person and
// writes its node token. Running it again rotates the token.
func busEnroll(machine string) error {
	if machine == "" {
		machine = defaultMachine()
	}
	if !machineName.MatchString(machine) {
		return fmt.Errorf("%q is not a usable machine name (lowercase letters, digits and dashes)", machine)
	}
	tok, err := accessToken()
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"machine": machine})
	req, _ := http.NewRequest("POST", partyLineURL()+"/api/v1/enrollments", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("party-line unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		OK      bool   `json:"ok"`
		Machine string `json:"machine"`
		Token   string `json:"token"`
		Error   string `json:"error"`
		Tenant  struct {
			Slug string `json:"slug"`
		} `json:"tenant"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode/100 != 2 || out.Token == "" {
		msg := out.Error
		if plain, ok := enrollErrors[msg]; ok {
			msg = plain + " (" + msg + ")"
		}
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
			if len(msg) > 200 {
				msg = msg[:200]
			}
		}
		return fmt.Errorf("party-line refused enrollment (HTTP %d): %s", resp.StatusCode, msg)
	}
	if out.Machine != "" {
		machine = out.Machine
	}
	dir := partyLineDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, machine+".env")
	env := fmt.Sprintf("PARTY_LINE_URL=%s\nNODE_TOKEN=%s\nNODE_MACHINE=%s\n", partyLineURL(), out.Token, machine)
	if err := os.WriteFile(path, []byte(env), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil { // WriteFile keeps an existing file's mode
		return err
	}
	tenant := ""
	if out.Tenant.Slug != "" {
		tenant = " (tenant " + out.Tenant.Slug + ")"
	}
	fmt.Printf("Enrolled %s on party-line%s; token in %s.\n", machine, tenant, path)
	return nil
}

func bus(args []string) error {
	if len(args) == 0 || args[0] != "enroll" {
		return errors.New("usage: work bus enroll [machine]")
	}
	machine := ""
	if len(args) > 1 {
		machine = args[1]
	}
	return busEnroll(machine)
}
