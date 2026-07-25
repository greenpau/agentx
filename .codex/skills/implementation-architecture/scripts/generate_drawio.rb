#!/usr/bin/env ruby
# frozen_string_literal: true

require 'cgi'
require 'digest'
require 'fileutils'

ROOT = File.expand_path('../../../..', __dir__)
SKILLS = File.join(ROOT, '.codex/skills')

SPECS = {
  'implementation-architecture' => ['System boundary and dependency direction',
    ['External event', 'Identity · trust · settings · policy', 'Session state + registries', 'Context + model loop', 'Capability boundary · bounded edited-input reauthorization', 'Transcript + tasks + usage', 'Surface adapters'],
    [[0,1],[1,2],[2,3],[3,4],[4,5],[5,3],[3,6],[5,6]]],
  'implementation-runtime-core' => ['Semantic runtime core',
    ['Entrypoint event', 'Startup + settings', 'State + context', 'Query + model stream', 'Canonical events', 'Continuity', 'Surface adapter'],
    [[0,1],[1,2],[2,3],[3,4],[4,5],[4,6],[5,2]]],
  'implementation-capability-runtime' => ['Capability execution boundary',
    ['Model tool_use', 'Canonical tool protocol', 'Permission + sandbox', 'Concrete tool family', 'Long-lived task', 'Normalized tool_result', 'Transcript continuation'],
    [[0,1],[1,2],[2,3],[3,5],[3,4],[4,5],[5,6]]],
  'implementation-user-surfaces' => ['Surface adapters over semantic events',
    ['Semantic event stream', 'Terminal engine', 'Interactive REPL', 'Headless + SDK', 'Optional experience', 'Human / host', 'Normalized input'],
    [[0,1],[1,2],[0,3],[0,4],[2,5],[3,5],[4,5],[5,6],[6,0]]],
  'implementation-extension-plane' => ['Extension discovery and registry merge',
    ['Built-in + filesystem + remote sources', 'Validate + attribute', 'Commands + input', 'Skills + output styles', 'Plugins + hooks', 'MCP + LSP', 'Session registries'],
    [[0,1],[1,2],[1,3],[1,4],[1,5],[2,6],[3,6],[4,6],[5,6]]],
  'implementation-continuity' => ['Durable history and derived projections',
    ['Canonical event', 'Append-safe transcript', 'Message graph', 'Resume / fork / rewind', 'Memory selection', 'Compaction projection', 'Next model context'],
    [[0,1],[1,2],[2,3],[2,4],[2,5],[4,6],[5,6],[3,6]]],
  'implementation-distributed-runtime' => ['Distributed placement with shared semantics',
    ['Parent session', 'Task identity', 'Agent / team', 'Bridge / remote transport', 'Permission relay', 'File output + append-backed mailbox · attempt ≠ ack · no dedup ID', 'Result synthesis'],
    [[0,1],[1,2],[1,3],[2,4],[3,4],[2,5],[3,5],[5,6],[6,0]]],
  'implementation-operations' => ['Operational services around the core',
    ['Startup profile', 'Auth + network', 'Platform resources', 'Semantic core', 'Observability', 'Updates + diagnostics', 'Graceful shutdown'],
    [[0,1],[0,2],[1,3],[2,3],[3,4],[4,5],[3,6],[2,6]]],
  'implementation-startup-settings' => ['Startup, trust, and configuration precedence',
    ['Early mode dispatch', 'Safe pre-trust environment', 'Load + validate sources', 'Managed policy', 'Trust decision', 'Discover registries', 'Create / restore session'],
    [[0,1],[1,2],[2,3],[3,4],[4,5],[5,6],[2,5]]],
  'implementation-state-context' => ['State lifetimes and prompt assembly',
    ['Process bootstrap', 'Session application state', 'Turn snapshot', 'Durable transcript', 'Ordered context sections', 'Cacheable / volatile boundary', 'Effective prompt'],
    [[0,1],[1,2],[3,1],[1,4],[2,4],[4,5],[5,6]]],
  'implementation-query-model' => ['Recursive query and stream recovery',
    ['Accepted input persisted', 'Compose request', 'Stream model blocks', 'Execute paired tools', 'Drain queued context', 'Enforce limits / compact', 'Final or recurse'],
    [[0,1],[1,2],[2,3],[3,4],[4,5],[5,6],[6,1]]],
  'implementation-tool-protocol' => ['Validated tool-use lifecycle',
    ['Resolve + structural validation', 'PreToolUse hooks', 'Permission + bounded edited-input rebuild', 'Postauthorization semantic validation', 'Execute + progress', 'Map terminal result', 'Post hooks + cleanup'],
    [[0,1],[1,2],[2,3],[3,4],[4,5],[5,6]]],
  'implementation-tool-catalog' => ['Concrete capability registry',
    ['Build / gate profile', 'Built-in primitives', 'Search + read', 'Write + shell', 'External / open-world', 'Coordination tools', 'Filtered stable registry'],
    [[0,1],[1,2],[1,3],[1,4],[1,5],[2,6],[3,6],[4,6],[5,6]]],
  'implementation-permissions-sandbox' => ['Composed permission and isolation decision',
    ['Structurally validated request', 'Deny / ask / allow rules', 'Tool, path, and sandbox analysis', 'Hooks / approval response', 'Rebuild edited input + bounded reauthorization', 'Final sandbox disposition + semantic handoff', 'Execute final authorized input'],
    [[0,1],[1,2],[2,3],[3,4],[4,5],[5,6],[3,5]]],
  'implementation-task-runtime' => ['Asynchronous task lifecycle and durability boundaries',
    ['Create identity + output', 'pending', 'running', 'Progress / delta output', 'completed / failed / killed', 'Live-state notification gate', 'Evict + cleanup'],
    [[0,1],[1,2],[2,3],[3,2],[2,4],[4,5],[5,6]]],
  'implementation-terminal-engine' => ['Retained terminal rendering pipeline',
    ['Input byte tokenizer', 'Focus + event propagation', 'Component reconciliation', 'Layout + clipping', 'Logical frame', 'Front/back diff + ANSI', 'Terminal + selection'],
    [[0,1],[1,2],[2,3],[3,4],[4,5],[5,6],[6,0]]],
  'implementation-interactive-repl' => ['Interactive query and presentation state',
    ['Prompt editor + paste/history', 'Command / queue dispatch', 'Generation-aware query guard', 'Semantic session events', 'Message projection', 'Dialogs / overlays / scroll', 'Rendered interaction'],
    [[0,1],[1,2],[2,3],[3,4],[4,5],[5,6],[6,0]]],
  'implementation-headless-sdk' => ['Headless dual loop and correlated protocol',
    ['NDJSON / text input', 'Input + control reader', 'Priority queue + mutex', 'Semantic query loop', 'FIFO SDK event writer', 'Finite work → result → suggestion → internal flush → idle', 'EOF after workload → unbounded team gate → adapter close / cleanup'],
    [[0,1],[1,2],[2,3],[3,4],[4,5],[4,3],[5,6]]],
  'implementation-optional-experiences' => ['Feature-profile optional experiences',
    ['Compile inclusion', 'Runtime gate', 'Identity / account', 'Policy + platform', 'Optional adapter', 'Owned resources', 'Disabled or degraded behavior'],
    [[0,1],[1,2],[2,3],[3,4],[4,5],[5,6],[3,6]]],
  'implementation-commands-input' => ['User input and command routing',
    ['Raw prompt / attachments', 'Normalize + expand refs', 'Slash / shell / text classify', 'Command registry resolution', 'Local result / UI · durability is command-specific', 'Process-local priority queue · crash-before-consume loses item', 'Consumption commit → transcript / history'],
    [[0,1],[1,2],[2,3],[3,4],[3,5],[5,6]]],
  'implementation-skills-output' => ['Skill and output-style lifecycle',
    ['Project skills / multi-source output styles', 'Discover + realpath dedup', 'Parse frontmatter', 'Filter visibility / conditions', 'Substitute invocation context', 'Inject prompt / fork agent', 'Track + survive compaction'],
    [[0,1],[1,2],[2,3],[3,4],[4,5],[5,6]]],
  'implementation-plugins-hooks' => ['Plugin, marketplace, and hook lifecycle',
    ['Marketplace / session / built-in source', 'Policy before download', 'Resolve dependencies', 'Validate + version install', 'Load attributed components', 'Run matched lifecycle hooks', 'Reload / disable / cleanup'],
    [[0,1],[1,2],[2,3],[3,4],[4,5],[5,6],[6,0]]],
  'implementation-mcp-lsp' => ['External protocol provider lifecycle',
    ['Scoped configuration', 'Policy + trust + auth', 'Connect provider', 'Discover tools/resources/language server', 'Adapt validated capability', 'Retry / reconnect / restart', 'Session registry + status'],
    [[0,1],[1,2],[2,3],[3,4],[4,5],[5,2],[3,6]]],
  'implementation-transcript-recovery' => ['Append-only message graph and resume',
    ['Semantic message', 'Append queue + shared JSONL recovery', 'UUID parent graph / DAG', 'Metadata + snapshots', 'Tail-safe load + repair', 'Resume or fork identity', 'Coherent next turn'],
    [[0,1],[1,2],[2,3],[3,4],[4,5],[5,6],[2,4]]],
  'implementation-memory-compaction' => ['Context pressure, file memory, and consolidation',
    ['Transcript + file-memory roots', 'Pressure / recall select', 'Relevant recall + session extraction', 'Full / partial summary', 'Team sync · auto-dream · cache edits', 'Restore files · skills · hooks', 'Bounded model projection'],
    [[0,1],[1,2],[1,3],[1,4],[2,5],[3,5],[4,5],[5,6]]],
  'implementation-remote-bridge' => ['Remote session transport and replay',
    ['Authenticate + create/attach', 'Transport + connection generation', 'Observe / dispatch event', 'Adapter response + declared loss window', 'Reconnect from retained cursor', 'Permission/control relay · no local permission deadline', 'Complete / interrupt / close'],
    [[0,1],[1,2],[2,3],[3,4],[4,2],[2,5],[5,6]]],
  'implementation-multi-agent' => ['Agent, team, and mailbox ownership',
    ['Resolve definition + authority', 'Create agent/task identity', 'Select in-process / process / remote / worktree', 'Independent context + abort', 'Mailbox permission relay · 500/1000 ms polls · no deadline', 'File evidence + live status', 'Correlated shutdown · no auto timeout · explicit cleanup separate'],
    [[0,1],[1,2],[2,3],[3,4],[4,5],[5,6],[6,0]]],
  'implementation-auth-network' => ['Credential and network boundary',
    ['Provider/profile selection', 'Credential precedence', 'Secure cache + refresh lock', 'TLS / CA / proxy / client', 'Authenticated request', 'Retry / recreate client', 'Redacted result + cleanup'],
    [[0,1],[1,2],[2,3],[3,4],[4,5],[5,4],[5,6]]],
  'implementation-platform-lifecycle' => ['Platform resource ownership',
    ['Capability probe', 'Portable adapter', 'Acquire file/process/terminal resource', 'Register cleanup owner', 'Operate with bounds', 'Signal / cancel / shutdown', 'Release idempotently'],
    [[0,1],[1,2],[2,3],[3,4],[4,5],[5,6]]],
  'implementation-observability' => ['Non-authoritative operational evidence',
    ['Canonical semantic event', 'Privacy + opt-out filter', 'Metrics / traces / logs / cost', 'Bounded batch + queue', 'Sink / disk fallback', 'Retry or drop policy', 'No effect on semantic outcome'],
    [[0,1],[1,2],[2,3],[3,4],[4,5],[5,6],[0,6]]],
  'implementation-conformance-audit' => ['Traceability and conformance evidence',
    ['Generated artifact + hash', 'Reviewed hash + primary owner', 'Stable contract ID', 'Draw.io topology', 'Acceptance scenario suite', 'Conformance trace', 'verified AgentX change'],
    [[0,1],[1,2],[2,3],[2,4],[3,5],[4,5],[5,6]]]
}.freeze

