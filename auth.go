package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Sign-in is the OAuth 2.0 device authorization grant (RFC 8628) against
// Zitadel: `work login` prints a code, you approve it in a browser with your
// own Google account, and the CLI keeps the access + refresh tokens in
// ~/.config/work/token.json (0600). The access token is a JWT whose audience
// includes the Switchboard project, which is what Switchboard's /mcp accepts.

// issuer is a var so tests can point it at a stub token endpoint.
var issuer = "https://identity.jumpstartlab.com"

const (
	clientID = "399472863209404232" // "work CLI" native app, Switchboard project
	scope    = "openid email profile offline_access urn:zitadel:iam:org:project:id:383836969857207435:aud"
)

type token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
}

func tokenPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "work", "token.json")
}

// The token file is shared with other tools that sign in the same way (the
// Beacon Mac app), and Zitadel rotates the refresh token on every use, so two
// tools refreshing at once would leave one holding a dead refresh token. The
// contract every writer follows:
//
//   - Hold an exclusive flock(2) on token.lock (same directory, created 0600)
//     across read-check-refresh-write, and across logout's delete. After
//     taking it, re-read token.json: another process may have refreshed.
//   - Write token.json atomically: a temp file in the same directory
//     (".token.json.*.tmp"), mode 0600, fsync, rename over token.json.
//   - Readers don't need the lock. A file that reads empty or half-written is
//     retried once after a short pause before it counts as unreadable.
func tokenLockPath() string {
	return filepath.Join(filepath.Dir(tokenPath()), "token.lock")
}

// withTokenLock runs fn while holding the exclusive lock on token.lock. The
// lock belongs to the open file, so it also excludes other goroutines in this
// process that take it through their own call.
func withTokenLock(fn func() error) error {
	p := tokenLockPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("locking %s: %w", p, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

// tokenRetryDelay is how long a reader waits before re-reading a token file
// that looked empty or half-written.
var tokenRetryDelay = 100 * time.Millisecond

func loadToken() (*token, error) {
	p := tokenPath()
	t, err := readToken(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("not signed in — run `work login`")
	}
	if err != nil {
		time.Sleep(tokenRetryDelay)
		t, err = readToken(p)
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("not signed in — run `work login`")
	}
	if err != nil {
		return nil, fmt.Errorf("token file unreadable (%v) — run `work login`", err)
	}
	fixTokenMode(p)
	return t, nil
}

func readToken(p string) (*token, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, errors.New("empty file")
	}
	var t token
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// fixTokenMode puts a token file that someone loosened back to 0600.
func fixTokenMode(p string) {
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() == 0o600 {
		return
	}
	if err := os.Chmod(p, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s is mode %04o and could not be made 0600: %v\n", p, fi.Mode().Perm(), err)
		return
	}
	fmt.Fprintf(os.Stderr, "warning: %s was mode %04o; set it back to 0600\n", p, fi.Mode().Perm())
}

func saveToken(t *token) error {
	return withTokenLock(func() error { return writeToken(t) })
}

// writeToken replaces token.json atomically. The caller holds the token lock.
func writeToken(t *token) error {
	p := tokenPath()
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(t, "", "  ")
	f, err := os.CreateTemp(dir, ".token.json.*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op once renamed
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func postForm(path string, form url.Values) (*tokenResponse, map[string]any, error) {
	resp, err := http.PostForm(issuer+path, form)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, nil, fmt.Errorf("%s: unreadable response (%s)", path, resp.Status)
	}
	b, _ := json.Marshal(raw)
	var tr tokenResponse
	_ = json.Unmarshal(b, &tr)
	return &tr, raw, nil
}

func (tr *tokenResponse) toToken(prevRefresh string) *token {
	t := &token{AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken,
		Expiry: time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)}
	if t.RefreshToken == "" {
		t.RefreshToken = prevRefresh
	}
	return t
}

// loginScope adds Zitadel's org scope for the email's domain. The device flow
// never passes the edge's "work email" step, so without it Zitadel signs the
// person in through the default org, which refuses customer accounts. An empty
// email (a personal address that belongs to no org) keeps the default org.
func loginScope(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return scope, nil
	}
	at := strings.LastIndex(email, "@")
	domain := email[at+1:]
	if at < 1 || !strings.Contains(domain, ".") || strings.ContainsAny(domain, " /") {
		return "", fmt.Errorf("%q is not an email address", email)
	}
	return scope + " urn:zitadel:iam:org:domain:primary:" + domain, nil
}

func login(email string) error {
	sc, err := loginScope(email)
	if err != nil {
		return err
	}
	_, raw, err := postForm("/oauth/v2/device_authorization", url.Values{"client_id": {clientID}, "scope": {sc}})
	if err != nil {
		return err
	}
	if e, ok := raw["error"]; ok {
		return fmt.Errorf("sign-in refused: %v %v", e, raw["error_description"])
	}
	device, _ := raw["device_code"].(string)
	code, _ := raw["user_code"].(string)
	link, _ := raw["verification_uri_complete"].(string)
	interval := 5
	if v, ok := raw["interval"].(float64); ok && v > 0 {
		interval = int(v)
	}
	fmt.Printf("Open %s\nand confirm the code %s (sign in with your own Google account).\n", link, code)
	openBrowser(link)

	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(time.Duration(interval) * time.Second)
		tr, _, err := postForm("/oauth/v2/token", url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {device}, "client_id": {clientID}})
		if err != nil {
			return err
		}
		switch tr.Error {
		case "":
			if err := saveToken(tr.toToken("")); err != nil {
				return err
			}
			who, _ := whoami()
			fmt.Printf("Signed in%s.\n", strings.TrimSpace(" as "+who))
			return nil
		case "authorization_pending":
		case "slow_down":
			interval += 5
		default:
			return fmt.Errorf("sign-in failed: %s %s", tr.Error, tr.ErrorDesc)
		}
	}
	return errors.New("sign-in timed out — run `work login` again")
}

