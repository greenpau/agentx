package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/greenpau/agentx/pkg/cli"
	"github.com/greenpau/agentx/pkg/identity"
	"github.com/greenpau/agentx/pkg/mcp"
	"github.com/greenpau/agentx/pkg/permission"
	"github.com/greenpau/agentx/pkg/platform"
	"github.com/greenpau/agentx/pkg/sandbox"
	"github.com/greenpau/agentx/pkg/surface"
	"github.com/greenpau/agentx/pkg/task"
	agenttesting "github.com/greenpau/agentx/pkg/testing"
	"github.com/greenpau/agentx/pkg/tool"
)

type mcpRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpRPCError    `json:"error,omitempty"`
}

const mcpHostConcurrency = 3
const mcpHostQueueDepth = 64

type mcpRequestHandler func(context.Context, mcpRPCRequest) (any, int, string)

// runMCPServer hosts the portable core registry without constructing a model
// client. It reuses the exact validator, permission evaluator, executor, task
// manager, and normalized result contracts used by conversational turns.
func runMCPServer(ctx context.Context, opts cli.Options, workspace string, stdin io.Reader, stdout, stderr io.Writer) (returnErr error) {
	_ = stderr // stdout is protocol-only; future diagnostics are routed here.
	root, err := os.MkdirTemp("", "agentx-mcp-host-")
	if err != nil {
		return err
	}
	rootOwner, err := platform.AcquirePrivateDirectory(root)
	if err != nil {
		return fmt.Errorf("acquire MCP host temporary directory: %w", err)
	}
	root = rootOwner.Path()
	defer func() { returnErr = errors.Join(returnErr, rootOwner.RemoveAll()) }()
	tasks, err := task.Open(filepath.Join(root, "tasks"), task.Options{})
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, tasks.Close()) }()
	results, err := tool.NewResultStore(filepath.Join(root, "results"))
	if err != nil {
		return err
	}
	sandboxRunner := sandbox.Detect(ctx, workspace, os.Environ())
	protectedPaths := []string{resolveEnvFilePath(workspace, opts.EnvFile)}
	registry, err := tool.NewCoreRegistry(tool.CoreOptions{Workspace: workspace, Tasks: tasks, Environment: os.Environ(), Results: results, Sandbox: sandboxRunner, ProtectedPaths: protectedPaths})
	if err != nil {
		return err
	}
	registry, err = registryWithTestingCapability(registry, os.Environ())
	if err != nil {
		return err
	}
	rules, err := permissionRules(opts)
	if err != nil {
		return err
	}
	mode := permission.Mode(opts.PermissionMode)
	if mode == "" {
		mode = permission.ModeDefault
	}
	if opts.DangerouslyBypass {
		mode = permission.ModeBypass
	}
	evaluator, err := permission.NewEvaluator(permission.Config{Workspace: workspace, ProtectedPaths: protectedPaths, Mode: mode, Rules: rules, PromptSuppressed: true, BypassAvailable: opts.DangerouslyBypass})
	if err != nil {
		return err
	}
	executor, err := tool.NewExecutor(tool.ExecutorOptions{Registry: registry, Authorizer: evaluator, ResultStore: results})
	if err != nil {
		return err
	}
	scheduler := tool.NewScheduler(executor, registry, tool.DefaultConcurrency)
	return serveMCPProtocol(ctx, stdin, stdout, func(requestCtx context.Context, request mcpRPCRequest) (any, int, string) {
		return handleMCPRequest(requestCtx, registry, scheduler, request)
	})
}

func registryWithTestingCapability(core *tool.Registry, environment []string) (*tool.Registry, error) {
	descriptors := appendTestingCapability(core.Descriptors(), environment)
	return tool.NewRegistry(descriptors...)
}

func appendTestingCapability(descriptors []tool.Descriptor, environment []string) []tool.Descriptor {
	result := append([]tool.Descriptor(nil), descriptors...)
	return append(result, agenttesting.PermissionDescriptor(environment))
}