ROUTING_GROUPS = {
  'implementation-runtime-core' => %w[
    implementation-startup-settings implementation-state-context implementation-query-model
  ],
  'implementation-capability-runtime' => %w[
    implementation-tool-protocol implementation-tool-catalog implementation-permissions-sandbox
    implementation-task-runtime
  ],
  'implementation-user-surfaces' => %w[
    implementation-terminal-engine implementation-interactive-repl implementation-headless-sdk
    implementation-optional-experiences
  ],
  'implementation-extension-plane' => %w[
    implementation-commands-input implementation-skills-output implementation-plugins-hooks
    implementation-mcp-lsp
  ],
  'implementation-continuity' => %w[
    implementation-transcript-recovery implementation-memory-compaction
  ],
  'implementation-distributed-runtime' => %w[
    implementation-remote-bridge implementation-multi-agent
  ],
  'implementation-operations' => %w[
    implementation-auth-network implementation-platform-lifecycle implementation-observability
  ],
  'implementation-conformance-audit' => []
}.freeze

DIAGRAM_TEMPLATE_VERSION = '2.0-context-flow'.freeze

LIFECYCLE_STAGES = [
  'Entrypoint · identity · policy',
  'Session state · registries · context',
  'Query engine · model stream',
  'Capability decision · execution',
  'Transcript · tasks · continuity',
  'Surface · remote projection'
].freeze

LIFECYCLE_EDGE_LABELS = [
  'establishes · session',
  'composes · request',
  'requests · capability',
  'records · outcome',
  'projects · event'
].freeze

DOMAIN_META = {
  'implementation-architecture' => {
    focus: (0...LIFECYCLE_STAGES.length).to_a,
    guarantee: 'Owns the whole-system boundary, dependency direction, vocabulary, and standalone routing contract.',
    boundary: 'Defers exact domain behavior to the routed implementation skills; never substitutes diagrams for numbered prose.',
    crosscut: false
  },
  'implementation-runtime-core' => {
    focus: [0, 1, 2],
    guarantee: 'Owns shared startup, session state, context composition, model streaming, and turn continuation.',
    boundary: 'Defers tool semantics, presentation, remote placement, and operating-system mechanisms to their authoritative domains.',
    crosscut: false
  },
  'implementation-capability-runtime' => {
    focus: [3],
    guarantee: 'Owns the validated and authorized boundary from model tool request to normalized result or registered task.',
    boundary: 'Defers model continuation, surface rendering, transcript recovery, and platform primitives to collaborating skills.',
    crosscut: false
  },
  'implementation-user-surfaces' => {
    focus: [5],
    guarantee: 'Owns terminal, interactive, headless, SDK, and optional presentation adapters over shared semantic events.',
    boundary: 'Does not own permission truth, transcript truth, task truth, or the meaning of a model turn.',
    crosscut: false
  },
  'implementation-extension-plane' => {
    focus: [1, 3],
    guarantee: 'Owns attributed discovery, validation, filtering, merging, invocation, and invalidation of extension contributions.',
    boundary: 'Extensions contribute capabilities and context but do not bypass shared permission, query, transcript, or policy contracts.',
    crosscut: false
  },
  'implementation-continuity' => {
    focus: [4],
    guarantee: 'Owns durable conversation evidence, recovery, branching, memory selection, and bounded context projection.',
    boundary: 'Does not infer external side-effect success or let derived summaries replace authoritative history.',
    crosscut: false
  },
  'implementation-distributed-runtime' => {
    focus: [4, 5],
    guarantee: 'Owns placement, identity translation, delivery, replay, delegation, and distributed lifecycle adaptation.',
    boundary: 'Remote or child placement cannot redefine permission, tool, transcript, or task semantics.',
    crosscut: false
  },
  'implementation-operations' => {
    focus: [],
    guarantee: 'Owns authentication, networking, platform resources, cleanup, diagnostics, telemetry, and update mechanisms around the core.',
    boundary: 'Operational services may degrade without becoming semantic authority or corrupting session correctness.',
    crosscut: true
  },
  'implementation-conformance-audit' => {
    focus: [],
    guarantee: 'Owns evidence coverage, route reachability, diagram quality, contract traceability, and conformance proof.',
    boundary: 'Audit evidence proves review scope and internal consistency; it never invents missing runtime behavior.',
    crosscut: true
  }
}.freeze

ROUTER_PURPOSES = {
  'implementation-runtime-core' => 'startup · state · query',
  'implementation-capability-runtime' => 'tools · permission · tasks',
  'implementation-user-surfaces' => 'terminal · REPL · SDK',
  'implementation-extension-plane' => 'commands · skills · plugins · MCP',
  'implementation-continuity' => 'transcript · memory · compaction',
  'implementation-distributed-runtime' => 'remote · agents · teams',
  'implementation-operations' => 'auth · platform · observability',
  'implementation-conformance-audit' => 'coverage · conformance · clarity'
}.freeze

def xml_escape(value)
  CGI.escapeHTML(value.to_s)
end

def html_lines(value)
  xml_escape(value).gsub("\n", '&lt;br&gt;')
end

def wrap_edge_label(value, limit = 31)
  words = value.to_s.split(/\s+/)
  lines = []
  current = ''
  words.each do |word|
    candidate = current.empty? ? word : "#{current} #{word}"
    if !current.empty? && candidate.length > limit
      lines << current
      current = word
    else
      current = candidate
    end
  end
  lines << current unless current.empty?
  lines.join("\n")
