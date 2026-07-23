#!/usr/bin/env ruby
# frozen_string_literal: true

skill_root = if ARGV[0]
               File.expand_path(ARGV[0])
             else
               File.expand_path('..', __dir__)
             end
repo_root = File.expand_path('../../..', skill_root)
catalog_path = File.join(skill_root, 'references', 'command-catalog.md')
index_path = File.join(skill_root, 'references', 'command-workflow-index.md')
manifest_path = File.join(skill_root, 'references', 'source-registry-manifest.md')
skill_path = File.join(skill_root, 'SKILL.md')
registry_path = File.join(repo_root, 'commands.ts')
tag_descriptor_path = File.join(repo_root, 'commands', 'tag', 'index.ts')

errors = []

def duplicates(values)
  values.group_by { |value| value }
    .select { |_value, occurrences| occurrences.length > 1 }
    .keys
    .sort
end

def first_code_span(value)
  value[/`([^`]+)`/, 1]
end

# Parse the two registry arrays, not imports. Imported compatibility artifacts
# are intentionally invisible until one of these arrays contributes a symbol.
def registry_symbols(block)
  symbols = []
  block.each_line do |raw_line|
    line = raw_line.sub(%r{//.*}, '').strip
    next if line.empty?

    direct = line.match(/\A([A-Za-z_]\w*)(?:\(\))?,?\z/)
    if direct
      symbols << direct[1]
      next
    end

    # Conditional contributions have one or more symbols in their true array,
    # for example `[webCmd]` or `[logout, login()]`.
    line.scan(/\[([A-Za-z_]\w*(?:\(\))?(?:\s*,\s*[A-Za-z_]\w*(?:\(\))?)*)\]/) do |match|
      match.fetch(0).split(',').each do |candidate|
        symbols << candidate.strip.sub(/\(\)\z/, '')
      end
    end
  end
  symbols
end

[catalog_path, index_path, manifest_path, skill_path, registry_path, tag_descriptor_path].each do |path|
  errors << "missing required file: #{path}" unless File.file?(path)
end

if errors.empty?
  catalog_text = File.read(catalog_path)
  index_text = File.read(index_path)
  manifest_text = File.read(manifest_path)
  skill_text = File.read(skill_path)
  registry_text = File.read(registry_path)

  identity_count = manifest_text[/Registry identity count:\s*\*\*(\d+)\*\*/, 1]&.to_i
  descriptor_count = manifest_text[/Registry descriptor-symbol count:\s*\*\*(\d+)\*\*/, 1]&.to_i
  errors << 'source registry manifest has no declared identity count' unless identity_count&.positive?
  errors << 'source registry manifest has no declared descriptor-symbol count' unless descriptor_count&.positive?

  manifest_rows = []
  manifest_text.each_line.with_index(1) do |line, line_number|
    next unless line.match?(/^\|\s*Registry anchor:\s*CC-\d{3}\s*\|/)

    cells = line.split('|', -1).map(&:strip)
    id_match = cells.fetch(1, '').match(/\ARegistry anchor:\s*(CC-\d{3})\z/)
    canonical = first_code_span(cells.fetch(2, ''))
    symbols = cells.fetch(3, '').scan(/`([A-Za-z_]\w*)`/).flatten
    registry_set = cells.fetch(4, '')
    unless id_match && canonical && !symbols.empty? && !registry_set.empty?
      errors << "malformed source-registry row at #{manifest_path}:#{line_number}"
      next
    end
    manifest_rows << {
      id: id_match[1],
      canonical: canonical,
      symbols: symbols,
      registry_set: registry_set,
      line: line_number
    }
  end

  manifest_ids = manifest_rows.map { |row| row[:id] }
  manifest_symbols = manifest_rows.flat_map { |row| row[:symbols] }
  errors << "duplicate manifest IDs: #{duplicates(manifest_ids).join(', ')}" unless duplicates(manifest_ids).empty?
  errors << "duplicate manifest symbols: #{duplicates(manifest_symbols).join(', ')}" unless duplicates(manifest_symbols).empty?

  if identity_count&.positive?
    expected_ids = (1..identity_count).map { |number| format('CC-%03d', number) }
    unless manifest_ids.sort == expected_ids
      errors << "manifest IDs differ from contiguous CC-001..#{expected_ids.last} (missing=#{(expected_ids - manifest_ids).join(',')}; extra=#{(manifest_ids - expected_ids).join(',')})"
    end
    errors << "manifest row count #{manifest_rows.length} differs from declared identity count #{identity_count}" unless manifest_rows.length == identity_count
  else
    expected_ids = manifest_ids.uniq.sort
  end
  if descriptor_count&.positive? && manifest_symbols.length != descriptor_count
    errors << "manifest symbol count #{manifest_symbols.length} differs from declared descriptor-symbol count #{descriptor_count}"
  end

  tag_manifest_rows = manifest_rows.select { |row| row[:canonical] == 'tag' }
  unless tag_manifest_rows.length == 1 && tag_manifest_rows.first[:id] == 'CC-112' && tag_manifest_rows.first[:symbols] == ['tag']
    errors << 'required /tag identity must be exactly CC-112 -> tag in source registry manifest'
  end

  internal_match = registry_text.match(/export const INTERNAL_ONLY_COMMANDS = \[(.*?)\]\.filter\(Boolean\)/m)
  base_match = registry_text.match(/const COMMANDS = memoize\(\(\): Command\[\] => \[(.*?)\n\]\)\n\nexport const builtInCommandNames/m)
  unless internal_match && base_match
    errors << 'could not locate built-in/internal source registry arrays in commands.ts'
    actual_symbols = []
  else
    actual_symbols = registry_symbols(base_match[1]) + registry_symbols(internal_match[1])
  end
  actual_duplicates = duplicates(actual_symbols)
  errors << "source registry repeats descriptor symbols: #{actual_duplicates.join(', ')}" unless actual_duplicates.empty?
  unless actual_symbols.sort == manifest_symbols.sort
    errors << "source registry symbols differ from manifest (unmanifested=#{(actual_symbols - manifest_symbols).sort.join(',')}; no-longer-registered=#{(manifest_symbols - actual_symbols).sort.join(',')})"
  end
  if descriptor_count&.positive? && actual_symbols.length != descriptor_count
    errors << "actual source registry symbol count #{actual_symbols.length} differs from declared #{descriptor_count}"
  end
  unless File.read(tag_descriptor_path).match?(/\bname:\s*['\"]tag['\"]/) && actual_symbols.include?('tag')
    errors << 'registered /tag descriptor or tag registry symbol is missing'
  end

  catalog_rows = []
  catalog_text.each_line.with_index(1) do |line, line_number|
    next unless line.match?(/^\|\s*CC-\d{3}\s*\|/)

    cells = line.split('|', -1).map(&:strip)
    canonical = first_code_span(cells.fetch(2, ''))
    catalog_rows << { id: cells.fetch(1), canonical: canonical, line: line_number }
    errors << "catalog row #{cells.fetch(1)} has no canonical code-span name at line #{line_number}" unless canonical
  end
  catalog_ids = catalog_rows.map { |row| row[:id] }

  mapping_rows = []
  index_text.each_line.with_index(1) do |line, line_number|
    next unless line.match?(/^\|\s*Catalog anchor:\s*CC-\d{3}\s*\|/)

    cells = line.split('|', -1).map(&:strip)
    id_match = cells.fetch(1, '').match(/\ACatalog anchor:\s*(CC-\d{3})\z/)
    unless id_match && cells.length >= 6
      errors << "malformed mapping row at #{index_path}:#{line_number}"
      next
    end

    mapping_rows << {
      id: id_match[1],
      canonical: first_code_span(cells.fetch(2, '')),
      classification: cells.fetch(3, ''),
      workflow: cells.fetch(4, ''),
      line: line_number
    }
  end

  mapping_ids = mapping_rows.map { |row| row[:id] }
  catalog_duplicates = duplicates(catalog_ids)
  mapping_duplicates = duplicates(mapping_ids)
  errors << "duplicate catalog IDs: #{catalog_duplicates.join(', ')}" unless catalog_duplicates.empty?
  errors << "duplicate workflow mapping IDs: #{mapping_duplicates.join(', ')}" unless mapping_duplicates.empty?

  unless catalog_ids.sort == expected_ids
    errors << "catalog IDs differ from source manifest (missing=#{(expected_ids - catalog_ids).join(',')}; extra=#{(catalog_ids - expected_ids).join(',')})"
  end
  unless mapping_ids.sort == expected_ids
    errors << "mapping IDs differ from source manifest (missing=#{(expected_ids - mapping_ids).join(',')}; extra=#{(mapping_ids - expected_ids).join(',')})"
  end

  manifest_by_id = manifest_rows.to_h { |row| [row[:id], row] }
  (catalog_rows + mapping_rows).each do |row|
    expected = manifest_by_id[row[:id]]&.fetch(:canonical, nil)
    next if expected == row[:canonical]

    errors << "#{row[:id]} canonical name #{row[:canonical].inspect} differs from source manifest #{expected.inspect} at line #{row[:line]}"
  end

  allowed_classifications = %w[workflow atomic prompt profile stub]
  mapped_workflows = []
  mapping_rows.each do |row|
    unless allowed_classifications.include?(row[:classification])
      errors << "#{row[:id]} has invalid classification #{row[:classification].inspect} at line #{row[:line]}"
    end

    if row[:classification] == 'workflow'
      if row[:workflow].match?(/\ACMD-WF-[A-Z0-9-]+\z/)
        mapped_workflows << row[:workflow]
      else
        errors << "#{row[:id]} is workflow but has invalid primary workflow #{row[:workflow].inspect}"
      end
    elsif row[:workflow] != '—'
      errors << "#{row[:id]} is #{row[:classification]} but maps to #{row[:workflow].inspect}"
    end
  end

  unless index_text.match?(/^## CC-155 — Workflow coverage maintenance$/)
    errors << 'missing literal CC-155 workflow coverage maintenance heading'
  end

  linked_workflow_paths = skill_text.scan(/\]\((references\/workflow-[^)]+\.md)\)/).flatten.uniq.sort
  errors << 'SKILL.md links no workflow references' if linked_workflow_paths.empty?
  errors << 'SKILL.md does not link source-registry-manifest.md' unless skill_text.include?('(references/source-registry-manifest.md)')

  definition_ids = []
  linked_workflow_paths.each do |relative_path|
    absolute_path = File.join(skill_root, relative_path)
    unless File.file?(absolute_path)
      errors << "broken workflow reference link: #{relative_path}"
      next
    end
    definition_ids.concat(File.read(absolute_path).scan(/^## (CMD-WF-[A-Z0-9-]+) —/).flatten)
  end

  definition_duplicates = duplicates(definition_ids)
  errors << "duplicate CMD-WF definitions: #{definition_duplicates.join(', ')}" unless definition_duplicates.empty?
  undefined = mapped_workflows.uniq - definition_ids.uniq
  orphaned = definition_ids.uniq - mapped_workflows.uniq
  errors << "mapped but undefined workflows: #{undefined.sort.join(', ')}" unless undefined.empty?
  errors << "defined but unmapped workflows: #{orphaned.sort.join(', ')}" unless orphaned.empty?

  if errors.empty?
    puts "command workflow audit passed: #{catalog_ids.length} catalog identities, #{actual_symbols.length} registered descriptor symbols, #{mapping_rows.length} mappings, #{definition_ids.uniq.length} workflow definitions"
  end
end

unless errors.empty?
  warn "command workflow audit failed (#{errors.length} issue#{errors.length == 1 ? '' : 's'}):"
  errors.each { |error| warn "- #{error}" }
  exit 1
end