// serveMCPProtocol keeps input ownership on one bounded scanner while tool
// calls execute on a three-slot lane. This lets cancellation notifications be
// consumed while a call is running without allowing concurrent writes to
// corrupt the JSON-RPC stream.
func serveMCPProtocol(ctx context.Context, stdin io.Reader, stdout io.Writer, handler mcpRequestHandler) error {
	serverCtx, cancelServer := context.WithCancel(ctx)
	defer cancelServer()
	encoder := surface.NewEncoder(stdout)
	lines, stopInput := scanLinesContext(serverCtx, stdin, surface.MaxNDJSONRecordBytes)
	defer stopInput()

	var workers sync.WaitGroup
	defer func() {
		cancelServer()
		workers.Wait()
	}()
	semaphore := make(chan struct{}, mcpHostConcurrency)
	admission := make(chan struct{}, mcpHostConcurrency+mcpHostQueueDepth)
	pending := make(map[string]context.CancelFunc)
	var pendingMu sync.Mutex
	workerErrors := make(chan error, 1)
	reportWorkerError := func(err error) {
		if err == nil {
			return
		}
		select {
		case workerErrors <- err:
			cancelServer()
		default:
		}
	}
	removePending := func(key string, ownCancel context.CancelFunc) {
		pendingMu.Lock()
		if _, exists := pending[key]; exists {
			delete(pending, key)
		}
		pendingMu.Unlock()
		ownCancel()
	}
	cancelPending := func(key string) {
		pendingMu.Lock()
		requestCancel := pending[key]
		pendingMu.Unlock()
		if requestCancel != nil {
			requestCancel()
		}
	}

	for {
		var item lineResult
		var ok bool
		select {
		case err := <-workerErrors:
			return err
		case <-serverCtx.Done():
			if err := ctx.Err(); err != nil {
				return err
			}
			select {
			case err := <-workerErrors:
				return err
			default:
				return serverCtx.Err()
			}
		case item, ok = <-lines:
			if !ok {
				return nil
			}
		}
		if item.err != nil {
			if errors.Is(item.err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read MCP stdio: %w", item.err)
		}
		line := bytes.TrimSpace([]byte(item.line))
		if len(line) == 0 {
			continue
		}
		var request mcpRPCRequest
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
			if encodeErr := encoder.Encode(mcpRPCResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &mcpRPCError{Code: -32700, Message: "parse error"}}); encodeErr != nil {
				return encodeErr
			}
			continue
		}
		if request.JSONRPC != "2.0" || strings.TrimSpace(request.Method) == "" {
			if len(request.ID) > 0 {
				if err := encodeMCPError(encoder, request.ID, -32600, "invalid request"); err != nil {
					return err
				}
			}
			continue
		}
		// Notifications deliberately have no response, but cancellation is an
		// active control message rather than ignorable progress.
		if len(request.ID) == 0 {
			if request.Method == "notifications/cancelled" {
				if key, valid := cancelledMCPRequestKey(request.Params); valid {
					cancelPending(key)
				}
			}
			continue
		}
		if request.Method == "tools/call" {
			key, valid := mcpRequestKey(request.ID)
			if !valid {
				if err := encodeMCPError(encoder, json.RawMessage("null"), -32600, "invalid request id"); err != nil {
					return err
				}
				continue
			}
			select {
			case admission <- struct{}{}:
			default:
				if err := encodeMCPError(encoder, request.ID, -32000, "MCP host request queue is full"); err != nil {
					return err
				}
				continue
			}
			requestCtx, requestCancel := context.WithCancel(serverCtx)
			pendingMu.Lock()
			_, duplicate := pending[key]
			if !duplicate {
				pending[key] = requestCancel
			}
			pendingMu.Unlock()
			if duplicate {
				<-admission
				requestCancel()
				if err := encodeMCPError(encoder, request.ID, -32600, "duplicate in-flight request id"); err != nil {
					return err
				}
				continue
			}
			workers.Add(1)
			go func(request mcpRPCRequest) {
				defer workers.Done()
				defer func() { <-admission }()
				defer removePending(key, requestCancel)
				select {
				case semaphore <- struct{}{}:
					defer func() { <-semaphore }()
				case <-requestCtx.Done():
					reportWorkerError(encodeMCPError(encoder, request.ID, -32800, "request cancelled"))
					return
				}
				result, code, message := handler(requestCtx, request)
				if requestCtx.Err() != nil {
					reportWorkerError(encodeMCPError(encoder, request.ID, -32800, "request cancelled"))
					return
				}
				if code != 0 {
					reportWorkerError(encodeMCPError(encoder, request.ID, code, message))
					return
				}
				reportWorkerError(encoder.Encode(mcpRPCResponse{JSONRPC: "2.0", ID: append(json.RawMessage(nil), request.ID...), Result: result}))
			}(request)
			continue
		}
		result, code, message := handler(serverCtx, request)
		if code != 0 {
			if err := encodeMCPError(encoder, request.ID, code, message); err != nil {
				return err
			}
			continue
		}
		if err := encoder.Encode(mcpRPCResponse{JSONRPC: "2.0", ID: append(json.RawMessage(nil), request.ID...), Result: result}); err != nil {
			return err
		}
	}
}

