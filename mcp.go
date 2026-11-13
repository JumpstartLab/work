package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
)

// Switchboard's /mcp is stateless, so each call is one JSON-RPC tools/call of
// the `invoke` meta-tool naming a boxes.* capability.
func switchboardURL() string {
	if u := os.Getenv("WORK_SWITCHBOARD_URL"); u != "" {
		return u
	}
	return "https://my.jumpstartlab.com/mcp"
}

var httpClient = &http.Client{Timeout: 60 * time.Second}

func invoke(capability string, args map[string]any, out any) error {
	tok, err := accessToken()
	if err != nil {
		return err
	}
	if args == nil {
		args = map[string]any{}
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "invoke", "arguments": map[string]any{"capability": capability, "args": args}},
	})
	req, _ := http.NewRequest("POST", switchboardURL(), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("switchboard unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		return errors.New("switchboard refused the sign-in — run `work login`")
	}
	var rpc struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpc); err != nil {
		return fmt.Errorf("switchboard: unreadable response (%s)", resp.Status)
	}
	if rpc.Error != nil {
		return fmt.Errorf("switchboard: %s", rpc.Error.Message)
	}
	if len(rpc.Result.Content) == 0 {
		return errors.New("switchboard: empty response")
	}
	text := rpc.Result.Content[0].Text
	if rpc.Result.IsError {
		var e struct {
			Error []string `json:"error"`
		}
		if json.Unmarshal([]byte(text), &e) == nil && len(e.Error) > 0 {
			return errors.New(e.Error[0])
		}
		return errors.New(text)
	}
	return json.Unmarshal([]byte(text), out)
}

type connect struct {
	User string `json:"user"`
	Host string `json:"host"`
}

type box struct {
	Name    string  `json:"name"`
	Connect connect `json:"connect"`
}

type boxState struct {
	Box     string  `json:"box"`
	State   string  `json:"state"`
	Health  string  `json:"health"`
	Connect connect `json:"connect"`
}

func myBoxes() ([]box, error) {
	var r struct {
		Boxes []box `json:"boxes"`
	}
	if err := invoke("boxes.mine", nil, &r); err != nil {
		return nil, err
	}
	return r.Boxes, nil
}

func status(name string) (*boxState, error) {
	var s boxState
	return &s, invoke("boxes.status", map[string]any{"box": name}, &s)
}

func wake(name string) (*boxState, error) {
	var s boxState
	return &s, invoke("boxes.wake", map[string]any{"box": name}, &s)
}
