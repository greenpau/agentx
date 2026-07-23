#!/usr/bin/env ruby
# frozen_string_literal: true

# Add the standard implementation-context band and connector metadata to
# hand-authored Draw.io assets. Generated architecture diagrams have their own
# template and are deliberately excluded.

require 'cgi'
require 'optparse'
require 'rexml/document'
require 'rexml/formatters/pretty'

ROOT = File.expand_path('../../../..', __dir__)
SKILL_ROOT = File.join(ROOT, '.codex', 'skills')
ARCHITECTURE_SKILL = 'implementation-architecture'
CONTEXT_VERSION = '2.1'
LEGACY_CONTEXT_VERSION = '2.0'
SEMANTIC_LABEL_VERSION = '2.3'
ROUTE_VERSION = '2.2'
LABEL_LAYOUT_VERSION = '2.3'
CONTENT_SHIFT = 365.0
LEGACY_CONTENT_SHIFT = 210.0
CONTEXT_IDS = %w[
  custom-context-band
  custom-context-breadcrumb
  custom-context-lifecycle
  custom-context-question
  custom-context-starts
  custom-context-owns
  custom-context-ends
  custom-context-defers
  custom-context-contracts
  custom-context-links
  custom-context-legend
  custom-context-authority
].freeze
EXCLUDED_BASENAMES = %w[architecture.drawio skill-routing.drawio].freeze
INFERRED_LABEL_PREFIXES = %w[
  evaluates records\ in terminates\ at releases\ via retries\ via emits
  branches\ to signals continues\ to checks passes hands\ off\ to handoff
].freeze

LIFECYCLE_STAGES = [
  'startup + policy',
  'state + registries',
  'query + model',
  'capability execution',
  'continuity + tasks',
  'surface + remote'
].freeze

LIFECYCLE_FOCUS = {
  'implementation-runtime-core' => [0, 1, 2],
  'implementation-startup-settings' => [0],
  'implementation-state-context' => [1],
  'implementation-query-model' => [2],
  'implementation-capability-runtime' => [3],
  'implementation-tool-protocol' => [3],
  'implementation-tool-catalog' => [1, 3],
  'implementation-permissions-sandbox' => [3],
  'implementation-task-runtime' => [3, 4],
  'implementation-user-surfaces' => [5],
  'implementation-terminal-engine' => [5],
  'implementation-interactive-repl' => [5],
  'implementation-headless-sdk' => [5],
  'implementation-optional-experiences' => [5],
  'implementation-extension-plane' => [1, 3],
  'implementation-commands-input' => [1, 2],
  'implementation-skills-output' => [1, 2],
  'implementation-plugins-hooks' => [1, 3],
  'implementation-mcp-lsp' => [1, 3],
  'implementation-continuity' => [4],
  'implementation-transcript-recovery' => [4],
  'implementation-memory-compaction' => [4],
  'implementation-distributed-runtime' => [4, 5],
  'implementation-remote-bridge' => [4, 5],
  'implementation-multi-agent' => [3, 4, 5],
  'implementation-auth-network' => :cross_cutting,
  'implementation-platform-lifecycle' => :cross_cutting,
  'implementation-observability' => :cross_cutting,
  'implementation-operations' => :cross_cutting,
  'implementation-conformance-audit' => :cross_cutting
}.freeze

class EnhancementError < StandardError; end

def visible_text(value)
  decoded = CGI.unescapeHTML(value.to_s)
  decoded = decoded.gsub(/<br\s*\/?\s*>/i, ' ')
  decoded = decoded.gsub(/<[^>]+>/, ' ')
  decoded.gsub(/\s+/, ' ').strip
end

def first_line(value)
  raw = CGI.unescapeHTML(value.to_s).split(/<br\s*\/?\s*>/i, 2).first.to_s
  raw = raw.split(/\r?\n/, 2).first.to_s
  visible_text(raw)
end

def humanize_slug(value)
  value.to_s.tr('_', '-').split('-').reject(&:empty?).map do |word|
    case word.downcase
    when 'api' then 'API'
    when 'cli' then 'CLI'
    when 'ide' then 'IDE'
    when 'lsp' then 'LSP'
    when 'mcp' then 'MCP'
    when 'ndjson' then 'NDJSON'
    when 'repl' then 'REPL'
    when 'sdk' then 'SDK'
    when 'sse' then 'SSE'
    when 'ui' then 'UI'
    else word.capitalize
    end
  end.join(' ')
end

def format_number(number)
  rounded = number.round
  return rounded.to_s if (number - rounded).abs < 0.000_001

  format('%.3f', number).sub(/0+\z/, '').sub(/\.\z/, '')
end

def numeric_attribute(element, name, default = nil)
  raw = element.attributes[name]
  return default if raw.nil? || raw.empty?

  Float(raw)
rescue ArgumentError
  raise EnhancementError, "#{element.name} has nonnumeric #{name}=#{raw.inspect}"
end

def set_attribute(element, name, value)
  string = value.to_s
  return false if element.attributes[name] == string

  element.attributes[name] = string
  true
end

def style_has?(style, key)
  style.to_s.split(';').any? { |entry| entry.split('=', 2).first == key }
end

def append_style(cell, properties)
  style = cell.attributes['style'].to_s
  missing = properties.reject { |key, _value| style_has?(style, key) }
  return false if missing.empty?

  style += ';' unless style.empty? || style.end_with?(';')
  style += missing.map { |key, value| "#{key}=#{value};" }.join
  cell.attributes['style'] = style
  true
end

def style_properties(cell)
  cell.attributes['style'].to_s.split(';').each_with_object({}) do |entry, result|
    next if entry.empty?
    key, value = entry.split('=', 2)
    result[key] = value.to_s
  end
end

def set_style_properties(cell, properties)
  current = style_properties(cell)
  changed = properties.any? { |key, value| current[key] != value.to_s }
  return false unless changed

  properties.each { |key, value| current[key] = value.to_s }
  cell.attributes['style'] = current.map { |key, value| "#{key}=#{value}" }.join(';') + ';'
  true
end

def route_targets(skill_name)
  path = File.join(SKILL_ROOT, skill_name, 'SKILL.md')
  raise EnhancementError, "missing routed skill #{path}" unless File.file?(path)

  text = File.read(path)
  text.scan(/(?:^|\n)\s*(?:-\s*)?Use \[([a-z0-9-]+)\]\(\.\.\/([a-z0-9-]+)\/SKILL\.md\)/).map do |label, target|
    unless label == target
      raise EnhancementError, "route label #{label.inspect} does not match target #{target.inspect} in #{path}"
    end
    target
  end.uniq
end

def router_mapping
  routers = route_targets(ARCHITECTURE_SKILL)
  mapping = {}
  routers.each do |router|
    mapping[router] = ARCHITECTURE_SKILL
    route_targets(router).each do |child|
      prior = mapping[child]
      if prior && prior != router
        raise EnhancementError, "#{child} is routed by both #{prior} and #{router}"
      end
      mapping[child] = router
    end
  end
  mapping
end

def custom_asset_paths(arguments)
  candidates = if arguments.empty?
                 Dir.glob(File.join(SKILL_ROOT, 'implementation-*', 'assets', '*.drawio'))
               else
                 arguments.flat_map do |argument|
                   path = File.expand_path(argument, ROOT)
                   if File.directory?(path)
                     Dir.glob(File.join(path, '**', '*.drawio'))
                   else
                     [path]
                   end
                 end
               end

  candidates.select { |path| File.file?(path) }
            .reject { |path| EXCLUDED_BASENAMES.include?(File.basename(path)) }
            .uniq
            .sort
end

def cell_index(model)
  model.get_elements('.//mxCell').each_with_object({}) do |cell, index|
    id = cell.attributes['id']
    next if id.nil? || id.empty?

    raise EnhancementError, "duplicate cell ID #{id}" if index.key?(id)
    index[id] = cell
  end
end

