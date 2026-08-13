package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/greenpau/agentx/pkg/attachment"
	"github.com/greenpau/agentx/pkg/cli"
	"github.com/greenpau/agentx/pkg/config"
	"github.com/greenpau/agentx/pkg/engine"
	"github.com/greenpau/agentx/pkg/extensions"
	"github.com/greenpau/agentx/pkg/identity"
	"github.com/greenpau/agentx/pkg/model"
	"github.com/greenpau/agentx/pkg/observability"
	"github.com/greenpau/agentx/pkg/permission"
	"github.com/greenpau/agentx/pkg/platform"
	"github.com/greenpau/agentx/pkg/prompt"
	"github.com/greenpau/agentx/pkg/protocol"
	"github.com/greenpau/agentx/pkg/redact"
	"github.com/greenpau/agentx/pkg/sandbox"
	"github.com/greenpau/agentx/pkg/sessionlock"
	"github.com/greenpau/agentx/pkg/signals"
	"github.com/greenpau/agentx/pkg/surface"
	"github.com/greenpau/agentx/pkg/task"
	"github.com/greenpau/agentx/pkg/tool"
	"github.com/greenpau/agentx/pkg/transcript"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type runtimeSession struct {
	engine             *engine.Engine
	attachments        *attachment.Store
	inputMedia         model.InputMediaCapability
	inputMediaEnabled  bool
	transcript         *transcript.Store
	tasks              *task.Manager
	registry           *tool.Registry
	skills             extensions.Snapshot
	config             config.Runtime
	workspace          string
	sessionDir         string
	sessionOwner       *platform.OwnedDirectory
	temporary          bool
	lock               *sessionlock.Lock
	services           runtimeServices
	permissionMode     string
	shutdown           *signals.ProcessShutdown
	sanitize           func(string) string
	credentials        *redact.Set
	logger             *zap.Logger
	uploadMu           sync.Mutex
	uploadPrompts      map[attachment.UploadID]string
	uploadReserved     map[attachment.UploadID]int64
	attachmentPrompts  map[attachment.ID]string
	attachmentReserved map[attachment.ID]int64
	promptCounts       map[string]int
	promptBytes        map[string]int64
	uploadTerminalSent map[attachment.UploadID]bool
	closeOnce          sync.Once
	closeErr           error
}

type buildOptions struct {
	CLI       cli.Options
	Workspace string
	Sink      engine.EventSink
	Approver  permission.Approver
	Ask       tool.AskFunc
	Stderr    io.Writer
}

// modelHTTPClientContextKey is an internal dependency-injection seam for
// invocation-scoped transports. Production entrypoints leave it unset and use
// the model package's hardened default client. Package integration tests use it
// to trust only their ephemeral TLS server certificate without weakening
// endpoint validation or mutating http.DefaultTransport process-wide.
type modelHTTPClientContextKey struct{}

func modelHTTPClientFromContext(ctx context.Context) *http.Client {
	if ctx == nil {
		return nil
	}
	client, _ := ctx.Value(modelHTTPClientContextKey{}).(*http.Client)
	return client
}

