package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCP(t *testing.T) {
	api := &MeeseeksAPI{envs: &MockEnvironments{}}
	srv := httptest.NewServer(localOnly(http.HandlerFunc(api.mcp)))
	defer srv.Close()

	post := func(origin, body string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest("POST", srv.URL, strings.NewReader(body))
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	call := func(method, params string) map[string]any {
		t.Helper()
		code, out := post("", `{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":`+params+`}`)
		if code != 200 {
			t.Fatalf("%s: status %d", method, code)
		}
		return out
	}
	tool := func(args string) (string, bool) {
		t.Helper()
		res := call("tools/call", args)["result"].(map[string]any)
		return res["content"].([]any)[0].(map[string]any)["text"].(string), res["isError"].(bool)
	}

	hello := call("initialize", `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}`)
	if r := hello["result"].(map[string]any); r["protocolVersion"] != "2025-06-18" || r["capabilities"].(map[string]any)["tools"] == nil {
		t.Fatalf("initialize: %v", hello)
	}
	if code, _ := post("", `{"jsonrpc":"2.0","method":"notifications/initialized"}`); code != http.StatusAccepted {
		t.Fatalf("notification: status %d, want 202", code)
	}
	if tools := call("tools/list", `{}`)["result"].(map[string]any)["tools"].([]any); len(tools) != 3 {
		t.Fatalf("tools/list: %v", tools)
	}

	if text, isErr := tool(`{"name":"create_environment","arguments":{"name":"Bad_Name","branch":"main"}}`); !isErr || !strings.Contains(text, "invalid name") {
		t.Fatalf("invalid create must come back as a tool error: %q %v", text, isErr)
	}
	if text, isErr := tool(`{"name":"create_environment","arguments":{"name":"ok","branch":"main","ttl":"1h"}}`); isErr || !strings.Contains(text, `"commit"`) {
		t.Fatalf("create: %q %v", text, isErr)
	}
	if text, isErr := tool(`{"name":"delete_environment","arguments":{"name":"../x"}}`); !isErr {
		t.Fatalf("delete must validate the name: %q", text)
	}
	if out := call("tools/call", `{"name":"nope","arguments":{}}`); out["error"] == nil {
		t.Fatalf("unknown tool: %v", out)
	}
	if out := call("resources/list", `{}`); out["error"].(map[string]any)["code"] != float64(-32601) || out["result"] != nil {
		t.Fatalf("unknown method: %v", out)
	}

	if code, _ := post("https://evil.example", `{"jsonrpc":"2.0","id":1,"method":"ping"}`); code != http.StatusForbidden {
		t.Fatalf("foreign origin: status %d, want 403", code)
	}
	if code, _ := post("http://localhost:3000", `{"jsonrpc":"2.0","id":1,"method":"ping"}`); code != 200 {
		t.Fatalf("localhost origin: status %d", code)
	}
	if code, _ := post("", `{"jsonrpc":"2.0","id":7,"result":{}}`); code != http.StatusAccepted {
		t.Fatalf("a response from the client: status %d, want 202", code)
	}

	rest := httptest.NewServer(localOnly(http.HandlerFunc(api.createEnvironment)))
	defer rest.Close()
	req, _ := http.NewRequest("POST", rest.URL, strings.NewReader(`{"name":"x","branch":"main"}`))
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Content-Type", "text/plain") // a "simple" request: no CORS preflight
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site POST to the REST API must be rejected: %v %v", resp, err)
	}

	if resp, _ := http.Get(srv.URL); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET: status %d, want 405", resp.StatusCode)
	}

	rec := httptest.NewRecorder()
	api.serveHome(rec, httptest.NewRequest("GET", "/.well-known/oauth-protected-resource", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown path: status %d, want 404", rec.Code)
	}
}
