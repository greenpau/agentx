# frozen_string_literal: true

# Fail-closed production-Go inventory and behavioral-owner classifier for the
# ledger builder. The architecture audit intentionally owns a separate live
# filesystem enumeration and shares only this authority classification.
module SourceInventory
  SCOPE = {
    'implementation-startup-settings' => 'entrypoint, configuration, migration, trust, policy, or startup registry behavior',
    'implementation-state-context' => 'bootstrap/session state, prompt/context, identifiers, or shared semantic data',
    'implementation-query-model' => 'recursive query, message normalization, model request/stream, retry, or limit behavior',
    'implementation-tool-protocol' => 'generic tool schema, orchestration, hook/permission integration, progress, or result behavior',
    'implementation-tool-catalog' => 'concrete built-in capability behavior or registry filtering',
    'implementation-permissions-sandbox' => 'permission rules, shell/path analysis, protected resources, or isolation behavior',
    'implementation-task-runtime' => 'identity-bearing task, scheduled work, output storage, cancellation, notification, or crash behavior',
    'implementation-interactive-repl' => 'interactive presentation, prompt editing, keybinding, history, dialog, or UI state behavior',
    'implementation-headless-sdk' => 'CLI/headless/SDK framing, control, event projection, or structured I/O behavior',
    'implementation-commands-input' => 'command discovery/invocation, user-input normalization, suggestions, or local routing',
    'implementation-skills-output' => 'runtime skill discovery/invocation, output style, or skill prompt behavior',
    'implementation-plugins-hooks' => 'plugin/marketplace lifecycle, extension hooks, or attributed registry behavior',
    'implementation-mcp-lsp' => 'MCP/LSP configuration, transport, discovery, authentication, protocol, or tool adaptation',
    'implementation-transcript-recovery' => 'session persistence, message graph, resume/fork/rewind, file history, or attribution',
    'implementation-memory-compaction' => 'memory, summary, token pressure, projection, compaction, or derived assistance',
    'implementation-remote-bridge' => 'remote/bridge/direct-connect/teleport transport, relay, replay, or placement behavior',
    'implementation-auth-network' => 'credential, provider, TLS, proxy, API client, or network boundary behavior',
    'implementation-platform-lifecycle' => 'portable filesystem/process/OS integration, cleanup, updater mechanism, or shared primitive',
    'implementation-observability' => 'usage, cost, diagnostics, logging, telemetry, feature evaluation, or operational metrics'
  }.freeze

  EXACT_OWNER = {
    'main.go' => 'implementation-platform-lifecycle',

    'pkg/app/app.go' => 'implementation-headless-sdk',
    'pkg/app/application_home.go' => 'implementation-startup-settings',
    'pkg/app/capabilities.go' => 'implementation-tool-protocol',
    'pkg/app/commands.go' => 'implementation-commands-input',
    'pkg/app/error_projection.go' => 'implementation-headless-sdk',
    'pkg/app/extensions_runtime.go' => 'implementation-plugins-hooks',
    'pkg/app/input.go' => 'implementation-commands-input',
    'pkg/app/interactions.go' => 'implementation-headless-sdk',
    'pkg/app/mcp_server.go' => 'implementation-mcp-lsp',
    'pkg/app/renderers.go' => 'implementation-headless-sdk',
    'pkg/app/runtime.go' => 'implementation-transcript-recovery',
    'pkg/app/runtime_services.go' => 'implementation-observability',
    'pkg/app/session_management.go' => 'implementation-headless-sdk',
    'pkg/app/session_directory_sync_other.go' => 'implementation-transcript-recovery',
    'pkg/app/session_directory_sync_unix.go' => 'implementation-transcript-recovery',
    'pkg/app/structured.go' => 'implementation-headless-sdk',
    'pkg/app/terminal_text.go' => 'implementation-interactive-repl',

    'pkg/config/config.go' => 'implementation-auth-network',
    'pkg/config/authfile.go' => 'implementation-auth-network',
    'pkg/config/credential_file_links_other.go' => 'implementation-auth-network',
    'pkg/config/credential_file_links_unix.go' => 'implementation-auth-network',
    'pkg/config/credential_file_links_windows.go' => 'implementation-auth-network',
    'pkg/config/credential_file_open_other.go' => 'implementation-auth-network',
    'pkg/config/credential_file_open_unix.go' => 'implementation-auth-network',
    'pkg/config/credential_file_permissions_other.go' => 'implementation-auth-network',
    'pkg/config/credential_file_permissions_unix.go' => 'implementation-auth-network',
    'pkg/config/credential_file_permissions_windows.go' => 'implementation-auth-network',
    'pkg/config/environment.go' => 'implementation-auth-network',

    'pkg/platform/owned_directory_permissions_other.go' => 'implementation-platform-lifecycle',
    'pkg/platform/owned_directory_permissions_unix.go' => 'implementation-platform-lifecycle',
    'pkg/platform/owned_directory_permissions_windows.go' => 'implementation-platform-lifecycle',

    'pkg/extensions/hooks.go' => 'implementation-plugins-hooks',
    'pkg/extensions/hooks_exec.go' => 'implementation-plugins-hooks',
    'pkg/extensions/hooks_process_unix.go' => 'implementation-plugins-hooks',
    'pkg/extensions/hooks_process_windows.go' => 'implementation-plugins-hooks',
    'pkg/extensions/output_styles.go' => 'implementation-skills-output',
    'pkg/extensions/paths.go' => 'implementation-plugins-hooks',
    'pkg/extensions/plugins.go' => 'implementation-plugins-hooks',
    'pkg/extensions/safe_file.go' => 'implementation-plugins-hooks',
    'pkg/extensions/safe_file_other.go' => 'implementation-plugins-hooks',
    'pkg/extensions/safe_file_unix.go' => 'implementation-plugins-hooks',
    'pkg/extensions/safe_file_windows.go' => 'implementation-plugins-hooks',
    'pkg/extensions/skills.go' => 'implementation-skills-output',

    'pkg/model/azure.go' => 'implementation-auth-network',
    'pkg/model/error_graph.go' => 'implementation-query-model',
    'pkg/model/provider_metadata.go' => 'implementation-auth-network',
    'pkg/model/redact.go' => 'implementation-auth-network',
    'pkg/model/sse.go' => 'implementation-query-model',
    'pkg/model/stream.go' => 'implementation-query-model',
    'pkg/model/types.go' => 'implementation-query-model',

    'pkg/protocol/constructors.go' => 'implementation-query-model',
    'pkg/protocol/types.go' => 'implementation-query-model',
    'pkg/protocol/validate.go' => 'implementation-query-model',

    'pkg/tool/bash_tool.go' => 'implementation-tool-catalog',
    'pkg/tool/core.go' => 'implementation-tool-catalog',
    'pkg/tool/errors.go' => 'implementation-tool-protocol',
    'pkg/tool/executor.go' => 'implementation-tool-protocol',
    'pkg/tool/file_links_other.go' => 'implementation-tool-catalog',
    'pkg/tool/file_links_unix.go' => 'implementation-tool-catalog',
    'pkg/tool/file_links_windows.go' => 'implementation-tool-catalog',
    'pkg/tool/file_tools.go' => 'implementation-tool-catalog',
    'pkg/tool/file_tracker.go' => 'implementation-tool-catalog',
    'pkg/tool/interaction_tool.go' => 'implementation-tool-catalog',
    'pkg/tool/registry.go' => 'implementation-tool-catalog',
    'pkg/tool/reserved_rename_other.go' => 'implementation-tool-catalog',
    'pkg/tool/reserved_rename_unix.go' => 'implementation-tool-catalog',
    'pkg/tool/reserved_rename_windows.go' => 'implementation-tool-catalog',
    'pkg/tool/result_store.go' => 'implementation-tool-protocol',
    'pkg/tool/result_store_sync_other.go' => 'implementation-tool-protocol',
    'pkg/tool/result_store_sync_unix.go' => 'implementation-tool-protocol',
    'pkg/tool/scheduler.go' => 'implementation-tool-protocol',
    'pkg/tool/schema.go' => 'implementation-tool-protocol',
    'pkg/tool/search_tools.go' => 'implementation-tool-catalog',
    'pkg/tool/task_tools.go' => 'implementation-tool-catalog',
    'pkg/tool/types.go' => 'implementation-tool-protocol'
  }.freeze

  PACKAGE_OWNER = {
    'pkg/attachment/' => 'implementation-commands-input',
    'pkg/childenv/' => 'implementation-auth-network',
    'pkg/cli/' => 'implementation-headless-sdk',
    'pkg/command/' => 'implementation-commands-input',
    'pkg/compact/' => 'implementation-memory-compaction',
    'pkg/distributed/' => 'implementation-remote-bridge',
    'pkg/engine/' => 'implementation-query-model',
    'pkg/features/' => 'implementation-startup-settings',
    'pkg/identity/' => 'implementation-state-context',
    'pkg/mcp/' => 'implementation-mcp-lsp',
    'pkg/memory/' => 'implementation-memory-compaction',
    'pkg/observability/' => 'implementation-observability',
    'pkg/permission/' => 'implementation-permissions-sandbox',
    'pkg/platform/' => 'implementation-platform-lifecycle',
    'pkg/prompt/' => 'implementation-state-context',
    'pkg/redact/' => 'implementation-auth-network',
    'pkg/sandbox/' => 'implementation-permissions-sandbox',
    'pkg/sessionlock/' => 'implementation-transcript-recovery',
    'pkg/signals/' => 'implementation-platform-lifecycle',
    'pkg/surface/' => 'implementation-headless-sdk',
    'pkg/task/' => 'implementation-task-runtime',
    'pkg/testing/' => 'implementation-permissions-sandbox',
    'pkg/transcript/' => 'implementation-transcript-recovery'
  }.freeze

  module_function

  def production_go_files(root)
    patterns = [
      File.join(root, '*.go'),
      File.join(root, 'pkg/**/*.go')
    ]
    files = patterns.flat_map { |pattern| Dir.glob(pattern, File::FNM_DOTMATCH) }
                    .select { |path| File.file?(path) && !path.end_with?('_test.go') }
                    .map { |path| path.delete_prefix("#{root}/") }
                    .uniq
                    .sort
    abort 'production Go source inventory is empty' if files.empty?
    files
  end

  def owner_for(path)
    EXACT_OWNER[path] || PACKAGE_OWNER.find { |prefix, _owner| path.start_with?(prefix) }&.last
  end

  def validate_ownership!(files)
    unclassified = files.reject { |path| SCOPE.key?(owner_for(path)) }
    return if unclassified.empty?

    abort "Unclassified production Go artifacts; add explicit behavioral owners:\n#{unclassified.join("\n")}"
  end
end