func buildSession(ctx context.Context, options buildOptions) (_ *runtimeSession, resultErr error) {
	ctx, home, err := applicationHomeForContext(ctx)
	if err != nil {
		return nil, err
	}
	runtimeConfig, err := home.loadRuntimeConfig(os.Environ(), config.Overrides{Provider: options.CLI.Provider, Model: options.CLI.Model, ReasoningEffort: options.CLI.Effort})
	if err != nil {
		if !runtimeConfig.CredentialSanitizer().Empty() {
			err = redactOperationalError(err, runtimeConfig.Redact)
		}
		return nil, err
	}
	sanitize := runtimeConfig.Redact
	credentialSet := runtimeConfig.CredentialSanitizer()
	defer func() { resultErr = redactOperationalError(resultErr, sanitize) }()

	providerTranscriptValidator := runtimeTranscriptJSONValidator(
		runtimeConfig.CredentialSanitizer(),
	)
	layout, sourceSnapshot, err := resolveSessionLayout(
		ctx, options.Workspace, options.CLI, providerTranscriptValidator,
	)
	if err != nil {
		return nil, err
	}
	retainLayout := false
	defer func() {
		if retainLayout {
			return
		}
		if layout.lock != nil {
			if err := layout.lock.Close(); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("release session ownership after startup failure: %w", err))
			}
		}
		if layout.temporary {
			if err := layout.removeTemporary(); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("remove temporary session after startup failure: %w", err))
			}
		}
	}()
	if err := layout.verify(); err != nil {
		return nil, err
	}
	warnWorkspaceTrust := !options.CLI.TrustWorkspace && !options.CLI.Bare &&
		options.Stderr != nil && hasProjectRuntimeContent(options.Workspace)
	runtimeExt, externalDescriptors, err := discoverExtensions(ctx, options.Workspace, options.CLI, os.Environ())
	if err != nil {
		return nil, err
	}
	credentialSet, err = runtimeCredentialSanitizerSet(runtimeConfig.CredentialSanitizer(), runtimeExt.mcpCredentialConfigs)
	if err != nil {
		closeErr := runtimeExt.mcp.Close()
		if closeErr != nil {
			// No complete provider union exists on this branch, so a
			// third-party close diagnostic cannot be safely projected.
			closeErr = errors.New("MCP providers also failed to close after credential composition failed")
		}
		return nil, errors.Join(err, closeErr)
	}
	// The complete configured MCP union is already broad enough to protect
	// status and cleanup errors if hook-snapshot composition fails next.
	sanitize = credentialSet.Apply
	credentialSet, err = runtimeExt.runner.FreezeCredentialSanitizer(runtimeExt.hooks, credentialSet)
	if err != nil {
		return nil, errors.Join(err, runtimeExt.mcp.Close())
	}
	sanitize = credentialSet.Apply
	if sourceSnapshot != nil {
		// Validate the initial owned snapshot only after the complete provider,
		// MCP, and frozen-hook credential union can protect a diagnostic containing
		// the recorded selector. This still precedes model-client construction and
		// every replay, fork copy/publication, or model-provider request. Forks are
		// reloaded and revalidated under the source lock below before copying.
		if err := validateSnapshotProviderBinding(*sourceSnapshot, runtimeConfig); err != nil {
			return nil, errors.Join(err, runtimeExt.mcp.Close())
		}
	}
	logger := observability.NewLogger(observability.LoggerConfig{
		Writer:      options.Stderr,
		Debug:       options.CLI.Debug,
		Credentials: credentialSet,
	})
	defer func() {
		if resultErr == nil {
			return
		}
		safeErr := redactOperationalError(resultErr, sanitize)
		observability.Log(logger, zapcore.ErrorLevel, "session construction failed",
			zap.String("session_id", string(layout.sessionID)),
			zap.String("error_message", safeOperationalErrorText(safeErr)),
		)
	}()
	observability.Log(logger, zapcore.DebugLevel, "session construction started",
		zap.String("session_id", string(layout.sessionID)),
		zap.String("provider", runtimeConfig.SelectedProvider.ID),
		zap.String("model", runtimeConfig.Azure.ModelName),
		zap.String("surface", surfaceName(options.CLI)),
		zap.Bool("persistent", !options.CLI.NoSessionPersistence),
		zap.Bool("workspace_trusted", options.CLI.TrustWorkspace),
	)
	if warnWorkspaceTrust {
		if err := writeWorkspaceTrustWarning(options.Stderr, credentialSet); err != nil {
			return nil, errors.Join(err, runtimeExt.mcp.Close())
		}
	}
	attachmentOwner, err := layout.sessionOwner.EnsurePrivateChild("attachments")
	if err != nil {
		return nil, errors.Join(errors.New("prepare session attachment store"), runtimeExt.mcp.Close())
	}
	attachmentStore, err := attachment.OpenStore(attachmentOwner.Path(), attachment.Options{})
	if err != nil {
		return nil, errors.Join(errors.New("open session attachment store"), runtimeExt.mcp.Close())
	}
	retainAttachmentStore := false
	defer func() {
		if !retainAttachmentStore {
			resultErr = errors.Join(resultErr, attachmentStore.Close())
		}
	}()
	provider, err := model.NewAzureClient(runtimeConfig.Azure, model.AzureOptions{
		HTTPClient:          modelHTTPClientFromContext(ctx),
		CredentialSanitizer: credentialSet,
		OnRetry: func(info model.RetryInfo) {
			observability.Log(logger, zapcore.WarnLevel, "model request retry",
				zap.String("session_id", string(layout.sessionID)),
				zap.String("model", runtimeConfig.Azure.ModelName),
				zap.Int("attempt", info.Attempt),
				zap.Int("max_attempts", info.MaxAttempts),
				zap.Duration("delay", info.Delay),
				zap.String("reason", info.String()),
			)
		},
	})
	if err != nil {
		return nil, errors.Join(err, runtimeExt.mcp.Close())
	}
	inputMedia, inputMediaEnabled := provider.InputMediaCapability()
	validateJSON := credentialJSONValidator(credentialSet)
	validateTranscriptJSON := runtimeTranscriptJSONValidator(credentialSet)
	var store *transcript.Store
	if !options.CLI.NoSessionPersistence {
		metadata := protocol.SessionMetadata{
			WorkingDirectory: options.Workspace, Entrypoint: "main.go", Surface: surfaceName(options.CLI),
			ProductVersion: ProductVersion(), ProviderID: runtimeConfig.SelectedProvider.ID,
			ProviderType: runtimeConfig.SelectedProvider.Type, Model: runtimeConfig.SelectedProvider.Model,
			ProviderBinding: runtimeConfig.ProviderBinding(),
		}
		if options.CLI.ForkSession && sourceSnapshot != nil {
			metadata.ParentSessionID = sourceSnapshot.SessionID
		}
		store, err = transcript.Open(ctx, transcript.Config{
			Path: layout.transcriptPath, SessionID: layout.sessionID,
			SessionMetadata: metadata, SyncOnAppend: true, ValidateRecord: validateTranscriptJSON,
		})
		if err != nil {
			return nil, errors.Join(err, runtimeExt.mcp.Close())
		}
		if err := layout.verify(); err != nil {
			_ = store.Close()
			return nil, errors.Join(err, runtimeExt.mcp.Close())
		}
	}
	if err := layout.verify(); err != nil {
		if store != nil {
			_ = store.Close()
		}
		return nil, errors.Join(err, runtimeExt.mcp.Close())
	}
	if sourceSnapshot != nil {
		var validated transcript.Snapshot
		switch {
		case store != nil && !options.CLI.ForkSession:
			// Store.Open validated the authoritative file with the complete
			// Azure+MCP+hook union. Reload from that owned store instead of
			// restoring the earlier layout-resolution snapshot.
			validated, err = store.Load(ctx)
		default:
			// Fork and nonpersistent resume read a different source file from
			// the destination. Reacquire its ownership lock and validate every
			// physical record plus the recovered projection with the final
			// session union before copying or restoring it.
			validated, err = loadValidatedSourceSnapshot(
				ctx,
				layout,
				sourceSnapshot.SessionID,
				validateTranscriptJSON,
			)
		}
		if err != nil {
			if store != nil {
				_ = store.Close()
			}
			return nil, errors.Join(err, runtimeExt.mcp.Close())
		}
		if len(validated.Events) == 0 {
			if store != nil {
				_ = store.Close()
			}
			return nil, errors.Join(
				fmt.Errorf("session %s has no durable history", sourceSnapshot.SessionID),
				runtimeExt.mcp.Close(),
			)
		}
		if err := validateSnapshotProviderBinding(validated, runtimeConfig); err != nil {
			if store != nil {
				_ = store.Close()
			}
			return nil, errors.Join(err, runtimeExt.mcp.Close())
		}
		sourceSnapshot = &validated
	}
	tasks, err := task.Open(filepath.Join(layout.sessionDir, "task-runtime"), task.Options{
		SanitizeRecord: credentialSet.Redact,
		NewOutputSanitizer: func() task.OutputSanitizer {
			return redact.NewSetStream(credentialSet)
		},
		ValidateState: validateJSON,
	})
	if err != nil {
		if store != nil {
			_ = store.Close()
		}
		return nil, errors.Join(err, runtimeExt.mcp.Close())
	}
	if err := layout.verify(); err != nil {
		_ = tasks.Close()
		if store != nil {
			_ = store.Close()
		}
		return nil, errors.Join(err, runtimeExt.mcp.Close())
	}
	resultStore, err := tool.NewResultStoreWithValidator(filepath.Join(layout.sessionDir, "tool-results"), validateJSON)
	if err != nil {
		_ = tasks.Close()
		if store != nil {
			_ = store.Close()
		}
		return nil, errors.Join(err, runtimeExt.mcp.Close())
	}
	protectedPaths := home.protectedPaths(runtimeExt.protectedPaths)
	closeExtensionFailure := func(cause error) error {
		return errors.Join(cause, runtimeExt.mcp.Close(), closeBuildFailure(tasks, store, nil))
	}
	if err := writeOutputStyleSelectionDiagnostics(options.Stderr, runtimeExt.selection, credentialSet); err != nil {
		return nil, closeExtensionFailure(err)
	}
	skills := runtimeExt.skills
	skillScope := &skillPermissionScope{}

	sandboxRunner := sandbox.Detect(ctx, options.Workspace, os.Environ())
	coreRegistry, err := tool.NewCoreRegistry(tool.CoreOptions{Workspace: options.Workspace, Tasks: tasks, Ask: options.Ask, Environment: os.Environ(), Results: resultStore, Sandbox: sandboxRunner, ProtectedPaths: protectedPaths})
	if err != nil {
		return nil, closeExtensionFailure(err)
	}
	descriptors := coreRegistry.Descriptors()
	descriptors = appendTestingCapability(descriptors, os.Environ())
	for _, skill := range skills.Skills {
		if !skill.DisableModelInvocation && skill.Availability.Usable() {
			descriptors = append(descriptors, skillDescriptor(skills, skillScope, runtimeConfig.Azure.ModelName, runtimeConfig.Azure.ReasoningEffort))
			break
		}
	}
	descriptors = append(descriptors, externalDescriptors...)
	registry, err := tool.NewRegistry(descriptors...)
	if err != nil {
		return nil, closeExtensionFailure(err)
	}
	rules, err := permissionRules(options.CLI)
	if err != nil {
		return nil, closeExtensionFailure(err)
	}
	mode := permission.Mode(options.CLI.PermissionMode)
	if mode == "" {
		mode = permission.ModeDefault
	}
	if options.CLI.DangerouslyBypass {
		mode = permission.ModeBypass
	}
	hookAdapter := extensionToolHook{runner: runtimeExt.runner, snapshot: runtimeExt.hooks, sessionID: string(layout.sessionID), workspace: options.Workspace, permissionMode: string(mode)}
	if store != nil {
		hookAdapter.transcriptPath = layout.transcriptPath
	}
	runtimeExt.runner.ConditionMatcher = hookConditionMatcher(registry)
	approver := options.Approver
	if runtimeExt.hooks.HasHook(extensions.HookPermissionRequest) {
		approver = hookAdapter.permissionApprover(approver)
	}
	evaluator, err := permission.NewEvaluator(permission.Config{Workspace: options.Workspace, ProtectedPaths: protectedPaths, Mode: mode, Rules: rules, Approver: approver, PromptSuppressed: approver == nil, BypassAvailable: options.CLI.DangerouslyBypass})
	if err != nil {
		return nil, closeExtensionFailure(err)
	}
	var toolHooks []tool.Hook
	if len(runtimeExt.hooks.Hooks) > 0 {
		toolHooks = append(toolHooks, hookAdapter)
	}
	executor, err := tool.NewExecutor(tool.ExecutorOptions{
		Registry: registry, Authorizer: scopedAuthorizer{
			base: applicationHomeAuthorizer{home: home, base: evaluator}, scope: skillScope,
		},
		Hooks: toolHooks, ResultStore: resultStore,
		CredentialSanitizer: credentialSet,
	})
	if err != nil {
		return nil, closeExtensionFailure(err)
	}
	capabilities := &capabilityAdapter{registry: registry, scheduler: tool.NewScheduler(executor, registry, tool.DefaultConcurrency), scope: skillScope, credentials: credentialSet}

	toolNames := make([]string, 0)
	for _, descriptor := range registry.Descriptors() {
		toolNames = append(toolNames, descriptor.Name)
	}
	skillSummaries := make([]string, 0, len(skills.Skills))
	for _, skill := range skills.Skills {
		if !skill.DisableModelInvocation && skill.Availability.Usable() {
			skillSummaries = append(skillSummaries, skill.Summary())
		}
	}
	override, appendPrompt, err := loadPromptFlags(options.CLI)
	if err != nil {
		return nil, closeExtensionFailure(err)
	}
	memoryStore, err := openRuntimeMemory(layout, options.CLI.Bare, func(value string) bool {
		return sanitize(value) != value
	})
	if err != nil {
		return nil, closeExtensionFailure(err)
	}
	memorySection := ""
	if strings.TrimSpace(override) == "" {
		memorySection, err = memoryPrompt(memoryStore)
		if err != nil {
			return nil, closeExtensionFailure(err)
		}
	}
	appendPrompt = composeAppendPrompt(override, appendPrompt, runtimeExt.selection.PromptSection(), memorySection)
	// Repository commands are deliberately excluded until this workspace has
	// an explicit trust decision. Even observational Git porcelain can execute
	// repository-configured helpers such as core.fsmonitor.
	sections, err := prompt.NewBuilder().Build(ctx, prompt.Options{CWD: options.Workspace, Model: runtimeConfig.Azure.ModelName, Bare: options.CLI.Bare || !options.CLI.TrustWorkspace, Override: override, Append: appendPrompt, SkillSummaries: skillSummaries, ToolNames: toolNames, IncludeGit: false})
	if err != nil {
		return nil, closeExtensionFailure(err)
	}
	var system strings.Builder
	for index, section := range sections {
		if index > 0 {
			system.WriteString("\n\n")
		}
		fmt.Fprintf(&system, "<%s>\n%s\n</%s>", section.Name, section.Text, section.Name)
	}
	var semanticStore engine.Store
	if store != nil {
		semanticStore = store
	}
	query, err := engine.New(engine.Config{
		SessionID: layout.sessionID, Model: runtimeConfig.Azure.ModelName,
		ReasoningEffort:           runtimeConfig.Azure.ReasoningEffort,
		SupportedReasoningEfforts: append([]string(nil), runtimeConfig.Azure.SupportedReasoningEfforts...),
		Instructions:              sanitize(system.String()), MaxTurns: options.CLI.MaxTurns,
		MaxOutputTokens: engine.DefaultMaxOutputTokens, InputContextTokens: engine.DefaultInputContextTokens,
		Provider: provider, Capabilities: capabilities, Transcript: semanticStore, Sink: options.Sink,
		Sanitize: sanitize, CredentialSanitizer: credentialSet, Attachments: attachmentStore, Logger: logger,
	})
	if err != nil {
		return nil, closeExtensionFailure(err)
	}
	attachmentOwners := make(map[attachment.ID]string)
	if sourceSnapshot != nil {
		snapshot := *sourceSnapshot
		if options.CLI.ForkSession {
			if store == nil {
				return nil, closeExtensionFailure(errors.New("fork requires persistence"))
			}
			// Blob selection and transcript copying must use the identical
			// active-branch projection. Copying media from discarded branches
			// could otherwise exhaust limits or produce unreferenced fork data.
			snapshot = snapshot.ActiveConversation()
			if err := copyForkAttachments(ctx, layout, snapshot, attachmentStore); err != nil {
				return nil, closeExtensionFailure(err)
			}
			if err := copyFork(ctx, store, snapshot, layout.sessionID); err != nil {
				return nil, closeExtensionFailure(err)
			}
			snapshot, err = store.Load(ctx)
			if err != nil {
				return nil, closeExtensionFailure(err)
			}
		}
		if err := query.Restore(snapshot); err != nil {
			if errors.Is(err, engine.ErrUnsupportedRestoredReasoningEffort) {
				// Restore errors are engine-owned and already credential-safe, but the
				// engine's sealed error type is intentionally opaque to the general
				// operational-error projector. Re-project this package-owned class as a
				// constant diagnostic so resume/fork failures remain actionable without
				// inspecting or exposing any durable value.
				err = errors.New("session reasoning effort is unsupported by the selected provider; select a provider that supports every recorded effort or start a new session")
			}
			return nil, closeExtensionFailure(err)
		}
		attachmentOwners, err = attachmentPromptOwners(snapshot.Events)
		if err != nil {
			return nil, closeExtensionFailure(err)
		}
		if _, err := attachmentStore.Collect(ctx, attachmentIDs(snapshot.Events)); err != nil {
			return nil, closeExtensionFailure(errors.New("reconcile durable attachment storage"))
		}
		if options.CLI.ForkSession {
			// Publication is the final fork commit point. A provider/reasoning restore
			// failure or attachment reconciliation failure must leave the marker in
			// place so inventory cannot expose a destination that never started.
			if err := completeForkPublication(layout); err != nil {
				return nil, closeExtensionFailure(err)
			}
		}
	} else {
		if _, err := attachmentStore.Collect(ctx, nil); err != nil {
			return nil, closeExtensionFailure(errors.New("reconcile new attachment storage"))
		}
	}
	services, err := buildRuntimeServices(options, runtimeExt, query, runtimeConfig.SelectedProvider, func() int { return len(tasks.List()) }, skillScope, memoryStore, sandboxRunner)
	if err != nil {
		return nil, closeExtensionFailure(err)
	}
	result := &runtimeSession{
		engine: query, attachments: attachmentStore, inputMedia: inputMedia,
		inputMediaEnabled: inputMediaEnabled, transcript: store, tasks: tasks, registry: registry,
		skills: skills, config: runtimeConfig, workspace: options.Workspace,
		sessionDir: layout.sessionDir, sessionOwner: layout.sessionOwner,
		temporary: layout.temporary, lock: layout.lock, services: services,
		permissionMode: string(mode), shutdown: signals.ProcessShutdownFromContext(ctx),
		sanitize: sanitize, credentials: credentialSet, logger: logger,
		uploadPrompts:      make(map[attachment.UploadID]string),
		uploadReserved:     make(map[attachment.UploadID]int64),
		attachmentPrompts:  attachmentOwners,
		attachmentReserved: make(map[attachment.ID]int64),
		promptCounts:       make(map[string]int),
		promptBytes:        make(map[string]int64),
		uploadTerminalSent: make(map[attachment.UploadID]bool),
	}
	if runtimeExt.runner != nil && len(runtimeExt.hooks.Hooks) > 0 {
		source := "startup"
		if options.CLI.Resume != "" || options.CLI.Continue || options.CLI.ForkSession {
			source = "resume"
		}
		aggregate, hookErr := runtimeExt.runner.Dispatch(ctx, runtimeExt.hooks, extensions.HookInput{SessionID: string(layout.sessionID), TranscriptPath: result.transcriptPath(), CWD: options.Workspace, PermissionMode: string(mode), Event: extensions.HookSessionStart, Fields: map[string]any{"source": source, "provider": runtimeConfig.SelectedProvider.ID, "provider_type": runtimeConfig.SelectedProvider.Type, "model": runtimeConfig.Azure.ModelName}})
		if hookErr != nil || !aggregate.Continue {
			return nil, errors.Join(fallbackError(hookErr, aggregate.Reason, "session-start hook stopped startup"), result.Close())
		}
	}
	observability.Log(logger, zapcore.DebugLevel, "session construction completed",
		zap.String("session_id", string(layout.sessionID)),
		zap.Int("skill_count", len(skills.Skills)),
		zap.Int("tool_count", len(registry.Descriptors())),
		zap.String("permission_mode", string(mode)),
	)
	retainLayout = true
	retainAttachmentStore = true
	return result, nil
}