def layer_id(model)
  root = model.elements['root']
  raise EnhancementError, 'mxGraphModel lacks root' if root.nil?

  cells = root.get_elements('mxCell')
  root_cell = cells.find { |cell| cell.attributes['parent'].nil? }
  raise EnhancementError, 'mxGraphModel root lacks an unparented root cell' if root_cell.nil?

  layer = cells.find { |cell| cell.attributes['parent'] == root_cell.attributes['id'] }
  raise EnhancementError, 'mxGraphModel root lacks a default layer cell' if layer.nil?

  layer.attributes['id']
end

def top_level_vertex?(cell, page_layer_id)
  cell.attributes['vertex'] == '1' && cell.attributes['parent'] == page_layer_id
end

def shift_top_level_content(model, amount = CONTENT_SHIFT, excluded_ids = [])
  changed = false
  page_layer_id = layer_id(model)
  model.get_elements('.//mxCell').each do |cell|
    if top_level_vertex?(cell, page_layer_id) && !excluded_ids.include?(cell.attributes['id'])
      geometry = cell.elements['mxGeometry']
      raise EnhancementError, "top-level vertex #{cell.attributes['id']} lacks mxGeometry" if geometry.nil?

      y = numeric_attribute(geometry, 'y', 0.0)
      changed |= set_attribute(geometry, 'y', format_number(y + amount))
    end

    next unless cell.attributes['edge'] == '1' && cell.attributes['parent'] == page_layer_id

    geometry = cell.elements['mxGeometry']
    next if geometry.nil?

    geometry.get_elements('.//mxPoint').each do |point|
      # Label offsets are relative vectors. All other points under a top-level
      # edge are absolute route/source/target coordinates and move with content.
      next if point.attributes['as'] == 'offset'
      next if point.attributes['y'].nil?

      y = numeric_attribute(point, 'y')
      changed |= set_attribute(point, 'y', format_number(y + amount))
    end
  end
  changed
end

def diagram_header_cell?(cell, page_layer_id)
  return false unless top_level_vertex?(cell, page_layer_id)
  return false if cell.attributes['id'].to_s.start_with?('custom-context-')
  return true if %w[title subtitle].include?(cell.attributes['id'])

  entries = cell.attributes['style'].to_s.split(';')
  font_size = Float(style_properties(cell).fetch('fontSize', '0')) rescue 0.0
  entries.include?('text') && font_size >= 16 && !visible_text(cell.attributes['value']).empty?
end

def top_level_ancestor_id(cell, index, page_layer_id)
  current = cell
  seen = {}
  loop do
    id = current.attributes['id']
    return nil if id.nil? || seen[id]
    seen[id] = true
    return id if current.attributes['parent'] == page_layer_id
    current = index[current.attributes['parent']]
    return nil if current.nil?
  end
end

def ensure_diagram_header_gutter(model, desired_gap = 90.0)
  index = cell_index(model)
  memo = {}
  page_layer_id = layer_id(model)
  functional_cells = index.values.select do |cell|
    next false unless cell.attributes['vertex'] == '1'
    rect = absolute_rect(cell, index, memo)
    functional_routing_vertex?(cell, rect)
  end
  return false if functional_cells.empty?

  graph_top = functional_cells.map { |cell| absolute_rect(cell, index, memo)[1] }.min
  headers = index.values.select { |cell| diagram_header_cell?(cell, page_layer_id) }
                        .select do |cell|
    rect = absolute_rect(cell, index, memo)
    rect[1] + rect[3] <= graph_top + 0.000_001
  end
  return false if headers.empty?

  header_bottom = headers.map do |cell|
    rect = absolute_rect(cell, index, memo)
    rect[1] + rect[3]
  end.max
  gap = graph_top - header_bottom
  return false if gap >= desired_gap

  shift = desired_gap - gap
  movable_top_level_ids = functional_cells.map do |cell|
    top_level_ancestor_id(cell, index, page_layer_id)
  end.compact.uniq
  movable_top_level_ids.each do |id|
    geometry = index.fetch(id).elements['mxGeometry']
    y = numeric_attribute(geometry, 'y', 0.0)
    set_attribute(geometry, 'y', format_number(y + shift))
  end

  # Cross-container edges belong to the page layer and store absolute points;
  # nested edges move with their shifted container and require no adjustment.
  index.each_value do |edge|
    next unless edge.attributes['edge'] == '1' && edge.attributes['parent'] == page_layer_id
    geometry = edge.elements['mxGeometry']
    next if geometry.nil?
    geometry.get_elements('.//mxPoint').each do |point|
      next if point.attributes['as'] == 'offset' || point.attributes['y'].nil?
      y = numeric_attribute(point, 'y')
      set_attribute(point, 'y', format_number(y + shift))
    end
  end
  page_height = numeric_attribute(model, 'pageHeight')
  set_attribute(model, 'pageHeight', format_number(page_height + shift))
  true
end

def absolute_rect(cell, index, memo, visiting = {})
  id = cell.attributes['id']
  return memo[id] if memo.key?(id)
  raise EnhancementError, "cyclic cell parentage at #{id}" if visiting[id]

  geometry = cell.elements['mxGeometry']
  raise EnhancementError, "vertex #{id} lacks mxGeometry" if geometry.nil?

  visiting[id] = true
  x = numeric_attribute(geometry, 'x', 0.0)
  y = numeric_attribute(geometry, 'y', 0.0)
  width = numeric_attribute(geometry, 'width', 0.0)
  height = numeric_attribute(geometry, 'height', 0.0)
  parent_id = cell.attributes['parent']

  if parent_id
    parent = index[parent_id]
    raise EnhancementError, "vertex #{id} has unknown parent #{parent_id}" if parent.nil?
    # Draw.io root and layer cells intentionally have no geometry. A parent
    # with geometry is a real nested container whose offset must be included.
    if parent.elements['mxGeometry']
      parent_rect = absolute_rect(parent, index, memo, visiting)
      x += parent_rect[0]
      y += parent_rect[1]
    end
  end

  visiting.delete(id)
  memo[id] = [x, y, width, height]
end

def port_properties(source_rect, target_rect)
  source_x = source_rect[0] + source_rect[2] / 2.0
  source_y = source_rect[1] + source_rect[3] / 2.0
  target_x = target_rect[0] + target_rect[2] / 2.0
  target_y = target_rect[1] + target_rect[3] / 2.0
  dx = target_x - source_x
  dy = target_y - source_y

  if dx.abs >= dy.abs
    if dx >= 0
      { 'exitX' => '1', 'exitY' => '0.5', 'entryX' => '0', 'entryY' => '0.5' }
    else
      { 'exitX' => '0', 'exitY' => '0.5', 'entryX' => '1', 'entryY' => '0.5' }
    end
  elsif dy >= 0
    { 'exitX' => '0.5', 'exitY' => '1', 'entryX' => '0.5', 'entryY' => '0' }
  else
    { 'exitX' => '0.5', 'exitY' => '0', 'entryX' => '0.5', 'entryY' => '1' }
  end
end

def functional_routing_vertex?(cell, rect)
  return false unless cell.attributes['vertex'] == '1'
  return false if rect[2] <= 0 || rect[3] <= 0
  return false if cell.attributes['id'].to_s.start_with?('custom-context-')

  style = cell.attributes['style'].to_s
  entries = style.split(';')
  properties = style_properties(cell)
  return false if entries.include?('text') || entries.include?('group')
  return false if properties.key?('swimlane') || properties['container'] == '1'
  return false if properties['shape'] == 'line'

  geometry = cell.elements['mxGeometry']
  return false if geometry&.attributes&.[]('relative') == '1'
  return false if rect[2] <= 16 && rect[3] <= 16 && visible_text(cell.attributes['value']).empty?

  true
end

def edge_port_anchor(edge, endpoint, rect)
  style = style_properties(edge)
  prefix = endpoint == :source ? 'exit' : 'entry'
  x_fraction = Float(style.fetch("#{prefix}X", '0.5')) rescue 0.5
  y_fraction = Float(style.fetch("#{prefix}Y", '0.5')) rescue 0.5
  [rect[0] + rect[2] * x_fraction, rect[1] + rect[3] * y_fraction]
