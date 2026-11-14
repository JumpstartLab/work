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
	"time"
)

// Sign-in is the OAuth 2.0 device authorization grant (RFC 8628) against
// Zitadel: `work login` prints a code, you approve it in a browser with your
// own Google account, and the CLI keeps the access + refresh tokens in
// ~/.config/work/token.json (0600). The access token is a JWT whose audience
// includes the Switchboard project, which is what Switchboard's /mcp accepts.
const (
	issuer   = "https://identity.jumpstartlab.com"
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

func loadToken() (*token, error) {
	b, err := os.ReadFile(tokenPath())
	if err != nil {
		return nil, errors.New("not signed in — run `work login`")
	}
	var t token
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("token file unreadable (%v) — run `work login`", err)
	}
	return &t, nil
}

func saveToken(t *token) error {
	p := tokenPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(t, "", "  ")
	return os.WriteFile(p, b, 0o600)
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
// minute of expiry.
func accessToken() (string, error) {
	t, err := loadToken()
	if err != nil {
		return "", err
	}
	if time.Until(t.Expiry) > time.Minute {
		return t.AccessToken, nil
	}
	if t.RefreshToken == "" {
		return "", errors.New("session expired — run `work login`")
	}
	tr, _, err := postForm("/oauth/v2/token", url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {t.RefreshToken},
		"client_id": {clientID}, "scope": {scope}})
	if err != nil {
		return "", err
	}
	if tr.Error != "" {
		return "", fmt.Errorf("session expired (%s) — run `work login`", tr.Error)
	}
	nt := tr.toToken(t.RefreshToken)
	if err := saveToken(nt); err != nil {
		return "", err
	}
	return nt.AccessToken, nil
}

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
	err := os.Remove(tokenPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Println("Signed out.")
	return nil
}