func validateSnapshotProviderBinding(snapshot transcript.Snapshot, runtimeConfig config.Runtime) error {
	want := runtimeConfig.SelectedProvider
	wantBinding := runtimeConfig.ProviderBinding()
	for _, event := range snapshot.Events {
		metadata := event.Session
		if !sessionMetadataHasProviderBinding(metadata) {
			return errors.New("session predates provider binding metadata and cannot be resumed safely; start a new session")
		}
		if err := protocol.ValidateSessionProviderBinding(metadata); err != nil {
			return errors.New("session has incomplete or invalid provider binding metadata and cannot be resumed safely; start a new session")
		}
		if metadata.ProviderID != want.ID {
			return fmt.Errorf(
				"session is bound to provider %q; restart with --provider %s",
				metadata.ProviderID, metadata.ProviderID,
			)
		}
		if metadata.ProviderType != want.Type || metadata.Model != want.Model {
			return fmt.Errorf("session provider %q no longer has its recorded type and model", want.ID)
		}
		if metadata.ProviderBinding != wantBinding {
			return fmt.Errorf("session provider %q was changed in auth.json; restore its endpoint, deployment, model, type, and API version before resuming", want.ID)
		}
	}
	return nil
}

func sessionMetadataHasProviderBinding(metadata protocol.SessionMetadata) bool {
	return metadata.ProviderID != "" || metadata.ProviderType != "" ||
		metadata.ProviderBinding != "" || metadata.Model != ""
}