end

def routing_waypoints(edge, parent_origin)
  geometry = edge.elements['mxGeometry']
  return [] if geometry.nil?

  geometry.get_elements('Array').select { |array| array.attributes['as'] == 'points' }.flat_map do |array|
    array.get_elements('mxPoint').map do |point|
      [parent_origin[0] + numeric_attribute(point, 'x', 0.0),
       parent_origin[1] + numeric_attribute(point, 'y', 0.0)]
    end
  end
end

def segment_crosses_rect?(start_point, end_point, rect, margin = 5.0)
  left = rect[0] - margin
  right = rect[0] + rect[2] + margin
  top = rect[1] - margin
  bottom = rect[1] + rect[3] + margin
  delta_x = end_point[0] - start_point[0]
  delta_y = end_point[1] - start_point[1]
  t_min = 0.0
  t_max = 1.0
  [[-delta_x, start_point[0] - left], [delta_x, right - start_point[0]],
   [-delta_y, start_point[1] - top], [delta_y, bottom - start_point[1]]].each do |coefficient, distance|
    if coefficient.abs < 0.000_001
      return false if distance < 0
      next
    end
    ratio = distance.to_f / coefficient
    if coefficient < 0
      t_min = [t_min, ratio].max
    else
      t_max = [t_max, ratio].min
    end
    return false if t_min > t_max
  end
  t_max >= 0 && t_min <= 1
end

def path_clear?(path, obstacles)
  path.each_cons(2).all? do |start_point, end_point|
    obstacles.none? { |_cell, rect| segment_crosses_rect?(start_point, end_point, rect) }
  end
end

def simplify_path(points)
  result = []
  points.each do |point|
    next if result.last && (result.last[0] - point[0]).abs < 0.000_001 &&
                           (result.last[1] - point[1]).abs < 0.000_001
    while result.length >= 2
      first = result[-2]
      second = result[-1]
      horizontal = (first[1] - second[1]).abs < 0.000_001 && (second[1] - point[1]).abs < 0.000_001
      vertical = (first[0] - second[0]).abs < 0.000_001 && (second[0] - point[0]).abs < 0.000_001
      break unless horizontal || vertical
      result.pop
    end
    result << point
  end
  result
end

ROUTE_SIDES = %i[left right top bottom].freeze

def side_anchor(rect, side, fraction)
  case side
  when :left then [rect[0], rect[1] + rect[3] * fraction]
  when :right then [rect[0] + rect[2], rect[1] + rect[3] * fraction]
  when :top then [rect[0] + rect[2] * fraction, rect[1]]
  when :bottom then [rect[0] + rect[2] * fraction, rect[1] + rect[3]]
  end
end

def side_stub(anchor, side, distance = 12.0)
  case side
  when :left then [anchor[0] - distance, anchor[1]]
  when :right then [anchor[0] + distance, anchor[1]]
  when :top then [anchor[0], anchor[1] - distance]
  when :bottom then [anchor[0], anchor[1] + distance]
  end
end

def port_style(side, fraction, prefix)
  case side
  when :left then { "#{prefix}X" => '0', "#{prefix}Y" => format('%.3f', fraction) }
  when :right then { "#{prefix}X" => '1', "#{prefix}Y" => format('%.3f', fraction) }
  when :top then { "#{prefix}X" => format('%.3f', fraction), "#{prefix}Y" => '0' }
  when :bottom then { "#{prefix}X" => format('%.3f', fraction), "#{prefix}Y" => '1' }
  end
end

def candidate_rail_values(obstacles, source_rect, target_rect, page_width, page_height, routing_top)
  x_values = [18.0, page_width - 18.0]
  y_values = [routing_top, page_height - 18.0]
  (obstacles.map(&:last) + [source_rect, target_rect]).each do |rect|
    x_values.concat([rect[0] - 12.0, rect[0] + rect[2] + 12.0])
    y_values.concat([rect[1] - 12.0, rect[1] + rect[3] + 12.0])
  end
  [x_values.select { |value| value >= 8 && value <= page_width - 8 }.uniq,
   y_values.select { |value| value >= CONTENT_SHIFT - 15 && value <= page_height - 8 }.uniq]
end

def candidate_paths(start_point, end_point, x_values, y_values)
  candidates = [
    [start_point, [start_point[0], end_point[1]], end_point],
    [start_point, [end_point[0], start_point[1]], end_point]
  ]
  x_values.each do |rail_x|
    candidates << [start_point, [rail_x, start_point[1]], [rail_x, end_point[1]], end_point]
  end
  y_values.each do |rail_y|
    candidates << [start_point, [start_point[0], rail_y], [end_point[0], rail_y], end_point]
  end
  candidates.map { |path| simplify_path(path) }.uniq
end

# Some cross-lane relations cannot reach a single horizontal or vertical rail
# without first leaving a dense row of nodes. Keep the common-case candidate
# set small, then use this bounded two-corridor family only when every simpler
# route is blocked. The two forms are mirror images: leave vertically and use
# an outer x corridor, or leave horizontally and use an outer y corridor.
def two_corridor_candidate_paths(start_point, end_point, x_values, y_values)
  candidates = []
  x_values.each do |rail_x|
    y_values.each do |rail_y|
      candidates << [
        start_point,
        [start_point[0], rail_y],
        [rail_x, rail_y],
        [rail_x, end_point[1]],
        end_point
      ]
      candidates << [
        start_point,
        [rail_x, start_point[1]],
        [rail_x, rail_y],
        [end_point[0], rail_y],
        end_point
      ]
    end
  end
  candidates.map { |path| simplify_path(path) }.uniq
end

def path_score(path)
  length = path.each_cons(2).sum do |first, second|
    (first[0] - second[0]).abs + (first[1] - second[1]).abs
  end
  length + [path.length - 2, 0].max * 35.0
end

def segment_conflict_penalty(first, second)
  a1, a2 = first
  b1, b2 = second
  a_horizontal = (a1[1] - a2[1]).abs < 0.000_001
  b_horizontal = (b1[1] - b2[1]).abs < 0.000_001
  if a_horizontal && b_horizontal && (a1[1] - b1[1]).abs < 0.000_001
    overlap = [[a1[0], a2[0]].max, [b1[0], b2[0]].max].min -
              [[a1[0], a2[0]].min, [b1[0], b2[0]].min].max
    return overlap.positive? ? 2_000.0 + overlap * 20.0 : 0.0
  end
  if !a_horizontal && !b_horizontal && (a1[0] - b1[0]).abs < 0.000_001
    overlap = [[a1[1], a2[1]].max, [b1[1], b2[1]].max].min -
              [[a1[1], a2[1]].min, [b1[1], b2[1]].min].max
    return overlap.positive? ? 2_000.0 + overlap * 20.0 : 0.0
  end
  horizontal = a_horizontal ? first : second
  vertical = a_horizontal ? second : first
  hx1, hx2 = [horizontal[0][0], horizontal[1][0]].minmax
  vy1, vy2 = [vertical[0][1], vertical[1][1]].minmax
  crossing = vertical[0][0] > hx1 && vertical[0][0] < hx2 &&
             horizontal[0][1] > vy1 && horizontal[0][1] < vy2
  crossing ? 180.0 : 0.0
end

def collinear_overlap_length(first, second)
  a1, a2 = first
  b1, b2 = second
  a_horizontal = (a1[1] - a2[1]).abs < 0.000_001
  b_horizontal = (b1[1] - b2[1]).abs < 0.000_001
  if a_horizontal && b_horizontal && (a1[1] - b1[1]).abs < 0.000_001
    return [[a1[0], a2[0]].max, [b1[0], b2[0]].max].min -
           [[a1[0], a2[0]].min, [b1[0], b2[0]].min].max
  end
  if !a_horizontal && !b_horizontal && (a1[0] - b1[0]).abs < 0.000_001
    return [[a1[1], a2[1]].max, [b1[1], b2[1]].max].min -
           [[a1[1], a2[1]].min, [b1[1], b2[1]].min].max
  end
  0.0