// accessToken returns a live access token, refreshing it when it is within a
// minute of expiry. The refresh runs under the token lock and re-reads the file
// first, so when another process refreshed while we waited, we use its token.
func accessToken() (string, error) {
	t, err := loadToken()
	if err != nil {
		return "", err
	}
	if fresh(t) {
		return t.AccessToken, nil
	}
	var access string
	err = withTokenLock(func() error {
		t, err := loadToken()
		if err != nil {
			return err
		}
		if fresh(t) {
			access = t.AccessToken
			return nil
		}
		if t.RefreshToken == "" {
			return errors.New("session expired — run `work login`")
		}
		tr, _, err := postForm("/oauth/v2/token", url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {t.RefreshToken},
			"client_id": {clientID}, "scope": {scope}})
		if err != nil {
			return err
		}
		if tr.Error != "" {
			return fmt.Errorf("session expired (%s) — run `work login`", tr.Error)
		}
		nt := tr.toToken(t.RefreshToken)
		if err := writeToken(nt); err != nil {
			return err
		}
		access = nt.AccessToken
		return nil
	})
	return access, err
}

func fresh(t *token) bool { return time.Until(t.Expiry) > time.Minute }

// whoami reads the email claim from the access token (display only; Switchboard
// verifies it).
func whoami() (string, error) {
	t, err := loadToken()
	if err != nil {
		return "", err
	}
	parts := strings.Split(t.AccessToken, ".")
	if len(parts) != 3 {
		return "", errors.New("access token is not a JWT")
	}
	b, err := decodeSegment(parts[1])
	if err != nil {
		return "", err
	}
	var claims struct {
		Email string `json:"email"`
	}
	_ = json.Unmarshal(b, &claims)
	return claims.Email, nil
}

func logout() error {
	err := withTokenLock(func() error {
		err := os.Remove(tokenPath())
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	})
	if err != nil {
		return err
	}
	fmt.Println("Signed out.")
	return nil
}