// runtimeTranscriptJSONValidator composes complete-frame credential inspection
// with a provider-binding check that runs before transcript recovery is allowed
// to isolate schema-invalid records. The validator accepts both one physical
// Event and the final recovered Snapshot projection used by transcript.ReadFile.
func runtimeTranscriptJSONValidator(credentials *redact.Set) func([]byte) error {
	validateCredentials := credentialJSONValidator(credentials)
	return func(raw []byte) error {
		if err := validateCredentials(raw); err != nil {
			return err
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil || object == nil {
			return errors.New("transcript provider binding could not be safely inspected")
		}
		validateMetadata := func(metadata protocol.SessionMetadata) error {
			if !sessionMetadataHasProviderBinding(metadata) {
				// The protocol deliberately keeps an all-empty legacy tuple parseable.
				// It cannot be isolated as schema-invalid, so snapshot policy below can
				// return the actionable "start a new session" diagnostic.
				return nil
			}
			if err := protocol.ValidateSessionProviderBinding(metadata); err != nil {
				return errors.New("transcript record has invalid provider binding metadata")
			}
			return nil
		}
		if _, isEvent := object["kind"]; isEvent {
			var event struct {
				Session protocol.SessionMetadata `json:"session"`
			}
			if err := json.Unmarshal(raw, &event); err != nil {
				return errors.New("transcript provider binding could not be safely inspected")
			}
			return validateMetadata(event.Session)
		}
		if _, isSnapshot := object["events"]; isSnapshot {
			var snapshot struct {
				Events []struct {
					Session protocol.SessionMetadata `json:"session"`
				} `json:"events"`
			}
			if err := json.Unmarshal(raw, &snapshot); err != nil {
				return errors.New("transcript provider binding could not be safely inspected")
			}
			for _, event := range snapshot.Events {
				if err := validateMetadata(event.Session); err != nil {
					return err
				}
			}
			return nil
		}
		return errors.New("transcript provider binding projection is unsupported")
	}
}

func (r *runtimeSession) Close() error {
	r.closeOnce.Do(func() {
		observability.Log(r.logger, zapcore.DebugLevel, "session shutdown started",
			zap.String("session_id", string(r.engine.SessionID())),
		)
		r.closeErr = redactOperationalError(r.close(), r.sanitize)
		fields := []zap.Field{
			zap.String("session_id", string(r.engine.SessionID())),
			zap.Bool("success", r.closeErr == nil),
		}
		if r.closeErr != nil {
			fields = append(fields, zap.String("error", safeOperationalErrorText(r.closeErr)))
		}
		observability.Log(r.logger, zapcore.DebugLevel, "session shutdown completed", fields...)
	})
	return r.closeErr
}

type redactedOperationalError struct {
	message string
	classes map[error]struct{}
	usage   *cli.UsageError
}

func (e *redactedOperationalError) Error() string { return e.message }
func (e *redactedOperationalError) Is(target error) bool {
	return operationalErrorClassified(e.classes, target)
}
func (e *redactedOperationalError) As(target any) bool {
	usage, ok := target.(**cli.UsageError)
	if !ok || usage == nil || e.usage == nil {
		return false
	}
	*usage = e.usage
	return true
}
func (e *redactedOperationalError) Format(state fmt.State, verb rune) {
	_, _ = fmt.Fprint(state, e.message)
}

func redactOperationalError(cause error, redact func(string) string) error {
	if cause == nil {
		return nil
	}
	classes, usage := inspectOperationalError(cause)
	message := TerminalSafeText(safeOperationalErrorText(cause))
	message = applyOperationalRedactor(redact, message)
	var publicUsage *cli.UsageError
	if usage != nil {
		publicUsage = &cli.UsageError{Message: message}
	}
	return &redactedOperationalError{message: message, classes: classes, usage: publicUsage}
}

func credentialJSONValidator(credentials *redact.Set) func([]byte) error {
	return func(raw []byte) error {
		if credentials == nil || credentials.Empty() {
			return nil
		}
		reflected, err := credentials.JSONContains(raw)
		if err != nil {
			return errors.New("encoded JSON could not be safely inspected")
		}
		if reflected {
			return errors.New("encoded JSON reflected configured credential material")
		}
		return nil
	}
}

func (r *runtimeSession) close() error {
	var errs []error
	shutdown := platform.NewShutdownManager(platform.ShutdownConfig{})
	registerCritical := func(name string, close func() error) {
		if _, err := shutdown.Register(platform.StageCritical, name, func(context.Context, platform.ShutdownRequest) error { return close() }); err != nil {
			errs = append(errs, fmt.Errorf("register %s cleanup: %w", name, err))
		}
	}
	if r.services.extensions.mcp != nil {
		registerCritical("MCP providers", r.services.extensions.mcp.Close)
	}
	if r.transcript != nil || r.attachments != nil {
		registerCritical("semantic storage", func() error {
			var cleanupErr error
			if r.attachments != nil && r.transcript != nil {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), platform.DefaultCriticalCleanupTimeout)
				snapshot, loadErr := r.transcript.Load(cleanupCtx)
				if loadErr == nil {
					_, cleanupErr = r.attachments.Collect(cleanupCtx, attachmentIDs(snapshot.Events))
				} else {
					cleanupErr = errors.New("load attachment references for cleanup")
				}
				cancel()
			}
			if r.attachments != nil {
				cleanupErr = errors.Join(cleanupErr, r.attachments.Close())
			}
			if r.transcript != nil {
				cleanupErr = errors.Join(cleanupErr, r.transcript.Close())
			}
			return cleanupErr
		})
	}
	if r.tasks != nil {
		if _, err := shutdown.Register(platform.StageCritical, "task runtime", func(ctx context.Context, _ platform.ShutdownRequest) error {
			return r.tasks.CloseContext(ctx)
		}); err != nil {
			errs = append(errs, fmt.Errorf("register task runtime cleanup: %w", err))
		}
	}
	if r.services.extensions.runner != nil && len(r.services.extensions.hooks.Hooks) > 0 {
		if _, err := shutdown.Register(platform.StageHook, "SessionEnd", func(ctx context.Context, request platform.ShutdownRequest) error {
			_, hookErr := r.services.extensions.runner.Dispatch(ctx, r.services.extensions.hooks, extensions.HookInput{SessionID: string(r.engine.SessionID()), TranscriptPath: r.transcriptPath(), CWD: r.workspace, PermissionMode: r.permissionMode, Event: extensions.HookSessionEnd, Fields: map[string]any{"reason": sessionEndReason(request.Reason)}})
			return hookErr
		}); err != nil {
			errs = append(errs, fmt.Errorf("register session-end hook: %w", err))
		}
	}
	// The manager is the production coordinator, not merely a test contract:
	// it snapshots independent critical owners, runs them concurrently before
	// hooks, and bounds each phase. The process-level failsafe remains the final
	// guard if a callback ignores cancellation.
	request := platform.ShutdownRequest{Reason: "other"}
	if latched, ok := r.shutdown.Snapshot(); ok {
		request = latched
	}
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), shutdownFailsafeBudget())
	result, shutdownErr := shutdown.Shutdown(shutdownContext, request)
	cancelShutdown()
	resourcesQuiesced := shutdownErr == nil && len(errs) == 0
	if shutdownErr != nil {
		errs = append(errs, shutdownErr)
	}
	for _, phaseError := range result.Errors {
		if phaseError.Stage == platform.StageCritical {
			resourcesQuiesced = false
		}
		errs = append(errs, fmt.Errorf("%s %s: %s", phaseError.Stage, phaseError.Name, phaseError.Message))
	}
	if resourcesQuiesced && r.lock != nil {
		if err := r.lock.Close(); err != nil {
			errs = append(errs, fmt.Errorf("release session lock: %w", err))
			resourcesQuiesced = false
		}
	}
	if r.temporary && resourcesQuiesced {
		if r.sessionOwner == nil {
			errs = append(errs, errors.New("temporary session has no owned-directory identity"))
		} else if err := r.sessionOwner.RemoveAll(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func shutdownFailsafeBudget() time.Duration {
	// Root arms six seconds; keep the awaited manager below that boundary while
	// allowing every configured phase to reach its own timeout.
	return platform.DefaultCriticalCleanupTimeout + platform.DefaultHookTimeout + platform.DefaultObserverTimeout + time.Second
}

func sessionEndReason(processReason string) string {
	switch processReason {
	case "clear", "resume", "logout", "prompt_input_exit", "other", "bypass_permissions_disabled":
		return processReason
	default:
		// The hook wire contract has no signal-specific enum. Platform cleanup
		// still receives the exact sigint/sigterm/sighup reason and exit code;
		// SessionEnd projects those process reasons to its defined catch-all.
		return "other"
	}
}

func hasProjectRuntimeContent(workspace string) bool {
	for _, path := range []string{filepath.Join(workspace, "AGENTS.md"), filepath.Join(workspace, ".agentx")} {
		if _, err := os.Lstat(path); err == nil {
			return true
		}
	}
	return false
}

func writeWorkspaceTrustWarning(writer io.Writer, credentials *redact.Set) error {
	const record = "warning: project instructions and executable extensions are disabled; pass --trust-workspace to enable them\n"
	if err := writeTerminalRecord(writer, credentials, record); err != nil {
		return fmt.Errorf("write workspace trust warning: %w", err)
	}
	return nil
}

func fallbackError(err error, reason, defaultReason string) error {
	if err != nil {
		return err
	}
	if strings.TrimSpace(reason) == "" {
		reason = defaultReason
	}
	return errors.New(reason)
}

func closeBuildFailure(tasks *task.Manager, store *transcript.Store, cause error) error {
	var closeErrors []error
	if tasks != nil {
		if err := tasks.Close(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close task runtime after startup failure: %w", err))
		}
	}
	if store != nil {
		if err := store.Close(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close transcript after startup failure: %w", err))
		}
	}
	return errors.Join(append([]error{cause}, closeErrors...)...)
}

type sessionLayout struct {
	sessionID                  protocol.SessionID
	sessionDir, transcriptPath string
	sourceTranscriptPath       string
	sourceRevision             string
	sessionOwner, sourceOwner  *platform.OwnedDirectory
	sessionManager             *transcript.SessionManager
	memoryParent               *platform.OwnedDirectory
	memoryDir                  string
	memoryDisabled             bool
	temporary                  bool
	lock                       *sessionlock.Lock
}

const incompleteForkMarker = ".fork-incomplete"

func (layout sessionLayout) verify() error {
	if err := layout.verifySessionDirectory(); err != nil {
		return err
	}
	if layout.sessionManager == nil {
		return errors.New("session-management authority is unavailable")
	}
	if layout.temporary || layout.memoryDisabled {
		if layout.memoryParent != nil || layout.memoryDir != "" {
			return errors.New("memory-disabled session unexpectedly owns persistent project memory")
		}
		return nil
	}
	if layout.memoryParent == nil {
		return errors.New("project memory parent identity is unavailable")
	}
	if err := layout.memoryParent.Verify(); err != nil {
		return fmt.Errorf("verify project memory parent identity: %w", err)
	}
	if filepath.Clean(layout.memoryDir) != filepath.Join(layout.memoryParent.Path(), "memory") {
		return errors.New("project memory path does not match its owned parent directory")
	}
	return nil
}

func (layout sessionLayout) verifySessionDirectory() error {
	if layout.sessionOwner == nil {
		return errors.New("session directory identity is unavailable")
	}
	if err := layout.sessionOwner.Verify(); err != nil {
		return fmt.Errorf("verify session directory identity: %w", err)
	}
	if filepath.Clean(layout.sessionDir) != layout.sessionOwner.Path() || filepath.Clean(layout.transcriptPath) != filepath.Join(layout.sessionOwner.Path(), "transcript.jsonl") {
		return errors.New("session layout path does not match its owned directory")
	}
	if layout.sourceOwner != nil {
		if filepath.Clean(layout.sourceTranscriptPath) != filepath.Join(layout.sourceOwner.Path(), "transcript.jsonl") {
			return errors.New("source session path does not match its owned directory")
		}
		if layout.sourceRevision == "" {
			return errors.New("source session revision is unavailable")
		}
	} else if layout.sourceTranscriptPath != "" || layout.sourceRevision != "" {
		return errors.New("source session evidence lacks its owned directory")
	}
	return nil
}

func (layout sessionLayout) removeTemporary() error {
	if !layout.temporary {
		return nil
	}
	if layout.sessionOwner == nil {
		return errors.New("temporary session directory identity is unavailable")
	}
	return layout.sessionOwner.RemoveAll()
}

func resolveSessionLayout(ctx context.Context, workspace string, opts cli.Options, validateRecord func([]byte) error) (layout sessionLayout, source *transcript.Snapshot, resultErr error) {
	var heldLock *sessionlock.Lock
	var temporaryOwner *platform.OwnedDirectory
	defer func() {
		if resultErr == nil {
			return
		}
		if heldLock != nil {
			resultErr = errors.Join(resultErr, heldLock.Close())
		}
		if temporaryOwner != nil {
			resultErr = errors.Join(resultErr, temporaryOwner.RemoveAll())
		}
	}()

	_, home, err := applicationHomeForContext(ctx)
	if err != nil {
		return sessionLayout{}, nil, err
	}
	sessionManager, err := transcript.NewSessionManager(home.sessions, workspace)
	if err != nil {
		return sessionLayout{}, nil, err
	}
	// This bounded standalone profile keys both the session partition and the
	// separate project-memory tree by the selected absolute workspace. It does
	// not claim canonical-main-repository identity or linked-worktree sharing.
	projectKey := sessionManager.WorkspacePartitionKey()
	sourceID := ""
	sourceRevision := ""
	if opts.Resume != "" {
		sourceID = opts.Resume
	}
	var projectDir *platform.OwnedDirectory
	requiresStoredSessions := !opts.NoSessionPersistence || sourceID != "" || opts.Continue
	if requiresStoredSessions {
		if opts.NoSessionPersistence {
			var exists bool
			projectDir, exists, err = sessionManager.OpenWorkspacePartition()
			if err == nil && !exists {
				err = transcript.ErrNoPreviousSession
			}
		} else {
			projectDir, err = sessionManager.EnsureWorkspacePartition()
		}
		if err != nil {
			return sessionLayout{}, nil, fmt.Errorf("open workspace session directory: %w", err)
		}
	}
	if opts.Continue {
		var latest transcript.SessionInventoryItem
		latest, err = sessionManager.LatestItem(ctx)
		if err != nil {
			return sessionLayout{}, nil, err
		}
		sourceID = latest.SessionID
		sourceRevision = latest.Revision
	}
	if sourceID != "" {
		if !transcript.ValidSessionID(sourceID) {
			return sessionLayout{}, nil, errors.New("resume session identifier is invalid")
		}
	}
	var memoryParent *platform.OwnedDirectory
	memoryDir := ""
	if !opts.NoSessionPersistence && !opts.Bare {
		memoryParent, err = home.root.EnsurePrivateChild("projects", projectKey)
		if err != nil {
			return sessionLayout{}, nil, fmt.Errorf("create project data directory: %w", err)
		}
		memoryDir = filepath.Join(memoryParent.Path(), "memory")
	}
	var sourceOwner *platform.OwnedDirectory
	if sourceID != "" {
		state, stateErr := sessionManager.State(ctx, sourceID)
		if stateErr != nil {
			return sessionLayout{}, nil, fmt.Errorf("inspect source session %s: %w", sourceID, stateErr)
		}
		switch {
		case state.DeletionPending:
			return sessionLayout{}, nil, fmt.Errorf("%w: session %s", transcript.ErrSessionDeletionStaged, sourceID)
		case state.IncompleteFork:
			return sessionLayout{}, nil, fmt.Errorf("session %s is an incomplete fork and cannot be resumed", sourceID)
		case !state.Resumable:
			return sessionLayout{}, nil, fmt.Errorf("session %s has no durable history", sourceID)
		}
		if sourceRevision != "" && state.Revision != sourceRevision {
			return sessionLayout{}, nil, fmt.Errorf("session %s changed after latest-session selection", sourceID)
		}
		sourceRevision = state.Revision
		sourceOwner, err = projectDir.OpenPrivateChild(sourceID)
		if err != nil {
			return sessionLayout{}, nil, fmt.Errorf("open source session %s: %w", sourceID, err)
		}
	}
	sessionID := protocol.SessionID(sourceID)
	if sessionID == "" || opts.ForkSession {
		if opts.SessionID != "" {
			if !transcript.ValidSessionID(opts.SessionID) {
				return sessionLayout{}, nil, errors.New("session identifier is invalid")
			}
			sessionID = protocol.SessionID(opts.SessionID)
		} else {
			generated, generateErr := identity.NewSession()
			if generateErr != nil {
				return sessionLayout{}, nil, generateErr
			}
			sessionID = protocol.SessionID(generated)
		}
	}
	creatingPersistentDestination := !opts.NoSessionPersistence && (sourceID == "" || opts.ForkSession)
	var destinationState transcript.NativeSessionState
	if creatingPersistentDestination {
		destinationState, err = sessionManager.State(ctx, string(sessionID))
		if err != nil {
			return sessionLayout{}, nil, fmt.Errorf("inspect requested session state: %w", err)
		}
		if destinationState.DeletionPending {
			return sessionLayout{}, nil, fmt.Errorf("%w: session %s", transcript.ErrSessionDeletionStaged, sessionID)
		}
		if destinationState.Exists {
			// Generated IDs and fork destinations must be genuinely fresh. An
			// explicitly named ordinary session may reacquire a pre-created empty
			// directory; the session lock below remains the contention authority.
			if opts.ForkSession || opts.SessionID == "" {
				return sessionLayout{}, nil, fmt.Errorf("session %s already exists; choose another identifier or use --resume", sessionID)
			}
			if destinationState.IncompleteFork {
				return sessionLayout{}, nil, fmt.Errorf("session %s is an incomplete fork and cannot be reused", sessionID)
			}
			if destinationState.Resumable {
				return sessionLayout{}, nil, fmt.Errorf("session %s already exists; use --resume", sessionID)
			}
		}
	}
	createdPersistentDestination := false
	if opts.NoSessionPersistence {
		temporary, makeErr := os.MkdirTemp("", "agentx-session-")
		if makeErr != nil {
			return sessionLayout{}, nil, makeErr
		}
		var ownErr error
		temporaryOwner, ownErr = platform.AcquirePrivateDirectory(temporary)
		if ownErr != nil {
			return sessionLayout{}, nil, fmt.Errorf("acquire temporary session directory: %w", ownErr)
		}
		layout = sessionLayout{sessionID: sessionID, sessionDir: temporaryOwner.Path(), transcriptPath: filepath.Join(temporaryOwner.Path(), "transcript.jsonl"), sessionOwner: temporaryOwner, sessionManager: sessionManager, temporary: true}
	} else {
		sessionOwner := sourceOwner
		if sessionOwner == nil || opts.ForkSession {
			if destinationState.Exists {
				sessionOwner, err = projectDir.OpenPrivateChild(string(sessionID))
			} else {
				sessionOwner, err = projectDir.CreatePrivateChild(string(sessionID))
				createdPersistentDestination = err == nil
			}
			if err != nil {
				return sessionLayout{}, nil, err
			}
		}
		layout = sessionLayout{
			sessionID: sessionID, sessionDir: sessionOwner.Path(),
			transcriptPath: filepath.Join(sessionOwner.Path(), "transcript.jsonl"),
			sessionOwner:   sessionOwner, sessionManager: sessionManager,
			memoryParent: memoryParent, memoryDir: memoryDir,
			memoryDisabled: opts.Bare,
		}
	}
	if err := layout.verify(); err != nil {
		return sessionLayout{}, nil, err
	}
	heldLock, err = sessionlock.Acquire(ctx, filepath.Join(layout.sessionDir, ".session.lock"))
	if err != nil {
		return sessionLayout{}, nil, fmt.Errorf("acquire session %s ownership: %w", sessionID, err)
	}
	layout.lock = heldLock
	if err := layout.verify(); err != nil {
		return sessionLayout{}, nil, err
	}
	if !opts.NoSessionPersistence {
		state, stateErr := sessionManager.State(ctx, string(sessionID))
		if stateErr == nil {
			switch {
			case creatingPersistentDestination:
				stateErr = validateNewSessionDestinationState(state, sessionID)
			case sourceID != "" && !opts.ForkSession:
				stateErr = validateSourceSessionGeneration(state, sourceID, sourceRevision)
			case state.DeletionPending:
				stateErr = fmt.Errorf("%w: session %s", transcript.ErrSessionDeletionStaged, sessionID)
			}
		}
		if stateErr != nil {
			closeErr := heldLock.Close()
			heldLock = nil
			layout.lock = nil
			var removeErr error
			if closeErr == nil && createdPersistentDestination {
				removeErr = layout.sessionOwner.RemoveAll()
			}
			return sessionLayout{}, nil, errors.Join(stateErr, closeErr, removeErr)
		}
	}
	if opts.ForkSession && !opts.NoSessionPersistence {
		if err := beginForkPublication(layout); err != nil {
			return sessionLayout{}, nil, err
		}
	}

	if sourceID != "" {
		if sourceOwner == nil {
			return sessionLayout{}, nil, errors.New("source session directory identity is unavailable")
		}
		if err := sourceOwner.Verify(); err != nil {
			return sessionLayout{}, nil, fmt.Errorf("verify source session %s: %w", sourceID, err)
		}
		sourcePath := filepath.Join(sourceOwner.Path(), "transcript.jsonl")
		layout.sourceOwner = sourceOwner
		layout.sourceTranscriptPath = sourcePath
		layout.sourceRevision = sourceRevision
		var sourceLock *sessionlock.Lock
		if sourceOwner.Path() != layout.sessionDir {
			sourceLock, err = sessionlock.Acquire(ctx, filepath.Join(sourceOwner.Path(), ".session.lock"))
			if err != nil {
				return sessionLayout{}, nil, fmt.Errorf("acquire source session %s for fork: %w", sourceID, err)
			}
			if err := sourceOwner.Verify(); err != nil {
				_ = sourceLock.Close()
				return sessionLayout{}, nil, fmt.Errorf("verify source session %s after locking: %w", sourceID, err)
			}
		}
		state, stateErr := sessionManager.State(ctx, sourceID)
		if stateErr == nil {
			stateErr = validateSourceSessionGeneration(state, sourceID, sourceRevision)
		}
		if stateErr != nil {
			if sourceLock != nil {
				stateErr = errors.Join(stateErr, sourceLock.Close())
			}
			return sessionLayout{}, nil, stateErr
		}
		if sourceLock != nil {
			if err := sourceLock.Verify(); err != nil {
				_ = sourceLock.Close()
				return sessionLayout{}, nil, fmt.Errorf("verify source session %s lock: %w", sourceID, err)
			}
		} else if err := heldLock.Verify(); err != nil {
			return sessionLayout{}, nil, fmt.Errorf("verify resumed session %s lock: %w", sourceID, err)
		}
		snapshot, readErr := transcript.ReadFile(ctx, sourcePath, transcript.ReadOptions{
			ExpectedSessionID: protocol.SessionID(sourceID), ValidateRecord: validateRecord,
		})
		readErr = errors.Join(readErr, sourceOwner.Verify())
		var sourceUnlockErr error
		if sourceLock != nil {
			sourceUnlockErr = sourceLock.Close()
		}
		if readErr != nil || sourceUnlockErr != nil {
			return sessionLayout{}, nil, errors.Join(readErr, sourceUnlockErr)
		}
		if len(snapshot.Events) == 0 {
			return sessionLayout{}, nil, fmt.Errorf("session %s has no durable history", sourceID)
		}
		source = &snapshot
	}
	return layout, source, nil
}

func loadValidatedSourceSnapshot(
	ctx context.Context,
	layout sessionLayout,
	expected protocol.SessionID,
	validate func([]byte) error,
) (_ transcript.Snapshot, resultErr error) {
	if expected == "" || layout.sourceOwner == nil || layout.sourceTranscriptPath == "" {
		return transcript.Snapshot{}, errors.New("source session identity is unavailable")
	}
	if layout.sourceOwner.Path() == layout.sessionDir {
		return transcript.Snapshot{}, errors.New("owned destination store must load a non-fork resume")
	}
	if err := layout.sourceOwner.Verify(); err != nil {
		return transcript.Snapshot{}, fmt.Errorf("verify source session before validated load: %w", err)
	}
	sourceLock, err := sessionlock.Acquire(ctx, filepath.Join(layout.sourceOwner.Path(), ".session.lock"))
	if err != nil {
		return transcript.Snapshot{}, fmt.Errorf("acquire source session for validated load: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, sourceLock.Close())
	}()
	if err := layout.sourceOwner.Verify(); err != nil {
		return transcript.Snapshot{}, fmt.Errorf("verify source session after locking: %w", err)
	}
	state, err := layout.sessionManager.State(ctx, string(expected))
	if err != nil {
		return transcript.Snapshot{}, fmt.Errorf("inspect source session after locking: %w", err)
	}
	if err := validateSourceSessionGeneration(state, string(expected), layout.sourceRevision); err != nil {
		return transcript.Snapshot{}, err
	}
	if err := sourceLock.Verify(); err != nil {
		return transcript.Snapshot{}, fmt.Errorf("verify source session lock: %w", err)
	}
	snapshot, err := transcript.ReadFile(ctx, layout.sourceTranscriptPath, transcript.ReadOptions{
		ExpectedSessionID: expected,
		ValidateRecord:    validate,
	})
	if err != nil {
		return transcript.Snapshot{}, fmt.Errorf("load source session with complete credential validation: %w", err)
	}
	if err := layout.sourceOwner.Verify(); err != nil {
		return transcript.Snapshot{}, fmt.Errorf("verify source session after validated load: %w", err)
	}
	return snapshot, nil
}

func validateNewSessionDestinationState(
	state transcript.NativeSessionState,
	sessionID protocol.SessionID,
) error {
	if state.DeletionPending {
		return fmt.Errorf("%w: session %s", transcript.ErrSessionDeletionStaged, sessionID)
	}
	if !state.Exists || state.IncompleteFork || state.Resumable {
		return fmt.Errorf("new session destination %s changed before ownership was acquired", sessionID)
	}
	return nil
}

func validateSourceSessionGeneration(
	state transcript.NativeSessionState,
	sessionID string,
	revision string,
) error {
	switch {
	case state.DeletionPending:
		return fmt.Errorf("%w: source session %s", transcript.ErrSessionDeletionStaged, sessionID)
	case state.IncompleteFork:
		return fmt.Errorf("source session %s became an incomplete fork", sessionID)
	case !state.Resumable:
		return fmt.Errorf("source session %s is no longer resumable", sessionID)
	case revision == "" || state.Revision != revision:
		return fmt.Errorf("source session %s changed before its ownership lock was acquired", sessionID)
	default:
		return nil
	}
}

func beginForkPublication(layout sessionLayout) error {
	if err := layout.verifySessionDirectory(); err != nil {
		return err
	}
	path := filepath.Join(layout.sessionDir, incompleteForkMarker)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("mark fork publication incomplete: %w", err)
	}
	if _, err = file.WriteString("incomplete\n"); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return fmt.Errorf("persist incomplete fork marker: %w", err)
	}
	if err := syncSessionDirectory(layout.sessionDir); err != nil {
		return fmt.Errorf("sync incomplete fork marker: %w", err)
	}
	if err := layout.verifySessionDirectory(); err != nil {
		return err
	}
	return nil
}

func completeForkPublication(layout sessionLayout) error {
	if err := layout.verifySessionDirectory(); err != nil {
		return err
	}
	path := filepath.Join(layout.sessionDir, incompleteForkMarker)
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect incomplete fork marker: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("incomplete fork marker identity is unsafe")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("publish completed fork: %w", err)
	}
	if err := syncSessionDirectory(layout.sessionDir); err != nil {
		return fmt.Errorf("sync completed fork publication: %w", err)
	}
	return layout.verifySessionDirectory()
}