end

def path_overlaps_segments?(path, used_segments, tolerance = 0.5)
  path.each_cons(2).any? do |segment|
    used_segments.any? { |used| collinear_overlap_length(segment, used) > tolerance }
  end
end

def endpoint_side(edge, endpoint)
  style = style_properties(edge)
  prefix = endpoint == :source ? 'exit' : 'entry'
  x = Float(style.fetch("#{prefix}X", '0.5')) rescue 0.5
  y = Float(style.fetch("#{prefix}Y", '0.5')) rescue 0.5
  return :left if x.abs < 0.000_001 && y > 0 && y < 1
  return :right if (x - 1).abs < 0.000_001 && y > 0 && y < 1
  return :top if y.abs < 0.000_001
  return :bottom if (y - 1).abs < 0.000_001
  return :left if x.abs < 0.000_001
  return :right if (x - 1).abs < 0.000_001

  nil
end

def departs_outward?(anchor, next_point, side)
  case side
  when :left then next_point[0] <= anchor[0] + 0.000_001
  when :right then next_point[0] >= anchor[0] - 0.000_001
  when :top then next_point[1] <= anchor[1] + 0.000_001
  when :bottom then next_point[1] >= anchor[1] - 0.000_001
  else false
  end
end

def incident_path_clear?(edge, path, source_rect, target_rect)
  return false if path.length < 2
  source_side = endpoint_side(edge, :source)
  target_side = endpoint_side(edge, :target)
  return false unless departs_outward?(path.first, path[1], source_side)
  # Reverse the final segment: it must leave the target boundary outward when
  # viewed from target to the prior point, which means the forward edge enters
  # the target without crossing its interior.
  return false unless departs_outward?(path.last, path[-2], target_side)

  source_reentry = path.each_cons(2).drop(1).any? do |first, second|
    segment_crosses_rect?(first, second, source_rect, -0.5)
  end
  target_reentry = path.each_cons(2).to_a[0...-1].any? do |first, second|
    segment_crosses_rect?(first, second, target_rect, -0.5)
  end
  !source_reentry && !target_reentry
end

def path_conflict_penalty(path, used_segments)
  path.each_cons(2).sum do |segment|
    used_segments.sum { |used| segment_conflict_penalty(segment, used) }
  end
end

def set_edge_waypoints(edge, points, parent_origin)
  geometry = edge.elements['mxGeometry'] || edge.add_element('mxGeometry', { 'relative' => '1', 'as' => 'geometry' })
  geometry.get_elements('Array').select { |array| array.attributes['as'] == 'points' }.each do |array|
    geometry.delete_element(array)
  end
  array = geometry.add_element('Array', { 'as' => 'points' })
  points.each do |x, y|
    point = array.add_element('mxPoint')
    point.add_attribute('x', format_number(x - parent_origin[0]))
    point.add_attribute('y', format_number(y - parent_origin[1]))
  end
end

def compact_edge_concept(cell, maximum = 22)
  concept = first_line(cell.attributes['value'])
  concept = humanize_slug(cell.attributes['id']) if concept.empty?
  return concept if concept.length <= maximum

  "#{concept[0, maximum - 1].sub(/\s+\S*\z/, '').rstrip}…"
end

def concise_edge_label(edge, source, target)
  inferred = edge.attributes['customSemanticLabelVersion'] == SEMANTIC_LABEL_VERSION
  original = inferred ? visible_text(edge.attributes['value']) : edge.attributes['customFullSemanticLabel'].to_s
  original = visible_text(edge.attributes['value']) if original.empty?
  target_label = compact_edge_concept(target)
  source_label = compact_edge_concept(source)
  if inferred
    display = "handoff · #{compact_edge_concept(target, 16)}"
    return [display, original]
  end

  decision = original.match?(/\A(?:yes|no|true|false|success|failure|available|unavailable|match|no match)\b/i)
  display = original
  if decision && !original.downcase.include?(target_label.downcase)
    display = "#{original} · #{target_label}"
  elsif original.length > 38
    prefix = original[0, 35].sub(/\s+\S*\z/, '').strip
    display = "#{prefix}…"
  end
  display = display[0, 39].sub(/\s+\S*\z/, '').rstrip + '…' if display.length > 40
  [display, original]
end

def rectangle_intersection_area(first, second, padding = 0.0)
  first_left = first[0] - padding
  first_top = first[1] - padding
  first_right = first[0] + first[2] + padding
  first_bottom = first[1] + first[3] + padding
  second_left = second[0]
  second_top = second[1]
  second_right = second[0] + second[2]
  second_bottom = second[1] + second[3]
  width = [first_right, second_right].min - [first_left, second_left].max
  height = [first_bottom, second_bottom].min - [first_top, second_top].max
  width.positive? && height.positive? ? width * height : 0.0
end

def edge_path(edge, index, memo)
  source = index.fetch(edge.attributes['source'])
  target = index.fetch(edge.attributes['target'])
  source_rect = absolute_rect(source, index, memo)
  target_rect = absolute_rect(target, index, memo)
  parent = index[edge.attributes['parent']]
  parent_origin = if parent && parent.elements['mxGeometry']
                    rect = absolute_rect(parent, index, memo)
                    [rect[0], rect[1]]
                  else
                    [0.0, 0.0]
                  end
  [edge_port_anchor(edge, :source, source_rect)] +
    routing_waypoints(edge, parent_origin) +
    [edge_port_anchor(edge, :target, target_rect)]
end

def set_edge_label_geometry(edge, relative_x, offset_x, offset_y)
  geometry = edge.elements['mxGeometry'] || edge.add_element('mxGeometry', { 'relative' => '1', 'as' => 'geometry' })
  changed = false
  changed |= set_attribute(geometry, 'relative', '1')
  changed |= set_attribute(geometry, 'x', format('%.3f', relative_x))
  changed |= set_attribute(geometry, 'y', '0')
  offsets = geometry.get_elements('mxPoint').select { |point| point.attributes['as'] == 'offset' }
  expected = [format_number(offset_x), format_number(offset_y)]
  if offsets.length != 1 || offsets.first.attributes['x'] != expected[0] || offsets.first.attributes['y'] != expected[1]
    offsets.each { |point| geometry.delete_element(point) }
    point = geometry.add_element('mxPoint')
    point.add_attribute('x', expected[0])
    point.add_attribute('y', expected[1])
    point.add_attribute('as', 'offset')
    changed = true
  end
  changed
end