end

def edge_label_box(wrapped_label)
  lines = wrapped_label.split("\n")
  width = [[lines.map(&:length).max.to_i * 5.8 + 18.0, 108.0].max, 230.0].min
  height = [lines.length * 12.0 + 8.0, 24.0].max
  [width, height]
end

def parent_router(skill)
  return nil if skill == 'implementation-architecture'
  return 'implementation-architecture' if ROUTING_GROUPS.key?(skill)
  ROUTING_GROUPS.each do |router, leaves|
    return router if leaves.include?(skill)
  end
  nil
end

def domain_meta(skill)
  return DOMAIN_META.fetch('implementation-architecture') if skill == 'implementation-architecture'
  owner = ROUTING_GROUPS.key?(skill) ? skill : parent_router(skill)
  DOMAIN_META.fetch(owner)
end

def collaborating_owners(skill)
  return ROUTING_GROUPS.keys if skill == 'implementation-architecture'
  if skill == 'implementation-conformance-audit'
    return [
      'implementation-architecture — system and routing authority',
      'all behavioral implementation skills — contracts and evidence',
      'skill-authoring — route and diagram authoring rules'
    ]
  end
  return ROUTING_GROUPS.fetch(skill) if ROUTING_GROUPS.key?(skill)
  router = parent_router(skill)
  [router] + (ROUTING_GROUPS.fetch(router) - [skill])
end

def definition_ids(line)
  ids = []
  ids.concat(line.scan(/\*\*([A-Z][A-Z0-9-]*-[A-Z]?[0-9]{2,3})\b/).flatten)
  ids.concat(line.scan(/^\s*(?:(?:[-*+]\s+)|(?:\d+[.)]\s+))?`([A-Z][A-Z0-9-]*-[A-Z]?[0-9]{2,3})`\s+—/).flatten)
  ids.concat(line.scan(/^\|\s*([A-Z][A-Z0-9-]*-[A-Z]?[0-9]{2,3})\s*\|/).flatten)
  ids.concat(line.scan(/^\|\s*`([A-Z][A-Z0-9-]*-[A-Z]?[0-9]{2,3})`\s*\|/).flatten)
  ids.concat(line.scan(/^\x23{2,}\s+`([A-Z][A-Z0-9-]*-[A-Z]?[0-9]{2,3})`/).flatten)
  ids.uniq
end

def contract_anchors(skill)
  if skill == 'implementation-skills-output'
    return %w[SKILL-003 SKILL-011 SKILL-016 SKILL-017 SKILL-018]
  end

  ids = []
  owners = [skill]
  owners.concat(ROUTING_GROUPS.fetch(skill)) if ROUTING_GROUPS.key?(skill)
  owners.each do |owner|
    directory = File.join(SKILLS, owner)
    files = [File.join(directory, 'SKILL.md')] + Dir.glob(File.join(directory, 'references/**/*.md')).sort
    files.each do |path|
      File.readlines(path).each do |line|
        definition_ids(line).each do |id|
          next if id.start_with?('CONF-') || id.match?(/(?:\A|-)A\d{2,3}\z/)
          ids << id unless ids.include?(id)
        end
      end
    end
    break if ids.length >= 5
  end
  anchors = ids.first(5)
  abort "no concrete contract anchors found for #{skill}" if anchors.empty?
  anchors
end

def ownership_guarantee(skill, title, sources, sinks)
  return domain_meta(skill)[:guarantee] if skill == 'implementation-architecture' || ROUTING_GROUPS.key?(skill)

  "Owns #{title.downcase}: accepts #{sources.join(' / ')}, governs the responsibilities and branches shown on the flow page, and produces #{sinks.join(' / ')}."
end

def route_breadcrumb(skill)
  if skill == 'implementation-architecture'
    ['AGENTS.md', 'implementation-architecture', 'all behavioral domains']
  elsif ROUTING_GROUPS.key?(skill)
    ['implementation-architecture', skill, 'routed leaf skills']
  else
    ['implementation-architecture', parent_router(skill), skill]
  end
end

def cycle_closing_edge_indices(nodes, edges)
  edges.each_index.select do |edge_index|
    source, target = edges.fetch(edge_index)
    next false unless target < source
    adjacency = Hash.new { |hash, key| hash[key] = [] }
    edges.each_with_index do |(from, to), candidate_index|
      next if candidate_index == edge_index
      adjacency[from] << to
    end
    seen = {}
    stack = [target]
    reachable = false
    until stack.empty?
      current = stack.pop
      next if seen[current]
      seen[current] = true
      if current == source
        reachable = true
        break
      end
      adjacency[current].each { |neighbor| stack << neighbor }
    end
    reachable
  end
end

def dependency_ranks(nodes, edges, feedback_indices)
  retained = edges.each_with_index.reject { |_edge, index| feedback_indices.include?(index) }.map(&:first)
  indegree = Array.new(nodes.length, 0)
  adjacency = Hash.new { |hash, key| hash[key] = [] }
  retained.each do |source, target|
    adjacency[source] << target
    indegree[target] += 1
  end
  queue = (0...nodes.length).select { |index| indegree[index].zero? }.sort
  ranks = Array.new(nodes.length, 0)
  visited = []
  until queue.empty?
    source = queue.shift
    visited << source
    adjacency[source].sort.each do |target|
      ranks[target] = [ranks[target], ranks[source] + 1].max
      indegree[target] -= 1
      queue << target if indegree[target].zero?
      queue.sort!
    end
  end
  if visited.length != nodes.length
    # The specification order remains only a deterministic emergency layout;
    # the generated page visibly labels it as topology rather than sequence.
    nodes.each_index { |index| ranks[index] = index }
  end
  ranks
end

def graph_summary(nodes, edges, feedback_indices)
  retained = edges.each_with_index.reject { |_edge, index| feedback_indices.include?(index) }.map(&:first)
  indegree = Array.new(nodes.length, 0)
  outdegree = Array.new(nodes.length, 0)
  retained.each do |source, target|
    outdegree[source] += 1
    indegree[target] += 1
  end
  sources = nodes.each_index.select { |index| indegree[index].zero? }.map { |index| nodes[index] }
  sinks = nodes.each_index.select { |index| outdegree[index].zero? }.map { |index| nodes[index] }
  [sources, sinks, indegree, outdegree]
end

def node_tag(index, indegree, outdegree, feedback_indices, edges)
  feedback_nodes = feedback_indices.flat_map { |edge_index| edges.fetch(edge_index) }.uniq
  return 'ENTRY' if indegree[index].zero?
  return 'OUTCOME' if outdegree[index].zero?
  return 'FAN-OUT' if outdegree[index] > 1
  return 'MERGE' if indegree[index] > 1
  return 'STATE / LOOP' if feedback_nodes.include?(index)
  'RESPONSIBILITY'
end

def node_palette(tag)
  case tag
  when 'ENTRY' then ['#f1f3f4', '#80868b']
  when 'OUTCOME' then ['#e6f4ea', '#34a853']
  when 'FAN-OUT' then ['#fef7e0', '#f9ab00']
  when 'MERGE' then ['#e6f4ea', '#188038']
  when 'STATE / LOOP' then ['#f3e8fd', '#a142f4']
  else ['#e8f0fe', '#1a73e8']
  end
end

def node_scope(label, tag)
  return 'CONSUMED' if tag == 'ENTRY'
  return 'PRODUCED' if tag == 'OUTCOME'
  return 'INTERFACE' if label.match?(/\b(?:human|host|model|provider|external)\b/i)
  'IN SCOPE'
end

