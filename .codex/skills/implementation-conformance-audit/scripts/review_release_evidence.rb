#!/usr/bin/env ruby
# frozen_string_literal: true

require 'date'
require 'digest'
require 'open3'
require 'optparse'
require 'pathname'
require 'set'

DEFAULT_ROOT = File.expand_path('../../../..', __dir__)
COVERAGE_PATH = '.codex/skills/implementation-conformance-audit/references/source-coverage.tsv'
TRACE_PATH = '.codex/skills/implementation-conformance-audit/references/source-contract-trace.tsv'
ALLOWED_CHANGED_PATHS = Set.new(['VERSION', 'main.go', COVERAGE_PATH, TRACE_PATH]).freeze
VERSION_PATTERN = /\A(\d+)\.(\d+)\.(\d+)\z/
FALLBACK_PATTERNS = {
  version: /^(\s*app\.SetVersion\(appVersion,\s*")(\d+\.\d+\.\d+)("\)\s*)$/,
  commit: /^(\s*app\.SetGitCommit\(gitCommit,\s*")(\d+\.\d+\.\d+)("\)\s*)$/
}.freeze
COVERAGE_HEADER = %w[path lines bytes sha256 primary_owner contract_scope].freeze
TRACE_HEADER = %w[
  path reviewed_sha256 primary_owner contract_ids scenario_ids
  review_generation coverage_class boundary_reason
].freeze

def fail_release(message)
  abort "release evidence review failed: #{message}"
end

def git(root, *arguments)
  output, error, status = Open3.capture3('git', '-C', root, *arguments)
  fail_release(error.strip.empty? ? "git #{arguments.join(' ')} failed" : error.strip) unless status.success?
  output
end

def read_version(value, label)
  version = value.strip
  match = VERSION_PATTERN.match(version)
  fail_release("#{label} must contain one major.minor.patch version") unless match
  [version, match.captures.map(&:to_i)]
end

def fallback_version(source, pattern, label)
  matches = source.scan(pattern)
  fail_release("#{label} must appear exactly once in main.go") unless matches.length == 1
  matches.first[1]
end

def replace_fallback(source, pattern, old_version, new_version, label)
  replacement_count = 0
  updated = source.gsub(pattern) do
    replacement_count += 1
    "#{Regexp.last_match(1)}#{new_version}#{Regexp.last_match(3)}"
  end
  fail_release("#{label} must appear exactly once in committed main.go") unless replacement_count == 1
  fail_release("#{label} does not match committed VERSION") unless fallback_version(source, pattern, label) == old_version
  updated
end

def parse_tsv(content, label, expected_header)
  lines = content.lines(chomp: true)
  fail_release("#{label} is empty") if lines.empty?
  header = lines.shift.split("\t", -1)
  fail_release("#{label} has an unexpected header") unless header == expected_header
  rows = lines.map { |line| line.split("\t", -1) }
  fail_release("#{label} has a malformed row") unless rows.all? { |row| row.length == expected_header.length }
  [header, rows]
end

def read_tsv(path, expected_header)
  parse_tsv(File.binread(path), path, expected_header)
end

options = { root: DEFAULT_ROOT }
OptionParser.new do |parser|
  parser.on('--root PATH', 'Repository root (used by isolated tests)') { |path| options[:root] = path }
end.parse!

root = File.expand_path(options.fetch(:root))
version_path = File.join(root, 'VERSION')
main_path = File.join(root, 'main.go')
coverage_path = File.join(root, COVERAGE_PATH)
trace_path = File.join(root, TRACE_PATH)
[version_path, main_path, coverage_path, trace_path].each do |path|
  fail_release("missing required file #{Pathname.new(path).relative_path_from(Pathname.new(root))}") unless File.file?(path)
end

current_version, current_parts = read_version(File.binread(version_path), 'VERSION')
previous_version, previous_parts = read_version(git(root, 'show', 'HEAD:VERSION'), 'committed VERSION')
unless current_parts[0, 2] == previous_parts[0, 2] && current_parts[2] == previous_parts[2] + 1
  fail_release("VERSION must advance exactly one patch from #{previous_version} to #{current_version}")
end

current_source = File.binread(main_path)
previous_source = git(root, 'show', 'HEAD:main.go')
FALLBACK_PATTERNS.each do |name, pattern|
  value = fallback_version(current_source, pattern, name)
  fail_release("#{name} fallback #{value} does not match VERSION #{current_version}") unless value == current_version
end

expected_source = previous_source.dup
FALLBACK_PATTERNS.each do |name, pattern|
  expected_source = replace_fallback(
    expected_source,
    pattern,
    previous_version,
    current_version,
    name
  )
end
fail_release('main.go contains changes beyond the two release-version fallbacks') unless current_source == expected_source

changed_paths = git(root, 'diff', '--name-only', 'HEAD', '--').lines(chomp: true).to_set
unexpected_paths = changed_paths - ALLOWED_CHANGED_PATHS
fail_release("unexpected working-tree changes: #{unexpected_paths.to_a.sort.join(', ')}") unless unexpected_paths.empty?
%w[VERSION main.go].each do |path|
  fail_release("#{path} has not changed from HEAD") unless changed_paths.include?(path)