def layout_edge_labels(model)
  changed = false
  index = cell_index(model)
  memo = {}
  node_rects = index.values.each_with_object([]) do |cell, result|
    next unless cell.attributes['vertex'] == '1'
    rect = absolute_rect(cell, index, memo)
    style = style_properties(cell)
    if functional_routing_vertex?(cell, rect)
      result << rect
    elsif !cell.attributes['id'].to_s.start_with?('custom-context-') &&
          cell.attributes['style'].to_s.split(';').include?('text') &&
          !visible_text(cell.attributes['value']).empty?
      # Main titles and explanatory text are presentation content even though
      # they are not routing vertices. Labels must not use them as whitespace.
      result << rect
    elsif style.key?('swimlane')
      # Reserve only the visible swimlane header, not the entire container.
      header_height = Float(style.fetch('startSize', '34')) rescue 34.0
      result << [rect[0], rect[1], rect[2], [header_height, rect[3]].min]
    end
  end
  occupied_labels = []
  page_width = numeric_attribute(model, 'pageWidth')
  page_height = numeric_attribute(model, 'pageHeight')
  edges = index.values.select { |cell| cell.attributes['edge'] == '1' && cell.attributes['source'] && cell.attributes['target'] }
  edge_paths = edges.each_with_object({}) do |edge, result|
    result[edge.attributes['id'].to_s] = edge_path(edge, index, memo)
  end
  edges.sort_by { |edge| edge.attributes['id'].to_s }.each do |edge|
    source = index.fetch(edge.attributes['source'])
    target = index.fetch(edge.attributes['target'])
    display, original = concise_edge_label(edge, source, target)
    changed |= set_attribute(edge, 'customFullSemanticLabel', original) if display != original
    changed |= set_attribute(edge, 'value', display)
    changed |= set_style_properties(edge, {
      'fontSize' => '9',
      'labelBackgroundColor' => '#ffffff',
      'labelBorderColor' => '#dfe1e5',
      'spacing' => '2'
    })
    if edge.attributes['customLabelLayoutLocked'] == '1'
      changed |= set_attribute(edge, 'customLabelLayoutVersion', LABEL_LAYOUT_VERSION)
      next
    end
    path = edge_paths.fetch(edge.attributes['id'].to_s)
    segments = []
    total_length = path.each_cons(2).sum do |first, second|
      (first[0] - second[0]).abs + (first[1] - second[1]).abs
    end
    traversed = 0.0
    path.each_cons(2) do |first, second|
      length = (first[0] - second[0]).abs + (first[1] - second[1]).abs
      next if length < 1.0
      segments << [first, second, length, traversed]
      traversed += length
    end
    next if segments.empty? || total_length <= 0

    label_width = [[display.length * 5.2 + 14.0, 48.0].max, 220.0].min
    label_height = display.length * 5.2 + 14.0 > 220.0 ? 30.0 : 18.0
    candidates = []
    segments.sort_by { |_first, _second, length, _traversed| -length }.each do |first, second, length, prior|
      horizontal = (first[1] - second[1]).abs < 0.000_001
      offsets = if horizontal
                  [[0, -18], [0, 18], [0, -36], [0, 36], [0, -58], [0, 58],
                   [0, -76], [0, 76], [0, -100], [0, 100]]
                else
                  [[20, 0], [-20, 0], [40, 0], [-40, 0], [80, 0], [-80, 0],
                   [120, 0], [-120, 0], [160, 0], [-160, 0]]
                end
      [0.5, 0.3, 0.7].each do |fraction|
        center_x = first[0] + (second[0] - first[0]) * fraction
        center_y = first[1] + (second[1] - first[1]) * fraction
        path_fraction = (prior + length * fraction) / total_length
        offsets.each do |offset_x, offset_y|
          rect = [center_x + offset_x - label_width / 2.0,
                  center_y + offset_y - label_height / 2.0,
                  label_width,
                  label_height]
          outside = rect[0] < 4 || rect[1] < CONTENT_SHIFT - 8 ||
                    rect[0] + rect[2] > page_width - 4 || rect[1] + rect[3] > page_height - 4
          overlap = node_rects.sum { |node_rect| rectangle_intersection_area(rect, node_rect, 4.0) }
          overlap += occupied_labels.sum { |label_rect| rectangle_intersection_area(rect, label_rect, 5.0) }
          other_edge_hits = edge_paths.sum do |other_id, other_path|
            next 0 if other_id == edge.attributes['id'].to_s
            other_path.each_cons(2).count do |edge_start, edge_end|
              segment_crosses_rect?(edge_start, edge_end, rect, 2.0)
            end
          end
          score = overlap * 1_000.0 + other_edge_hits * 300_000.0 +
                  (outside ? 1_000_000.0 : 0.0) +
                  offset_x.abs + offset_y.abs - [length, 220.0].min * 0.1
          candidates << [score, path_fraction, offset_x, offset_y, rect]
        end
      end
    end
    best = candidates.min_by { |candidate| candidate[0, 4] }
    relative_x = best[1] * 2.0 - 1.0
    changed |= set_edge_label_geometry(edge, relative_x, best[2], best[3])
    changed |= set_attribute(edge, 'customLabelLayoutVersion', LABEL_LAYOUT_VERSION)
    occupied_labels << best[4]
  end
  changed
end

def route_blocked_edges(model)
  changed = false
  index = cell_index(model)
  memo = {}
  vertices = index.values.each_with_object([]) do |cell, result|
    next unless cell.attributes['vertex'] == '1'
    rect = absolute_rect(cell, index, memo)
    result << [cell, rect] if functional_routing_vertex?(cell, rect)
  end
  edges = index.values.select { |cell| cell.attributes['edge'] == '1' && cell.attributes['source'] && cell.attributes['target'] }
                      .sort_by { |cell| cell.attributes['id'].to_s }
  incident = Hash.new { |hash, key| hash[key] = [] }
  edges.each do |edge|
    incident[edge.attributes['source']] << [edge, :source]
    incident[edge.attributes['target']] << [edge, :target]
  end
  fractions = {}
  incident.each_value do |entries|
    entries.sort_by! { |edge, endpoint| [edge.attributes['id'].to_s, endpoint.to_s] }
    entries.each_with_index do |(edge, endpoint), ordinal|
      fractions[[edge.attributes['id'], endpoint]] = (ordinal + 1.0) / (entries.length + 1.0)
    end
  end
  page_width = numeric_attribute(model, 'pageWidth')
  page_height = numeric_attribute(model, 'pageHeight')
  page_layer_id = layer_id(model)
  header_bottoms = index.values.each_with_object([]) do |cell, result|
    next unless diagram_header_cell?(cell, page_layer_id)
    rect = absolute_rect(cell, index, memo)
    result << rect[1] + rect[3]
  end
  routing_top = [header_bottoms.max.to_f + 24.0, CONTENT_SHIFT + 20.0].max
  used_segments = []

  edges.each do |edge|
    source_id = edge.attributes['source']
    target_id = edge.attributes['target']
    source = index.fetch(source_id)
    target = index.fetch(target_id)
    source_rect = absolute_rect(source, index, memo)
    target_rect = absolute_rect(target, index, memo)
    parent = index[edge.attributes['parent']]
    parent_origin = if parent && parent.elements['mxGeometry']
                      rect = absolute_rect(parent, index, memo)
                      [rect[0], rect[1]]
                    else
                      [0.0, 0.0]
                    end
    source_anchor = edge_port_anchor(edge, :source, source_rect)
    target_anchor = edge_port_anchor(edge, :target, target_rect)
    waypoints = routing_waypoints(edge, parent_origin)
    obstacles = vertices.reject do |cell, _rect|
      [source_id, target_id].include?(cell.attributes['id'])
    end
    existing_path = [source_anchor] + waypoints + [target_anchor]
    # A computed route may legitimately be a straight segment with no bend
    # points. The version marker records that the router evaluated it; do not
    # churn an empty waypoint array on every maintenance pass.
    if edge.attributes['customRouteVersion'] == ROUTE_VERSION &&
       path_clear?(existing_path, obstacles) &&
       incident_path_clear?(edge, existing_path, source_rect, target_rect) &&
       !path_overlaps_segments?(existing_path, used_segments)
      used_segments.concat(existing_path.each_cons(2).to_a)
      next
    end

    source_fraction = fractions.fetch([edge.attributes['id'], :source])
    target_fraction = fractions.fetch([edge.attributes['id'], :target])
    best = nil
    endpoint_options = []
    ROUTE_SIDES.each do |source_side|
      ROUTE_SIDES.each do |target_side|
        actual_source = side_anchor(source_rect, source_side, source_fraction)
        actual_target = side_anchor(target_rect, target_side, target_fraction)
        start_stub = side_stub(actual_source, source_side)
        end_stub = side_stub(actual_target, target_side)
        next unless path_clear?([actual_source, start_stub], obstacles)
        next unless path_clear?([end_stub, actual_target], obstacles)
        x_values, y_values = candidate_rail_values(
          obstacles,
          source_rect,
          target_rect,
          page_width,
          page_height,
          routing_top
        )
        endpoint_options << [source_side, target_side, actual_source, actual_target,
                             start_stub, end_stub, x_values, y_values]
        candidate_paths(start_stub, end_stub, x_values, y_values).each do |middle|
          full_path = simplify_path([actual_source] + middle + [actual_target])
          next unless path_clear?(full_path, obstacles)
          next unless incident_path_clear?(edge, full_path, source_rect, target_rect)
          next if path_overlaps_segments?(full_path, used_segments)
          score = path_score(full_path) + path_conflict_penalty(full_path, used_segments)
          candidate = [score, source_side.to_s, target_side.to_s, full_path, source_side, target_side]
          best = candidate if best.nil? || (candidate[0, 3] <=> best[0, 3]) == -1
        end
      end
    end


    # Cross-lane and feedback relations occasionally need both a horizontal
    # and a vertical corridor. Evaluate this larger family only after the
    # short routes fail so normal diagrams remain fast and visually compact.
    if best.nil?
      endpoint_options.each do |source_side, target_side, actual_source, actual_target,
                                start_stub, end_stub, x_values, y_values|
        two_corridor_candidate_paths(start_stub, end_stub, x_values, y_values).each do |middle|
          full_path = simplify_path([actual_source] + middle + [actual_target])
          next unless path_clear?(full_path, obstacles)
          next unless incident_path_clear?(edge, full_path, source_rect, target_rect)
          next if path_overlaps_segments?(full_path, used_segments)
          score = path_score(full_path) + path_conflict_penalty(full_path, used_segments)
          candidate = [score, source_side.to_s, target_side.to_s, full_path, source_side, target_side]
          best = candidate if best.nil? || (candidate[0, 3] <=> best[0, 3]) == -1
        end
      end
    end
    raise EnhancementError, "cannot find obstacle-safe route for edge #{edge.attributes['id']}" if best.nil?

    full_path = best[3]
    changed |= set_style_properties(edge, {
      'edgeStyle' => 'orthogonalEdgeStyle',
      'rounded' => '0',
      'exitDx' => '0', 'exitDy' => '0', 'entryDx' => '0', 'entryDy' => '0'
    }.merge(port_style(best[4], source_fraction, 'exit'))
     .merge(port_style(best[5], target_fraction, 'entry')))
    set_edge_waypoints(edge, full_path[1...-1], parent_origin)
    changed |= set_attribute(edge, 'customRouteVersion', ROUTE_VERSION)
    changed = true
    used_segments.concat(full_path.each_cons(2).to_a)
  end
  changed
