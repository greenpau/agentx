#!/usr/bin/env ruby
# frozen_string_literal: true

# Renders one uncompressed Draw.io page with the locally installed diagrams.net
# viewer and a local Chromium-family browser. The source diagram is read only;
# all generated files are confined to an explicit output directory and a
# disposable temporary directory. Network access is disabled by both browser
# policy and the generated page's Content Security Policy.

require 'cgi'
require 'digest'
require 'fileutils'
require 'json'
require 'open3'
require 'optparse'
require 'pathname'
require 'rexml/document'
require 'rexml/formatters/default'
require 'securerandom'
require 'tmpdir'
require 'timeout'
require 'uri'

class PreviewError < StandardError; end

Page = Struct.new(:index, :id, :name, :model)

MAX_VIEWPORT_DIMENSION = 16_384
DEFAULT_TIMEOUT_SECONDS = 25

DRAWIO_GLOBS = [
  '.vscode/extensions/hediet.vscode-drawio-*/drawio/src/main/webapp',
  '.vscode-insiders/extensions/hediet.vscode-drawio-*/drawio/src/main/webapp',
  '.cursor/extensions/hediet.vscode-drawio-*/drawio/src/main/webapp'
].freeze

BROWSER_CANDIDATES = [
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary',
  '/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge',
  '/Applications/Chromium.app/Contents/MacOS/Chromium',
  'google-chrome',
  'google-chrome-stable',
  'chromium',
  'chromium-browser',
  'microsoft-edge'
].freeze

def fail_preview(message)
  raise PreviewError, message
end

def resolve_executable(candidate)
  return nil if candidate.nil? || candidate.empty?

  expanded = File.expand_path(candidate)
  return expanded if File.file?(expanded) && File.executable?(expanded)
  return nil if candidate.include?(File::SEPARATOR)

  ENV.fetch('PATH', '').split(File::PATH_SEPARATOR).each do |directory|
    path = File.join(directory, candidate)
    return path if File.file?(path) && File.executable?(path)
  end
  nil
end

def normalize_drawio_root(candidate)
  return nil if candidate.nil? || candidate.empty?

  expanded = File.expand_path(candidate)
  alternatives = [
    expanded,
    File.join(expanded, 'drawio/src/main/webapp'),
    File.join(expanded, 'src/main/webapp')
  ]
  alternatives.find do |path|
    File.file?(File.join(path, 'js/viewer-static.min.js'))
  end
end

def detect_drawio_root(explicit)
  if explicit
    root = normalize_drawio_root(explicit)
    fail_preview("Draw.io root does not contain js/viewer-static.min.js: #{explicit}") unless root
    return root
  end

  candidates = []
  DRAWIO_GLOBS.each do |suffix|
    candidates.concat(Dir.glob(File.join(Dir.home, suffix)))
  end
  valid = candidates.map { |candidate| normalize_drawio_root(candidate) }.compact
  fail_preview(<<~MESSAGE.strip) if valid.empty?
    Could not find local Draw.io viewer assets. Install the VS Code Draw.io extension
    or pass --drawio-root PATH pointing at its drawio/src/main/webapp directory.
  MESSAGE

  valid.max_by { |path| [File.mtime(path).to_i, path] }
end

def detect_browser(explicit)
  if explicit
    browser = resolve_executable(explicit)
    fail_preview("Browser is not an executable file or PATH command: #{explicit}") unless browser
    return browser
  end

  ([ENV['CHROME_BIN']] + BROWSER_CANDIDATES).each do |candidate|
    browser = resolve_executable(candidate)
    return browser if browser
  end

  fail_preview(<<~MESSAGE.strip)
    Could not find headless Google Chrome, Chromium, or Microsoft Edge.
    Pass --chrome PATH to a Chromium-family browser executable.
  MESSAGE
end

def serialize_element(element)
  output = +''
  REXML::Formatters::Default.new.write(element, output)
  output
end