def context_page_xml(skill, title, nodes, edges, anchors)
  breadcrumb = route_breadcrumb(skill)
  meta = domain_meta(skill)
  feedback_indices = cycle_closing_edge_indices(nodes, edges)
  sources, sinks, = graph_summary(nodes, edges, feedback_indices)
  deferred = collaborating_owners(skill)
  guarantee = ownership_guarantee(skill, title, sources, sinks)
  context_id = "#{Digest::SHA256.hexdigest(skill)[0, 12]}-context"
  breadcrumb_x = [110, 680, 1250]
  current_route_index = breadcrumb.index(skill) || 0
  breadcrumb_cells = breadcrumb.each_with_index.map do |label, index|
    current = index == current_route_index
    downstream = index > current_route_index
    fill = current ? '#1a73e8' : (downstream ? '#fff7e0' : '#f1f3f4')
    stroke = current ? '#174ea6' : (downstream ? '#f9ab00' : '#9aa0a6')
    font = current ? '#ffffff' : '#202124'
    relation = current ? 'YOU ARE HERE' : (downstream ? 'routed downstream' : 'documentation route')
    <<~XML
      <mxCell id="ctx-route-#{index}" value="&lt;b&gt;#{xml_escape(label)}&lt;/b&gt;&lt;br&gt;&lt;font style=&quot;font-size:10px&quot;&gt;#{relation}&lt;/font&gt;" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#{fill};strokeColor=#{stroke};fontColor=#{font};strokeWidth=#{current ? 3 : 1};fontSize=14;" vertex="1" parent="1">
        <mxGeometry x="#{breadcrumb_x[index]}" y="135" width="440" height="66" as="geometry"/>
      </mxCell>
    XML
  end.join
  breadcrumb_edges = (0...2).map do |index|
    <<~XML
      <mxCell id="ctx-route-edge-#{index}" value="routes documentation" style="edgeStyle=none;html=1;exitX=1;exitY=0.5;entryX=0;entryY=0.5;endArrow=block;endFill=1;strokeColor=#80868b;strokeWidth=2;fontSize=10;labelBackgroundColor=#ffffff;" edge="1" parent="1" source="ctx-route-#{index}" target="ctx-route-#{index + 1}">
        <mxGeometry relative="1" as="geometry"/>
      </mxCell>
    XML
  end.join
  lifecycle_x = 70
  stage_width = 200
  # The gap is deliberately wider than the longest lifecycle transfer label.
  # Connector text therefore sits in whitespace instead of on either card.
  stage_gap = 91
  lifecycle_cells = LIFECYCLE_STAGES.each_with_index.map do |stage, index|
    focused = meta[:focus].include?(index)
    fill = focused ? '#e8f0fe' : '#f8f9fa'
    stroke = focused ? '#1a73e8' : '#bdc1c6'
    width = focused ? 3 : 1
    marker = focused ? 'YOU ARE HERE' : "context #{index + 1}"
    <<~XML
      <mxCell id="ctx-life-#{index}" value="&lt;b&gt;#{xml_escape(stage)}&lt;/b&gt;&lt;br&gt;&lt;font style=&quot;font-size:9px&quot;&gt;#{marker}&lt;/font&gt;" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#{fill};strokeColor=#{stroke};strokeWidth=#{width};fontSize=12;" vertex="1" parent="1">
        <mxGeometry x="#{lifecycle_x + index * (stage_width + stage_gap)}" y="275" width="#{stage_width}" height="78" as="geometry"/>
      </mxCell>
    XML
  end.join
  lifecycle_edges = (0...(LIFECYCLE_STAGES.length - 1)).map do |index|
    <<~XML
      <mxCell id="ctx-life-edge-#{index}" value="#{xml_escape(LIFECYCLE_EDGE_LABELS[index])}" style="edgeStyle=none;html=1;exitX=1;exitY=0.5;entryX=0;entryY=0.5;endArrow=block;endFill=1;strokeColor=#5f6368;strokeWidth=1;fontSize=9;labelBackgroundColor=#ffffff;" edge="1" parent="1" source="ctx-life-#{index}" target="ctx-life-#{index + 1}">
        <mxGeometry relative="1" as="geometry"/>
      </mxCell>
    XML
  end.join
  crosscut_fill = meta[:crosscut] ? '#e8f0fe' : '#f1f3f4'
  crosscut_stroke = meta[:crosscut] ? '#1a73e8' : '#9aa0a6'
  <<~XML
    <diagram id="#{context_id}" name="01 — Context &amp; Boundaries">
      <mxGraphModel dx="1800" dy="1120" grid="1" gridSize="10" guides="1" tooltips="1" connect="1" arrows="1" fold="1" page="1" pageScale="1" pageWidth="1800" pageHeight="1120" math="0" shadow="0">
        <root>
          <mxCell id="0"/>
          <mxCell id="1" parent="0"/>
          <mxCell id="ctx-title" value="&lt;b&gt;Context &amp;amp; boundaries — #{xml_escape(title)}&lt;/b&gt;" style="text;html=1;strokeColor=none;fillColor=none;align=left;verticalAlign=middle;fontSize=26;" vertex="1" parent="1"><mxGeometry x="55" y="28" width="1365" height="42" as="geometry"/></mxCell>
          <mxCell id="ctx-template-version" value="architecture overview · template #{DIAGRAM_TEMPLATE_VERSION}" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#f1f3f4;strokeColor=#9aa0a6;fontSize=10;" vertex="1" parent="1"><mxGeometry x="1435" y="29" width="310" height="40" as="geometry"/></mxCell>
          <mxCell id="ctx-breadcrumb" value="&lt;b&gt;Context:&lt;/b&gt; #{xml_escape(breadcrumb.join(' → '))}" style="text;html=1;strokeColor=none;fillColor=none;fontSize=13;" vertex="1" parent="1"><mxGeometry x="60" y="78" width="1680" height="28" as="geometry"/></mxCell>
          #{breadcrumb_edges}
          #{breadcrumb_cells}
          <mxCell id="ctx-lifecycle" value="&lt;b&gt;Canonical product lifecycle — broader context, not a replacement for the detailed flow&lt;/b&gt;" style="text;html=1;strokeColor=none;fillColor=none;fontSize=13;" vertex="1" parent="1"><mxGeometry x="70" y="232" width="1500" height="28" as="geometry"/></mxCell>
          #{lifecycle_edges}
          #{lifecycle_cells}
          <mxCell id="ctx-crosscut" value="&lt;b&gt;Cross-cutting operations and evidence#{meta[:crosscut] ? " — YOU ARE HERE: #{xml_escape(skill)}" : ''}&lt;/b&gt; · authentication · networking · platform ownership · cleanup · observability · implementation audit" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#{crosscut_fill};strokeColor=#{crosscut_stroke};strokeWidth=#{meta[:crosscut] ? 3 : 1};dashed=1;fontSize=12;" vertex="1" parent="1"><mxGeometry x="70" y="370" width="1655" height="46" as="geometry"/></mxCell>
          <mxCell id="ctx-owner-boundary" value="CURRENT OWNERSHIP BOUNDARY" style="swimlane;html=1;horizontal=1;startSize=34;rounded=1;fillColor=#e8f0fe;strokeColor=#1a73e8;strokeWidth=2;fontSize=13;fontStyle=1;" vertex="1" parent="1"><mxGeometry x="60" y="450" width="1120" height="430" as="geometry"/></mxCell>
          <mxCell id="ctx-question" value="&lt;b&gt;Question&lt;/b&gt;&lt;br&gt;Where does #{xml_escape(skill)} sit, what does it own, and how does it hand off its result?" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#ffffff;strokeColor=#8ab4f8;fontSize=13;align=left;spacingLeft=12;" vertex="1" parent="1"><mxGeometry x="90" y="505" width="1060" height="70" as="geometry"/></mxCell>
          <mxCell id="ctx-starts" value="&lt;b&gt;Starts with&lt;/b&gt;&lt;br&gt;#{html_lines(sources.join("\n"))}" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#f1f3f4;strokeColor=#80868b;fontSize=12;align=left;spacingLeft=12;" vertex="1" parent="1"><mxGeometry x="90" y="605" width="300" height="150" as="geometry"/></mxCell>
          <mxCell id="ctx-owns" value="&lt;b&gt;Owns&lt;/b&gt;&lt;br&gt;#{html_lines(title)}&lt;br&gt;&lt;br&gt;#{html_lines(guarantee)}" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#e8f0fe;strokeColor=#1a73e8;strokeWidth=2;fontSize=12;align=left;spacingLeft=12;" vertex="1" parent="1"><mxGeometry x="430" y="605" width="380" height="150" as="geometry"/></mxCell>
          <mxCell id="ctx-ends" value="&lt;b&gt;Ends with&lt;/b&gt;&lt;br&gt;#{html_lines(sinks.join("\n"))}" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#e6f4ea;strokeColor=#34a853;fontSize=12;align=left;spacingLeft=12;" vertex="1" parent="1"><mxGeometry x="850" y="605" width="300" height="150" as="geometry"/></mxCell>
          <mxCell id="ctx-defers" value="&lt;b&gt;Defers to / collaborates with&lt;/b&gt;&lt;br&gt;#{html_lines(deferred.join("\n"))}&lt;br&gt;&lt;br&gt;&lt;b&gt;Boundary&lt;/b&gt;&lt;br&gt;#{html_lines(meta[:boundary])}" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#f8f9fa;strokeColor=#9aa0a6;dashed=1;fontSize=11;align=left;spacingLeft=12;" vertex="1" parent="1"><mxGeometry x="1210" y="450" width="515" height="305" as="geometry"/></mxCell>
          <mxCell id="ctx-contracts" value="&lt;b&gt;Contracts&lt;/b&gt;&lt;br&gt;#{xml_escape(anchors.join(' · '))}&lt;br&gt;&lt;font style=&quot;font-size:10px&quot;&gt;Read SKILL.md and references for the complete numbered contract set.&lt;/font&gt;" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#fff7e0;strokeColor=#f9ab00;fontSize=12;align=left;spacingLeft=12;" vertex="1" parent="1"><mxGeometry x="90" y="790" width="520" height="70" as="geometry"/></mxCell>
          <mxCell id="ctx-authority" value="&lt;b&gt;Authority&lt;/b&gt;&lt;br&gt;This page is authoritative for product position and the highlighted ownership boundary. Numbered prose owns exact fields, values, timing, ordering, errors, and compatibility behavior." style="rounded=1;whiteSpace=wrap;html=1;fillColor=#fce8e6;strokeColor=#d93025;fontSize=11;align=left;spacingLeft=12;" vertex="1" parent="1"><mxGeometry x="640" y="790" width="510" height="70" as="geometry"/></mxCell>
          <mxCell id="ctx-legend" value="&lt;b&gt;How to read this page&lt;/b&gt; · blue border = current owner · gray = external/deferred context · green = downstream outcome · arrows in the breadcrumb are documentation routes · arrows in the lifecycle strip are product-semantic progression" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#ffffff;strokeColor=#bdc1c6;fontSize=11;align=left;spacingLeft=12;" vertex="1" parent="1"><mxGeometry x="60" y="930" width="1665" height="70" as="geometry"/></mxCell>
        </root>
      </mxGraphModel>
    </diagram>
  XML
