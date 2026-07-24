#!/usr/bin/env ruby
# frozen_string_literal: true

require 'digest'
require 'fileutils'
require 'minitest/autorun'
require 'open3'
require 'tmpdir'

SCRIPT = File.expand_path('review_release_evidence.rb', __dir__)
REFERENCE_ROOT = '.codex/skills/implementation-conformance-audit/references'

class ReviewReleaseEvidenceTest < Minitest::Test
  def test_reviews_an_exact_patch_release
    with_repository do |root|
      advance_release(root)

      output, error, status = Open3.capture3('ruby', SCRIPT, '--root', root)

      assert status.success?, error
      assert_includes output, 'reviewed release evidence'
      row = File.readlines(File.join(root, REFERENCE_ROOT, 'source-contract-trace.tsv'), chomp: true)[1].split("\t")
      assert_equal Digest::SHA256.file(File.join(root, 'main.go')).hexdigest, row[1]
      assert_match(/\A\d{4}-\d{2}-\d{2}-v1\.0\.5-release-review\z/, row[5])
      assert_equal 'PLAT-005,PLAT-035', row[3]
      assert_equal 'CONF-021,PLAT-A07', row[4]
    end
  end

  def test_rejects_an_unrelated_source_change
    with_repository do |root|
      advance_release(root)
      File.open(File.join(root, 'main.go'), 'a') { |file| file << "// unrelated\n" }
      refresh_coverage(root)

      _output, error, status = Open3.capture3('ruby', SCRIPT, '--root', root)

      refute status.success?
      assert_includes error, 'changes beyond the two release-version fallbacks'
      assert_equal committed_main_sha(root), trace_sha(root)
    end
  end

  def test_rejects_a_stale_prior_review
    with_repository do |root|
      advance_release(root)
      trace_path = File.join(root, REFERENCE_ROOT, 'source-contract-trace.tsv')
      trace = File.binread(trace_path).sub(committed_main_sha(root), '0' * 64)
      File.binwrite(trace_path, trace)

      _output, error, status = Open3.capture3('ruby', SCRIPT, '--root', root)

      refute status.success?
      assert_includes error, 'working main.go trace does not contain the committed reviewed hash'
      assert_equal '0' * 64, trace_sha(root)
    end
  end

  private

  def with_repository
    Dir.mktmpdir('agentx-release-evidence-') do |root|
      FileUtils.mkdir_p(File.join(root, REFERENCE_ROOT))
      write_release_files(root, '1.0.4')
      run_git(root, 'init', '-q')
      run_git(root, 'config', 'user.email', 'test@example.com')
      run_git(root, 'config', 'user.name', 'Release Evidence Test')
      run_git(root, 'add', '.')
      run_git(root, 'commit', '-q', '-m', 'fixture')
      yield root
    end
  end

  def advance_release(root)
    write_release_files(root, '1.0.5', preserve_trace: true)
  end

  def write_release_files(root, version, preserve_trace: false)
    File.binwrite(File.join(root, 'VERSION'), version)
    File.binwrite(
      File.join(root, 'main.go'),
      <<~GO
        package main

        func main() {
        	app.SetVersion(appVersion, "#{version}")
        	app.SetGitCommit(gitCommit, "#{version}")
        }
      GO
    )
    refresh_coverage(root)
    write_trace(root) unless preserve_trace
  end

  def refresh_coverage(root)
    source = File.binread(File.join(root, 'main.go'))
    lines = source.count("\n") + (source.end_with?("\n") ? 0 : 1)
    body = String.new("path\tlines\tbytes\tsha256\tprimary_owner\tcontract_scope\n")
    body << [
      'main.go',
      lines,
      source.bytesize,
      Digest::SHA256.hexdigest(source),
      'implementation-platform-lifecycle',
      'portable lifecycle'
    ].join("\t") << "\n"
    File.binwrite(File.join(root, REFERENCE_ROOT, 'source-coverage.tsv'), body)
  end

  def write_trace(root)
    body = String.new("path\treviewed_sha256\tprimary_owner\tcontract_ids\tscenario_ids\treview_generation\tcoverage_class\tboundary_reason\n")
    body << [
      'main.go',
      Digest::SHA256.file(File.join(root, 'main.go')).hexdigest,
      'implementation-platform-lifecycle',
      'PLAT-005,PLAT-035',
      'CONF-021,PLAT-A07',
      '2026-07-23-build-identity-review',
      'normative',
      '-'
    ].join("\t") << "\n"
    File.binwrite(File.join(root, REFERENCE_ROOT, 'source-contract-trace.tsv'), body)
  end

  def run_git(root, *arguments)
    _output, error, status = Open3.capture3('git', '-C', root, *arguments)
    raise error unless status.success?
  end

  def committed_main_sha(root)
    source, error, status = Open3.capture3('git', '-C', root, 'show', 'HEAD:main.go')
    raise error unless status.success?
    Digest::SHA256.hexdigest(source)
  end

  def trace_sha(root)
    File.readlines(File.join(root, REFERENCE_ROOT, 'source-contract-trace.tsv'), chomp: true)[1].split("\t")[1]
  end
end