def load_pages(path)
  source = File.binread(path)
  if source.match?(/<!DOCTYPE|<!ENTITY/i)
    fail_preview('DOCTYPE and ENTITY declarations are not accepted in preview input')
  end

  document = REXML::Document.new(source)
  root = document.root
  fail_preview('Input has no XML root element') unless root

  if root.name == 'mxGraphModel'
    return [Page.new(1, 'root', File.basename(path, File.extname(path)), root)]
  end
  fail_preview("Expected mxfile or mxGraphModel root, found #{root.name.inspect}") unless root.name == 'mxfile'

  diagrams = root.get_elements('diagram')
  fail_preview('The mxfile has no diagram pages') if diagrams.empty?

  diagrams.each_with_index.map do |diagram, index|
    model = diagram.elements.to_a.find { |element| element.name == 'mxGraphModel' }
    Page.new(
      index + 1,
      diagram.attributes['id'].to_s,
      diagram.attributes['name'].to_s.empty? ? "Page #{index + 1}" : diagram.attributes['name'].to_s,
      model
    )
  end
rescue Errno::ENOENT
  fail_preview("Input does not exist: #{path}")
rescue Errno::EACCES
  fail_preview("Input is not readable: #{path}")
rescue REXML::ParseException => error
  fail_preview("Input is not valid XML: #{error.message.lines.first.to_s.strip}")
end

def select_page(pages, selector)
  selected = nil
  if selector.match?(/\A[1-9][0-9]*\z/)
    selected = pages[selector.to_i - 1]
  end
  selected ||= pages.find { |page| page.id == selector }
  selected ||= pages.find { |page| page.name == selector }
  return selected if selected

  available = pages.map { |page| "#{page.index}: #{page.name.inspect} (id=#{page.id.inspect})" }.join(', ')
  fail_preview("No page matches #{selector.inspect}. Available pages: #{available}")
end

def page_dimensions(page)
  fail_preview(<<~MESSAGE.strip) unless page.model
    Page #{page.index} (#{page.name.inspect}) is compressed or encoded. This utility
    intentionally accepts only pages containing a direct mxGraphModel child.
  MESSAGE

  width = Float(page.model.attributes['pageWidth'].to_s)
  height = Float(page.model.attributes['pageHeight'].to_s)
  unless width.positive? && height.positive? && width.finite? && height.finite?
    fail_preview("Page #{page.index} has invalid pageWidth/pageHeight values")
  end

  pixel_width = width.ceil
  pixel_height = height.ceil
  if pixel_width > MAX_VIEWPORT_DIMENSION || pixel_height > MAX_VIEWPORT_DIMENSION
    fail_preview(<<~MESSAGE.strip)
      Page viewport #{pixel_width}x#{pixel_height} exceeds the supported
      #{MAX_VIEWPORT_DIMENSION}px browser dimension. Split the page or use another exporter.
    MESSAGE
  end
  [pixel_width, pixel_height]
rescue ArgumentError
  fail_preview("Page #{page.index} must declare numeric pageWidth and pageHeight values")
end

def file_url(path)
  URI::DEFAULT_PARSER.escape("file://#{File.expand_path(path)}")
end

