#!/usr/bin/env ruby
# frozen_string_literal: true

require 'digest'
require 'fileutils'
require_relative 'source_inventory'

ROOT = File.expand_path('../../../..', __dir__)
OUTPUT = File.join(ROOT, '.codex/skills/implementation-conformance-audit/references/source-coverage.tsv')

files = SourceInventory.production_go_files(ROOT)
SourceInventory.validate_ownership!(files)

rows = files.map do |path|
  absolute = File.join(ROOT, path)
  data = File.binread(absolute)
  lines = data.empty? ? 0 : data.count("\n") + (data.end_with?("\n") ? 0 : 1)
  owner = SourceInventory.owner_for(path)
  [path, lines, data.bytesize, Digest::SHA256.hexdigest(data), owner, SourceInventory::SCOPE.fetch(owner)]
end

body = String.new("path\tlines\tbytes\tsha256\tprimary_owner\tcontract_scope\n")
rows.each { |row| body << row.join("\t") << "\n" }

if ARGV.include?('--check')
  abort "coverage ledger missing: #{OUTPUT}" unless File.file?(OUTPUT)
  abort 'coverage ledger is stale; run build_source_coverage.rb' unless File.binread(OUTPUT) == body
  puts "coverage ledger current: #{rows.length} artifacts, #{rows.sum { |row| row[1] }} lines"
else
  FileUtils.mkdir_p(File.dirname(OUTPUT))
  temporary = "#{OUTPUT}.tmp-#{Process.pid}"
  File.binwrite(temporary, body)
  File.rename(temporary, OUTPUT)
  puts "wrote #{OUTPUT}: #{rows.length} artifacts, #{rows.sum { |row| row[1] }} lines"
end