end

def target_concept(cell)
  concept = first_line(cell.attributes['value'])
  concept = humanize_slug(cell.attributes['id']) if concept.empty?
  return concept if concept.length <= 32

  "#{concept[0, 29].rstrip}…"
end

def inferred_edge_label(edge, source, target)
  # Derive only the visible adjacency already asserted by the connector. Do
  # not guess whether the relation stores, emits, terminates, retries, or
  # authorizes; numbered prose owns those semantics.
  "handoff · #{target_concept(target)}"
end

def enhance_edges(model)
  changed = false
  index = cell_index(model)
  rect_memo = {}

  index.each_value do |edge|
    next unless edge.attributes['edge'] == '1'

    source_id = edge.attributes['source']
    target_id = edge.attributes['target']
    # Connector-like decoration without semantic endpoints is not a behavioral
    # edge and cannot receive a meaningful target-derived label or port.
    next if source_id.nil? && target_id.nil?
    if source_id.nil? || target_id.nil?
      raise EnhancementError, "behavioral edge #{edge.attributes['id']} lacks source or target"
    end

    source = index[source_id]
    target = index[target_id]
    raise EnhancementError, "edge #{edge.attributes['id']} has unknown source #{source_id}" if source.nil?
    raise EnhancementError, "edge #{edge.attributes['id']} has unknown target #{target_id}" if target.nil?

    current_label = visible_text(edge.attributes['value'])
    enhancer_label = !edge.attributes['customSemanticLabelVersion'].to_s.empty? ||
                     INFERRED_LABEL_PREFIXES.any? { |prefix| current_label.start_with?("#{prefix} ") }
    if current_label.empty? || enhancer_label
      changed |= set_attribute(edge, 'value', inferred_edge_label(edge, source, target))
      changed |= set_attribute(edge, 'customSemanticLabelVersion', SEMANTIC_LABEL_VERSION)
      changed |= set_attribute(edge, 'customSemanticLabelOrigin', 'topology-derived')
    end

    changed |= append_style(edge, {
      'labelBackgroundColor' => '#ffffff',
      'jumpStyle' => 'arc',
      'jumpSize' => '8'
    })

    source_rect = absolute_rect(source, index, rect_memo)
    target_rect = absolute_rect(target, index, rect_memo)
    changed |= append_style(edge, port_properties(source_rect, target_rect).merge({
      'exitDx' => '0',
      'exitDy' => '0',
      'entryDx' => '0',
      'entryDy' => '0'
    }))
  end
  changed
end

def page_subject(model, diagram, fallback)
  cells = model.get_elements('.//mxCell')
  title = cells.find { |cell| cell.attributes['id'] == 'title' }
  page_layer_id = layer_id(model)
  title ||= cells.find do |cell|
    top_level_vertex?(cell, page_layer_id) && cell.attributes['style'].to_s.match?(/(?:^|;)fontSize=2[0-9](?:;|$)/)
  end
  subject = title && first_line(title.attributes['value'])
  subject = diagram.attributes['name'].to_s if subject.nil? || subject.empty?
  subject = fallback if subject.empty?
  subject
end

def definition_ids(line)
  ids = []
  ids.concat(line.scan(/\*\*([A-Z][A-Z0-9-]*-[A-Z]?[0-9]{2,3})\b/).flatten)
  ids.concat(line.scan(/^`([A-Z][A-Z0-9-]*-[A-Z]?[0-9]{2,3})`\s+—/).flatten)
  ids.concat(line.scan(/^\|\s*`?([A-Z][A-Z0-9-]*-[A-Z]?[0-9]{2,3})`?\s*\|/).flatten)
  ids.uniq
end

def diagram_contract_anchors(skill, diagram_slug, model)
  directory = File.join(SKILL_ROOT, skill)
  files = [File.join(directory, 'SKILL.md')] + Dir.glob(File.join(directory, 'references', '**', '*.md')).sort
  definitions = {}
  files.each do |path|
    File.readlines(path).each do |line|
      definition_ids(line).each { |id| definitions[id] ||= path }
    end
  end
  page_ids = model.get_elements('.//mxCell').each_with_object([]) do |cell, result|
    next if cell.attributes['id'].to_s.start_with?('custom-context-')
    visible_text(cell.attributes['value']).scan(/\b[A-Z][A-Z0-9-]*-[A-Z]?[0-9]{2,3}\b/).each do |id|
      next unless definitions.key?(id)
      result << id unless result.include?(id)
    end
  end
  unless page_ids.empty?
    anchors = page_ids.first(5)
    references = anchors.map { |id| File.basename(definitions.fetch(id)) }.uniq.first(2)
    return "#{anchors.join(' · ')} · prose: #{references.join(' + ')}"
  end

  tokens = diagram_slug.split('-').reject { |token| token.length < 4 || %w[state machine workflow paths].include?(token) }
  ranked = files.map do |path|
    basename = File.basename(path, '.md')
    score = tokens.count { |token| basename.include?(token) }
    [score, path]
  end
  selected = ranked.select { |score, _path| score.positive? }.sort_by { |score, path| [-score, path] }.map(&:last).first(2)
  if selected.empty?
    # A diagram name may describe a cross-cutting scenario rather than repeat
    # its contract filename (for example durability-crash-windows). Choose the
    # richest numbered contract file instead of falling back to a vague SKILL
    # marker with no actionable anchor.
    definition_ranking = files.map do |path|
      count = File.readlines(path).sum { |line| definition_ids(line).length }
      [count, path]
    end
    best = definition_ranking.select { |count, _path| count.positive? }
                             .max_by { |count, path| [count, path] }
    selected << (best ? best[1] : File.join(directory, 'SKILL.md'))
  end
  ids = []
  selected.each do |path|
    File.readlines(path).each do |line|
      definition_ids(line).each do |id|
        next if id.start_with?('CONF-') || id.match?(/(?:\A|-)A\d{2,3}\z/)
        ids << id unless ids.include?(id)
      end
    end
  end
  references = selected.map { |path| File.basename(path) }.uniq.join(' + ')
  anchor_text = ids.first(5).empty? ? 'owning skill contracts' : ids.first(5).join(' · ')
  "#{anchor_text} · prose: #{references}"
