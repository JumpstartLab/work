package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func withToken(t *testing.T, claims string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	jwt := "h." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".s"
	if err := saveToken(&token{AccessToken: jwt, RefreshToken: "r", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
}

func fakeSwitchboard(t *testing.T, handler func(capability string, args map[string]any) (string, bool, int)) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer h.") {
			t.Errorf("missing bearer: %q", r.Header.Get("Authorization"))
		}
		b, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
			Params struct {
				Name      string `json:"name"`
				Arguments struct {
					Capability string         `json:"capability"`
					Args       map[string]any `json:"args"`
				} `json:"arguments"`
			} `json:"params"`
		}
		_ = json.Unmarshal(b, &req)
		if req.Method != "tools/call" || req.Params.Name != "invoke" {
			t.Errorf("unexpected call %s %s", req.Method, req.Params.Name)
		}
		text, isErr, code := handler(req.Params.Arguments.Capability, req.Params.Arguments.Args)
		if code != 0 {
			w.WriteHeader(code)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isErr}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("WORK_SWITCHBOARD_URL", srv.URL)
}

func TestTokenFileIsPrivate(t *testing.T) {
	withToken(t, `{"email":"guest@example.com"}`)
	fi, err := os.Stat(tokenPath())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode %v, want 0600", fi.Mode().Perm())
	}
	if filepath.Base(filepath.Dir(tokenPath())) != "work" {
		t.Fatalf("unexpected path %s", tokenPath())
	}
}

func TestWhoamiReadsEmailClaim(t *testing.T) {
	withToken(t, `{"email":"guest@example.com"}`)
	who, err := whoami()
	if err != nil || who != "guest@example.com" {
		t.Fatalf("whoami = %q, %v", who, err)
	}
}

func TestMyBoxesAndWake(t *testing.T) {
	withToken(t, `{"email":"guest@example.com"}`)
	fakeSwitchboard(t, func(capability string, args map[string]any) (string, bool, int) {
		switch capability {
		case "boxes.mine":
			return `{"boxes":[{"name":"guest-dev","connect":{"user":"guest","host":"guest-dev"}}]}`, false, 0
		case "boxes.wake":
			if args["box"] != "guest-dev" {
				t.Errorf("wake args %v", args)
			}
			return `{"box":"guest-dev","state":"running","health":"starting","connect":{"user":"guest","host":"guest-dev"}}`, false, 0
		}
		return `{"error":["unexpected"]}`, true, 0
	})
	boxes, err := myBoxes()
	if err != nil || len(boxes) != 1 || boxes[0].Connect.Host != "guest-dev" {
		t.Fatalf("myBoxes = %+v, %v", boxes, err)
	}
	s, err := wake("guest-dev")
	if err != nil || s.State != "running" || s.Connect.User != "guest" {
		t.Fatalf("wake = %+v, %v", s, err)
	}
}

func TestToolErrorsSurfaceTheirMessage(t *testing.T) {
	withToken(t, `{}`)
	fakeSwitchboard(t, func(string, map[string]any) (string, bool, int) {
		return `{"error":["you have no box named tractor"]}`, true, 0
	})
	_, err := status("tractor")
	if err == nil || err.Error() != "you have no box named tractor" {
		t.Fatalf("err = %v", err)
	}
}

func TestUnauthorizedSaysLogin(t *testing.T) {
	withToken(t, `{}`)
	fakeSwitchboard(t, func(string, map[string]any) (string, bool, int) { return "", false, 401 })
	_, err := myBoxes()
	if err == nil || !strings.Contains(err.Error(), "work login") {
		t.Fatalf("err = %v", err)
	}
}

