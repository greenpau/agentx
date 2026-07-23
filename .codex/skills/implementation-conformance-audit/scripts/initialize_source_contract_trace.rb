#!/usr/bin/env ruby
# frozen_string_literal: true

# One-time initializer for the source-to-contract review scaffold. This script
# deliberately refuses to overwrite the trace. Every generated row is marked
# unreviewed and keeps the architecture audit red until a reviewer supplies
# contract/scenario anchors and binds the exact source hash deliberately.

require 'fileutils'

ROOT = File.expand_path('../../../..', __dir__)
LEDGER = File.join(ROOT, '.codex/skills/implementation-conformance-audit/references/source-coverage.tsv')
TRACE = File.join(ROOT, '.codex/skills/implementation-conformance-audit/references/source-contract-trace.tsv')
REVIEW_GENERATION = 'unreviewed'.freeze

abort "coverage ledger missing: #{LEDGER}" unless File.file?(LEDGER)
abort "refusing to overwrite reviewed trace: #{TRACE}" if File.exist?(TRACE)

ledger_lines = File.readlines(LEDGER, chomp: true)
header = ledger_lines.shift
expected = "path\tlines\tbytes\tsha256\tprimary_owner\tcontract_scope"
abort "unexpected ledger header: #{header.inspect}" unless header == expected

output = String.new("path\treviewed_sha256\tprimary_owner\tcontract_ids\tscenario_ids\treview_generation\tcoverage_class\tboundary_reason\n")
ledger_lines.each_with_index do |line, index|
  path, _lines, _bytes, sha, owner, _scope = line.split("\t", -1)
  abort "#{LEDGER}:#{index + 2}: missing owner" if owner.nil? || owner.empty?
  output << [
    path, sha, owner, '', '', REVIEW_GENERATION, 'unreviewed',
    'initializer scaffold; perform semantic review'
  ].join("\t") << "\n"
end

FileUtils.mkdir_p(File.dirname(TRACE))
temporary = "#{TRACE}.tmp-#{Process.pid}"
File.binwrite(temporary, output)
File.rename(temporary, TRACE)
puts "initialized #{TRACE}: #{ledger_lines.length} unreviewed artifact bindings (audit intentionally fails until review)"