end

def flow_layout(nodes, edges)
  feedback_indices = cycle_closing_edge_indices(nodes, edges)
  ranks = dependency_ranks(nodes, edges, feedback_indices)
  sources, sinks, indegree, outdegree = graph_summary(nodes, edges, feedback_indices)
  groups = Hash.new { |hash, key| hash[key] = [] }
  ranks.each_with_index { |rank, index| groups[rank] << index }
  sorted_ranks = groups.keys.sort
  top = 310.0
  bottom = 1240.0
  node_height = 78.0
  vertical_step = sorted_ranks.length > 1 ? (bottom - top - node_height) / (sorted_ranks.length - 1) : 0
  positions = {}
  sorted_ranks.each_with_index do |rank, rank_index|
    members = groups.fetch(rank)
    gap = 34.0
    available_width = 1060.0
    node_width = [330.0, (available_width - gap * (members.length - 1)) / members.length].min
    node_width = [node_width, 175.0].max
    total_width = node_width * members.length + gap * (members.length - 1)
    start_x = 275.0 + (available_width - total_width) / 2
    members.each_with_index do |node_index, member_index|
      positions[node_index] = {
        x: start_x + member_index * (node_width + gap),
        y: top + rank_index * vertical_step,
        width: node_width,
        height: node_height,
        rank: rank
      }
    end
  end
  [positions, ranks, feedback_indices, sources, sinks, indegree, outdegree]
end

def node_center_x(position)
  position[:x] + position[:width] / 2.0
end

def node_center_y(position)
  position[:y] + position[:height] / 2.0
end

def adjacent_crossing_detours(edges, positions, feedback_indices)
  candidates = edges.each_index.reject { |index| feedback_indices.include?(index) }.select do |index|
    source, target = edges.fetch(index)
    positions.fetch(target)[:rank] == positions.fetch(source)[:rank] + 1
  end
  detours = []
  candidates.combination(2) do |left_index, right_index|
    left_source, left_target = edges.fetch(left_index)
    right_source, right_target = edges.fetch(right_index)
    next unless positions.fetch(left_source)[:rank] == positions.fetch(right_source)[:rank]
    next unless positions.fetch(left_target)[:rank] == positions.fetch(right_target)[:rank]
    next if left_source == right_source || left_target == right_target
    source_order = node_center_x(positions.fetch(left_source)) <=> node_center_x(positions.fetch(right_source))
    target_order = node_center_x(positions.fetch(left_target)) <=> node_center_x(positions.fetch(right_target))
    detours << [left_index, right_index].max if source_order * target_order == -1
  end
  detours.uniq
end

def segment_intersects_position?(x1, y1, x2, y2, position, margin = 8.0)
  left = position[:x] - margin
  right = position[:x] + position[:width] + margin
  top = position[:y] - margin
  bottom = position[:y] + position[:height] + margin
  if (x1 - x2).abs < 0.1
    x1.between?(left, right) && [y1, y2].min < bottom && [y1, y2].max > top
  elsif (y1 - y2).abs < 0.1
    y1.between?(top, bottom) && [x1, x2].min < right && [x1, x2].max > left
  else
    false
  end
end

def vertical_segment_available?(rail_x, source_y, target_y, used_vertical_segments, margin = 5.0)
  low, high = [source_y, target_y].minmax
  used_vertical_segments.none? do |used_x, used_low, used_high|
    (used_x - rail_x).abs < margin && low < used_high + margin && high > used_low - margin
  end
end

def clear_vertical_corridor(source, target, positions, used_vertical_segments: [], preferred_side: nil,
                            source_fraction: 0.5, target_fraction: 0.5)
  source_position = positions.fetch(source)
  target_position = positions.fetch(target)
  candidates = [
    source_position[:x] - 22.0,
    source_position[:x] + source_position[:width] + 22.0,
    target_position[:x] - 22.0,
    target_position[:x] + target_position[:width] + 22.0,
    150.0,
    1480.0
  ].uniq
  source_y = source_position[:y] + source_position[:height] * source_fraction
  target_y = target_position[:y] + target_position[:height] * target_fraction
  valid = candidates.select do |rail_x|
    next false if rail_x.between?(source_position[:x], source_position[:x] + source_position[:width])
    next false if rail_x.between?(target_position[:x], target_position[:x] + target_position[:width])
    source_side_x = rail_x < node_center_x(source_position) ? source_position[:x] : source_position[:x] + source_position[:width]
    target_side_x = rail_x < node_center_x(target_position) ? target_position[:x] : target_position[:x] + target_position[:width]
    vertical_segment_available?(rail_x, source_y, target_y, used_vertical_segments) && positions.each_key.none? do |node_index|
      next false if [source, target].include?(node_index)
      position = positions.fetch(node_index)
      segment_intersects_position?(source_side_x, source_y, rail_x, source_y, position) ||
        segment_intersects_position?(rail_x, source_y, rail_x, target_y, position) ||
        segment_intersects_position?(rail_x, target_y, target_side_x, target_y, position)
    end
  end
  preferred = valid.select do |rail_x|
    (preferred_side == :left && rail_x < node_center_x(source_position) && rail_x < node_center_x(target_position)) ||
      (preferred_side == :right && rail_x > node_center_x(source_position) && rail_x > node_center_x(target_position))
  end
  candidates_for_distance = preferred.empty? ? valid : preferred
  candidates_for_distance.min_by do |rail_x|
    (rail_x - node_center_x(source_position)).abs + (rail_x - node_center_x(target_position)).abs
  end
end

def outer_vertical_rail(side, source_y, target_y, used_vertical_segments, ordinal)
  step = 20.0
  candidate = side == :left ? 170.0 - ordinal * step : 1440.0 + ordinal * step
  direction = side == :left ? -1.0 : 1.0
  candidate += direction * step until vertical_segment_available?(candidate, source_y, target_y, used_vertical_segments, 12.0)
  candidate
end

def label_geometry(outdegree, indegree, source, target, source_ordinal, target_ordinal)
  relative_x = if outdegree[source] > 1 && indegree[target] <= 1
                 0.48
               elsif indegree[target] > 1 && outdegree[source] <= 1
                 -0.48
               elsif outdegree[source] > 1 && indegree[target] > 1
                 source_ordinal.even? ? -0.38 : 0.38
               else
                 0.0
               end
  ordinal = source_ordinal + target_ordinal
  offset_y = [-16, 0, 16][ordinal % 3]
  [relative_x, offset_y]
end

def edge_label(nodes, _source, target, _kind, _outdegree, _indegree)
  # SPECS records graph topology, not a prose-proven transition verb. Keep the
  # connector useful without turning layout-derived guesses into requirements.
  "handoff · #{nodes[target]}"
end