end

current_sha = Digest::SHA256.hexdigest(current_source)
previous_sha = Digest::SHA256.hexdigest(previous_source)

_coverage_header, coverage_rows = read_tsv(coverage_path, COVERAGE_HEADER)
_committed_coverage_header, committed_coverage_rows = parse_tsv(
  git(root, 'show', "HEAD:#{COVERAGE_PATH}"),
  "committed #{COVERAGE_PATH}",
  COVERAGE_HEADER
)
unless coverage_rows.map(&:first) == committed_coverage_rows.map(&:first)
  fail_release('source-coverage.tsv changed the committed artifact inventory')
end
coverage_rows_for_main = coverage_rows.select { |row| row[0] == 'main.go' }
committed_coverage_rows_for_main = committed_coverage_rows.select { |row| row[0] == 'main.go' }
fail_release('source-coverage.tsv must contain exactly one main.go row') unless coverage_rows_for_main.length == 1
unless committed_coverage_rows_for_main.length == 1
  fail_release('committed source-coverage.tsv must contain exactly one main.go row')
end
coverage_row = coverage_rows_for_main.first
committed_coverage_row = committed_coverage_rows_for_main.first
coverage_rows.each_index do |index|
  next if coverage_rows[index][0] == 'main.go'
  fail_release('source-coverage.tsv changed an unrelated artifact row') unless coverage_rows[index] == committed_coverage_rows[index]
end
unless coverage_row.values_at(0, 4, 5) == committed_coverage_row.values_at(0, 4, 5)
  fail_release('source-coverage.tsv changed the main.go classification')
end
fail_release('committed source-coverage.tsv does not match committed main.go') unless committed_coverage_row[3] == previous_sha
current_lines = current_source.empty? ? 0 : current_source.count("\n") + (current_source.end_with?("\n") ? 0 : 1)
unless coverage_row.values_at(1, 2, 3) == [current_lines.to_s, current_source.bytesize.to_s, current_sha]
  fail_release('source-coverage.tsv does not match the released main.go')
end
unless coverage_row[4] == 'implementation-platform-lifecycle'
  fail_release('source-coverage.tsv changed the main.go primary owner')
end

trace_header, trace_rows = read_tsv(trace_path, TRACE_HEADER)
_committed_trace_header, committed_trace_rows = parse_tsv(
  git(root, 'show', "HEAD:#{TRACE_PATH}"),
  "committed #{TRACE_PATH}",
  TRACE_HEADER
)
unless trace_rows.map(&:first) == committed_trace_rows.map(&:first)
  fail_release('source-contract-trace.tsv changed the committed artifact inventory')
end
trace_rows_for_main = trace_rows.each_index.select { |index| trace_rows[index][0] == 'main.go' }
committed_trace_rows_for_main = committed_trace_rows.each_index.select do |index|
  committed_trace_rows[index][0] == 'main.go'
end
fail_release('source-contract-trace.tsv must contain exactly one main.go row') unless trace_rows_for_main.length == 1
unless committed_trace_rows_for_main.length == 1
  fail_release('committed source-contract-trace.tsv must contain exactly one main.go row')
end
trace_index = trace_rows_for_main.first
trace_row = trace_rows[trace_index]
committed_trace_row = committed_trace_rows[committed_trace_rows_for_main.first]
trace_rows.each_index do |index|
  next if trace_rows[index][0] == 'main.go'
  unless trace_rows[index] == committed_trace_rows[index]
    fail_release('source-contract-trace.tsv changed an unrelated artifact row')
  end
end
unless trace_row.values_at(0, 2, 3, 4, 6, 7) == committed_trace_row.values_at(0, 2, 3, 4, 6, 7)
  fail_release('source-contract-trace.tsv changed the main.go semantic binding')
end

fail_release('main.go trace changed primary owner') unless trace_row[2] == 'implementation-platform-lifecycle'
fail_release('main.go trace is missing PLAT-005') unless trace_row[3].split(',').include?('PLAT-005')
fail_release('main.go trace is missing PLAT-A07') unless trace_row[4].split(',').include?('PLAT-A07')
fail_release('main.go trace must remain normative') unless trace_row[6] == 'normative' && trace_row[7] == '-'
fail_release('committed main.go hash is not reviewed') unless committed_trace_row[1] == previous_sha

expected_generation = "#{Date.today.iso8601}-v#{current_version}-release-review"
if trace_row[1] == current_sha
  fail_release('current main.go trace has an unexpected review generation') unless trace_row[5] == expected_generation
  puts "release evidence current: main.go #{current_sha}, v#{current_version}"
  exit
end
unless trace_row[1] == previous_sha
  fail_release('working main.go trace does not contain the committed reviewed hash')
end

trace_row[1] = current_sha
trace_row[5] = expected_generation
body = String.new("#{trace_header.join("\t")}\n")
trace_rows.each { |row| body << row.join("\t") << "\n" }
temporary = "#{trace_path}.tmp-#{Process.pid}"
File.binwrite(temporary, body)
File.rename(temporary, trace_path)
puts "reviewed release evidence: main.go #{previous_sha} -> #{current_sha}, v#{current_version}"