func mcpRequestKey(raw json.RawMessage) (string, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return "", false
	}
	switch value.(type) {
	case string, json.Number:
		canonical, err := json.Marshal(value)
		return string(canonical), err == nil
	default:
		return "", false
	}
}

func cancelledMCPRequestKey(raw json.RawMessage) (string, bool) {
	var params struct {
		RequestID json.RawMessage `json:"requestId"`
		Reason    string          `json:"reason,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&params); err != nil || decoder.Decode(&struct{}{}) != io.EOF || len(params.RequestID) == 0 {
		return "", false
	}
	return mcpRequestKey(params.RequestID)
}

func handleMCPRequest(ctx context.Context, registry *tool.Registry, scheduler *tool.Scheduler, request mcpRPCRequest) (any, int, string) {
	switch request.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": mcp.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]string{"name": "agentx-core", "version": ProductVersion()},
			"instructions":    "Core workstation capabilities use AgentX validation and permission policy. Mutations require explicit allow rules or bypass mode because stdio has no approval UI.",
		}, 0, ""
	case "ping":
		return map[string]any{}, 0, ""
	case "tools/list":
		descriptors := registry.Descriptors()
		items := make([]map[string]any, 0, len(descriptors))
		for _, descriptor := range descriptors {
			items = append(items, map[string]any{"name": descriptor.Name, "description": descriptor.Description, "inputSchema": descriptor.InputSchema})
		}
		return map[string]any{"tools": items}, 0, ""
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		decoder := json.NewDecoder(bytes.NewReader(request.Params))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&params); err != nil || decoder.Decode(&struct{}{}) != io.EOF || strings.TrimSpace(params.Name) == "" {
			return nil, -32602, "invalid tools/call parameters"
		}
		if len(params.Arguments) == 0 {
			params.Arguments = json.RawMessage(`{}`)
		}
		id, err := identity.New("tool")
		if err != nil {
			return nil, -32603, "failed to create tool correlation"
		}
		results := scheduler.Execute(ctx, []tool.Request{{ID: string(id), Name: params.Name, Input: params.Arguments}})
		if len(results) != 1 {
			return nil, -32603, "tool runtime did not settle the call"
		}
		settled := results[0]
		return map[string]any{"content": []map[string]string{{"type": "text", "text": settled.Content}}, "isError": settled.IsError}, 0, ""
	default:
		return nil, -32601, "method not found"
	}
}

func encodeMCPError(encoder *surface.Encoder, id json.RawMessage, code int, message string) error {
	return encoder.Encode(mcpRPCResponse{JSONRPC: "2.0", ID: append(json.RawMessage(nil), id...), Error: &mcpRPCError{Code: code, Message: message}})
}