def flow_page_xml(skill, title, nodes, edges, anchors)
  positions, ranks, feedback_indices, sources, sinks, indegree, outdegree = flow_layout(nodes, edges)
  breadcrumb = route_breadcrumb(skill)
  meta = domain_meta(skill)
  deferred = collaborating_owners(skill)
  flow_id = "#{Digest::SHA256.hexdigest(skill)[0, 12]}-flow"
  outgoing = Hash.new { |hash, key| hash[key] = [] }
  incoming = Hash.new { |hash, key| hash[key] = [] }
  edges.each_with_index do |(source, target), edge_index|
    outgoing[source] << edge_index
    incoming[target] << edge_index
  end
  crossing_detours = adjacent_crossing_detours(edges, positions, feedback_indices)
  feedback_nodes = feedback_indices.flat_map { |edge_index| edges.fetch(edge_index) }.uniq
  feedback_rails = {}
  used_vertical_segments = []
  feedback_indices.each_with_index do |edge_index, feedback_ordinal|
    source, target = edges.fetch(edge_index)
    rail_x = 90.0 + feedback_ordinal * 20.0
    feedback_rails[edge_index] = rail_x
    source_y = node_center_y(positions.fetch(source))
    target_y = node_center_y(positions.fetch(target))
    low, high = [source_y, target_y].minmax
    used_vertical_segments << [rail_x, low, high]
  end
  outer_detour_numbers = { left: 0, right: 0 }
  edge_xml = edges.each_with_index.map do |(source, target), edge_index|
    source_position = positions.fetch(source)
    target_position = positions.fetch(target)
    kind = if feedback_indices.include?(edge_index)
             :feedback
           elsif crossing_detours.include?(edge_index)
             :detour
           elsif target_position[:rank] == source_position[:rank]
             :lateral
           elsif target_position[:rank] > source_position[:rank] + 1
             :skip
           else
             :direct
           end
    source_ordinal = outgoing[source].index(edge_index)
    target_ordinal = incoming[target].index(edge_index)
    source_fraction = (source_ordinal + 1.0) / (outgoing[source].length + 1.0)
    target_fraction = (target_ordinal + 1.0) / (incoming[target].length + 1.0)
    label = edge_label(nodes, source, target, kind, outdegree, indegree)
    wrapped_label = wrap_edge_label(label)
    label_width, label_height = edge_label_box(wrapped_label)
    if kind == :direct
      branch = outdegree[source] > 1 || indegree[target] > 1
      stroke = branch ? '#b06000' : '#1a73e8'
      dashed = branch ? 'dashed=1;dashPattern=8 4;' : ''
      relative_x, offset_y = label_geometry(outdegree, indegree, source, target, source_ordinal, target_ordinal)
      style = "edgeStyle=none;html=1;whiteSpace=wrap;exitX=#{format('%.3f', source_fraction)};exitY=1;entryX=#{format('%.3f', target_fraction)};entryY=0;endArrow=block;endFill=1;strokeColor=#{stroke};strokeWidth=2;#{dashed}fontSize=9;labelBackgroundColor=#ffffff;jumpStyle=arc;jumpSize=10;"
      geometry = "<mxGeometry x=\"#{format('%.2f', relative_x)}\" y=\"0\" width=\"#{format('%.1f', label_width)}\" height=\"#{format('%.1f', label_height)}\" relative=\"1\" as=\"geometry\"><mxPoint x=\"0\" y=\"#{offset_y}\" as=\"offset\"/></mxGeometry>"
    else
      if kind == :feedback
        rail_x = feedback_rails.fetch(edge_index)
        stroke = '#7e57c2'
        dash = 'dashed=1;dashPattern=8 4;'
        exit_x = 0
        entry_x = 0
        exit_y = source_fraction
        entry_y = target_fraction
        source_y = source_position[:y] + source_position[:height] * exit_y
        target_y = target_position[:y] + target_position[:height] * entry_y
        points = [
          [rail_x, source_y],
          [rail_x, target_y]
        ]
      else
        stroke = '#b06000'
        dash = 'dashed=1;dashPattern=8 4;'
        exit_y = source_fraction
        entry_y = target_fraction
        preferred_side = feedback_nodes.include?(source) || feedback_nodes.include?(target) ? :right : nil
        rail_x = if kind == :detour
                   nil
                 else
                   clear_vertical_corridor(
                     source,
                     target,
                     positions,
                     used_vertical_segments: used_vertical_segments,
                     preferred_side: preferred_side,
                     source_fraction: exit_y,
                     target_fraction: entry_y
                   )
                 end
        if rail_x
          exit_x = rail_x < node_center_x(source_position) ? 0 : 1
          entry_x = rail_x < node_center_x(target_position) ? 0 : 1
          source_y = source_position[:y] + source_position[:height] * exit_y
          target_y = target_position[:y] + target_position[:height] * entry_y
          points = [
            [rail_x, source_y],
            [rail_x, target_y]
          ]
          low, high = [source_y, target_y].minmax
          used_vertical_segments << [rail_x, low, high]
        else
          # A reversed adjacent-rank edge cannot be made planar inside the
          # rank gap. Route it around the row and enter from below so it cannot
          # form an X with the forward branch/merge bundle.
          side = node_center_x(source_position) <= 805.0 ? :left : :right
          source_y = source_position[:y] + source_position[:height] * exit_y
          below_y = target_position[:y] + target_position[:height] + 34.0 + outer_detour_numbers.fetch(side) * 18.0
          rail_x = outer_vertical_rail(side, source_y, below_y, used_vertical_segments, outer_detour_numbers.fetch(side))
          outer_detour_numbers[side] += 1
          exit_x = side == :left ? 0 : 1
          entry_x = target_fraction
          entry_y = 1
          points = [
            [rail_x, source_y],
            [rail_x, below_y],
            [node_center_x(target_position), below_y]
          ]
          low, high = [source_y, below_y].minmax
          used_vertical_segments << [rail_x, low, high]
        end
      end
      label_x = kind == :detour ? 0.64 : 0.0
      label_offset = kind == :detour ? -14 : (edge_index.even? ? -12 : 12)
      # A feedback label is centered on its outer rail by default. The leftmost
      # rail starts at x=90, so a normal 180–230px label can sit flush with, or
      # extend beyond, the page boundary even though the connector itself is
      # valid. Shift the label inward by half its box plus a small gutter while
      # leaving the rail and its node-avoidance geometry unchanged.
      label_offset_x = kind == :feedback ? label_width / 2.0 + 12.0 : 0.0
      style = "edgeStyle=orthogonalEdgeStyle;rounded=0;html=1;whiteSpace=wrap;exitX=#{exit_x};exitY=#{format('%.3f', exit_y)};entryX=#{format('%.3f', entry_x)};entryY=#{format('%.3f', entry_y)};endArrow=block;endFill=1;strokeColor=#{stroke};strokeWidth=2;#{dash}fontSize=9;labelBackgroundColor=#ffffff;jumpStyle=arc;jumpSize=10;"
      geometry = <<~XML.chomp
        <mxGeometry x="#{format('%.2f', label_x)}" y="0" width="#{format('%.1f', label_width)}" height="#{format('%.1f', label_height)}" relative="1" as="geometry">
          <mxPoint x="#{format('%.1f', label_offset_x)}" y="#{label_offset}" as="offset"/>
          <Array as="points">
            #{points.map { |x, y| %(<mxPoint x="#{format('%.1f', x)}" y="#{format('%.1f', y)}"/>) }.join("\n            ")}
          </Array>
        </mxGeometry>
      XML
    end
    <<~XML
      <mxCell id="flow-edge-#{edge_index}" value="#{html_lines(wrapped_label)}" semanticLabel="#{xml_escape(label)}" style="#{style}" edge="1" parent="1" source="flow-node-#{source}" target="flow-node-#{target}">
        #{geometry}
      </mxCell>
    XML
  end.join
  node_xml = nodes.each_with_index.map do |label, index|
    position = positions.fetch(index)
    tag = node_tag(index, indegree, outdegree, feedback_indices, edges)
    scope = node_scope(label, tag)
    fill, stroke = node_palette(tag)
    <<~XML
      <mxCell id="flow-node-#{index}" value="&lt;b&gt;#{xml_escape(label)}&lt;/b&gt;&lt;br&gt;&lt;font style=&quot;font-size:9px&quot;&gt;#{xml_escape(scope)} · #{xml_escape(tag)}&lt;/font&gt;" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#{fill};strokeColor=#{stroke};strokeWidth=2;fontSize=13;" vertex="1" parent="1">
        <mxGeometry x="#{format('%.1f', position[:x])}" y="#{format('%.1f', position[:y])}" width="#{format('%.1f', position[:width])}" height="#{format('%.1f', position[:height])}" as="geometry"/>
      </mxCell>
    XML
  end.join
  focus_text = meta[:crosscut] ? 'cross-cutting operations / evidence' : meta[:focus].map { |index| LIFECYCLE_STAGES[index] }.join(' + ')
  <<~XML
    <diagram id="#{flow_id}" name="02 — Responsibility Flow">
      <mxGraphModel dx="2200" dy="1420" grid="1" gridSize="10" guides="1" tooltips="1" connect="1" arrows="1" fold="1" page="1" pageScale="1" pageWidth="2200" pageHeight="1420" math="0" shadow="0">
        <root>
          <mxCell id="0"/>
          <mxCell id="1" parent="0"/>
          <mxCell id="flow-title" value="&lt;b&gt;Responsibility flow — #{xml_escape(title)}&lt;/b&gt;" style="text;html=1;strokeColor=none;fillColor=none;align=left;fontSize=26;" vertex="1" parent="1"><mxGeometry x="55" y="26" width="1750" height="42" as="geometry"/></mxCell>
          <mxCell id="flow-breadcrumb" value="&lt;b&gt;Context:&lt;/b&gt; #{xml_escape(breadcrumb.join(' → '))}&lt;br&gt;&lt;b&gt;Lifecycle focus:&lt;/b&gt; #{xml_escape(focus_text)}" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#f8f9fa;strokeColor=#bdc1c6;fontSize=11;align=left;spacingLeft=12;" vertex="1" parent="1"><mxGeometry x="55" y="78" width="2080" height="64" as="geometry"/></mxCell>
          <mxCell id="flow-owned-boundary" value="CURRENT SKILL — RESPONSIBILITY TOPOLOGY (INTERFACES + OWNED WORK)" style="swimlane;html=1;horizontal=1;startSize=34;rounded=1;fillColor=#ffffff;strokeColor=#1a73e8;strokeWidth=2;fontSize=13;fontStyle=1;" vertex="1" parent="1"><mxGeometry x="45" y="175" width="1510" height="1170" as="geometry"/></mxCell>
          <mxCell id="flow-reading-direction" value="Read top → bottom by dependency depth. CONSUMED and PRODUCED mark the skill boundary; INTERFACE marks an external actor; IN SCOPE does not transfer that actor's authority." style="rounded=1;whiteSpace=wrap;html=1;fillColor=#f1f3f4;strokeColor=#9aa0a6;fontSize=10;" vertex="1" parent="1"><mxGeometry x="245" y="220" width="1120" height="48" as="geometry"/></mxCell>
          #{edge_xml}
          #{node_xml}
          <mxCell id="flow-question" value="&lt;b&gt;Question&lt;/b&gt;&lt;br&gt;How do responsibilities connect from #{html_lines(sources.join(' / '))} to #{html_lines(sinks.join(' / '))}?" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#e8f0fe;strokeColor=#1a73e8;fontSize=12;align=left;spacingLeft=12;" vertex="1" parent="1"><mxGeometry x="1600" y="175" width="535" height="105" as="geometry"/></mxCell>
          <mxCell id="flow-authority" value="&lt;b&gt;Authority&lt;/b&gt;&lt;br&gt;Authoritative for topology, responsibility direction, branch/merge shape, and visible feedback rails. Numbered prose owns exact values, timing, errors, and compatibility." style="rounded=1;whiteSpace=wrap;html=1;fillColor=#fce8e6;strokeColor=#d93025;fontSize=11;align=left;spacingLeft=12;" vertex="1" parent="1"><mxGeometry x="1600" y="305" width="535" height="125" as="geometry"/></mxCell>
          <mxCell id="flow-contracts" value="&lt;b&gt;Contracts&lt;/b&gt;&lt;br&gt;#{xml_escape(anchors.join(' · '))}&lt;br&gt;&lt;font style=&quot;font-size:10px&quot;&gt;Complete definitions and scenarios are in SKILL.md and references.&lt;/font&gt;" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#fff7e0;strokeColor=#f9ab00;fontSize=11;align=left;spacingLeft=12;" vertex="1" parent="1"><mxGeometry x="1600" y="455" width="535" height="105" as="geometry"/></mxCell>
          <mxCell id="flow-defers" value="&lt;b&gt;Deferred / collaborating owners&lt;/b&gt;&lt;br&gt;#{html_lines(deferred.join("\n"))}&lt;br&gt;&lt;br&gt;#{html_lines(meta[:boundary])}" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#f8f9fa;strokeColor=#9aa0a6;dashed=1;fontSize=10;align=left;spacingLeft=12;" vertex="1" parent="1"><mxGeometry x="1600" y="585" width="535" height="235" as="geometry"/></mxCell>
          <mxCell id="flow-legend" value="&lt;b&gt;Line grammar&lt;/b&gt;&lt;br&gt;SOLID BLUE — primary responsibility transfer&lt;br&gt;DASHED AMBER — fan-out, merge, skip, or crossing detour&lt;br&gt;DASHED PURPLE — feedback, retry, continuation, or recovery&lt;br&gt;&lt;br&gt;Every edge is labeled. &amp;quot;handoff&amp;quot; asserts topology only; numbered prose owns the transferred event, state, or authority. Distinct ports prevent false merges; dedicated corridors keep alternate and feedback paths outside the forward bundle." style="rounded=1;whiteSpace=wrap;html=1;fillColor=#ffffff;strokeColor=#5f6368;fontSize=10;align=left;spacingLeft=12;" vertex="1" parent="1"><mxGeometry x="1600" y="845" width="535" height="205" as="geometry"/></mxCell>
          <mxCell id="flow-compatibility" value="&lt;b&gt;Failure / compatibility lens&lt;/b&gt;&lt;br&gt;Follow dashed perimeter relations before implementing retries, cancellation, recovery, or feedback. If the exact failure path is deferred, load the named collaborator and prose contracts rather than inferring it from this overview." style="rounded=1;whiteSpace=wrap;html=1;fillColor=#f3e8fd;strokeColor=#a142f4;fontSize=10;align=left;spacingLeft=12;" vertex="1" parent="1"><mxGeometry x="1600" y="1075" width="535" height="125" as="geometry"/></mxCell>
        </root>
      </mxGraphModel>
    </diagram>
  XML