func permissionRules(opts cli.Options) ([]permission.Rule, error) {
	var rules []permission.Rule
	for _, raw := range opts.DisallowedTools {
		rule, err := permission.ParseRule(raw, permission.EffectDeny, "cliArg", false)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	for _, raw := range opts.AllowedTools {
		rule, err := permission.ParseRule(raw, permission.EffectAllow, "cliArg", false)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, nil
}
func loadPromptFlags(opts cli.Options) (string, string, error) {
	override := opts.SystemPrompt
	appendPrompt := opts.AppendSystemPrompt
	var err error
	if opts.SystemPromptFile != "" {
		override, err = readPromptFile(opts.SystemPromptFile)
		if err != nil {
			return "", "", err
		}
	}
	if strings.TrimSpace(override) != "" {
		return override, "", nil
	}
	if opts.AppendSystemPromptFile != "" {
		appendPrompt, err = readPromptFile(opts.AppendSystemPromptFile)
		if err != nil {
			return "", "", err
		}
	}
	return override, appendPrompt, nil
}

func composeAppendPrompt(override, appendPrompt string, generated ...string) string {
	if strings.TrimSpace(override) != "" {
		return ""
	}
	result := appendPrompt
	for _, extra := range generated {
		if strings.TrimSpace(extra) == "" {
			continue
		}
		if strings.TrimSpace(result) != "" {
			result += "\n\n"
		}
		result += extra
	}
	return result
}
func readPromptFile(path string) (string, error) {
	data, err := extensions.ReadRegularFile(path, 1<<20)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
func surfaceName(opts cli.Options) string {
	if !opts.Print {
		return "interactive"
	}
	return string(opts.OutputFormat)
}

func copyFork(ctx context.Context, store *transcript.Store, source transcript.Snapshot, destination protocol.SessionID) error {
	source = source.ActiveConversation()
	idMap := make(map[protocol.EventID]protocol.EventID, len(source.Events))
	events := make([]protocol.Event, 0, len(source.Events))
	for _, original := range source.Events {
		if original.Persistence == protocol.PersistenceEphemeral {
			continue
		}
		event := original
		newID, err := protocol.NewEventID()
		if err != nil {
			return err
		}
		idMap[original.ID] = newID
		event.ID = newID
		event.SessionID = destination
		event.Sequence = 0
		event.Persistence = protocol.PersistenceDurable
		logicalParent := original.ID
		event.LogicalParentID = &logicalParent
		if original.ParentID != nil {
			if mapped, ok := idMap[*original.ParentID]; ok {
				event.ParentID = &mapped
			} else {
				event.ParentID = nil
			}
		}
		events = append(events, event)
	}
	return store.AppendBatch(ctx, events)
}

func attachmentIDs(events []protocol.Event) []attachment.ID {
	seen := make(map[attachment.ID]struct{})
	var ids []attachment.ID
	for _, event := range events {
		if event.Kind != protocol.EventKindMessage || event.Message == nil {
			continue
		}
		for _, block := range event.Message.Content {
			if block.Type != protocol.ContentAttachment {
				continue
			}
			if _, exists := seen[block.AttachmentID]; exists {
				continue
			}
			seen[block.AttachmentID] = struct{}{}
			ids = append(ids, block.AttachmentID)
		}
	}
	return ids
}

func attachmentPromptOwners(events []protocol.Event) (map[attachment.ID]string, error) {
	owners := make(map[attachment.ID]string)
	for _, event := range events {
		if event.Kind != protocol.EventKindMessage ||
			event.Message == nil ||
			event.Message.Role != protocol.RoleUser {
			continue
		}
		hasAttachments := false
		for _, block := range event.Message.Content {
			if block.Type == protocol.ContentAttachment {
				hasAttachments = true
				break
			}
		}
		if !hasAttachments {
			continue
		}
		if err := surface.ValidateUUID(event.Message.PromptID); err != nil {
			return nil, errors.New("durable attachment message has no canonical prompt UUID")
		}
		for _, block := range event.Message.Content {
			if block.Type != protocol.ContentAttachment {
				continue
			}
			promptID := event.Message.PromptID
			if existing, found := owners[block.AttachmentID]; found && existing != promptID {
				return nil, errors.New("durable attachment identity has conflicting prompt ownership")
			}
			owners[block.AttachmentID] = promptID
		}
	}
	return owners, nil
}

func copyForkAttachments(
	ctx context.Context,
	layout sessionLayout,
	source transcript.Snapshot,
	destination *attachment.Store,
) (resultErr error) {
	ids := attachmentIDs(source.Events)
	if len(ids) == 0 {
		return nil
	}
	if destination == nil || layout.sourceOwner == nil || layout.sourceRevision == "" {
		return errors.New("fork attachment storage identity is unavailable")
	}
	if err := layout.sourceOwner.Verify(); err != nil {
		return errors.New("verify source attachment session")
	}
	sourceLock, err := sessionlock.Acquire(ctx, filepath.Join(layout.sourceOwner.Path(), ".session.lock"))
	if err != nil {
		return errors.New("acquire source session for attachment fork")
	}
	defer func() {
		resultErr = errors.Join(resultErr, sourceLock.Close())
	}()
	if err := layout.sourceOwner.Verify(); err != nil {
		return errors.New("verify source attachment session after locking")
	}
	state, err := layout.sessionManager.State(ctx, string(source.SessionID))
	if err != nil {
		return errors.New("inspect source session for attachment fork")
	}
	if err := validateSourceSessionGeneration(state, string(source.SessionID), layout.sourceRevision); err != nil {
		return err
	}
	sourceAttachmentOwner, err := layout.sourceOwner.OpenPrivateChild("attachments")
	if err != nil {
		return errors.New("source session attachment store is unavailable")
	}
	sourceStore, err := attachment.OpenStore(sourceAttachmentOwner.Path(), attachment.Options{})
	if err != nil {
		return errors.New("open source session attachment store")
	}
	defer func() {
		resultErr = errors.Join(resultErr, sourceStore.Close())
	}()
	if err := sourceStore.CopyTo(ctx, destination, ids); err != nil {
		return errors.New("copy fork attachment content")
	}
	return nil
}
