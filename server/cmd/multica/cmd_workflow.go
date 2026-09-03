package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

const workflowActionSchemaVersion = "1"

type workflowTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

var workflowTools = []workflowTool{
	workflowToolSpec("template.list", "List workflow templates", nil, nil),
	workflowToolSpec("template.get", "Get a workflow template", []string{"template_id"}, map[string]string{"template_id": "string"}),
	workflowToolSpec("template.validate", "Validate a seven-node workflow definition", []string{"definition"}, map[string]string{"definition": "object"}),
	workflowToolSpec("run.start", "Start a workflow Run idempotently", []string{"template_id", "idempotency_key", "title", "description"}, map[string]string{"template_id": "string", "idempotency_key": "string", "title": "string", "description": "string", "project_id": "string", "input": "object"}),
	workflowToolSpec("run.get", "Get a workflow Run", []string{"run_id"}, map[string]string{"run_id": "string"}),
	workflowToolSpec("run.cancel", "Cancel a workflow Run", []string{"run_id"}, map[string]string{"run_id": "string"}),
	workflowToolSpec("run.decide_acceptance", "Accept or reject a workflow acceptance gate", []string{"run_id", "accept"}, map[string]string{"run_id": "string", "accept": "boolean", "reason": "string", "rework_target": "string"}),
}

func workflowToolSpec(name, description string, required []string, props map[string]string) workflowTool {
	properties := map[string]any{"schema_version": map[string]any{"type": "string", "const": workflowActionSchemaVersion}}
	for name, kind := range props {
		properties[name] = map[string]any{"type": kind}
	}
	required = append([]string{"schema_version"}, required...)
	return workflowTool{Name: name, Description: description, InputSchema: map[string]any{
		"type": "object", "properties": properties, "required": required, "additionalProperties": false,
	}}
}

var workflowCmd = &cobra.Command{Use: "workflow", Short: "Operate the PostgreSQL-backed workflow engine"}
var workflowCallCmd = &cobra.Command{Use: "call <action>", Short: "Invoke a Workflow Action Contract v1 action", Args: cobra.ExactArgs(1), RunE: runWorkflowCall}
var workflowMCPCmd = &cobra.Command{Use: "mcp", Short: "Workflow MCP adapter"}
var workflowMCPServeCmd = &cobra.Command{Use: "serve", Short: "Serve Workflow Action Contract v1 over MCP stdio", Args: cobra.NoArgs, RunE: runWorkflowMCPServe}

func init() {
	workflowCallCmd.Flags().String("input-json", `{}`, "Action arguments as JSON")
	workflowCallCmd.Flags().String("output", "json", "Output format (json)")
	workflowMCPCmd.AddCommand(workflowMCPServeCmd)
	workflowCmd.AddCommand(workflowCallCmd, workflowMCPCmd)
}

func runWorkflowCall(cmd *cobra.Command, args []string) error {
	if output, _ := cmd.Flags().GetString("output"); output != "json" {
		return fmt.Errorf("workflow output must be json")
	}
	raw, _ := cmd.Flags().GetString("input-json")
	var input map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&input); err != nil {
		return fmt.Errorf("decode --input-json: %w", err)
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	result, err := executeWorkflowAction(ctx, client, args[0], input)
	if err != nil {
		return err
	}
	return cli.PrintJSON(os.Stdout, result)
}

func executeWorkflowAction(ctx context.Context, client *cli.APIClient, action string, input map[string]any) (json.RawMessage, error) {
	tool, ok := lookupWorkflowTool(action)
	if !ok {
		return nil, workflowContractError{"unknown_action", "unknown workflow action"}
	}
	if err := validateWorkflowActionInput(tool, input); err != nil {
		return nil, err
	}
	var path string
	var body any
	method := http.MethodGet
	switch action {
	case "template.list":
		path = "/api/workflow-templates"
	case "template.get":
		path = "/api/workflow-templates/" + stringArg(input, "template_id")
	case "template.validate":
		method, path, body = http.MethodPost, "/api/workflow-templates/validate", map[string]any{"definition": input["definition"]}
	case "run.start":
		method, path = http.MethodPost, "/api/workflow-templates/"+stringArg(input, "template_id")+"/run"
		restBody := copyWorkflowActionBody(input, "template_id", "schema_version")
		if fields, ok := restBody["input"].(map[string]any); ok {
			delete(restBody, "input")
			for key, value := range fields {
				if _, reserved := restBody[key]; reserved {
					return nil, workflowContractError{"validation_error", "input collides with reserved argument: " + key}
				}
				restBody[key] = value
			}
		}
		body = restBody
	case "run.get":
		path = "/api/workflow-runs/" + stringArg(input, "run_id")
	case "run.cancel":
		method, path, body = http.MethodPost, "/api/workflow-runs/"+stringArg(input, "run_id")+"/cancel", map[string]any{}
	case "run.decide_acceptance":
		method, path = http.MethodPost, "/api/workflow-runs/"+stringArg(input, "run_id")+"/acceptance"
		body = copyWorkflowActionBody(input, "run_id", "schema_version")
	}
	var result json.RawMessage
	var err error
	if method == http.MethodGet {
		err = client.GetJSON(ctx, path, &result)
	} else {
		err = client.PostJSON(ctx, path, body, &result)
	}
	if err != nil {
		return nil, normalizeWorkflowActionError(err)
	}
	return result, nil
}