end

def diagram_xml(skill, title, nodes, edges)
  anchors = contract_anchors(skill)
  <<~XML
    <?xml version="1.0" encoding="UTF-8"?>
    <mxfile host="app.diagrams.net" modified="2026-07-21T00:00:00.000Z" agent="Codex" version="24.7.17" type="device" diagramTemplateVersion="#{DIAGRAM_TEMPLATE_VERSION}">
      #{context_page_xml(skill, title, nodes, edges, anchors)}
      #{flow_page_xml(skill, title, nodes, edges, anchors)}
    </mxfile>
  XML
end

def routing_xml
  cards = []
  edges = []
  root_edges = []
  nodes = []
  page_width = 2200
  page_height = 1380
  card_width = 470
  card_height = 360
  column_x = [60, 590, 1120, 1650].freeze
  row_y = [350, 850].freeze
  root_x = 40
  root_y = 170
  root_width = 2120
  root_height = 72
  route_gap_y = 785

  ROUTING_GROUPS.each_with_index do |(router, leaves), group_index|
    column = group_index % 4
    row = group_index / 4
    x = column_x.fetch(column)
    y = row_y.fetch(row)
    card_id = "routing-card-#{group_index}"
    cards << <<~XML
      <mxCell id="#{card_id}" value="&lt;b&gt;#{xml_escape(ROUTER_PURPOSES.fetch(router))}&lt;/b&gt;" style="swimlane;html=1;horizontal=1;startSize=40;rounded=1;fillColor=#f8f9fa;strokeColor=#9aa0a6;fontSize=13;fontStyle=1;" vertex="1" parent="1"><mxGeometry x="#{x}" y="#{y}" width="#{card_width}" height="#{card_height}" as="geometry"/></mxCell>
    XML
    router_x = x + 20
    router_y = y + 88
    router_width = 150
    router_height = 116
    nodes << <<~XML
      <mxCell id="routing-router-#{group_index}" value="&lt;b&gt;#{xml_escape(router)}&lt;/b&gt;&lt;br&gt;&lt;font style=&quot;font-size:10px&quot;&gt;ROUTER · child of root&lt;/font&gt;" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#e8f0fe;strokeColor=#1a73e8;strokeWidth=2;fontSize=11;" vertex="1" parent="1"><mxGeometry x="#{router_x}" y="#{router_y}" width="#{router_width}" height="#{router_height}" as="geometry"/></mxCell>
    XML

    router_center_x = router_x + router_width / 2.0
    if row.zero?
      root_port_x = router_center_x
      root_entry_x = 0.5
      root_entry_y = 0.0
      root_points = [[root_port_x, y - 34.0], [root_port_x, router_y]]
      label_position = -0.68
      label_offset_x = 0
      label_offset_y = 0
    else
      root_port_x = x + card_width + 30.0
      root_entry_x = 0.5
      root_entry_y = 0.0
      root_points = [
        [root_port_x, route_gap_y],
        [router_center_x, route_gap_y],
        [router_center_x, router_y]
      ]
      label_position = 0
      label_offset_x = -118
      label_offset_y = -18
    end
    root_source_fraction = (root_port_x - root_x) / root_width.to_f
    root_label = "routes · #{router}"
    root_wrapped_label = wrap_edge_label(root_label, 32)
    root_label_width, root_label_height = edge_label_box(root_wrapped_label)
    root_edges << <<~XML
      <mxCell id="routing-root-edge-#{group_index}" value="#{html_lines(root_wrapped_label)}" semanticLabel="#{xml_escape(root_label)}" style="edgeStyle=none;rounded=0;html=1;whiteSpace=wrap;exitX=#{format('%.3f', root_source_fraction)};exitY=1;entryX=#{format('%.3f', root_entry_x)};entryY=#{format('%.3f', root_entry_y)};endArrow=block;endFill=1;strokeColor=#1a73e8;strokeWidth=2;fontSize=11;labelBackgroundColor=#ffffff;" edge="1" parent="1" source="routing-root" target="routing-router-#{group_index}">
        <mxGeometry x="#{label_position}" y="0" width="#{format('%.1f', root_label_width)}" height="#{format('%.1f', root_label_height)}" relative="1" as="geometry">
          <mxPoint x="#{label_offset_x}" y="#{label_offset_y}" as="offset"/>
          <Array as="points">
            #{root_points.map { |point_x, point_y| %(<mxPoint x="#{format('%.1f', point_x)}" y="#{format('%.1f', point_y)}"/>) }.join("\n            ")}
          </Array>
        </mxGeometry>
      </mxCell>
    XML
    displayed_leaves = leaves.empty? ? ['AUD-001–AUD-014 + AUD-A01–AUD-A10'] : leaves
    displayed_leaves.each_with_index do |leaf, leaf_index|
      leaf_id = "routing-leaf-#{group_index}-#{leaf_index}"
      leaf_y = y + 58 + leaf_index * 74
      nodes << <<~XML
        <mxCell id="#{leaf_id}" value="#{leaves.empty? ? '&lt;b&gt;Direct audit contracts&lt;/b&gt;&lt;br&gt;' : ''}#{xml_escape(leaf)}" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#fff7e0;strokeColor=#f9ab00;fontSize=10;" vertex="1" parent="1"><mxGeometry x="#{x + 315}" y="#{leaf_y}" width="135" height="58" as="geometry"/></mxCell>
      XML
      source_fraction = (leaf_index + 1.0) / (displayed_leaves.length + 1.0)
      route_label = leaves.empty? ? 'owns · audit contracts and scenarios' : "delegates · #{leaf}"
      visible_route_label = leaves.empty? ? 'owns · audit contracts' : 'delegates · contract'
      route_wrapped_label = wrap_edge_label(visible_route_label, 24)
      route_label_width, route_label_height = edge_label_box(route_wrapped_label)
      edges << <<~XML
        <mxCell id="routing-edge-#{group_index}-#{leaf_index}" value="#{html_lines(route_wrapped_label)}" semanticLabel="#{xml_escape(route_label)}" style="edgeStyle=none;html=1;whiteSpace=wrap;exitX=1;exitY=#{format('%.3f', source_fraction)};entryX=0;entryY=0.5;endArrow=block;endFill=1;strokeColor=#5f6368;strokeWidth=1;fontSize=10;labelBackgroundColor=#ffffff;" edge="1" parent="1" source="routing-router-#{group_index}" target="#{leaf_id}"><mxGeometry width="#{format('%.1f', route_label_width)}" height="#{format('%.1f', route_label_height)}" relative="1" as="geometry"/></mxCell>
      XML
    end
  end
  <<~XML
    <?xml version="1.0" encoding="UTF-8"?>
    <mxfile host="app.diagrams.net" modified="2026-07-21T00:00:00.000Z" agent="Codex" version="24.7.17" type="device" diagramTemplateVersion="#{DIAGRAM_TEMPLATE_VERSION}">
      <diagram id="implementation-routing" name="Skill Routing Hierarchy">
        <mxGraphModel dx="#{page_width}" dy="#{page_height}" grid="1" gridSize="10" guides="1" tooltips="1" connect="1" arrows="1" fold="1" page="1" pageScale="1" pageWidth="#{page_width}" pageHeight="#{page_height}" math="0" shadow="0">
          <root>
            <mxCell id="0"/>
            <mxCell id="1" parent="0"/>
            <mxCell id="routing-title" value="&lt;b&gt;Implementation architecture skill routing&lt;/b&gt;" style="text;html=1;strokeColor=none;fillColor=none;fontSize=26;" vertex="1" parent="1"><mxGeometry x="45" y="25" width="1300" height="42" as="geometry"/></mxCell>
            <mxCell id="routing-context" value="&lt;b&gt;Context:&lt;/b&gt; AGENTS.md → implementation-architecture → behavioral router → focused leaf&lt;br&gt;&lt;b&gt;Question:&lt;/b&gt; Which skill owns the implementation task, and which narrower contracts must be loaded next?" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#f8f9fa;strokeColor=#bdc1c6;fontSize=12;align=left;spacingLeft=12;" vertex="1" parent="1"><mxGeometry x="45" y="78" width="1375" height="70" as="geometry"/></mxCell>
            <mxCell id="routing-contracts" value="&lt;b&gt;Contracts&lt;/b&gt; · ARCH-001 · ARCH-DGM-003 · ARCH-DGM-012 · ARCH-DGM-020 · AUD-003&lt;br&gt;&lt;b&gt;Collaborating owners&lt;/b&gt; · AGENTS.md · skill-authoring · every routed implementation skill" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#fff7e0;strokeColor=#f9ab00;fontSize=10;align=left;spacingLeft=12;" vertex="1" parent="1"><mxGeometry x="1445" y="78" width="710" height="70" as="geometry"/></mxCell>
            <mxCell id="routing-root" value="&lt;b&gt;implementation-architecture&lt;/b&gt;&lt;br&gt;&lt;font style=&quot;font-size:11px&quot;&gt;ROOT ROUTER · whole-product position, dependency direction, and domain handoff · eight distinct routing ports&lt;/font&gt;" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#1a73e8;strokeColor=#174ea6;fontColor=#ffffff;strokeWidth=2;fontSize=15;" vertex="1" parent="1"><mxGeometry x="#{root_x}" y="#{root_y}" width="#{root_width}" height="#{root_height}" as="geometry"/></mxCell>
            #{cards.join}
            #{root_edges.join}
            #{edges.join}
            #{nodes.join}
            <mxCell id="routing-authority" value="&lt;b&gt;Authority&lt;/b&gt; · The blue root owns whole-product placement; every labeled blue path is one direct root route with its own port and corridor; no blue paths merge. Each card is one behavioral router; amber leaves own focused details; the audit router owns its contracts directly. AGENTS.md and SKILL.md prose decides when to load each route.&lt;br&gt;&lt;b&gt;Line grammar&lt;/b&gt; · SOLID BLUE — root routes domain owner · SOLID GRAY — router delegates focused contract · arrowheads point toward the skill to load." style="rounded=1;whiteSpace=wrap;html=1;fillColor=#fce8e6;strokeColor=#d93025;fontSize=11;align=left;spacingLeft=12;" vertex="1" parent="1"><mxGeometry x="45" y="1260" width="2110" height="80" as="geometry"/></mxCell>
          </root>
        </mxGraphModel>
      </diagram>
    </mxfile>
  XML
