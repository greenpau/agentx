#!/usr/bin/env ruby
# frozen_string_literal: true

require 'digest'
require 'cgi'
require 'pathname'
require 'rexml/document'
require 'set'
require 'tmpdir'
require_relative 'source_inventory'

ROOT = File.expand_path('../../../..', __dir__)
SKILL_ROOT = File.join(ROOT, '.codex/skills')
LEDGER = File.join(SKILL_ROOT, 'implementation-conformance-audit/references/source-coverage.tsv')
TRACE = File.join(SKILL_ROOT, 'implementation-conformance-audit/references/source-contract-trace.tsv')
CONTRACT_COVERAGE = File.join(SKILL_ROOT, 'implementation-conformance-audit/references/contract-scenario-coverage.tsv')
SOURCE_EXTENSIONS = %w[
  .ts .tsx .js .jsx .mts .cts .mjs .cjs
  .sh .bash .zsh .rb .py .go .rs .java .kt .swift .c .h .cc .cpp .cs
].freeze
TRACE_COVERAGE_CLASSES = %w[
  normative client-observed inferred intentional-divergence opaque-optional
  excluded unreviewed
].freeze

def live_production_go_sources(root)
  patterns = [
    File.join(root, '*.go'),
    File.join(root, 'pkg/**/*.go')
  ]
  patterns.flat_map { |pattern| Dir.glob(pattern, File::FNM_DOTMATCH) }
          .select { |path| File.file?(path) && !path.end_with?('_test.go') }
          .map { |path| path.delete_prefix("#{root}/") }
          .uniq
          .sort
          .to_set
end

def source_set_errors(source_files, ledger_paths)
  errors = []
  errors << 'production Go source inventory is empty' if source_files.empty?
  (source_files - ledger_paths).sort.each { |path| errors << "unmapped source artifact: #{path}" }
  (ledger_paths - source_files).sort.each { |path| errors << "ledger contains non-source artifact: #{path}" }
  errors
end

def trace_set_errors(ledger_paths, trace_paths)
  errors = []
  (ledger_paths - trace_paths).sort.each { |path| errors << "source artifact lacks reviewed contract trace: #{path}" }
  (trace_paths - ledger_paths).sort.each { |path| errors << "review trace contains non-source artifact: #{path}" }
  errors
end

def fingerprint_errors(root, path, expected_lines, expected_bytes, expected_sha)
  absolute = File.join(root, path)
  return ["source missing: #{path}"] unless File.file?(absolute)

  data = File.binread(absolute)
  actual_lines = data.empty? ? 0 : data.count("\n") + (data.end_with?("\n") ? 0 : 1)
  errors = []
  errors << "#{path}: ledger line count stale" unless actual_lines == expected_lines.to_i
  errors << "#{path}: ledger byte count stale" unless data.bytesize == expected_bytes.to_i
  errors << "#{path}: ledger hash stale" unless Digest::SHA256.hexdigest(data) == expected_sha
  errors
end

def fingerprint_shape_errors(label, expected_lines, expected_bytes, expected_sha)
  errors = []
  unsigned_decimal = /\A(?:0|[1-9][0-9]*)\z/
  errors << "#{label}: invalid line count #{expected_lines.inspect}" unless expected_lines.match?(unsigned_decimal)
  errors << "#{label}: invalid byte count #{expected_bytes.inspect}" unless expected_bytes.match?(unsigned_decimal)
  errors << "#{label}: invalid SHA-256 #{expected_sha.inspect}" unless expected_sha.match?(/\A[0-9a-f]{64}\z/)
  errors
end

def record_unique_row(rows, path, fields, label, line_number, errors)
  errors << "#{label}:#{line_number}: duplicate #{path}" if rows.key?(path)
  rows[path] = fields
end