def html_document(page_xml:, width:, height:, drawio_root:)
  root_url = file_url(drawio_root)
  viewer_url = file_url(File.join(drawio_root, 'js/viewer-static.min.js'))
  css_url = file_url(File.join(drawio_root, 'styles/grapheditor.css'))
  config = {
    'xml' => page_xml,
    'zoom' => 1,
    'resize' => 0,
    'border' => 0,
    'auto-fit' => false,
    'auto-crop' => false,
    'auto-origin' => false,
    'allow-zoom-in' => false,
    'allow-zoom-out' => false,
    'center' => false,
    'responsive' => false,
    'nav' => false,
    'toolbar' => '',
    'tooltips' => false,
    'dark-mode' => false
  }
  config_attribute = CGI.escapeHTML(JSON.generate(config))
  script_nonce = SecureRandom.urlsafe_base64(18)

  <<~HTML
    <!doctype html>
    <html lang="en" data-preview-ready="0" data-preview-stage="html-created">
    <head>
      <meta charset="utf-8">
      <meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'nonce-#{script_nonce}' 'unsafe-eval' file:; style-src 'unsafe-inline' file:; img-src data: file:; font-src data: file:; connect-src 'none'; media-src 'none'; object-src 'none'; frame-src 'none';">
      <meta name="color-scheme" content="light">
      <title>Local Draw.io preview</title>
      <link rel="stylesheet" href="#{CGI.escapeHTML(css_url)}">
      <style>
        html, body { width: #{width}px; height: #{height}px; margin: 0; padding: 0; overflow: hidden; background: #fff; }
        #preview { position: relative; width: #{width}px; height: #{height}px; margin: 0; padding: 0; overflow: hidden; background: #fff; }
        #serialized-svg, #preview-error { display: none; }
      </style>
      <script nonce="#{script_nonce}">
        window.DRAWIO_BASE_URL = #{JSON.generate(root_url)};
        window.DRAWIO_LIGHTBOX_URL = #{JSON.generate(root_url)};
        window.DRAWIO_SERVER_URL = #{JSON.generate("#{root_url}/")};
        window.STYLE_PATH = #{JSON.generate("#{root_url}/styles")};
        window.SHAPES_PATH = #{JSON.generate("#{root_url}/shapes")};
        window.STENCIL_PATH = #{JSON.generate("#{root_url}/stencils")};
        window.GRAPH_IMAGE_PATH = #{JSON.generate("#{root_url}/img")};
        window.IMAGE_PATH = #{JSON.generate("#{root_url}/images")};
        window.CSS_PATH = #{JSON.generate("#{root_url}/styles")};
        window.RESOURCES_PATH = #{JSON.generate("#{root_url}/resources")};
        window.DRAW_MATH_URL = #{JSON.generate("#{root_url}/math/es5")};
        window.mxBasePath = #{JSON.generate("#{root_url}/mxgraph")};
        window.mxImageBasePath = #{JSON.generate("#{root_url}/mxgraph/images")};
        window.mxLoadStylesheets = false;
        window.urlParams = { configure: '0', chrome: '0', dark: '0' };

        window.onDrawioViewerLoad = function() {
          document.documentElement.setAttribute('data-preview-stage', 'viewer-loaded');
          GraphViewer.viewerInitialized = function(viewer) {
            document.documentElement.setAttribute('data-preview-stage', 'graph-initialized');
            viewer.addListener('render', function() {
              document.documentElement.setAttribute('data-preview-stage', 'render-event');
              if (window.__drawioPreviewCaptured) return;
              window.__drawioPreviewCaptured = true;
              try {
                var scale = viewer.graph.view.scale;
                if (Math.abs(scale - 1) > 0.0001) {
                  throw new Error('Expected viewer scale 1 (100%), got ' + scale);
                }
                var sourceSvg = viewer.graph.view.canvas.ownerSVGElement;
                if (!sourceSvg) throw new Error('Draw.io viewer produced no SVG canvas');
                var svg = sourceSvg.cloneNode(true);
                svg.setAttribute('xmlns', 'http://www.w3.org/2000/svg');
                svg.setAttribute('xmlns:xlink', 'http://www.w3.org/1999/xlink');
                svg.setAttribute('width', '#{width}');
                svg.setAttribute('height', '#{height}');
                svg.setAttribute('viewBox', '0 0 #{width} #{height}');
                svg.removeAttribute('style');

                var backdrop = document.createElementNS('http://www.w3.org/2000/svg', 'rect');
                backdrop.setAttribute('x', '0');
                backdrop.setAttribute('y', '0');
                backdrop.setAttribute('width', '#{width}');
                backdrop.setAttribute('height', '#{height}');
                backdrop.setAttribute('fill', '#ffffff');
                svg.insertBefore(backdrop, svg.firstChild);

                var css = [];
                Array.prototype.forEach.call(document.styleSheets, function(sheet) {
                  try {
                    Array.prototype.forEach.call(sheet.cssRules || [], function(rule) {
                      css.push(rule.cssText);
                    });
                  } catch (_ignored) {}
                });
                if (css.length > 0) {
                  var definitions = document.createElementNS('http://www.w3.org/2000/svg', 'defs');
                  var style = document.createElementNS('http://www.w3.org/2000/svg', 'style');
                  style.setAttribute('type', 'text/css');
                  style.textContent = css.join('\\n');
                  definitions.appendChild(style);
                  svg.insertBefore(definitions, backdrop.nextSibling);
                }

                var serialized = new XMLSerializer().serializeToString(svg);
                var serializedOutput = document.getElementById('serialized-svg');
                serializedOutput.value = serialized;
                serializedOutput.textContent = serialized;
                document.documentElement.setAttribute('data-preview-scale', String(scale));
                document.documentElement.setAttribute('data-preview-ready', '1');
                document.documentElement.setAttribute('data-preview-stage', 'captured');
              } catch (error) {
                var errorText = error && error.stack ? error.stack : String(error);
                var errorOutput = document.getElementById('preview-error');
                errorOutput.value = errorText;
                errorOutput.textContent = errorText;
                document.documentElement.setAttribute('data-preview-ready', 'error');
              }
            });
          };
          GraphViewer.processElements();
        };
      </script>
    </head>
    <body>
      <div id="preview" class="mxgraph" data-mxgraph="#{config_attribute}"></div>
      <textarea id="serialized-svg" aria-hidden="true"></textarea>
      <textarea id="preview-error" aria-hidden="true"></textarea>
      <script nonce="#{script_nonce}" src="#{CGI.escapeHTML(viewer_url)}"></script>
    </body>
    </html>
  HTML
end

def signal_process_group(signal, pid)
  Process.kill(signal, -pid)
rescue Errno::ESRCH
  begin
    Process.kill(signal, pid)
  rescue Errno::ESRCH
    nil
  end
end

def terminate_process(wait_thread)
  signal_process_group('TERM', wait_thread.pid)
  return if wait_thread.join(2)

  signal_process_group('KILL', wait_thread.pid)
  wait_thread.join(2)
end

def run_browser(command, timeout_seconds, completion: nil)
  stdout = +''
  stderr = +''
  status = nil
  completed = false
  mutex = Mutex.new

  Open3.popen3(*command, pgroup: true) do |stdin, out, err, wait_thread|
    stdin.close
    stdout_reader = Thread.new do
      loop do
        chunk = out.readpartial(16_384)
        mutex.synchronize { stdout << chunk }
      end
    rescue EOFError, IOError
      nil
    end
    stderr_reader = Thread.new do
      loop do
        chunk = err.readpartial(16_384)
        mutex.synchronize { stderr << chunk }
      end
    rescue EOFError, IOError
      nil
    end
    deadline = Process.clock_gettime(Process::CLOCK_MONOTONIC) + timeout_seconds

    begin
      loop do
        snapshot = mutex.synchronize { [stdout.dup, stderr.dup] }
        if completion && completion.call(*snapshot)
          completed = true
          terminate_process(wait_thread)
          break
        end
        unless wait_thread.alive?
          status = wait_thread.value
          break
        end
        if Process.clock_gettime(Process::CLOCK_MONOTONIC) >= deadline
          terminate_process(wait_thread)
          fail_preview("Browser timed out after #{timeout_seconds}s")
        end
        sleep 0.05
      end
    ensure
      stdout_reader.join(2)
      stderr_reader.join(2)
      stdout_reader.kill if stdout_reader.alive?
      stderr_reader.kill if stderr_reader.alive?
      stdout, stderr = mutex.synchronize { [stdout.dup, stderr.dup] }
    end
  end

  unless completed || status&.success?
    detail = stderr.lines.last(12).join.strip
    fail_preview("Browser exited with status #{status&.exitstatus || 'unknown'}#{detail.empty? ? '' : ":\n#{detail}"}")
  end
  [stdout, stderr]
end

def browser_arguments(browser:, profile_dir:, width:, height:, timeout_seconds:, action:, html_path:)
  arguments = [
    browser,
    '--headless=new',
    '--disable-gpu',
    '--hide-scrollbars',
    '--force-device-scale-factor=1',
    '--force-color-profile=srgb',
    '--run-all-compositor-stages-before-draw',
    '--allow-file-access-from-files',
    '--disable-background-networking',
    '--disable-component-update',
    '--disable-default-apps',
    '--disable-extensions',
    '--disable-sync',
    '--metrics-recording-only',
    '--mute-audio',
    '--no-first-run',
    '--no-default-browser-check',
    '--host-resolver-rules=MAP * ~NOTFOUND',
    "--user-data-dir=#{profile_dir}",
    "--window-size=#{width},#{height}",
    '--virtual-time-budget=3000'
  ]
  arguments.concat(action)
  arguments << file_url(html_path)
  arguments
end

def extract_textarea(dom, id)
  match = dom.match(%r{<textarea\b[^>]*\bid=["']#{Regexp.escape(id)}["'][^>]*>(.*?)</textarea>}mi)
  match ? CGI.unescapeHTML(match[1]) : nil
end

def validate_svg(svg)
  fail_preview('Browser completed before the viewer emitted an SVG') if svg.nil? || svg.empty?

  document = REXML::Document.new(svg)
  fail_preview('Serialized preview root is not svg') unless document.root&.name == 'svg'
rescue REXML::ParseException => error
  fail_preview("Browser emitted invalid SVG: #{error.message.lines.first.to_s.strip}")
end

def validate_png(path, expected_width, expected_height)
  data = File.binread(path, 24)
  fail_preview('Browser did not create a PNG screenshot') unless data&.start_with?("\x89PNG\r\n\x1a\n".b)
  fail_preview('PNG header is truncated') if data.bytesize < 24

  width, height = data.byteslice(16, 8).unpack('NN')
  return if width == expected_width && height == expected_height

  fail_preview("PNG is #{width}x#{height}; expected exact 100% viewport #{expected_width}x#{expected_height}")
rescue Errno::ENOENT
  fail_preview('Browser did not create a PNG screenshot')
end

def complete_png?(path)
  size = File.size(path)
  return false if size < 12

  File.binread(path, 12, size - 12) == "\x00\x00\x00\x00IEND\xaeB\x60\x82".b
rescue Errno::ENOENT, Errno::EACCES
  false
end

def slug(value)
  normalized = value.downcase.gsub(/[^a-z0-9]+/, '-').gsub(/\A-+|-+\z/, '')
  normalized.empty? ? 'page' : normalized
end

def atomic_publish(source, destination)
  temporary = File.join(File.dirname(destination), ".#{File.basename(destination)}.tmp-#{Process.pid}")
  FileUtils.cp(source, temporary)
  File.rename(temporary, destination)
ensure
  FileUtils.rm_f(temporary) if defined?(temporary) && temporary
end

options = {
  format: 'png',
  page: '1',
  timeout: DEFAULT_TIMEOUT_SECONDS,
  force: false,
  keep_html: false,
  list_pages: false
}

parser = OptionParser.new do |opts|
  opts.banner = <<~BANNER
    Usage: #{File.basename($PROGRAM_NAME)} [options] INPUT.drawio

    Render one direct, uncompressed mxGraphModel page with local Draw.io assets.
    Numeric --page values are 1-based; otherwise an exact page ID or name is used.
  BANNER
  opts.on('--output-dir PATH', 'Required destination directory (except with --list-pages)') { |value| options[:output_dir] = value }
  opts.on('--format FORMAT', %w[png svg both], 'png, svg, or both (default: png)') { |value| options[:format] = value }
  opts.on('--page SELECTOR', '1-based index, exact page ID, or exact page name (default: 1)') { |value| options[:page] = value }
  opts.on('--output-name NAME', 'Output basename without an extension') { |value| options[:output_name] = value }
  opts.on('--drawio-root PATH', 'Local diagrams.net webapp or VS Code extension root') { |value| options[:drawio_root] = value }
  opts.on('--chrome PATH', 'Chrome, Chromium, or Edge executable') { |value| options[:chrome] = value }
  opts.on('--timeout SECONDS', Integer, "Browser timeout (default: #{DEFAULT_TIMEOUT_SECONDS})") { |value| options[:timeout] = value }
  opts.on('--force', 'Replace output files that already exist') { options[:force] = true }
  opts.on('--keep-html', 'Also copy the generated offline preview HTML to the output directory') { options[:keep_html] = true }
  opts.on('--list-pages', 'List page selectors without launching a browser') { options[:list_pages] = true }
  opts.on('-h', '--help', 'Show this help') do
    puts opts
    exit 0
  end
end

begin
  parser.parse!
  fail_preview('Provide exactly one INPUT.drawio path') unless ARGV.length == 1
  input = File.expand_path(ARGV.first)
  fail_preview("Input must be a file: #{input}") unless File.file?(input)

  pages = load_pages(input)
  if options[:list_pages]
    pages.each do |page|
      encoding = page.model ? 'uncompressed' : 'compressed/encoded'
      puts format("%d\t%s\t%s\t%s", page.index, page.id, page.name, encoding)
    end
    exit 0
  end

  fail_preview('--output-dir is required') unless options[:output_dir]
  fail_preview('--timeout must be between 1 and 300 seconds') unless (1..300).cover?(options[:timeout])
  if options[:output_name]&.match?(%r{[/\\]}) || %w[. ..].include?(options[:output_name])
    fail_preview('--output-name must be a basename, not a path')
  end

  page = select_page(pages, options[:page])
  width, height = page_dimensions(page)
  drawio_root = detect_drawio_root(options[:drawio_root])
  browser = detect_browser(options[:chrome])
  output_dir = File.expand_path(options[:output_dir])
  FileUtils.mkdir_p(output_dir)

  default_name = "#{slug(File.basename(input, File.extname(input)))}-page-#{page.index}-#{slug(page.name)}"
  output_name = options[:output_name].to_s.empty? ? default_name : options[:output_name]
  extensions = options[:format] == 'both' ? %w[png svg] : [options[:format]]
  extensions << 'preview.html' if options[:keep_html]
  destinations = extensions.to_h { |extension| [extension, File.join(output_dir, "#{output_name}.#{extension}")] }
  conflicts = destinations.values.select { |path| File.exist?(path) }
  unless conflicts.empty? || options[:force]
    fail_preview("Output exists; pass --force to replace it: #{conflicts.join(', ')}")
  end

  source_digest = Digest::SHA256.file(input).hexdigest
  page_xml = serialize_element(page.model)

  Dir.mktmpdir('drawio-preview-') do |temporary_dir|
    html_path = File.join(temporary_dir, 'preview.html')
    File.binwrite(html_path, html_document(page_xml: page_xml, width: width, height: height, drawio_root: drawio_root))

    profile_dir = File.join(temporary_dir, 'chrome-profile-dom')
    dom_command = browser_arguments(
      browser: browser,
      profile_dir: profile_dir,
      width: width,
      height: height,
      timeout_seconds: options[:timeout],
      action: ['--dump-dom'],
      html_path: html_path
    )
    dom, browser_stderr = run_browser(
      dom_command,
      options[:timeout] + 5,
      completion: ->(stdout, _stderr) { stdout.include?('</html>') }
    )
    error = extract_textarea(dom, 'preview-error')
    fail_preview("Draw.io viewer failed: #{error}") unless error.nil? || error.empty?
    unless dom.match?(/<html\b[^>]*\bdata-preview-ready=["']1["']/i)
      stage = dom[/<html\b[^>]*\bdata-preview-stage=["']([^"']+)["']/i, 1] || 'unknown'
      surface = dom[%r{<div\b[^>]*\bid=["']preview["'][^>]*>(.*?)</div>}mi, 1].to_s
      surface = CGI.unescapeHTML(surface.gsub(/<[^>]+>/, ' ').gsub(/\s+/, ' ').strip)
      diagnostic = ["stage=#{stage}"]
      diagnostic << "viewer=#{surface}" unless surface.empty?
      log_tail = browser_stderr.lines.last(4).join.strip
      diagnostic << "browser=#{log_tail}" unless log_tail.empty?
      fail_preview("Draw.io viewer did not reach its rendered state (#{diagnostic.join('; ')})")
    end
    scale = dom[/<html\b[^>]*\bdata-preview-scale=["']([^"']+)["']/i, 1]
    fail_preview("Preview scale check failed (reported #{scale.inspect})") unless scale == '1'

    staged = {}
    if extensions.include?('svg')
      svg = extract_textarea(dom, 'serialized-svg')
      validate_svg(svg)
      svg_path = File.join(temporary_dir, 'preview.svg')
      File.binwrite(svg_path, svg)
      staged['svg'] = svg_path
    end

    if extensions.include?('png')
      png_path = File.join(temporary_dir, 'preview.png')
      png_profile = File.join(temporary_dir, 'chrome-profile-png')
      png_command = browser_arguments(
        browser: browser,
        profile_dir: png_profile,
        width: width,
        height: height,
        timeout_seconds: options[:timeout],
        action: ["--screenshot=#{png_path}"],
        html_path: html_path
      )
      run_browser(
        png_command,
        options[:timeout] + 5,
        completion: ->(_stdout, _stderr) { complete_png?(png_path) }
      )
      validate_png(png_path, width, height)
      staged['png'] = png_path
    end

    staged['preview.html'] = html_path if options[:keep_html]
    fail_preview('Source diagram changed during preview; no outputs were published') unless Digest::SHA256.file(input).hexdigest == source_digest

    staged.each { |extension, path| atomic_publish(path, destinations.fetch(extension)) }
  end

  puts "Rendered page #{page.index} #{page.name.inspect} at 100% (#{width}x#{height})"
  puts "Draw.io assets: #{drawio_root}"
  puts "Browser: #{browser}"
  destinations.each_value { |path| puts path }
rescue OptionParser::ParseError, PreviewError => error
  warn "error: #{error.message}"
  warn parser
  exit 1
end