end

def generated_outputs
  outputs = {}
  SPECS.each do |skill, (title, nodes, edges)|
    outputs[File.join(SKILLS, skill, 'assets/architecture.drawio')] = diagram_xml(skill, title, nodes, edges)
  end
  outputs[File.join(SKILLS, 'implementation-architecture/assets/skill-routing.drawio')] = routing_xml
  outputs
end

def main
  missing = SPECS.keys.reject { |skill| File.file?(File.join(SKILLS, skill, 'SKILL.md')) }
  abort "missing initialized skills: #{missing.join(', ')}" unless missing.empty?
  outputs = generated_outputs
  if ARGV.include?('--check')
    stale = outputs.keys.reject { |path| File.file?(path) && File.binread(path) == outputs.fetch(path).b }
    abort "generated Draw.io documents are stale: #{stale.join(', ')}" unless stale.empty?
    puts "generated Draw.io documents current: #{outputs.length} files, #{SPECS.length * 2 + 1} pages"
    return
  end
  outputs.each do |path, body|
    FileUtils.mkdir_p(File.dirname(path))
    File.binwrite(path, body)
  end
  puts "generated #{outputs.length} Draw.io architecture documents (#{SPECS.length * 2 + 1} pages)"
end

main if __FILE__ == $PROGRAM_NAME