func lookupWorkflowTool(name string) (workflowTool, bool) {
	for _, tool := range workflowTools {
		if tool.Name == name {
			return tool, true
		}
	}
	return workflowTool{}, false
}

func validateWorkflowActionInput(tool workflowTool, input map[string]any) error {
	props := tool.InputSchema["properties"].(map[string]any)
	for key := range input {
		if _, ok := props[key]; !ok {
			return workflowContractError{"validation_error", "unknown argument: " + key}
		}
	}
	for _, value := range tool.InputSchema["required"].([]string) {
		if _, ok := input[value]; !ok {
			return workflowContractError{"validation_error", "missing argument: " + value}
		}
	}
	if stringArg(input, "schema_version") != workflowActionSchemaVersion {
		return workflowContractError{"unsupported_schema_version", "schema_version must be 1"}
	}
	for key, rawSchema := range props {
		value, ok := input[key]
		if !ok || key == "schema_version" {
			continue
		}
		kind := rawSchema.(map[string]any)["type"]
		valid := false
		switch kind {
		case "string":
			text, isString := value.(string)
			valid = isString && strings.TrimSpace(text) != ""
		case "boolean":
			_, valid = value.(bool)
		case "object":
			_, valid = value.(map[string]any)
		}
		if !valid {
			return workflowContractError{"validation_error", key + " has invalid type or value"}
		}
	}
	return nil
}

func stringArg(input map[string]any, key string) string {
	value, _ := input[key].(string)
	return value
}

func copyWorkflowActionBody(input map[string]any, exclude ...string) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	for _, key := range exclude {
		delete(result, key)
	}
	return result
}

type workflowContractError struct{ Code, Message string }

func (e workflowContractError) Error() string { return e.Code + ": " + e.Message }

func normalizeWorkflowActionError(err error) error {
	var httpErr *cli.HTTPError
	if !errors.As(err, &httpErr) {
		return workflowContractError{"transport_error", err.Error()}
	}
	code := "server_error"
	switch httpErr.StatusCode {
	case http.StatusBadRequest:
		code = "validation_error"
	case http.StatusUnauthorized:
		code = "unauthorized"
	case http.StatusForbidden:
		code = "forbidden"
	case http.StatusNotFound:
		code = "not_found"
	case http.StatusConflict:
		code = "conflict"
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		code = "retryable"
	}
	var apiBody struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(httpErr.Body), &apiBody) == nil && apiBody.Code != "" {
		code = apiBody.Code
		if apiBody.Error != "" {
			return workflowContractError{code, apiBody.Error}
		}
	}
	return workflowContractError{code, http.StatusText(httpErr.StatusCode)}
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}
type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}
type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func runWorkflowMCPServe(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	return serveWorkflowMCP(cmd.Context(), client, os.Stdin, os.Stdout)
}

func serveWorkflowMCP(ctx context.Context, client *cli.APIClient, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		var request mcpRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			if err := encoder.Encode(mcpResponse{JSONRPC: "2.0", Error: &mcpError{Code: -32700, Message: "Parse error"}}); err != nil {
				return err
			}
			continue
		}
		response := handleWorkflowMCP(ctx, client, request)
		if len(request.ID) > 0 {
			if err := encoder.Encode(response); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func handleWorkflowMCP(ctx context.Context, client *cli.APIClient, request mcpRequest) mcpResponse {
	response := mcpResponse{JSONRPC: "2.0", ID: request.ID}
	switch request.Method {
	case "initialize":
		response.Result = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "multica-workflow", "version": workflowActionSchemaVersion}}
	case "notifications/initialized":
	case "tools/list":
		response.Result = map[string]any{"tools": workflowTools}
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			response.Error = &mcpError{Code: -32602, Message: "Invalid params"}
			break
		}
		result, err := executeWorkflowAction(ctx, client, params.Name, params.Arguments)
		if err != nil {
			var contractErr workflowContractError
			if !errors.As(err, &contractErr) {
				contractErr = workflowContractError{"internal_error", "workflow action failed"}
			}
			response.Result = map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": contractErr.Message}}, "structuredContent": map[string]any{"schema_version": workflowActionSchemaVersion, "error": map[string]any{"code": contractErr.Code, "message": contractErr.Message}}}
			break
		}
		response.Result = map[string]any{"content": []any{map[string]any{"type": "text", "text": string(result)}}, "structuredContent": json.RawMessage(result)}
	default:
		response.Error = &mcpError{Code: -32601, Message: "Method not found"}
	}
	return response
}