end

def compact_concepts(cells, limit = 2)
  concepts = cells.map { |cell| target_concept(cell) }.reject(&:empty?).uniq
  return 'event / state shown below' if concepts.empty?
  visible = concepts.first(limit)
  visible << "+#{concepts.length - limit} more" if concepts.length > limit
  visible.join(' / ')
end

def page_boundary_concepts(model)
  index = cell_index(model)
  edges = index.values.select { |cell| cell.attributes['edge'] == '1' && cell.attributes['source'] && cell.attributes['target'] }
  indegree = Hash.new(0)
  outdegree = Hash.new(0)
  edges.each do |edge|
    outdegree[edge.attributes['source']] += 1
    indegree[edge.attributes['target']] += 1
  end
  endpoint_ids = edges.flat_map { |edge| [edge.attributes['source'], edge.attributes['target']] }.uniq
  sources = endpoint_ids.select { |id| indegree[id].zero? }.map { |id| index[id] }.compact
  sinks = endpoint_ids.select { |id| outdegree[id].zero? }.map { |id| index[id] }.compact
  memo = {}
  candidates = endpoint_ids.map { |id| index[id] }.compact.select do |cell|
    rect = absolute_rect(cell, index, memo)
    functional_routing_vertex?(cell, rect)
  end
  sources = candidates.sort_by do |cell|
    rect = absolute_rect(cell, index, memo)
    text = visible_text(cell.attributes['value']).downcase
    entry_rank = if text.match?(/\b(trigger|state changed|event|request|candidate|source|start|entry)\b/)
                   0
                 elsif text.match?(/\b(input|open|initial)\b/)
                   1
                 else
                   2
                 end
    [entry_rank, rect[1], rect[0]]
  end.first(1) if sources.empty?
  sinks = candidates.sort_by do |cell|
    rect = absolute_rect(cell, index, memo)
    [-(rect[1] + rect[3]), -(rect[0] + rect[2])]
  end.first(2) if sinks.empty?
  [compact_concepts(sources), compact_concepts(sinks)]
end

def lifecycle_text(router, skill)
  focus = LIFECYCLE_FOCUS.fetch(skill, LIFECYCLE_FOCUS.fetch(router))
  if focus == :cross_cutting
    "<b>Lifecycle:</b> <font color=\"#1a73e8\"><b>YOU ARE HERE · cross-cutting</b></font> across #{LIFECYCLE_STAGES.join(' → ')}"
  else
    stages = LIFECYCLE_STAGES.each_with_index.map do |stage, index|
      focus.include?(index) ? "<font color=\"#1a73e8\"><b>[YOU ARE HERE · #{stage}]</b></font>" : stage
    end
    "<b>Lifecycle:</b> #{stages.join(' → ')}"
  end
end

def new_cell(id, value, style, x, y, width, height, parent_id)
  cell = REXML::Element.new('mxCell')
  cell.add_attribute('id', id)
  cell.add_attribute('value', value)
  cell.add_attribute('style', style)
  cell.add_attribute('vertex', '1')
  cell.add_attribute('parent', parent_id)
  geometry = cell.add_element('mxGeometry')
  geometry.add_attribute('x', format_number(x))
  geometry.add_attribute('y', format_number(y))
  geometry.add_attribute('width', format_number(width))
  geometry.add_attribute('height', format_number(height))
  geometry.add_attribute('as', 'geometry')
  cell
end

def context_cells(page_width, breadcrumb, lifecycle, question, starts, owns, ends_with, defers, contracts, broader_view, parent_id)
  content_width = [page_width - 96.0, 400.0].max
  band_width = [page_width - 40.0, 440.0].max
  gap = 12.0
  scope_width = (content_width - gap * 3) / 4.0
  contract_width = (content_width * 0.58).floor
  link_x = 48 + contract_width + gap
  link_width = content_width - contract_width - gap
  text_style = 'text;html=1;strokeColor=none;fillColor=none;align=left;verticalAlign=middle;whiteSpace=wrap;rounded=0;'
  scope_style = 'rounded=1;whiteSpace=wrap;html=1;align=left;verticalAlign=middle;spacingLeft=8;fontSize=10;'

  [
    new_cell(
      'custom-context-band',
      'CUSTOM DIAGRAM CONTEXT · v2.1',
      'rounded=1;whiteSpace=wrap;html=1;fillColor=#f8f9fa;strokeColor=#1a73e8;strokeWidth=2;align=left;verticalAlign=top;spacingTop=8;spacingLeft=10;fontSize=10;fontStyle=1;fontColor=#5f6368;',
      20, 20, band_width, 325, parent_id
    ),
    new_cell(
      'custom-context-breadcrumb',
      "<b>Context:</b> #{breadcrumb}",
      "#{text_style}fontSize=12;fontColor=#3c4043;",
      48, 40, content_width, 25, parent_id
    ),
    new_cell(
      'custom-context-lifecycle',
      lifecycle,
      "#{text_style}fontSize=10;fontColor=#5f6368;",
      48, 67, content_width, 40, parent_id
    ),
    new_cell(
      'custom-context-question',
      "<b>Question:</b> #{question}",
      "#{text_style}fontSize=14;fontColor=#202124;",
      48, 108, content_width, 38, parent_id
    ),
    new_cell(
      'custom-context-starts',
      "<b>Starts with:</b> #{starts}",
      "#{scope_style}fillColor=#f1f3f4;strokeColor=#80868b;",
      48, 153, scope_width, 66, parent_id
    ),
    new_cell(
      'custom-context-owns',
      "<b>Owns:</b> #{owns}",
      "#{scope_style}fillColor=#e8f0fe;strokeColor=#1a73e8;strokeWidth=2;",
      48 + scope_width + gap, 153, scope_width, 66, parent_id
    ),
    new_cell(
      'custom-context-ends',
      "<b>Ends with:</b> #{ends_with}",
      "#{scope_style}fillColor=#e6f4ea;strokeColor=#34a853;",
      48 + (scope_width + gap) * 2, 153, scope_width, 66, parent_id
    ),
    new_cell(
      'custom-context-defers',
      "<b>Defers to:</b> #{defers}",
      "#{scope_style}fillColor=#ffffff;strokeColor=#9aa0a6;dashed=1;",
      48 + (scope_width + gap) * 3, 153, scope_width, 66, parent_id
    ),
    new_cell(
      'custom-context-contracts',
      "<b>Contracts:</b> #{contracts}",
      "#{text_style}fontSize=10;fontColor=#3c4043;",
      48, 225, contract_width, 34, parent_id
    ),
    new_cell(
      'custom-context-links',
      "<b>Broader view:</b> #{broader_view}",
      "#{text_style}fontSize=10;fontColor=#5f6368;",
      link_x, 225, link_width, 34, parent_id
    ),
    new_cell(
      'custom-context-legend',
      '<b>Line grammar:</b> solid = primary · dashed = alternate / feedback / recovery · arc = crossing without merge · “handoff” asserts topology only; other edge text is prose-verified',
      "#{text_style}fontSize=10;fontColor=#5f6368;",
      48, 264, content_width, 27, parent_id
    ),
    new_cell(
      'custom-context-authority',
      '<b>Authority:</b> This page owns only the visible topology, state relation, and boundary handoffs. Numbered prose owns exact fields, values, timing, ordering, errors, and compatibility.',
      'rounded=1;whiteSpace=wrap;html=1;fillColor=#fff2cc;strokeColor=#d6b656;align=left;verticalAlign=middle;spacingLeft=8;fontSize=11;fontColor=#3c4043;',
      48, 298, content_width, 32, parent_id
    )
  ]
end

def context_cell_signature(cell)
  geometry = cell.elements['mxGeometry']
  [
    cell.attributes['value'],
    cell.attributes['style'],
    cell.attributes['vertex'],
    cell.attributes['parent'],
    geometry && geometry.attributes['x'],
    geometry && geometry.attributes['y'],
    geometry && geometry.attributes['width'],
    geometry && geometry.attributes['height'],
    geometry && geometry.attributes['as']
  ]