func TestNotSignedIn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := accessToken(); err == nil || !strings.Contains(err.Error(), "work login") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoginScopeAddsTheEmailsOrg(t *testing.T) {
	sc, err := loginScope("  Me@Example.COM ")
	if err != nil || !strings.HasSuffix(sc, " urn:zitadel:iam:org:domain:primary:example.com") {
		t.Fatalf("scope %q, err %v", sc, err)
	}
	if sc, _ := loginScope(""); sc != scope {
		t.Fatalf("no email must keep the default scope, got %q", sc)
	}
	for _, bad := range []string{"nobody", "@example.com", "x@localhost", "x@exa mple.com"} {
		if _, err := loginScope(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestConnectForwardsTheMCPCallbackPort(t *testing.T) {
	c := connect{User: "guest", Host: "guest-vm"}
	ssh := strings.Join(connectArgs("ssh", c), " ")
	if ssh != "ssh -t -o ServerAliveInterval=15 -o ServerAliveCountMax=3 -o IgnoreUnknown=UseKeychain -o UseKeychain=yes -o AddKeysToAgent=yes -L 3335:localhost:3335 guest@guest-vm tmux new-session -A -s work" {
		t.Fatalf("ssh argv: %s", ssh)
	}
	et := strings.Join(connectArgs("et", c), " ")
	if et != "et -t 3335:3335 guest@guest-vm -c tmux new-session -A -s work" {
		t.Fatalf("et argv: %s", et)
	}
}

func fakeSSH(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStayConnectedReconnectsAfterADrop(t *testing.T) {
	settled, reconnectWait = 50*time.Millisecond, 10*time.Millisecond
	count := filepath.Join(t.TempDir(), "n")
	// First run: a session that was up, then dropped (255). Second: clean exit.
	ssh := fakeSSH(t, `n=$(cat `+count+` 2>/dev/null || echo 0); echo $((n+1)) > `+count+`
if [ "$n" = 0 ]; then sleep 0.2; exit 255; fi; exit 0`)
	if err := stayConnected(ssh, connect{User: "u", Host: "h"}); err != nil {
		t.Fatalf("want a clean exit after reconnecting, got %v", err)
	}
	if b, _ := os.ReadFile(count); strings.TrimSpace(string(b)) != "2" {
		t.Fatalf("ssh ran %s times, want 2", b)
	}
}

func TestStayConnectedDoesNotLoopOnARefusedLogin(t *testing.T) {
	settled = time.Second
	ssh := fakeSSH(t, "exit 255")
	if err := stayConnected(ssh, connect{User: "u", Host: "h"}); err == nil {
		t.Fatal("a first attempt that fails at once must be an error, not a retry loop")
	}
}

func TestBusEnrollWritesTheNodeToken(t *testing.T) {
	withToken(t, `{"email":"guest@example.com"}`)
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/enrollments" || r.Header.Get("Authorization") != "Bearer h.eyJlbWFpbCI6Imd1ZXN0QGV4YW1wbGUuY29tIn0.s" {
			t.Errorf("unexpected request %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"ok":true,"machine":"guest-vm","token":"nt-123","tenant":{"slug":"guest"}}`))
	}))
	defer srv.Close()
	t.Setenv("WORK_PARTY_LINE_URL", srv.URL)
	if err := busEnroll("guest-vm"); err != nil {
		t.Fatal(err)
	}
	if got["machine"] != "guest-vm" {
		t.Fatalf("sent %v", got)
	}
	p := filepath.Join(partyLineDir(), "guest-vm.env")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	want := "PARTY_LINE_URL=" + srv.URL + "\nNODE_TOKEN=nt-123\nNODE_MACHINE=guest-vm\n"
	if string(b) != want {
		t.Fatalf("env file:\n%s\nwant:\n%s", b, want)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
	}
}

func TestBusEnrollReportsARefusal(t *testing.T) {
	withToken(t, `{"email":"guest@example.com"}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"enrollment is not open for this address"}`))
	}))
	defer srv.Close()
	t.Setenv("WORK_PARTY_LINE_URL", srv.URL)
	err := busEnroll("guest-vm")
	if err == nil || !strings.Contains(err.Error(), "not open for this address") {
		t.Fatalf("want the server's reason, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(partyLineDir(), "guest-vm.env")); statErr == nil {
		t.Fatal("a refused enrollment must not write a token file")
	}
	if busEnroll("Bad Name") == nil {
		t.Fatal("bad machine name accepted")
	}
}
