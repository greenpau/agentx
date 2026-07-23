#!/usr/bin/env ruby
# frozen_string_literal: true

require 'fileutils'
require 'pathname'

ROOT = File.expand_path('../../../..', __dir__)
SKILL_ROOT = File.join(ROOT, '.codex/skills')
OUTPUT = File.join(
  SKILL_ROOT,
  'implementation-conformance-audit/references/contract-scenario-coverage.tsv'
)

SUITE_BY_SKILL = {
  'implementation-architecture' => 'CONF-000',
  'implementation-runtime-core' => 'CONF-000',
  'implementation-capability-runtime' => 'CONF-000',
  'implementation-user-surfaces' => 'CONF-000',
  'implementation-extension-plane' => 'CONF-000',
  'implementation-continuity' => 'CONF-000',
  'implementation-distributed-runtime' => 'CONF-000',
  'implementation-operations' => 'CONF-000',
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
  'implementation-observability' => 'CONF-022',
  'implementation-conformance-audit' => 'CONF-023'
}.freeze

def definition_ids(line)
  suffix = '[A-Z]?[0-9]{2,3}[A-Z]?'
  ids = []
  ids.concat(line.scan(/\*\*([A-Z][A-Z0-9-]*-#{suffix})\b/).flatten)
  ids.concat(line.scan(/^`([A-Z][A-Z0-9-]*-#{suffix})`\s+—/).flatten)
  ids.concat(line.scan(/^\|\s*([A-Z][A-Z0-9-]*-#{suffix})\s*\|/).flatten)
  ids.concat(line.scan(/^\|\s*`([A-Z][A-Z0-9-]*-#{suffix})`\s*\|/).flatten)
  ids.concat(line.scan(/^\x23{2,}\s+`([A-Z][A-Z0-9-]*-#{suffix})`/).flatten)
  ids.uniq
end

records = {}
files = Dir.glob(
  File.join(SKILL_ROOT, 'implementation-*/{SKILL.md,references/**/*.md}')
).sort

files.each do |source|
  skill = Pathname.new(source).each_filename.find { |part| part.start_with?('implementation-') }
  suite = SUITE_BY_SKILL.fetch(skill) { abort "no conformance suite for #{skill}" }
  section_is_scenario = File.basename(source).match?(/acceptance|conformance|scenario/i)

  File.readlines(source).each_with_index do |line, index|
    if (heading = line.match(/^\x23{2,}\s+(.+)/))
      section_is_scenario = heading[1].match?(/acceptance|scenario|conformance/i)
    end
    definition_ids(line).each do |id|
      location = Pathname.new(source).relative_path_from(Pathname.new(ROOT)).to_s + ":#{index + 1}"
      kind = if section_is_scenario || id.start_with?('CONF-') || id.match?(/(?:\A|-)A\d{2,3}\z/) || id.include?('-AC-')
               'scenario'
             else
               'contract'
             end
      abort "duplicate definition #{id}: #{records[id]&.last}, #{location}" if records.key?(id)
      records[id] = [id, kind, skill, suite, location]
    end
  end
end

body = String.new("id\tkind\towner_skill\tparameterized_suite\tdefinition_location\n")
records.keys.sort.each { |id| body << records.fetch(id).join("\t") << "\n" }

if ARGV.include?('--check')
  abort "contract-scenario manifest missing: #{OUTPUT}" unless File.file?(OUTPUT)
  abort 'contract-scenario manifest is stale; run build_contract_scenario_coverage.rb' unless File.binread(OUTPUT) == body
  puts "contract-scenario coverage current: #{records.count { |_id, row| row[1] == 'contract' }} contracts, #{records.count { |_id, row| row[1] == 'scenario' }} scenarios"
else
  FileUtils.mkdir_p(File.dirname(OUTPUT))
  temporary = "#{OUTPUT}.tmp-#{Process.pid}"
  File.binwrite(temporary, body)
  File.rename(temporary, OUTPUT)
  puts "wrote #{OUTPUT}: #{records.length} stable IDs"
end