end

def install_or_refresh_context(model, cells)
  root = model.elements['root']
  raise EnhancementError, 'mxGraphModel lacks root' if root.nil?

  index = cell_index(model)
  present = CONTEXT_IDS.select { |id| index.key?(id) }
  existing_context_ids = index.keys.select { |id| id.start_with?('custom-context-') }
  version = model.attributes['customDiagramContextVersion']
  changed = false

  if version.nil? || version.empty?
    unless existing_context_ids.empty?
      raise EnhancementError, 'unversioned page already contains reserved custom-context-* cells'
    end
    changed |= shift_top_level_content(model)
    page_height = numeric_attribute(model, 'pageHeight')
    raise EnhancementError, 'mxGraphModel lacks numeric pageHeight' if page_height.nil?
    changed |= set_attribute(model, 'pageHeight', format_number(page_height + CONTENT_SHIFT))
    cells.each { |cell| root.add_element(cell) }
    changed = true
  elsif version == LEGACY_CONTEXT_VERSION
    legacy_ids = %w[
      custom-context-band custom-context-breadcrumb custom-context-question
      custom-context-links custom-context-authority
    ]
    missing_legacy = legacy_ids - existing_context_ids
    unless missing_legacy.empty?
      raise EnhancementError, "legacy context page is missing #{missing_legacy.join(', ')}"
    end
    delta = CONTENT_SHIFT - LEGACY_CONTENT_SHIFT
    changed |= shift_top_level_content(model, delta, existing_context_ids)
    page_height = numeric_attribute(model, 'pageHeight')
    raise EnhancementError, 'mxGraphModel lacks numeric pageHeight' if page_height.nil?
    changed |= set_attribute(model, 'pageHeight', format_number(page_height + delta))
    existing_context_ids.each do |id|
      cell = index.fetch(id)
      root.delete_element(cell)
    end
    cells.each { |cell| root.add_element(cell) }
    changed = true
  elsif version == CONTEXT_VERSION
    unless present.length == CONTEXT_IDS.length
      missing = CONTEXT_IDS - present
      raise EnhancementError, "context version #{CONTEXT_VERSION} page is missing #{missing.join(', ')}"
    end

    cells.each do |expected|
      actual = index.fetch(expected.attributes['id'])
      next if context_cell_signature(actual) == context_cell_signature(expected)

      replacement = expected.deep_clone
      root.insert_after(actual, replacement)
      root.delete_element(actual)
      changed = true
    end
  else
    raise EnhancementError, "unsupported customDiagramContextVersion=#{version.inspect}"
  end

  changed |= set_attribute(model, 'customDiagramContextVersion', CONTEXT_VERSION)
  changed
end

def serialize(document)
  output = +''
  formatter = REXML::Formatters::Pretty.new(2)
  formatter.compact = true
  formatter.write(document, output)
  output << "\n" unless output.end_with?("\n")
  output
end

def transform(path, router_by_skill)
  source = File.binread(path)
  document = REXML::Document.new(source)
  mxfile = document.root
  raise EnhancementError, 'root element is not mxfile' unless mxfile && mxfile.name == 'mxfile'

  skill = File.basename(File.dirname(File.dirname(path)))
  router = router_by_skill[skill]
  raise EnhancementError, "#{skill} is not reachable through an implementation router" if router.nil?

  diagrams = mxfile.get_elements('diagram')
  raise EnhancementError, 'mxfile has no diagram pages' if diagrams.empty?
  changed = false
  diagram_slug = File.basename(path, '.drawio')

  diagrams.each do |diagram|
    model = diagram.elements['mxGraphModel']
    if model.nil?
      raise EnhancementError, "compressed or malformed page #{diagram.attributes['name'].inspect} is unsupported"
    end

    page_width = numeric_attribute(model, 'pageWidth')
    raise EnhancementError, 'mxGraphModel lacks numeric pageWidth' if page_width.nil?
    page_name = diagram.attributes['name'].to_s
    page_name = humanize_slug(diagram_slug) if page_name.empty?
    breadcrumb = [ARCHITECTURE_SKILL, router, skill, "#{diagram_slug} / #{page_name}"].uniq.join(' → ')
    subject = page_subject(model, diagram, humanize_slug(diagram_slug))
    question = "What does #{subject} own, how do inputs become outcomes, and where do decision, failure, or recovery paths hand off?"
    starts, ends_with = page_boundary_concepts(model)
    owns = "#{skill} · #{subject}"
    adjacent_skills = route_targets(router).reject { |name| name == skill }.first(2)
    adjacent_text = adjacent_skills.empty? ? 'none at this resolution' : adjacent_skills.join(' + ')
    defers = "#{router}/architecture.drawio owns domain placement · #{adjacent_text} own adjacent protocols"
    contracts = diagram_contract_anchors(skill, diagram_slug, model)
    broader_view = "root routing → #{router} overview → #{skill}/SKILL.md"

    cells = context_cells(
      page_width,
      breadcrumb,
      lifecycle_text(router, skill),
      question,
      starts,
      owns,
      ends_with,
      defers,
      contracts,
      broader_view,
      layer_id(model)
    )
    changed |= install_or_refresh_context(model, cells)
    changed |= ensure_diagram_header_gutter(model)
    changed |= enhance_edges(model)
    changed |= route_blocked_edges(model)
    changed |= layout_edge_labels(model)
  end

  return [source, false] unless changed

  output = serialize(document)
  # Some dense paths can be recomputed to the byte-identical route. Treat the
  # serialized document—not an internal "touched" flag—as the idempotence
  # authority so --check reports only material repository changes.
  byte_identical = output.b == source
  [byte_identical ? source : output, !byte_identical]
rescue REXML::ParseException => e
  raise EnhancementError, "invalid XML: #{e.message}"
end

def atomic_write(path, content)
  directory = File.dirname(path)
  temporary = File.join(directory, ".#{File.basename(path)}.enhance-#{Process.pid}.tmp")
  mode = File.stat(path).mode & 0o777
  begin
    File.open(temporary, File::WRONLY | File::CREAT | File::TRUNC, mode) do |file|
      file.binmode
      file.write(content)
      file.flush
      file.fsync
    end
    File.rename(temporary, path)
  ensure
    File.delete(temporary) if File.exist?(temporary)
  end
end

options = { check: false }
parser = OptionParser.new do |opts|
  opts.banner = 'Usage: enhance_custom_drawio.rb [--check] [FILE_OR_DIRECTORY ...]'
  opts.on('--check', 'Report assets that would change; do not write') { options[:check] = true }
  opts.on('-h', '--help', 'Show this help') do
    puts opts
    exit 0
  end
end

begin
  parser.parse!(ARGV)
  mapping = router_mapping
  paths = custom_asset_paths(ARGV)
  raise EnhancementError, 'no non-generated Draw.io assets found' if paths.empty?

  changes = []
  paths.each do |path|
    output, changed = transform(path, mapping)
    next unless changed

    changes << [path, output]
  end
  # Complete parsing, route validation, and transformation for the whole batch
  # before the first write. One malformed later asset must not leave an earlier
  # subset migrated to a different context version.
  changes.each { |path, output| atomic_write(path, output) } unless options[:check]
  changed_paths = changes.map(&:first)

  if options[:check]
    changed_paths.each { |path| puts "would enhance #{path.sub(%r{\A#{Regexp.escape(ROOT)}/?}, '')}" }
    if changed_paths.empty?
      puts "custom Draw.io context is current for #{paths.length} asset(s)"
      exit 0
    end
    warn "#{changed_paths.length} of #{paths.length} custom Draw.io asset(s) require enhancement"
    exit 1
  end

  puts "enhanced #{changed_paths.length} of #{paths.length} custom Draw.io asset(s)"
rescue EnhancementError, OptionParser::ParseError => e
  warn "enhance_custom_drawio: #{e.message}"
  exit 2
end
