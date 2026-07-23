#!/usr/bin/env ruby
# frozen_string_literal: true

# Mechanical progressive-disclosure helper. It inserts a linked top-level
# contents list into long implementation references that do not already have
# one. Review the generated labels when headings contain unusual markup.

ROOT = File.expand_path('../../../..', __dir__)
SKILL_ROOT = File.join(ROOT, '.codex/skills')

def display_heading(raw)
  raw
    .gsub(/\[([^\]]+)\]\([^)]+\)/, '\\1')
    .gsub(/[`*_]/, '')
    .strip
end

def anchor_for(label)
  label
    .downcase
    .gsub(/[^\p{Alnum}\s-]/u, '')
    .strip
    .gsub(/\s+/, '-')
    .gsub(/-+/, '-')
end

changed = []
Dir.glob(File.join(SKILL_ROOT, 'implementation-*/references/**/*.md')).sort.each do |path|
  lines = File.readlines(path)
  next unless lines.length > 100
  next if lines.first(50).any? { |line| line.match?(/^## (?:Contents|Table of contents)\s*$/i) }

  headings = lines.each_with_object([]) do |line, collected|
    match = line.match(/^## (?!#)(.+?)\s*$/)
    next unless match
    label = display_heading(match[1])
    next if label.match?(/\A(?:Contents|Table of contents)\z/i)
    collected << [label, anchor_for(label)]
  end
  next if headings.empty?

  insertion = lines.index { |line| line.match?(/^## (?!#)/) } || 1
  toc = ["## Contents\n", "\n"]
  headings.each_with_index do |(label, anchor), index|
    toc << "#{index + 1}. [#{label}](##{anchor})\n"
  end
  toc << "\n"
  lines.insert(insertion, *toc)
  File.binwrite(path, lines.join)
  changed << path.delete_prefix("#{ROOT}/")
end

puts "added contents lists to #{changed.length} references"
changed.each { |path| puts path }
