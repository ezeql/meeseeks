package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// MCP (Model Context Protocol) over Streamable HTTP, stateless, with plain JSON responses.
// Agents get the same three operations as the REST API, through the same validation
// (ExpandRequest) and the same backend, so every change is still a reviewable commit.

var mcpVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}

var errUnknownTool = errors.New("unknown tool")

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any, rpcErr *rpcError) {
	msg := map[string]any{"jsonrpc": "2.0", "id": id} // exactly one of result and error
	if rpcErr != nil {
		msg["error"] = rpcErr
	} else {
		msg["result"] = result
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(msg)
}

// localOrigin guards against DNS rebinding: a browser page may only call /mcp from localhost.
func localOrigin(origin string) bool {
	u, err := url.Parse(origin)
	return err == nil && slices.Contains([]string{"localhost", "127.0.0.1", "::1"}, u.Hostname())
}

func (api *MeeseeksAPI) mcp(w http.ResponseWriter, r *http.Request) {
	if o := r.Header.Get("Origin"); o != "" && !localOrigin(o) {
		http.Error(w, "Forbidden origin", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost { // no server-initiated stream (GET) or sessions (DELETE)
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeRPC(w, nil, nil, &rpcError{-32700, "parse error"})
		return
	}
	if req.ID == nil { // a notification, e.g. notifications/initialized
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		version := mcpVersions[1]
		if slices.Contains(mcpVersions, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		writeRPC(w, req.ID, map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": "meeseeks", "version": "0.1.0"},
			"instructions": "Ephemeral Kubernetes environments, the GitOps way. create_environment and " +
				"delete_environment commit to Git and return the commit URL; ArgoCD applies the change " +
				"asynchronously, so poll list_environments for sync and health status.",
		}, nil)
	case "ping":
		writeRPC(w, req.ID, map[string]any{}, nil)
	case "tools/list":
		writeRPC(w, req.ID, map[string]any{"tools": mcpTools()}, nil)
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeRPC(w, req.ID, nil, &rpcError{-32602, "invalid params"})
			return
		}
		out, err := api.callTool(p.Name, p.Arguments)
		if errors.Is(err, errUnknownTool) {
			writeRPC(w, req.ID, nil, &rpcError{-32602, "unknown tool: " + p.Name})
			return
		}
		// Tool failures (e.g. validation) go back as results, so the agent can read them and retry.
		text, isError := "", err != nil
		if isError {
			text = err.Error()
		} else {
			b, _ := json.MarshalIndent(out, "", "  ")
			text = string(b)
		}
		writeRPC(w, req.ID, map[string]any{
			"content": []map[string]string{{"type": "text", "text": text}},
			"isError": isError,
		}, nil)
	default:
		writeRPC(w, req.ID, nil, &rpcError{-32601, "method not found: " + req.Method})
	}
}

func (api *MeeseeksAPI) callTool(name string, args json.RawMessage) (any, error) {
	switch name {
	case "create_environment":
		var req EnvironmentRequest
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		reqs, err := ExpandRequest(req)
		if err != nil {
			return nil, err
		}
		commit, err := api.envs.Create(reqs)
		if err != nil {
			return nil, err
		}
		return EnvironmentResponse{Status: "committed", Commit: commit, Group: reqs[0].Group, Environments: createdEnvs(reqs)}, nil
	case "list_environments":
		return api.envs.List()
	case "delete_environment":
		var p struct {
			Name string `json:"name"`
		}
		json.Unmarshal(args, &p)
		if err := validateName(p.Name); err != nil { // the name becomes a path in the GitOps repo
			return nil, err
		}
		commit, err := api.envs.Delete(p.Name)
		if err != nil {
			return nil, err
		}
		return map[string]string{"id": p.Name, "status": "deletion committed", "commit": commit}, nil
	}
	return nil, errUnknownTool
}

func mcpTools() []map[string]any {
	names := slices.Sorted(maps.Keys(dependencies))
	var versions []string
	for _, n := range names {
		versions = append(versions, n+" "+strings.Join(dependencies[n].versions, "/"))
	}
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	strMap := map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}

	return []map[string]any{{
		"name": "create_environment",
		"description": "Create an ephemeral environment (or a group of them with matrix) by committing it to the GitOps repo. " +
			"Returns the commit URL; ArgoCD deploys it asynchronously.",
		"inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"name", "branch"},
			"properties": map[string]any{
				"name":     str("Lowercase letters, digits and dashes. With matrix, the group name."),
				"branch":   str("Branch of the base manifests, e.g. main."),
				"env_type": map[string]any{"type": "string", "enum": []string{"dev", "development", "testing", "staging", "prod"}},
				"cpu":      str("CPU request and limit, e.g. 100m."),
				"memory":   str("Memory request and limit, e.g. 128Mi."),
				"replicas": map[string]any{"type": "integer", "minimum": 0, "maximum": 10},
				"dependencies": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": names},
					"description": "Each one injects DATABASE_URL, REDIS_URL or MONGODB_URL into the app."},
				"versions": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"},
					"description": "Version per dependency, e.g. {\"postgresql\": \"17\"}. Available: " + strings.Join(versions, ", ") + "."},
				"matrix": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"description": fmt.Sprintf("Versions to compare, e.g. {\"postgresql\": [\"16\", \"17\"]}: one environment per combination, as a group, in one commit (at most %d).", maxMatrix)},
				"benchmark": map[string]any{"type": "boolean", "description": "Run pgbench in each environment (needs postgresql)."},
				"ttl":       str("How long until it is deleted automatically: a Go duration from 1m to 168h. Default 24h."),
				"env_vars":  strMap,
			},
		},
		"annotations": map[string]any{"destructiveHint": false},
	}, {
		"name":        "list_environments",
		"description": "List environments with their ArgoCD sync and health status, expiry, and pgbench results.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		"annotations": map[string]any{"readOnlyHint": true},
	}, {
		"name":        "delete_environment",
		"description": "Delete an environment, or a whole group by its name, by committing the removal to the GitOps repo.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"name"},
			"properties": map[string]any{"name": str("Environment or group name.")}},
		"annotations": map[string]any{"destructiveHint": true},
	}}
}