def stable_definition_ids(line)
  suffix = '[A-Z]?[0-9]{2,3}[A-Z]?'
  ids = []
  ids.concat(line.scan(/\*\*([A-Z][A-Z0-9-]*-#{suffix})\b/).flatten)
  ids.concat(line.scan(/^`([A-Z][A-Z0-9-]*-#{suffix})`\s+—/).flatten)
  ids.concat(line.scan(/^\|\s*([A-Z][A-Z0-9-]*-#{suffix})\s*\|/).flatten)
  ids.concat(line.scan(/^\|\s*`([A-Z][A-Z0-9-]*-#{suffix})`\s*\|/).flatten)
  ids.concat(line.scan(/^\x23{2,}\s+`([A-Z][A-Z0-9-]*-#{suffix})`/).flatten)
  ids.uniq
end

def ledger_authority_errors(path, actual_owner, actual_scope)
  expected_owner = SourceInventory.owner_for(path)
  return ["#{path}: no behavioral authority classification"] if expected_owner.nil?

  errors = []
  if actual_owner != expected_owner
    errors << "#{path}: expected authority owner #{expected_owner}, found #{actual_owner}"
  end
  expected_scope = SourceInventory::SCOPE.fetch(expected_owner)
  if actual_scope != expected_scope
    errors << "#{path}: contract scope differs from authority classifier"
  end
  errors
end

def trace_coverage_class_errors(label, coverage_class, boundary_reason)
  errors = []
  unless TRACE_COVERAGE_CLASSES.include?(coverage_class)
    return ["#{label}: invalid coverage class #{coverage_class.inspect}"]
  end

  errors << "#{label}: artifact remains unreviewed" if coverage_class == 'unreviewed'
  if coverage_class == 'normative'
    errors << "#{label}: normative boundary reason must use '-'" unless boundary_reason == '-'
  elsif boundary_reason.empty? || boundary_reason == '-'
    errors << "#{label}: #{coverage_class} row needs a boundary reason"
  end
  errors
end

def duplicate_anchor_errors(anchors, label, kind)
  seen = Set.new
  anchors.each_with_object([]) do |anchor, errors|
    errors << "#{label}: duplicate #{kind} anchor #{anchor}" if seen.include?(anchor)
    seen << anchor
  end
end

def trace_anchor_errors(label:, contracts:, scenarios:, stable_ids:, owner:, expected_suite:, summary_anchor:)
  errors = []
  errors << "#{label}: no contract anchors" if contracts.empty?
  errors << "#{label}: no scenario suites" if scenarios.empty?
  errors.concat(duplicate_anchor_errors(contracts, label, 'contract'))
  errors.concat(duplicate_anchor_errors(scenarios, label, 'scenario'))

  contracts.each do |contract_id|
    record = stable_ids[contract_id]
    if record.nil?
      errors << "#{label}: unknown contract anchor #{contract_id}"
    elsif record[1] != 'contract'
      errors << "#{label}: contract anchor #{contract_id} is a #{record[1]}, not a contract"
    end
  end
  scenarios.each do |scenario_id|
    record = stable_ids[scenario_id]
    if record.nil?
      errors << "#{label}: unknown scenario suite #{scenario_id}"
    elsif record[1] != 'scenario'
      errors << "#{label}: scenario anchor #{scenario_id} is a #{record[1]}, not a scenario"
    end
  end

  recognized_contracts = contracts.select do |contract_id|
    stable_ids[contract_id]&.[](1) == 'contract'
  end
  primary_contracts = recognized_contracts.select do |contract_id|
    stable_ids[contract_id][2] == owner
  end
  if primary_contracts.empty?
    errors << "#{label}: no contract anchor is defined by primary owner #{owner}"
  elsif summary_anchor && (primary_contracts.uniq - [summary_anchor]).empty?
    errors << "#{label}: generic primary-owner summary-only trace #{summary_anchor}; bind the artifact to a narrower reviewed behavior"
  end
  errors << "#{label}: missing owner scenario suite #{expected_suite}" unless scenarios.include?(expected_suite)
  required_suites = recognized_contracts.map { |contract_id| stable_ids[contract_id][3] }.uniq
  (required_suites - [expected_suite] - scenarios).each do |suite|
    errors << "#{label}: missing collaborating contract scenario suite #{suite}"
  end
  errors
end

def run_source_evidence_self_test!
  Dir.mktmpdir('agentx-source-evidence-') do |root|
    FileUtils.mkdir_p(File.join(root, 'pkg'))
    File.binwrite(File.join(root, 'main.go'), "package main\n")
    File.binwrite(File.join(root, 'main_test.go'), "package main\n")
    File.binwrite(File.join(root, 'pkg/platform_windows.go'), "package pkg\n")
    File.binwrite(File.join(root, 'pkg/generated.go'), "// Code generated by fixture; DO NOT EDIT.\npackage pkg\n")

    inventory = live_production_go_sources(root)
    expected = Set.new(%w[main.go pkg/generated.go pkg/platform_windows.go])
    raise "inventory mismatch: #{inventory.to_a.inspect}" unless inventory == expected

    builder_inventory = SourceInventory.production_go_files(root).to_set
    raise "builder inventory mismatch: #{builder_inventory.to_a.inspect}" unless builder_inventory == expected

    clean = source_set_errors(inventory, expected)
    raise "clean inventory failed: #{clean.inspect}" unless clean.empty?

    missing = source_set_errors(inventory, Set.new(%w[main.go pkg/platform_windows.go]))
    raise 'unmapped production source was not detected' unless missing == ['unmapped source artifact: pkg/generated.go']

    extra = source_set_errors(inventory, expected | Set.new(['pkg/ghost.go']))
    raise 'non-source ledger row was not detected' unless extra == ['ledger contains non-source artifact: pkg/ghost.go']

    data = File.binread(File.join(root, 'main.go'))
    stale = fingerprint_errors(root, 'main.go', 1, data.bytesize, '0' * 64)
    raise 'stale hash was not detected' unless stale == ['main.go: ledger hash stale']

    malformed_fingerprint = fingerprint_shape_errors('ledger:2', '1junk', '13', 'A' * 64)
    raise 'malformed fingerprint fields were accepted' unless malformed_fingerprint == [
      'ledger:2: invalid line count "1junk"',
      "ledger:2: invalid SHA-256 #{('A' * 64).inspect}"
    ]

    rows = {}
    duplicates = []
    record_unique_row(rows, 'main.go', ['first'], 'ledger', 2, duplicates)
    record_unique_row(rows, 'main.go', ['second'], 'ledger', 3, duplicates)
    raise 'duplicate ledger row was not detected' unless duplicates == ['ledger:3: duplicate main.go']

    trace_missing = trace_set_errors(expected, Set.new(%w[main.go pkg/platform_windows.go]))
    raise 'missing trace row was not detected' unless trace_missing == ['source artifact lacks reviewed contract trace: pkg/generated.go']

    trace_extra = trace_set_errors(expected, expected | Set.new(['pkg/ghost.go']))
    raise 'extra trace row was not detected' unless trace_extra == ['review trace contains non-source artifact: pkg/ghost.go']

    stable_ids = {
      'TP-001' => %w[TP-001 contract implementation-tool-protocol CONF-004 fixture:1],
      'TP-020' => %w[TP-020 contract implementation-tool-protocol CONF-004 fixture:2],
      'TP-A03' => %w[TP-A03 scenario implementation-tool-protocol CONF-004 fixture:3],
      'CONF-004' => %w[CONF-004 scenario implementation-tool-protocol CONF-004 fixture:4],
      'AUTH-014' => %w[AUTH-014 contract implementation-auth-network CONF-020 fixture:5]
    }
    valid_anchors = trace_anchor_errors(
      label: 'trace:2',
      contracts: %w[TP-020],
      scenarios: %w[CONF-004 TP-A03],
      stable_ids: stable_ids,
      owner: 'implementation-tool-protocol',
      expected_suite: 'CONF-004',
      summary_anchor: 'TP-001'
    )
    raise "valid trace anchors failed: #{valid_anchors.inspect}" unless valid_anchors.empty?

    wrong_kinds = trace_anchor_errors(
      label: 'trace:3',
      contracts: %w[TP-A03],
      scenarios: %w[CONF-004 TP-020],
      stable_ids: stable_ids,
      owner: 'implementation-tool-protocol',
      expected_suite: 'CONF-004',
      summary_anchor: 'TP-001'
    )
    raise 'contract/scenario anchor kinds were not enforced' unless
      wrong_kinds.include?('trace:3: contract anchor TP-A03 is a scenario, not a contract') &&
      wrong_kinds.include?('trace:3: scenario anchor TP-020 is a contract, not a scenario')

    summary_bypass = trace_anchor_errors(
      label: 'trace:4',
      contracts: %w[TP-001 TP-001 TP-A03],
      scenarios: %w[CONF-004],
      stable_ids: stable_ids,
      owner: 'implementation-tool-protocol',
      expected_suite: 'CONF-004',
      summary_anchor: 'TP-001'
    )
    raise 'duplicate/generic summary anchors bypassed trace validation' unless
      summary_bypass.include?('trace:4: duplicate contract anchor TP-001') &&
      summary_bypass.any? { |error| error.include?('generic primary-owner summary-only trace TP-001') }

    cross_domain_summary_bypass = trace_anchor_errors(
      label: 'trace:4-cross',
      contracts: %w[TP-001 AUTH-014],
      scenarios: %w[CONF-004 CONF-020],
      stable_ids: stable_ids,
      owner: 'implementation-tool-protocol',
      expected_suite: 'CONF-004',
      summary_anchor: 'TP-001'
    )
    raise 'cross-domain contract bypassed narrow primary-owner review' unless
      cross_domain_summary_bypass.include?(
        'trace:4-cross: generic primary-owner summary-only trace TP-001; bind the artifact to a narrower reviewed behavior'
      )

    missing_collaborator = trace_anchor_errors(
      label: 'trace:5',
      contracts: %w[TP-020 AUTH-014],
      scenarios: %w[CONF-004],
      stable_ids: stable_ids,
      owner: 'implementation-tool-protocol',
      expected_suite: 'CONF-004',
      summary_anchor: 'TP-001'
    )
    raise 'collaborating contract suite was not enforced' unless
      missing_collaborator.include?('trace:5: missing collaborating contract scenario suite CONF-020')

    clean_authority = ledger_authority_errors(
      'pkg/tool/executor.go',
      'implementation-tool-protocol',
      SourceInventory::SCOPE.fetch('implementation-tool-protocol')
    )
    raise "valid authority classification failed: #{clean_authority.inspect}" unless clean_authority.empty?
    stale_scope = ledger_authority_errors(
      'pkg/tool/executor.go',
      'implementation-tool-protocol',
      'arbitrary nonempty scope'
    )
    raise 'stale contract scope was not detected' unless
      stale_scope == ['pkg/tool/executor.go: contract scope differs from authority classifier']

    raise 'valid normative coverage class failed' unless
      trace_coverage_class_errors('trace:6', 'normative', '-').empty?
    raise 'normative reason text was accepted' unless
      trace_coverage_class_errors('trace:7', 'normative', 'stale rationale') ==
      ["trace:7: normative boundary reason must use '-'"]
    raise 'inferred row without rationale was accepted' unless
      trace_coverage_class_errors('trace:8', 'inferred', '-') ==
      ['trace:8: inferred row needs a boundary reason']

    trailing_ids = stable_definition_ids('**TP-053A — Bounded persisted-result retrieval.**')
    raise 'trailing-letter stable contract ID was not recognized' unless trailing_ids == ['TP-053A']

    empty = live_production_go_sources(File.join(root, 'empty'))
    raise 'empty source inventory was not detected' unless source_set_errors(empty, Set.new) == ['production Go source inventory is empty']
  end
  puts 'source evidence self-test passed'
end

if ARGV == ['--source-evidence-self-test']
  require 'fileutils'
  run_source_evidence_self_test!
  exit 0
end

DIAGRAM_TEMPLATE_VERSION = '2.0-context-flow'
GENERATED_CONTEXT_PAGE = '01 — Context & Boundaries'
GENERATED_FLOW_PAGE = '02 — Responsibility Flow'
GENERATED_CONTEXT_IDS = %w[
  ctx-breadcrumb ctx-question ctx-starts ctx-owns ctx-ends ctx-defers
  ctx-contracts ctx-authority ctx-lifecycle
].freeze
GENERATED_FLOW_IDS = %w[
  flow-breadcrumb flow-question flow-authority flow-contracts flow-defers
  flow-legend flow-owned-boundary
].freeze
CUSTOM_CONTEXT_VERSION = '2.1'
CUSTOM_ROUTE_VERSION = '2.2'
CUSTOM_LABEL_LAYOUT_VERSION = '2.3'
CUSTOM_CONTEXT_IDS = %w[
  custom-context-band custom-context-breadcrumb custom-context-lifecycle
  custom-context-question custom-context-starts custom-context-owns
  custom-context-ends custom-context-defers custom-context-contracts
  custom-context-links custom-context-legend custom-context-authority
].freeze
CUSTOM_CONTEXT_TEXT = {
  'custom-context-band' => /\ACUSTOM DIAGRAM CONTEXT\b/,
  'custom-context-breadcrumb' => /\AContext\s*:/i,
  'custom-context-lifecycle' => /\ALifecycle\s*:/i,
  'custom-context-question' => /\AQuestion\s*:/i,
  'custom-context-starts' => /\AStarts with\s*:/i,
  'custom-context-owns' => /\AOwns\s*:/i,
  'custom-context-ends' => /\AEnds with\s*:/i,
  'custom-context-defers' => /\ADefers to\s*:/i,
  'custom-context-contracts' => /\AContracts\s*:/i,
  'custom-context-links' => /\ABroader view\s*:/i,
  'custom-context-legend' => /\ALine grammar\s*:/i,
  'custom-context-authority' => /\AAuthority\s*:/i
}.freeze
NON_FUNCTIONAL_IDS = Set.new(
  GENERATED_CONTEXT_IDS + GENERATED_FLOW_IDS + CUSTOM_CONTEXT_IDS + %w[
    ctx-title ctx-template-version ctx-crosscut ctx-owner-boundary ctx-legend
    flow-title flow-reading-direction flow-compatibility routing-title
    routing-context routing-authority
  ]
).freeze
GEOMETRY_EPSILON = 0.01
NODE_OVERLAP_TOLERANCE = 2.0
NODE_INTERSECTION_INSET = 3.0
DRAWIO_RESERVED_CELL_IDS = Set.new(%w[
  __proto__ constructor hasOwnProperty isPrototypeOf propertyIsEnumerable
  toLocaleString toString valueOf length at concat copyWithin entries every
  fill filter find findIndex findLast findLastIndex flat flatMap forEach
  includes indexOf join keys lastIndexOf map pop push reduce reduceRight
  reverse shift slice some sort splice toReversed toSorted toSpliced unshift
  values with
]).freeze

def drawio_text(cell)
  CGI.unescapeHTML(cell.attributes['value'].to_s)
     .gsub(/<br\s*\/?\s*>/i, ' ')
     .gsub(/<[^>]+>/, ' ')
     .gsub(/\s+/, ' ')
     .strip
end

def drawio_style(cell)
  cell.attributes['style'].to_s.split(';').each_with_object({}) do |entry, result|
    next if entry.empty?
    key, value = entry.split('=', 2)
    result[key] = value.nil? ? true : value
  end
end

def numeric_attribute(element, name, default = 0.0)
  raw = element&.attributes&.[](name)
  raw.nil? || raw.empty? ? default : Float(raw)
rescue ArgumentError, TypeError
  default
end

def absolute_rectangle(cell, cells_by_id, cache, visiting = Set.new)
  id = cell.attributes['id']
  return cache[id] if !id.nil? && cache.key?(id)
  return nil if !id.nil? && visiting.include?(id)

  geometry = cell.elements['mxGeometry']
  return cache[id] = nil if geometry.nil? || geometry.attributes['relative'] == '1'

  next_visiting = visiting.dup
  next_visiting << id unless id.nil?
  x = numeric_attribute(geometry, 'x')
  y = numeric_attribute(geometry, 'y')
  parent = cells_by_id[cell.attributes['parent']]
  if parent && parent.attributes['vertex'] == '1'
    parent_rectangle = absolute_rectangle(parent, cells_by_id, cache, next_visiting)
    if parent_rectangle
      x += parent_rectangle[:x]
      y += parent_rectangle[:y]
    end
  end
  rectangle = {
    x: x,
    y: y,
    width: numeric_attribute(geometry, 'width'),
    height: numeric_attribute(geometry, 'height')
  }
  cache[id] = rectangle unless id.nil?
  rectangle
end

def ancestor_id?(possible_ancestor, cell_id, cells_by_id)
  current = cells_by_id[cell_id]
  seen = Set.new
  while current
    parent_id = current.attributes['parent']
    return true if parent_id == possible_ancestor
    break if parent_id.nil? || seen.include?(parent_id)
    seen << parent_id
    current = cells_by_id[parent_id]
  end
  false
end

def functional_vertex?(cell, rectangle)
  return false unless cell.attributes['vertex'] == '1'
  return false if rectangle.nil? || rectangle[:width] <= 0 || rectangle[:height] <= 0
  return false if NON_FUNCTIONAL_IDS.include?(cell.attributes['id'])

  style_text = cell.attributes['style'].to_s
  style = drawio_style(cell)
  return false if style_text.split(';').include?('text')
  return false if style_text.split(';').include?('group')
  return false if style.key?('swimlane') || style['container'] == '1'
  return false if style['shape'] == 'line'

  # Relative vertices are ports or edge labels. Tiny blank vertices are explicit
  # junctions, not responsibility nodes that should participate in overlap tests.
  geometry = cell.elements['mxGeometry']
  return false if geometry&.attributes&.[]('relative') == '1'
  return false if rectangle[:width] <= 16 && rectangle[:height] <= 16 && drawio_text(cell).empty?

  true
end

def rectangle_overlap(first, second)
  width = [first[:x] + first[:width], second[:x] + second[:width]].min -
          [first[:x], second[:x]].max
  height = [first[:y] + first[:height], second[:y] + second[:height]].min -
           [first[:y], second[:y]].max
  [width, height]
end

def explicit_edge_ports?(edge)
  style = drawio_style(edge)
  %w[exitX exitY entryX entryY].all? do |name|
    value = style[name]
    !value.nil? && value != true && !value.to_s.empty?
  end
end

def waypoint_elements(edge)
  geometry = edge.elements['mxGeometry']
  return [] if geometry.nil?
  arrays = geometry.get_elements('Array').select { |array| array.attributes['as'] == 'points' }
  arrays.flat_map { |array| array.get_elements('mxPoint') }
end

def edge_anchor(edge, endpoint, rectangle)
  return nil if rectangle.nil?
  style = drawio_style(edge)
  prefix = endpoint == :source ? 'exit' : 'entry'
  x_fraction = numeric_attribute_proxy(style["#{prefix}X"], 0.5)
  y_fraction = numeric_attribute_proxy(style["#{prefix}Y"], 0.5)
  [
    rectangle[:x] + rectangle[:width] * x_fraction,
    rectangle[:y] + rectangle[:height] * y_fraction
  ]
end

def numeric_attribute_proxy(raw, default)
  return default if raw.nil? || raw == true || raw.to_s.empty?
  Float(raw)
rescue ArgumentError, TypeError
  default
end

def edge_polyline(edge, cells_by_id, rectangle_cache)
  source = cells_by_id[edge.attributes['source']]
  target = cells_by_id[edge.attributes['target']]
  return [] if source.nil? || target.nil?

  source_rectangle = absolute_rectangle(source, cells_by_id, rectangle_cache)
  target_rectangle = absolute_rectangle(target, cells_by_id, rectangle_cache)
  return [] if source_rectangle.nil? || target_rectangle.nil?

  parent_origin = [0.0, 0.0]
  parent = cells_by_id[edge.attributes['parent']]
  if parent && parent.attributes['vertex'] == '1'
    parent_rectangle = absolute_rectangle(parent, cells_by_id, rectangle_cache)
    parent_origin = [parent_rectangle[:x], parent_rectangle[:y]] if parent_rectangle
  end
  waypoints = waypoint_elements(edge).map do |point|
    [
      parent_origin[0] + numeric_attribute(point, 'x'),
      parent_origin[1] + numeric_attribute(point, 'y')
    ]
  end
  source_anchor = edge_anchor(edge, :source, source_rectangle)
  target_anchor = edge_anchor(edge, :target, target_rectangle)

  # Automatic orthogonal routing is not serialized in Draw.io. Inspect every
  # explicit rail and every direct/axis-aligned relation whose path is
  # deterministic from the document. Do not invent a diagonal for an implicit
  # orthogonal route, which would create geometry false positives.
  if waypoints.empty?
    style = drawio_style(edge)
    axis_aligned = (source_anchor[0] - target_anchor[0]).abs <= GEOMETRY_EPSILON ||
                   (source_anchor[1] - target_anchor[1]).abs <= GEOMETRY_EPSILON
    return [] unless style['edgeStyle'].to_s == 'none' || axis_aligned
  end
  [source_anchor] + waypoints + [target_anchor]
end

def segment_crosses_rectangle_interior?(start_point, end_point, rectangle)
  left = rectangle[:x] + NODE_INTERSECTION_INSET
  right = rectangle[:x] + rectangle[:width] - NODE_INTERSECTION_INSET
  top = rectangle[:y] + NODE_INTERSECTION_INSET
  bottom = rectangle[:y] + rectangle[:height] - NODE_INTERSECTION_INSET
  return false if right <= left || bottom <= top

  delta_x = end_point[0] - start_point[0]
  delta_y = end_point[1] - start_point[1]
  t_min = 0.0
  t_max = 1.0
  [
    [-delta_x, start_point[0] - left],
    [delta_x, right - start_point[0]],
    [-delta_y, start_point[1] - top],
    [delta_y, bottom - start_point[1]]
  ].each do |coefficient, distance|
    if coefficient.abs <= GEOMETRY_EPSILON
      return false if distance < 0
      next
    end
    ratio = distance.to_f / coefficient
    if coefficient < 0
      t_min = [t_min, ratio].max
    else
      t_max = [t_max, ratio].min
    end
    return false if t_min - t_max > GEOMETRY_EPSILON
  end
  t_max - t_min > GEOMETRY_EPSILON
end

def edge_endpoint_side(edge, endpoint)
  style = drawio_style(edge)
  prefix = endpoint == :source ? 'exit' : 'entry'
  x = numeric_attribute_proxy(style["#{prefix}X"], 0.5)
  y = numeric_attribute_proxy(style["#{prefix}Y"], 0.5)
  return :left if x.abs <= GEOMETRY_EPSILON && y > GEOMETRY_EPSILON && y < 1 - GEOMETRY_EPSILON
  return :right if (x - 1).abs <= GEOMETRY_EPSILON && y > GEOMETRY_EPSILON && y < 1 - GEOMETRY_EPSILON
  return :top if y.abs <= GEOMETRY_EPSILON
  return :bottom if (y - 1).abs <= GEOMETRY_EPSILON
  return :left if x.abs <= GEOMETRY_EPSILON
  return :right if (x - 1).abs <= GEOMETRY_EPSILON

  nil
end

def departs_boundary_outward?(anchor, next_point, side)
  case side
  when :left then next_point[0] <= anchor[0] + GEOMETRY_EPSILON
  when :right then next_point[0] >= anchor[0] - GEOMETRY_EPSILON
  when :top then next_point[1] <= anchor[1] + GEOMETRY_EPSILON
  when :bottom then next_point[1] >= anchor[1] - GEOMETRY_EPSILON
  else false
  end
end

def incident_path_clear?(edge, polyline, source_rectangle, target_rectangle)
  return false if polyline.length < 2
  return false unless departs_boundary_outward?(
    polyline.first,
    polyline[1],
    edge_endpoint_side(edge, :source)
  )
  return false unless departs_boundary_outward?(
    polyline.last,
    polyline[-2],
    edge_endpoint_side(edge, :target)
  )

  source_reentry = polyline.each_cons(2).drop(1).any? do |start_point, end_point|
    segment_crosses_rectangle_interior?(start_point, end_point, source_rectangle)
  end
  target_reentry = polyline.each_cons(2).to_a[0...-1].any? do |start_point, end_point|
    segment_crosses_rectangle_interior?(start_point, end_point, target_rectangle)
  end
  !source_reentry && !target_reentry
end

def collinear_overlap_length(first_segment, second_segment)
  first_start, first_end = first_segment
  second_start, second_end = second_segment
  first_vector = [first_end[0] - first_start[0], first_end[1] - first_start[1]]
  second_vector = [second_end[0] - second_start[0], second_end[1] - second_start[1]]
  first_length = Math.hypot(first_vector[0], first_vector[1])
  second_length = Math.hypot(second_vector[0], second_vector[1])
  return 0.0 if first_length <= GEOMETRY_EPSILON || second_length <= GEOMETRY_EPSILON

  vector_cross = first_vector[0] * second_vector[1] - first_vector[1] * second_vector[0]
  offset = [second_start[0] - first_start[0], second_start[1] - first_start[1]]
  offset_cross = first_vector[0] * offset[1] - first_vector[1] * offset[0]
  scale = [first_length * second_length, 1.0].max
  return 0.0 if vector_cross.abs > GEOMETRY_EPSILON * scale
  return 0.0 if offset_cross.abs > GEOMETRY_EPSILON * [first_length, 1.0].max

  axis = first_vector[0].abs >= first_vector[1].abs ? 0 : 1
  first_min, first_max = [first_start[axis], first_end[axis]].minmax
  second_min, second_max = [second_start[axis], second_end[axis]].minmax
  projected = [first_max, second_max].min - [first_min, second_min].max
  return 0.0 unless projected > GEOMETRY_EPSILON

  projected * first_length / first_vector[axis].abs
end

errors = []

skill_files = Dir.glob(File.join(SKILL_ROOT, '*/SKILL.md')).sort
skill_by_path = skill_files.to_h { |path| [File.realpath(path), File.basename(File.dirname(path))] }

skill_files.each do |path|
  name = File.basename(File.dirname(path))
  text = File.read(path)
  frontmatter = text.match(/\A---\n(.*?)\n---\n/m)&.captures&.first
  if frontmatter.nil?
    errors << "#{path}: missing frontmatter"
    next
  end
  keys = frontmatter.scan(/^([a-zA-Z0-9_-]+):/).flatten
  errors << "#{path}: frontmatter keys must be name and description" unless keys.sort == %w[description name]
  declared = frontmatter[/^name:\s*(.+)$/, 1]&.strip
  errors << "#{path}: name #{declared.inspect} does not match directory #{name}" unless declared == name
  errors << "#{path}: contains placeholder text" if text.match?(/\bTODO\b|\[TODO|Structuring This Skill/)

  metadata = File.join(File.dirname(path), 'agents/openai.yaml')
  if !File.file?(metadata)
    errors << "#{path}: missing agents/openai.yaml"
  else
    meta_text = File.read(metadata)
    errors << "#{metadata}: missing $#{name} default prompt" unless meta_text.include?("$#{name}")
    errors << "#{metadata}: contains placeholder text" if meta_text.match?(/\bTODO\b|\[TODO/)
  end

  next unless name.start_with?('implementation-')

  linked = text.scan(/\((assets\/[^)]+\.drawio)\)/).flatten
  errors << "#{path}: must link a Draw.io asset" if linked.empty?
  diagrams = Dir.glob(File.join(File.dirname(path), 'assets/*.drawio'))
  errors << "#{path}: no Draw.io asset exists" if diagrams.empty?
  diagrams.each do |diagram|
    begin
      xml = REXML::Document.new(File.read(diagram))
      root = xml.root
      pages = root&.get_elements('diagram') || []
      generated_architecture = File.basename(diagram) == 'architecture.drawio'
      generated_routing = File.basename(diagram) == 'skill-routing.drawio'
      custom_diagram = !generated_architecture && !generated_routing
      valid_page = pages.any? { |page| !page.get_elements('mxGraphModel').empty? }
      errors << "#{diagram}: expected mxfile with a diagram/mxGraphModel page" unless root&.name == 'mxfile' && valid_page

      if generated_architecture
        actual_version = root&.attributes&.[]('diagramTemplateVersion')
        unless actual_version == DIAGRAM_TEMPLATE_VERSION
          errors << "#{diagram}: generated architecture requires diagramTemplateVersion=#{DIAGRAM_TEMPLATE_VERSION.inspect}"
        end
        page_names = pages.map { |page| CGI.unescapeHTML(page.attributes['name'].to_s) }
        required_names = [GENERATED_CONTEXT_PAGE, GENERATED_FLOW_PAGE]
        unless page_names == required_names
          errors << "#{diagram}: generated architecture pages must be exactly #{required_names.join(' then ')}"
        end
      elsif generated_routing
        actual_version = root&.attributes&.[]('diagramTemplateVersion')
        unless actual_version == DIAGRAM_TEMPLATE_VERSION
          errors << "#{diagram}: generated routing requires diagramTemplateVersion=#{DIAGRAM_TEMPLATE_VERSION.inspect}"
        end
      end

      cells = pages.flat_map { |page| page.get_elements('.//mxCell') }
      vertices = cells.count { |cell| cell.attributes['vertex'] == '1' }
      edges = cells.count { |cell| cell.attributes['edge'] == '1' }
      errors << "#{diagram}: diagram is too small to express an architecture" if vertices < 4 || edges < 3
      pages.each_with_index do |page, page_index|
        page_name = CGI.unescapeHTML(page.attributes['name'].to_s)
        page_label = "page #{page_index + 1} (#{page_name.inspect})"
        graph_model = page.elements['mxGraphModel']
        page_cells = page.get_elements('.//mxCell')
        id_counts = Hash.new(0)
        page_cells.each do |cell|
          id = cell.attributes['id']
          id_counts[id] += 1 unless id.nil?
        end
        duplicate_ids = id_counts.select { |_id, count| count > 1 }.keys
        unless duplicate_ids.empty?
          errors << "#{diagram}: page #{page_index + 1} has duplicate cell IDs #{duplicate_ids.join(',')}"
        end
        reserved_ids = id_counts.keys.compact.select { |id| DRAWIO_RESERVED_CELL_IDS.include?(id) }
        unless reserved_ids.empty?
          errors << "#{diagram}: #{page_label} uses Draw.io codec-reserved cell IDs #{reserved_ids.join(',')}"
        end
        known_ids = id_counts.keys.to_set
        cells_by_id = page_cells.each_with_object({}) do |cell, result|
          id = cell.attributes['id']
          result[id] = cell unless id.nil?
        end

        if generated_architecture
          required_ids = if page_name == GENERATED_CONTEXT_PAGE
                           GENERATED_CONTEXT_IDS
                         elsif page_name == GENERATED_FLOW_PAGE
                           GENERATED_FLOW_IDS
                         else
                           []
                         end
          missing_ids = required_ids.reject { |id| known_ids.include?(id) }
          unless missing_ids.empty?
            errors << "#{diagram}: #{page_label} lacks generated context cells #{missing_ids.join(',')}"
          end
          staged_cells = page_cells.select { |cell| drawio_text(cell).match?(/\bstage\s+\d{1,2}\b/i) }
          unless staged_cells.empty?
            errors << "#{diagram}: #{page_label} uses misleading stage NN labels in #{staged_cells.map { |cell| cell.attributes['id'] }.join(',')}"
          end
        elsif generated_routing
          routing_ids = %w[routing-root routing-context routing-authority]
          missing_ids = routing_ids.reject { |id| known_ids.include?(id) }
          unless missing_ids.empty?
            errors << "#{diagram}: #{page_label} lacks generated routing context cells #{missing_ids.join(',')}"
          end
        elsif custom_diagram
          unless graph_model&.attributes&.[]('customDiagramContextVersion') == CUSTOM_CONTEXT_VERSION
            errors << "#{diagram}: #{page_label} requires customDiagramContextVersion=#{CUSTOM_CONTEXT_VERSION.inspect}"
          end
          missing_ids = CUSTOM_CONTEXT_IDS.reject { |id| known_ids.include?(id) }
          unless missing_ids.empty?
            errors << "#{diagram}: #{page_label} lacks custom context cells #{missing_ids.join(',')}"
          end
          CUSTOM_CONTEXT_TEXT.each do |cell_id, pattern|
            marker_cell = cells_by_id[cell_id]
            next if marker_cell.nil?
            unless drawio_text(marker_cell).match?(pattern)
              errors << "#{diagram}: #{page_label} context cell #{cell_id} lacks its standardized visible marker"
            end
          end
          contracts_text = drawio_text(cells_by_id['custom-context-contracts'])
          unless contracts_text.match?(/\b[A-Z][A-Z0-9-]*-[A-Z]?[0-9]{2,3}[A-Z]?\b/) && contracts_text.match?(/\.md\b/)
            errors << "#{diagram}: #{page_label} custom context lacks a stable contract ID and named prose file"
          end
          %w[custom-context-starts custom-context-ends].each do |boundary_id|
            boundary_text = drawio_text(cells_by_id[boundary_id])
            if boundary_text.match?(/event\s*\/\s*state shown below|owning skill contracts/i)
              errors << "#{diagram}: #{page_label} context cell #{boundary_id} uses a non-actionable fallback"
            end
          end
          defers_text = drawio_text(cells_by_id['custom-context-defers'])
          deferred_skills = defers_text.scan(/\bimplementation-[a-z0-9-]+\b/).uniq
          if deferred_skills.empty? || deferred_skills.any? { |name| !File.directory?(File.join(SKILL_ROOT, name)) }
            errors << "#{diagram}: #{page_label} Defers to must name an existing implementation skill"
          end
        end

        page_edges = page_cells.select { |cell| cell.attributes['edge'] == '1' }
        page_edges.each do |edge|
          %w[source target].each do |endpoint|
            target_id = edge.attributes[endpoint]
            if target_id.nil?
              errors << "#{diagram}: edge #{edge.attributes['id']} lacks #{endpoint}"
            elsif !known_ids.include?(target_id)
              errors << "#{diagram}: edge #{edge.attributes['id']} has unknown #{endpoint} #{target_id}"
            end
          end
          if drawio_text(edge).empty?
            errors << "#{diagram}: #{page_label} edge #{edge.attributes['id']} lacks a visible semantic label"
          end

          edge_id = edge.attributes['id'].to_s
          if custom_diagram
            unless edge.attributes['customRouteVersion'] == CUSTOM_ROUTE_VERSION
              errors << "#{diagram}: #{page_label} edge #{edge_id} lacks verified custom route version #{CUSTOM_ROUTE_VERSION}"
            end
            unless edge.attributes['customLabelLayoutVersion'] == CUSTOM_LABEL_LAYOUT_VERSION
              errors << "#{diagram}: #{page_label} edge #{edge_id} lacks verified label layout version #{CUSTOM_LABEL_LAYOUT_VERSION}"
            end
            unless explicit_edge_ports?(edge)
              errors << "#{diagram}: #{page_label} edge #{edge_id} lacks explicit exit/entry ports"
            end
            style = drawio_style(edge)
            unless style['jumpStyle'] == 'arc' && style['labelBackgroundColor'] == '#ffffff' &&
                   !style['labelBorderColor'].to_s.empty?
              errors << "#{diagram}: #{page_label} edge #{edge_id} lacks crossing and label-contrast safeguards"
            end
            semantic_version = edge.attributes['customSemanticLabelVersion'].to_s
            unless semantic_version.empty?
              unless edge.attributes['customSemanticLabelOrigin'] == 'topology-derived' &&
                     drawio_text(edge).start_with?('handoff · ')
                errors << "#{diagram}: #{page_label} inferred edge #{edge_id} must expose a topology-only handoff label"
              end
            end
          end
          generated_behavior_edge = generated_architecture ||
                                    (generated_routing && edge_id.start_with?('routing-edge-'))
          if generated_behavior_edge && !explicit_edge_ports?(edge)
            errors << "#{diagram}: #{page_label} edge #{edge_id} lacks explicit exit/entry ports"
          end
          if generated_architecture && edge_id.start_with?('flow-edge-')
            style = drawio_style(edge)
            non_direct_rail = style['edgeStyle'].to_s != 'none'
            if non_direct_rail && waypoint_elements(edge).length < 2
              errors << "#{diagram}: #{page_label} non-direct rail #{edge_id} requires at least two explicit waypoints"
            end
          end
        end

        rectangle_cache = {}
        functional_nodes = page_cells.each_with_object([]) do |cell, result|
          rectangle = absolute_rectangle(cell, cells_by_id, rectangle_cache)
          result << [cell, rectangle] if functional_vertex?(cell, rectangle)
        end
        functional_nodes.combination(2).each do |(first_cell, first_rectangle), (second_cell, second_rectangle)|
          first_id = first_cell.attributes['id']
          second_id = second_cell.attributes['id']
          next if ancestor_id?(first_id, second_id, cells_by_id)
          next if ancestor_id?(second_id, first_id, cells_by_id)
          overlap_width, overlap_height = rectangle_overlap(first_rectangle, second_rectangle)
          next unless overlap_width > NODE_OVERLAP_TOLERANCE &&
                      overlap_height > NODE_OVERLAP_TOLERANCE
          errors << format(
            '%s: %s functional nodes %s and %s overlap by %.1fx%.1f',
            diagram,
            page_label,
            first_id,
            second_id,
            overlap_width,
            overlap_height
          )
        end

        page_edges.each do |edge|
          edge_id = edge.attributes['id'].to_s
          polyline = edge_polyline(edge, cells_by_id, rectangle_cache)
          next if polyline.length < 2
          source_id = edge.attributes['source']
          target_id = edge.attributes['target']
          source_rectangle = absolute_rectangle(cells_by_id[source_id], cells_by_id, rectangle_cache)
          target_rectangle = absolute_rectangle(cells_by_id[target_id], cells_by_id, rectangle_cache)
          if source_rectangle && target_rectangle &&
             !incident_path_clear?(edge, polyline, source_rectangle, target_rectangle)
            errors << "#{diagram}: #{page_label} edge #{edge_id} traverses its own source or target node"
          end
          intersected_ids = Set.new
          polyline.each_cons(2) do |start_point, end_point|
            next if (start_point[0] - end_point[0]).abs <= GEOMETRY_EPSILON &&
                    (start_point[1] - end_point[1]).abs <= GEOMETRY_EPSILON
            functional_nodes.each do |node, rectangle|
              node_id = node.attributes['id']
              next if node_id == source_id || node_id == target_id
              next if ancestor_id?(node_id, source_id, cells_by_id) ||
                      ancestor_id?(node_id, target_id, cells_by_id)
              next if ancestor_id?(source_id, node_id, cells_by_id) ||
                      ancestor_id?(target_id, node_id, cells_by_id)
              if segment_crosses_rectangle_interior?(start_point, end_point, rectangle)
                intersected_ids << node_id
              end
            end
          end
          intersected_ids.sort.each do |node_id|
            errors << "#{diagram}: #{page_label} edge #{edge_id} crosses unrelated functional node #{node_id}"
          end
        end


        explicit_polylines = page_edges.each_with_object([]) do |edge, result|
          polyline = edge_polyline(edge, cells_by_id, rectangle_cache)
          result << [edge.attributes['id'].to_s, polyline] if polyline.length >= 2
        end
        explicit_polylines.combination(2).each do |(first_id, first_path), (second_id, second_path)|
          maximum_overlap = first_path.each_cons(2).flat_map do |first_segment|
            second_path.each_cons(2).map do |second_segment|
              collinear_overlap_length(first_segment, second_segment)
            end
          end.max.to_f
          if maximum_overlap > 0.5
            errors << format(
              '%s: %s edges %s and %s share %.1fpx of an unstated collinear merge',
              diagram,
              page_label,
              first_id,
              second_id,
              maximum_overlap
            )
          end
        end


      end
    rescue StandardError => e
      errors << "#{diagram}: invalid XML (#{e.message})"
    end
  end
  linked.each do |relative|
    target = File.join(File.dirname(path), relative)
    errors << "#{path}: linked diagram does not exist: #{relative}" unless File.file?(target)
  end
  diagrams.each do |diagram|
    relative = Pathname.new(diagram).relative_path_from(Pathname.new(File.dirname(path))).to_s
    errors << "#{path}: Draw.io asset is not linked: #{relative}" unless linked.include?(relative)
  end
end

# Verify supporting Markdown and Draw.io links, not only hierarchy routes. Skip
# fenced examples because the skill-authoring contract intentionally shows a
# non-existent placeholder route as syntax documentation.
markdown_files = skill_files + Dir.glob(File.join(SKILL_ROOT, '*/references/**/*.md')).sort
markdown_files.each do |source|
  in_fence = false
  File.readlines(source).each_with_index do |line, index|
    if line.match?(/^\s*```/)
      in_fence = !in_fence
      next
    end
    next if in_fence
    line.scan(/\[[^\]]+\]\(([^)]+)\)/).flatten.each do |raw_target|
      target = raw_target.strip.sub(/\A</, '').sub(/>\z/, '').split('#', 2).first
      next if target.nil? || target.empty? || target.match?(%r{\A(?:https?:|mailto:|data:)})
      next unless target.match?(/\.(?:md|drawio)\z/i)
      absolute = File.expand_path(target, File.dirname(source))
      errors << "#{source}:#{index + 1}: broken supporting link #{raw_target}" unless File.file?(absolute)
    end
  end
end

# The readable slug lexicon is user-visible normative data whose ordering and
# duplicates affect deterministic entropy tests. Verify that the standalone
# reference contains every specified token rather than only a prose summary.
lexicon_path = File.join(
  SKILL_ROOT,
  'implementation-state-context/references/readable-identifier-lexicon.md'
)
if File.file?(lexicon_path)
  lexicon_text = File.read(lexicon_path)
  {
    'ADJECTIVES' => 'adjective',
    'NOUNS' => 'noun',
    'VERBS' => 'verb'
  }.each do |source_name, heading_name|
    documented_body = lexicon_text[
      /## Canonical #{heading_name} lexicon\s+```text\s+(.*?)\s+```/m,
      1
    ]
    documented_tokens = documented_body&.split(/\s+/) || []
    errors << "#{lexicon_path}: canonical #{heading_name} lexicon is empty" if documented_tokens.empty?
  end
else
  errors << "missing exact readable-identifier lexicon"
end

Dir.glob(File.join(SKILL_ROOT, 'implementation-*/references/**/*.md')).sort.each do |source|
  lines = File.readlines(source)
  next unless lines.length > 100
  has_contents = lines.first(50).any? { |line| line.match?(/^## (?:Contents|Table of contents)\s*$/i) }
  errors << "#{source}: references over 100 lines require a linked top-level contents list" unless has_contents
end

# Contract identifiers are repository-wide anchors. Recognize the definition
# forms used by the hierarchy and reject a second definition in another domain;
# ordinary cross-references are intentionally ignored.
defined_ids = Hash.new { |hash, key| hash[key] = [] }
implementation_markdown = Dir.glob(
  File.join(SKILL_ROOT, 'implementation-*/{SKILL.md,references/**/*.md}')
).sort
implementation_markdown.each do |source|
  File.readlines(source).each_with_index do |line, index|
    stable_definition_ids(line).each { |id| defined_ids[id] << "#{source}:#{index + 1}" }
  end
end
defined_ids.each do |id, locations|
  errors << "duplicate contract definition #{id}: #{locations.join(', ')}" if locations.length > 1
end

# Every stable definition, not merely every leaf, must be bound explicitly to
# a parameterized conformance suite. The generated manifest is line-sensitive,
# so adding/moving a contract without refreshing scenario coverage fails.
covered_ids = {}
if !File.file?(CONTRACT_COVERAGE)
  errors << "missing contract-scenario coverage manifest: #{CONTRACT_COVERAGE}"
else
  coverage_lines = File.readlines(CONTRACT_COVERAGE, chomp: true)
  coverage_header = coverage_lines.shift
  expected_coverage_header = "id\tkind\towner_skill\tparameterized_suite\tdefinition_location"
  errors << "#{CONTRACT_COVERAGE}: invalid header" unless coverage_header == expected_coverage_header
  coverage_lines.each_with_index do |line, index|
    fields = line.split("\t", -1)
    if fields.length != 5
      errors << "#{CONTRACT_COVERAGE}:#{index + 2}: expected five fields"
      next
    end
    id, kind, owner, suite, relative_location = fields
    errors << "#{CONTRACT_COVERAGE}:#{index + 2}: duplicate #{id}" if covered_ids.key?(id)
    covered_ids[id] = fields
    errors << "#{CONTRACT_COVERAGE}:#{index + 2}: invalid kind #{kind.inspect}" unless %w[contract scenario].include?(kind)
    errors << "#{CONTRACT_COVERAGE}:#{index + 2}: owner skill missing: #{owner}" unless skill_by_path.value?(owner)
    errors << "#{CONTRACT_COVERAGE}:#{index + 2}: suite #{suite} is not a defined CONF scenario" unless suite.start_with?('CONF-') && defined_ids.key?(suite)
    expected_locations = defined_ids[id].map do |location|
      absolute, line_number = location.rpartition(':').values_at(0, 2)
      "#{Pathname.new(absolute).relative_path_from(Pathname.new(ROOT))}:#{line_number}"
    end
    if expected_locations.empty?
      errors << "#{CONTRACT_COVERAGE}:#{index + 2}: unknown stable ID #{id}"
    elsif !expected_locations.include?(relative_location)
      errors << "#{CONTRACT_COVERAGE}:#{index + 2}: stale definition location for #{id}"
    end
  end
  (defined_ids.keys.to_set - covered_ids.keys.to_set).sort.each do |id|
    errors << "stable ID lacks parameterized scenario coverage: #{id}"
  end
  (covered_ids.keys.to_set - defined_ids.keys.to_set).sort.each do |id|
    errors << "contract-scenario manifest contains undefined ID: #{id}"
  end
end

route_sources = [File.join(ROOT, 'AGENTS.md')] + skill_files
edges = Hash.new { |h, k| h[k] = [] }
root_targets = []
route_sources.each do |source|
  in_fence = false
  File.readlines(source).each_with_index do |line, index|
    if line.match?(/^\s*```/)
      in_fence = !in_fence
      next
    end
    next if in_fence
    # Hierarchy routes are standalone actionable statements to another skill.
    # Supporting references such as "Use [diagram](assets/...)" and literal
    # examples of the route syntax are deliberately not graph edges.
    next unless line.match?(/^\s*(?:-\s+)?Use \[/) && line.include?('SKILL.md)')
    match = line.match(/Use \[([^\]]+)\]\(([^)]+\/SKILL\.md)\) to (.+?)(?:\.|$)/)
    unless match
      errors << "#{source}:#{index + 1}: malformed actionable skill route"
      next
    end
    label, relative, action = match.captures
    target = File.expand_path(relative, File.dirname(source))
    unless File.file?(target)
      errors << "#{source}:#{index + 1}: broken route #{relative}"
      next
    end
    canonical = File.realpath(target)
    target_name = skill_by_path[canonical]
    errors << "#{source}:#{index + 1}: route label #{label} does not match #{target_name}" unless label == target_name
    errors << "#{source}:#{index + 1}: route action is too vague" if action.strip.split.length < 4
    if source.end_with?('/AGENTS.md')
      root_targets << target_name
    else
      edges[File.basename(File.dirname(source))] << target_name
    end
  end
end

reachable = Set.new
queue = root_targets.compact.dup
until queue.empty?
  node = queue.shift
  next if reachable.include?(node)
  reachable << node
  queue.concat(edges[node])
end
all_skills = skill_files.map { |p| File.basename(File.dirname(p)) }.to_set
(all_skills - reachable).sort.each { |name| errors << "unreachable skill from AGENTS.md: #{name}" }

visiting = Set.new
visited = Set.new
visit = lambda do |node|
  return if visited.include?(node)
  if visiting.include?(node)
    errors << "routing cycle reaches #{node}"
    return
  end
  visiting << node
  edges[node].each { |child| visit.call(child) }
  visiting.delete(node)
  visited << node
end
root_targets.compact.each { |node| visit.call(node) }

implementation_leaf_names = %w[
  implementation-startup-settings implementation-state-context implementation-query-model
  implementation-tool-protocol implementation-tool-catalog implementation-permissions-sandbox
  implementation-task-runtime implementation-terminal-engine implementation-interactive-repl
  implementation-headless-sdk implementation-optional-experiences implementation-commands-input
  implementation-skills-output implementation-plugins-hooks implementation-mcp-lsp
  implementation-transcript-recovery implementation-memory-compaction implementation-remote-bridge
  implementation-multi-agent implementation-auth-network implementation-platform-lifecycle
  implementation-observability implementation-conformance-audit
]

conformance_suite_by_owner = {
  'implementation-startup-settings' => 'CONF-001',
  'implementation-state-context' => 'CONF-002',
  'implementation-query-model' => 'CONF-003',
  'implementation-tool-protocol' => 'CONF-004',
  'implementation-tool-catalog' => 'CONF-005',
  'implementation-permissions-sandbox' => 'CONF-006',
  'implementation-task-runtime' => 'CONF-007',
  'implementation-terminal-engine' => 'CONF-008',
  'implementation-interactive-repl' => 'CONF-009',
  'implementation-headless-sdk' => 'CONF-010',
  'implementation-optional-experiences' => 'CONF-011',
  'implementation-commands-input' => 'CONF-012',
  'implementation-skills-output' => 'CONF-013',
  'implementation-plugins-hooks' => 'CONF-014',
  'implementation-mcp-lsp' => 'CONF-015',
  'implementation-transcript-recovery' => 'CONF-016',
  'implementation-memory-compaction' => 'CONF-017',
  'implementation-remote-bridge' => 'CONF-018',
  'implementation-multi-agent' => 'CONF-019',
  'implementation-auth-network' => 'CONF-020',
  'implementation-platform-lifecycle' => 'CONF-021',
  'implementation-observability' => 'CONF-022'
}.freeze

# These are intentionally broad entry anchors. A source row bound only to its
# owner's entry anchor proves directory classification, not semantic review.
# Every reviewed artifact must name at least one narrower behavioral contract.
summary_anchor_by_owner = {
  'implementation-startup-settings' => 'SET-001',
  'implementation-state-context' => 'SC-001',
  'implementation-query-model' => 'QM-001',
  'implementation-tool-protocol' => 'TP-001',
  'implementation-tool-catalog' => 'TCAT-001',
  'implementation-permissions-sandbox' => 'PERM-001',
  'implementation-task-runtime' => 'TR-001',
  'implementation-terminal-engine' => 'TERM-001',
  'implementation-interactive-repl' => 'REPL-001',
  'implementation-headless-sdk' => 'SDK-001',
  'implementation-optional-experiences' => 'OPT-001',
  'implementation-commands-input' => 'CMDI-001',
  'implementation-skills-output' => 'SKILL-001',
  'implementation-plugins-hooks' => 'HOOK-001',
  'implementation-mcp-lsp' => 'MCP-001',
  'implementation-transcript-recovery' => 'TX-001',
  'implementation-memory-compaction' => 'MC-001',
  'implementation-remote-bridge' => 'RB-CORE-001',
  'implementation-multi-agent' => 'MA-DEF-001',
  'implementation-auth-network' => 'AUTH-001',
  'implementation-platform-lifecycle' => 'PLAT-001',
  'implementation-observability' => 'OBS-001'
}.freeze

ledger = {}
if !File.file?(LEDGER)
  errors << "missing source coverage ledger: #{LEDGER}"
else
  lines = File.readlines(LEDGER, chomp: true)
  header = lines.shift
  expected_header = "path\tlines\tbytes\tsha256\tprimary_owner\tcontract_scope"
  errors << "#{LEDGER}: invalid header" unless header == expected_header
  owner_counts = Hash.new(0)
  lines.each_with_index do |line, index|
    fields = line.split("\t", -1)
    if fields.length != 6
      errors << "#{LEDGER}:#{index + 2}: expected six fields"
      next
    end
    path, line_count, bytes, sha, owner, scope = fields
    record_unique_row(ledger, path, fields, LEDGER, index + 2, errors)
    owner_counts[owner] += 1
    errors << "#{LEDGER}:#{index + 2}: owner skill missing: #{owner}" unless all_skills.include?(owner)
    errors << "#{LEDGER}:#{index + 2}: owner must be an implementation leaf" unless implementation_leaf_names.include?(owner)
    errors << "#{LEDGER}:#{index + 2}: empty contract scope" if scope.empty?
    fingerprint_shape = fingerprint_shape_errors("#{LEDGER}:#{index + 2}", line_count, bytes, sha)
    errors.concat(fingerprint_shape)
    if fingerprint_shape.empty?
      fingerprint_errors(ROOT, path, line_count, bytes, sha).each do |error|
        errors << (error.start_with?('source missing:') ? "#{LEDGER}:#{index + 2}: #{error}" : error)
      end
    end
  end
  source_files = live_production_go_sources(ROOT)
  SourceInventory.validate_ownership!(source_files.to_a)
  errors.concat(source_set_errors(source_files, ledger.keys.to_set))
  ledger.each do |path, fields|
    errors.concat(ledger_authority_errors(path, fields[4], fields[5]))
  end
end

# The generated artifact ledger and the manually reviewed trace are separate
# on purpose. Refreshing source fingerprints must not silently refresh the
# semantic review binding.
if !File.file?(TRACE)
  errors << "missing reviewed source-contract trace: #{TRACE}"
else
  trace_lines = File.readlines(TRACE, chomp: true)
  trace_header = trace_lines.shift
  expected_trace_header = "path\treviewed_sha256\tprimary_owner\tcontract_ids\tscenario_ids\treview_generation\tcoverage_class\tboundary_reason"
  errors << "#{TRACE}: invalid header" unless trace_header == expected_trace_header
  trace = {}
  trace_lines.each_with_index do |line, index|
    fields = line.split("\t", -1)
    if fields.length != 8
      errors << "#{TRACE}:#{index + 2}: expected eight fields"
      next
    end
    path, reviewed_sha, owner, contract_text, scenario_text, review_generation, coverage_class, boundary_reason = fields
    record_unique_row(trace, path, fields, TRACE, index + 2, errors)
    ledger_row = ledger[path]
    if ledger_row.nil?
      errors << "#{TRACE}:#{index + 2}: no current artifact ledger row for #{path}"
      next
    end
    errors << "#{path}: semantic review hash is stale" unless reviewed_sha == ledger_row[3]
    errors << "#{path}: semantic review owner #{owner} differs from ledger owner #{ledger_row[4]}" unless owner == ledger_row[4]
    errors << "#{TRACE}:#{index + 2}: empty review generation" if review_generation.empty?
    errors.concat(trace_coverage_class_errors(
      "#{TRACE}:#{index + 2}",
      coverage_class,
      boundary_reason
    ))

    contracts = contract_text.split(',').map(&:strip).reject(&:empty?)
    scenarios = scenario_text.split(',').map(&:strip).reject(&:empty?)
    summary_anchor = summary_anchor_by_owner[owner]
    expected_suite = conformance_suite_by_owner[owner]
    errors.concat(trace_anchor_errors(
      label: "#{TRACE}:#{index + 2}",
      contracts: contracts,
      scenarios: scenarios,
      stable_ids: covered_ids,
      owner: owner,
      expected_suite: expected_suite,
      summary_anchor: summary_anchor
    ))
  end
  errors.concat(trace_set_errors(ledger.keys.to_set, trace.keys.to_set))
end

implementation_leaf_names.each do |name|
  dir = File.join(SKILL_ROOT, name)
  refs = Dir.glob(File.join(dir, 'references/*.md'))
  errors << "#{name}: leaf must have detailed Markdown references" if refs.empty?
  combined = refs.map { |p| File.read(p) }.join("\n")
  errors << "#{name}: references need stable contract IDs" unless combined.match?(/\b[A-Z][A-Z0-9-]*-\d{3}\b/)
  errors << "#{name}: references need acceptance scenarios" unless combined.match?(/Acceptance|Scenario/i)
end

if errors.empty?
  source_count = File.file?(LEDGER) ? [File.readlines(LEDGER).length - 1, 0].max : 0
  puts "architecture audit passed: #{skill_files.length} skills, #{source_count} source artifacts"
else
  warn "architecture audit failed with #{errors.length} issue(s):"
  errors.each { |error| warn "- #{error}" }
  exit 1
end
