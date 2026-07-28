# Portable Runtime Services

This reference specifies the exact compatibility behavior of the small
platform services that surround the semantic runtime. It refines the broader
`PLAT-*` rules. Where a convenience adapter is deliberately lossy or retains a
historical quirk, the narrower `PORT-*` contract below controls that adapter;
callers that need the stronger broad contract must use a different port.

## Contents

1. [Boundary and conformance profile](#boundary-and-conformance-profile)
2. [Filesystem port and path evidence](#filesystem-port-and-path-evidence)
3. [Bounded file reads](#bounded-file-reads)
4. [Native owned-directory primitives](#native-owned-directory-primitives)
5. [Path, cache, temporary, and XDG rules](#path-cache-temporary-and-xdg-rules)
6. [Platform, locale, and hyperlink services](#platform-locale-and-hyperlink-services)
7. [Process execution and process discovery](#process-execution-and-process-discovery)
8. [Buffered output, timers, signals, and locks](#buffered-output-timers-signals-and-locks)
9. [Notification and sleep prevention](#notification-and-sleep-prevention)
10. [Retention cleanup and background housekeeping](#retention-cleanup-and-background-housekeeping)
11. [Graceful shutdown](#graceful-shutdown)
12. [Acceptance scenarios](#acceptance-scenarios)
13. [Provenance](#provenance)

## Boundary and conformance profile

**PORT-BASE-001 — Replaceable filesystem port.** Expose one process-global
filesystem implementation with synchronous and asynchronous operations for
the current directory, existence, metadata, directory listing, reads, writes,
links, rename, removal, directory creation, and writable streams. Installing a
replacement changes subsequent consumers immediately; it does not change the
bootstrap working directory or migrate already-open resources. Reset restores
the native implementation.

**PORT-BASE-002 — Two filesystem planes.** Operations reached through the
replaceable port are virtualizable. Bounded range, tail, and reverse-line
readers intentionally open the host filesystem directly and therefore do not
observe a replacement implementation. An implementation must retain this split
unless it deliberately changes the test and remote-filesystem compatibility
contract.

**PORT-BASE-003 — Best-effort versus authoritative work.** Notifications,
sleep inhibition, diagnostics, retention cleanup, background housekeeping,
and analytics flushes are best effort. Session persistence registered in the
cleanup registry is authoritative enough to run before hooks and telemetry,
but even it is bounded during process shutdown.

**PORT-BASE-004 — Concrete platform names.** Use five classifications:
`macos`, `windows`, `wsl`, `linux`, and `unknown`. The legacy
"supported-platforms" compatibility list contains only `macos` then `wsl`;
this list is not the same thing as the set of platforms that can run the
client.

## Filesystem port and path evidence

**PORT-FS-001 — Native operation forwarding.** Native operations preserve the
host filesystem's result and error semantics except where a narrower rule
below says otherwise. Synchronous metadata, existence, and content reads are
also wrapped in slow-operation observation; this observation cannot change
the filesystem result.

**PORT-FS-002 — Recursive-directory compatibility.** Both asynchronous and
synchronous directory creation are recursive. If the host reports
"already exists," treat the operation as success without rechecking that the
existing object is a directory. This accommodates a Windows runtime that can
misclassify read-only directories; a later operation exposes an existing-file
collision.

**PORT-FS-003 — Safe resolution preflight.** A path beginning with either
double forward slashes or double backslashes is returned unchanged as
non-symlink and noncanonical without any filesystem access. For another path,
inspect the path entry without following it before canonicalization. A FIFO,
socket, character device, or block device is returned unchanged and
noncanonical so validation cannot block on the special object.

**PORT-FS-004 — Safe resolution outcome.** If entry inspection and canonical
resolution succeed, return the canonical path, mark it canonical, and mark it
as a symlink whenever the canonical string differs from the input string.
Canonical native paths are normalized to Unicode NFC. Any inspection or
resolution error—including absence, a dangling link, access denial, or a link
loop—collapses to the original path with both flags false.

**PORT-FS-005 — Duplicate-path set.** Resolve a candidate with the safe
resolver and compare only the resulting string against a caller-owned set. An
existing entry is a duplicate. Otherwise insert it and accept the candidate.
Unresolvable aliases can therefore remain distinct, while any canonical
string difference can deduplicate paths even when it resulted from lexical or
Unicode normalization rather than a link.

**PORT-FS-006 — Deepest-existing-ancestor resolution.** For a possibly absent
absolute path, walk upward one component at a time with non-following metadata,
remembering absent tail components. At the first existing symbolic link, try
full canonical resolution; if it is dangling, read its immediate target and
make a relative target absolute against the link's parent. Reattach the absent
tail and return it. At the first existing non-link, canonicalize once so links
in earlier ancestors are captured; return only when that canonical string
differs. Return no alternate path at the filesystem root or when no link was
observed. A failure to read a dangling link target propagates.

**PORT-FS-007 — Permission path seed.** Permission evidence starts with the
input path after expanding exactly `~` or the `~/` prefix. Other relative paths
remain relative despite the historical description calling the result
absolute. A UNC-form seed returns immediately as the sole evidence item.

**PORT-FS-008 — Permission link chain.** Follow at most 40 immediate symbolic
links. Preserve insertion order and uniqueness: seed first, then each absolute
immediate target, then the final canonical path when safe resolution identifies
a difference. Resolve a relative link target against the link's directory.
Stop on a repeated path, absence, a non-link, a special object, or any error.

**PORT-FS-009 — Absent-write evidence.** When the original seed does not exist
(including a dangling link because existence follows links), invoke the
deepest-existing-ancestor algorithm and add its result. This special recovery
is performed only while still on the original seed; a dangling link reached
later in a chain contributes its immediate target but is not recursively
specified.

**PORT-FS-010 — Atomic append-on-create.** An append with an explicit mode first
attempts exclusive create-and-append using that mode. On an existing target it
falls back to ordinary append and does not change the existing mode. Any other
exclusive-open error propagates. An append without a mode uses ordinary append
directly.

**PORT-FS-011 — Fixed-prefix synchronous read.** A synchronous fixed-length
read opens from byte zero, allocates exactly the requested length, performs one
read, returns both the allocated buffer and actual byte count, and closes the
descriptor on normal or exceptional completion. Compatibility note: the
reference tests the numeric handle for truthiness before close, so a valid
handle numbered zero can leak; no supported caller relies on that leak and a
portable implementation should close every valid handle.

**PORT-FS-012 — Bounded byte read.** An asynchronous byte read with no bound
loads the whole file. With a bound, open the file, snapshot its size, allocate
the lesser of size and bound, and loop positional reads from byte zero until
the allocation is full or a read returns zero. Return only initialized bytes
and always close the handle. Invalid negative bounds are not normalized and
surface as allocation or read errors.

## Bounded file reads

**PORT-FS-013 — Range read.** Open the host file, snapshot its byte size, and
return no result when `size <= offset`. Otherwise read at most `maxBytes`
starting at the supplied byte offset, looping across partial reads. Return
UTF-8-decoded content, actual bytes read, and the snapshotted total size. The
file handle is always closed.

**PORT-FS-014 — Tail read.** Snapshot the host file size and read from
`max(0, size - maxBytes)` through the snapshot end. An empty file returns empty
content and zero counts. The result reports actual bytes read and the original
snapshot size. A start point in the middle of a multi-byte character follows
ordinary UTF-8 replacement behavior.

**PORT-FS-015 — Reverse-line reader.** Snapshot the file size, then walk
backward in 4,096-byte chunks. Carry undecoded bytes across chunk boundaries
so a split UTF-8 sequence is decoded only after reassembly. Split only on line
feed, yield nonempty lines newest-first, retain carriage returns from CRLF, and
finally yield a nonempty leading remainder. Blank lines are omitted. Always
close the handle.

**PORT-FS-016 — Concurrent file mutation.** Range, tail, and reverse-line reads
do not lock the file. Appends after the size snapshot are ignored; truncation
or replacement can produce a short read. The readers report the original size
snapshot rather than restatting. Callers must treat these operations as
diagnostic views, not authoritative transactional reads.

## Native owned-directory primitives

**PORT-FS-017 — Read-only private-child inspection.** For inventory and other
read-only discovery, accept only simple child components beneath an acquired
private-directory identity. Open the verified parent root, inspect the direct
entry without following links, open that exact child as a directory root, and
require the before, opened, and after identities to match before reverifying
the textual parent. Do not create or chmod during inspection. Absence remains
absence; a non-directory, direct symlink, parent replacement, child identity
change, filesystem or mount boundary, reparse point, or access state that would
require permission repair fails closed. Linux compares both device and
`/proc/self/fdinfo` mount identifiers so a same-device bind mount is not
accepted as an ordinary child. Supported POSIX profiles require owner-private
access. Windows preserves direct-directory, same-volume, non-reparse, and
stable-identity evidence without claiming DACL privacy from synthesized mode
bits. Make an opened root or file handle the first authoritative retained
identity, then compare direct textual and rooted entries to it. In particular,
do not retain a plain Windows `Lstat` identity before opening the handle:
Windows may populate that identity lazily during `SameFile` and otherwise
adopt a replacement pathname as the expected object.

**PORT-FS-018 — Creator-exclusive private child.** When later cleanup authority
depends on having created a directory, accept exactly one simple component,
prove it absent beneath the verified parent root, and perform one
descriptor-relative owner-private directory creation. If another creator wins
after the absence observation, return the existence collision; never reacquire,
chmod, remove, or return ownership of that winner. After a successful creation,
prove that the child remains on the parent's filesystem and mount identity,
secure and capture the created identity, retain the owned parent relationship,
and reverify the textual parent before returning it. Keep this contract
distinct from compatibility helpers that may reacquire a safe concurrent
winner. Every acquired owned child retains the same direct-parent relationship;
verification recursively proves the complete retained ancestry rather than
accepting a stable child that was moved beneath a replacement parent.

**PORT-FS-019 — Identity-verified same-parent detach.** Detach only an acquired
direct child to a different simple destination name under the same acquired
parent. Verify textual and descriptor-rooted parent and child identities and
destination absence both initially and immediately before the rooted atomic
rename. When a caller supplies another authority verifier, run it after those
final checks and immediately before rename; verifier failure is a pre-commit
failure. The commit operation itself must be one operating-system no-replace
rename (`renameat2(RENAME_NOREPLACE)`, `renameatx_np(RENAME_EXCL)`, or
`SetFileInformationByHandle` with replacement disabled); absence observations
are not authority to use an ordinary replacing rename. A destination that
appears at the final syscall boundary remains unchanged. A platform, kernel,
filesystem, or architecture without the primitive fails closed. Expose a
nonmutating preflight that validates the exact parent/child, the available
kernel adapter, and `PORT-FS-020` before a caller persists deletion intent.
Linux's same-name `RENAME_NOREPLACE` collision proves only that the syscall and
flag route are available, not that the target filesystem implements the
eventual distinct-name rename. Windows' same-name `FileRenameInfo` collision
likewise proves only the adapter and collision path. Darwin additionally
requires the target volume's `VOL_CAP_INT_RENAME_EXCL` capability. If the real
rename then fails before commit because the platform or filesystem does not
support it, a session-deletion caller retains its already-durable exact intent
and pending receipt as a retry reservation and reports `delete_incomplete`; it
must not introduce a partially committed multi-object rollback. After rename,
retain the original child and parent identities in an owner bound to the
destination. Report
committed state even if a later
source-absence check, destination-identity check, parent sync, or textual
verification fails, so recovery can target only the detached owner. A caller
removes contents through that owner rather than recursively removing the live
source pathname.

**PORT-FS-020 — Owned-directory sync.** Open a root pinned to the acquired
directory identity, open its directory handle through that root, and compare
the handle identity before and after synchronization. Reject a replaced
textual pathname before using the handle. Unix profiles sync the directory
descriptor so preceding entry mutations reach the host's directory-durability
boundary. Windows reopens the same non-reparse directory identity with
write-through semantics and calls `FlushFileBuffers`; a reopen, identity,
flush, or close failure is not durable success. A profile without a real
directory durability primitive returns a stable unsupported error. Re-stat
alone is never a successful mutation durability boundary. Always close the
root and handle and preserve close failures.

**PORT-FS-021 — Strict owned-directory cleanup.** When a caller's success
claim depends on removing one exact acquired directory identity, absence of
the acquired textual path before cleanup is an identity failure rather than
idempotent success. Otherwise retain the ordinary descriptor-rooted recursive
cleanup: use the child's retained owned parent when available, pin both
identities, and reject a moved child or replacement anywhere in the ancestry.
Stream enumeration in fixed-size batches under total entry, depth, and rescan
ceilings; a bound failure leaves only identity-safe partial progress and the
detached owner remains retryable. Recursively descend through newly opened,
before/opened/after-verified direct directory handles instead of a raw
recursive-remove helper. Require every child to retain the cleanup root's
filesystem and mount identity; on Linux the mount identifier rejects
same-device bind mounts, and an unavailable identity check fails closed.
Revalidate the direct child immediately before a nonrecursive parent-root
removal, and never traverse a replacement, reparse point, external symlink,
device boundary, or mount boundary. Strict cleanup on a nil owner fails
identity verification. Keep the ordinary idempotent cleanup form for lifecycle
callers whose nil or already-absent temporary directory is a successful cleanup
outcome.

## Path, cache, temporary, and XDG rules

**PORT-PATH-001 — Expansion inputs.** Resolve a missing base from the session
working directory, then from the active filesystem current directory. Reject
a non-string path or base and reject a NUL byte in either. Trim leading and
trailing whitespace from the requested path before all other interpretation.

**PORT-PATH-002 — Empty and home expansion.** An empty or whitespace-only path
returns the normalized base. Exact `~` returns the user home. Exact `~/...`
joins the remainder beneath home. A backslash after tilde and named-user forms
have no special meaning. Every returned path is Unicode NFC.

**PORT-PATH-003 — Native absolute expansion.** On Windows only, attempt to
convert a leading slash, one ASCII drive letter, and slash into native drive
form; conversion failure leaves the string unchanged. Normalize an absolute
path. Resolve any other path against the base. The result always uses the
current platform's native path rules.

**PORT-PATH-004 — Display relativization.** Compute a path relative to the
session working directory. If the resulting string begins with the two dots
characters, return the original absolute path; otherwise return the relative
string, including the empty string for the working directory itself. This
prefix test also conservatively rejects an in-tree relative name beginning
with two dots.

**PORT-PATH-005 — Directory selection.** Expand the input first. For a UNC-form
result, return its lexical parent without touching the filesystem. Otherwise,
return the path itself only when metadata says it is a directory. Absence,
access failure, and non-directory objects return the lexical parent.

**PORT-PATH-006 — Traversal and config-key forms.** Traversal detection is a
pure lexical test for a complete `..` segment bounded by either separator or
string ends; it does not decode escapes. A configuration key first applies
native lexical normalization, including dot-segment removal, then replaces
every backslash with a forward slash. It does not make the path absolute.

**PORT-PATH-007 — Cache root lifetime.** Resolve the platform's standard cache
root once at module initialization under the application key `agentx-cli`.
For every later accessor, read the active filesystem current directory anew,
sanitize it into a project component, and append an optional service
component.

**PORT-PATH-008 — Stable cache sanitization.** Replace every non-ASCII
alphanumeric UTF-16 code unit with one hyphen without collapsing runs and
preserve ASCII case. At 200 code units or fewer, return that string directly,
so distinct source strings may collide. For a longer string, keep its first
200 sanitized code units and append a hyphen plus a stable base-36 hash.

**PORT-PATH-009 — Cache hash.** Start a signed 32-bit accumulator at zero and,
for each original UTF-16 code unit, update it as `31 * accumulator + unit`
modulo 2^32. Convert the absolute signed result to lowercase base 36. This is
a stable noncryptographic compatibility hash, not an authorization or
integrity primitive.

**PORT-PATH-010 — Cache layout.** The base log directory is
`cache-root/project-component`. Error files live beneath `errors`; legacy
message files beneath `messages`; and an MCP server's logs beneath
`mcp-logs-` followed by a separately sanitized server name. No accessor
creates its directory.

**PORT-PATH-011 — Temporary path generation.** Return a path beneath the host
temporary directory named `prefix-identifier` plus the extension. Defaults are
prefix `agentx-prompt` and extension `.md`. Without content, the identifier is
a random UUID. With content, it is the first 16 lowercase hexadecimal digits
of SHA-256 over the content's UTF-8 bytes, making the path stable across
processes for prompt-cache-sensitive material.

**PORT-PATH-012 — Temporary path ownership.** Path generation neither creates
nor reserves the file and performs no prefix or extension sanitization.
Concurrent deterministic users receive the same path, and untrusted path
components could escape the intended narrow root. Callers must use trusted
components, choose collision handling, and own cleanup.

**PORT-PATH-013 — XDG input precedence.** A caller may provide an environment
mapping and a home override. Directory-variable lookup uses the provided
mapping, but home selection uses the explicit home, then the process `HOME`,
then the operating-system home; it intentionally does not use `HOME` from the
provided mapping. Empty-string values are accepted because only null or
absence triggers fallback.

**PORT-PATH-014 — XDG layout.** State, cache, and data roots use
`XDG_STATE_HOME`, `XDG_CACHE_HOME`, and `XDG_DATA_HOME` when present; otherwise
they are respectively `home/.local/state`, `home/.cache`, and
`home/.local/share`. The user executable directory is always
`home/.local/bin`. Values are joined as supplied without enforcing absolute
paths or creating directories.

## Platform, locale, and hyperlink services

**PORT-OS-001 — Platform classification cache.** Classify once per process.
Darwin is macOS and Win32 is Windows. On Linux, read `/proc/version` through
the active filesystem and classify WSL if its lowercase content contains
either `microsoft` or `wsl`; a read failure is logged and yields ordinary
Linux. Any outer failure yields `unknown`. Later environment or filesystem
changes do not invalidate the cached result.

**PORT-OS-002 — WSL version cache.** Query only when the host runtime reports
Linux. Read `/proc/version` and first return decimal digits following a
case-insensitive `WSL` token. With no numbered token, return `1` if the content
contains `microsoft`; content containing only an unnumbered `wsl` yields no
version. Log read errors, return no value, and cache the first result.

**PORT-OS-003 — Linux distribution information.** On Linux, return a cached
asynchronous result containing the kernel release even when `/etc/os-release`
is absent. Parse only exact `ID=` and `VERSION_ID=` lines, remove one optional
pair of surrounding double quotes, and let later duplicate lines win. On
another host return no result. A kernel-release failure occurs before the
file-read recovery and remains a cached failed operation.

**PORT-OS-004 — Version-control detection.** Begin with `perforce` when
`P4PORT` is nonempty. Read one target directory without walking parents and
match exact entries in this order: `.git`, `.hg`, `.svn`, `.p4config`, `$tf`,
`.tfvc`, `.jj`, `.sl`. Emit unique names in first-insertion order using the
labels `git`, `mercurial`, `svn`, `perforce`, `tfs`, `jujutsu`, and `sapling`.
An unreadable directory still returns environment-derived Perforce.

**PORT-OS-005 — Unicode segmentation caches.** Lazily create and reuse one
system-locale grapheme segmenter and one system-locale word segmenter. The
first- and last-grapheme helpers return an empty string for empty input and a
whole extended grapheme cluster otherwise. Segmenter construction failures
propagate and are retried later.

**PORT-OS-006 — Locale-derived caches.** Cache English relative-time
formatters by the exact `style:numeric` pair. Cache the process timezone after
its first nonempty result. Determine the system locale's language subtag once;
if locale facilities fail, cache the unavailable result so later calls do not
retry.

**PORT-OS-007 — OSC 8 hyperlink format.** When support is explicitly false or
the terminal probe is false, return the URL alone and ignore display content.
When supported, display the supplied content or URL in basic blue and wrap it
with OSC 8 open and close sequences using BEL termination:
`ESC ] 8 ; ; URL BEL text ESC ] 8 ; ; BEL`.

**PORT-OS-008 — Hyperlink trust boundary.** The formatter does not validate or
escape the URL or display content. Callers must reject control characters and
apply scheme/path policy before invocation. The plain fallback intentionally
remains the raw URL rather than the custom label.

## Process execution and process discovery

**PORT-PROC-001 — Asynchronous no-throw adapter.** Launch an explicit program
and argument vector with cross-platform command-file support, no shell by
default, nonzero-exit rejection disabled, and a default timeout of 600,000 ms.
Pass through optional cancellation, working directory, environment overlay,
shell selection, standard-input mode, input text, and output bound.

**PORT-PROC-002 — Default-option compatibility.** Calling the convenience
entrypoint with no options uses session cwd and preserves failed output.
Passing an explicit empty options object still gets the 600,000 ms timeout and
failed-output preservation at the lower layer, but does not use session cwd.
The lower entrypoint gets a 1,000,000-byte output bound only when its entire
options argument is omitted; an explicit object with no bound delegates to the
underlying executor's default.

**PORT-PROC-003 — Lossy result shape.** Always resolve to `{stdout, stderr,
code}` plus optional `error`; never reject for an ordinary spawn, timeout,
cancellation, signal, output-limit, or nonzero-exit failure handled by the
executor. Success reports code zero. Failure uses the executor's exit code or
one and derives `error` from human-readable short message, then terminating
signal, then decimal code. This adapter does not expose separate timeout,
signal, or cancellation flags and must not serve a caller that needs the full
`PLAT-020` distinction.

**PORT-PROC-004 — Failed-output policy.** With preservation enabled, retain
available stdout and stderr on failure and include the derived error. With it
disabled, return empty streams and only the exit code. An unexpected executor
rejection is logged and collapses to empty streams and code one without an
error field.

**PORT-PROC-005 — Synchronous shell convenience.** The deprecated convenience
form accepts either an options record or the legacy cancellation-token plus
timeout signature. Check cancellation only before launch, execute the command
string through a shell with inherited environment, session cwd, a 1,000,000-
byte output bound, and a 600,000 ms default timeout. Do not cancel a running
synchronous child when the token later changes.

**PORT-PROC-006 — Synchronous return compatibility.** A synchronous shell
command returns trimmed stdout when nonempty, even when the command exits
nonzero. Empty/whitespace output, timeout, spawn failure, output overflow, and
other thrown failures return null. A pre-aborted token throws before the
failure-catching block. A separate legacy raw synchronous executor preserves
native return type and thrown errors while merely timing and labeling the
first 100 command characters.

**PORT-PROC-007 — Process liveness probe.** Return false for PID zero, one, or
any negative PID. For a larger PID, issue a signal-zero probe and return true
only on success. Every error—including permission denied for a live
other-user process—returns false. Callers must not interpret false as proof
that reusing the PID is race-free.

**PORT-PROC-008 — Ancestor PID query.** Use one bounded, 3,000 ms host command.
On Windows, walk parent records through PowerShell for at most the requested
depth and include each nonzero parent. On Unix, repeatedly query `ps`, stop
before emitting parent zero or one, and emit immediate parent first. Nonzero
command status, empty output, or failure returns an empty list; parse decimal
lines/fields and discard only values that do not begin as numbers.

**PORT-PROC-009 — Ancestor command query.** Use one bounded, 3,000 ms host
command and return the starting process command followed by ancestor commands
up to the requested depth. Separate records with NUL so embedded newlines are
preserved. Windows walks process records; Unix combines `ps` command and
parent queries and stops before moving to parent zero or one. Failure or empty
output returns an empty list.

**PORT-PROC-010 — Child and single-command compatibility.** Deprecated
single-process command lookup and child-PID lookup use a synchronous shell
with a 1,000 ms timeout. Windows uses PowerShell process records; Unix uses
`ps` or `pgrep`. Missing, failed, or unparsable output becomes null or an
empty list. Child order is the host command's order.

**PORT-PROC-011 — Trusted process identifiers.** Process-query scripts
interpolate the supplied PID and depth as text rather than binding them as
typed parameters. Supported callers provide trusted numeric values. A public
or remote boundary must validate integral, nonnegative identifiers and bounded
depth before reaching these helpers.

**PORT-PROC-012 — Output error handlers.** Registration adds one error listener
to each standard output stream every time it is called. On `EPIPE`, destroy the
affected stream. For another error, the installed listener returns without
destroying, logging, or rethrowing it; because a listener exists, the runtime's
unhandled-error path is suppressed.

**PORT-PROC-013 — Direct standard output.** Skip a write only when the stream
is already marked destroyed. Otherwise issue the write and ignore the
backpressure boolean and completion callback. A fast-path fatal error prints
the message through the console error channel and exits immediately with code
one.

**PORT-PROC-014 — Standard-input peek.** Race `end` against a referenced timer.
If no data arrives before the timeout, remove both listeners and resolve true.
End resolves false. The first data chunk cancels the timer but does not resolve;
the operation then waits without a bound for `end` so a caller can collect all
remaining data. Stream error or close without `end` has no special handling.

## Buffered output, timers, signals, and locks

**PORT-ASYNC-001 — Buffered-writer defaults.** Hold strings in insertion order,
flush after 1,000 ms by default, at 100 entries by default, or at an optional
size bound. Immediate mode calls the sink for every write and schedules
nothing. `flush` writes a pending overflow batch before the active buffer;
`dispose` is exactly a flush and does not permanently reject later writes.

**PORT-ASYNC-002 — Deferred overflow.** When a count or size threshold is met,
detach the active buffer synchronously, clear its timer, and schedule the sink
for the next immediate event-loop phase. If an earlier detached batch has not
run, append the new strings to it instead of scheduling another callback.
Explicit flush/dispose drains the detached batch synchronously; its already-
scheduled callback then observes no work.

**PORT-ASYNC-003 — Buffer measurement and errors.** The option named
`maxBufferBytes` actually sums string length in UTF-16 code units, not encoded
bytes. The flush timer is referenced. Sink errors from explicit or timer
flushes propagate through that call path; a sink error in the detached
immediate callback is not translated into a writer result.

**PORT-ASYNC-004 — Abort-responsive delay.** If cancellation is already set,
settle immediately. Otherwise arm one timer, add a one-shot listener, remove
the listener on timer completion, and clear the timer on cancellation. Default
cancellation resolves; `throwOnAbort` rejects with `Error("aborted")`;
providing an error factory also implies rejection. Optionally unreference the
timer. The error factory must be total: if it raises inside a later abort
listener, the timer has already been cleared, the exception escapes that
listener, and the delay completion is not reliably settled.

**PORT-ASYNC-005 — Timeout race.** Race an existing asynchronous operation
against an unreferenced timer that rejects with a new error carrying the
supplied message. Clear the timer when either side settles. The losing
operation is not cancelled and may continue causing side effects.

**PORT-ASYNC-006 — SDK idle exit timer.** Parse
`AGENTX_EXIT_AFTER_STOP_DELAY` once at manager construction as the leading
base-10 integer. Only a value greater than zero enables the timer. `start`
replaces any existing timer, records the start time, and checks once after the
delay; it requests normal graceful shutdown only if the supplied idle probe is
then true and elapsed time is at least the delay. It neither reschedules nor
observes intermediate activity. `stop` clears it. The timer is referenced.

**PORT-ASYNC-007 — Shell timeout values.** Default shell timeout is 120,000 ms
and nominal maximum is 600,000 ms. Each environment override accepts a leading
base-10 integer greater than zero, including a numeric prefix followed by
other characters. Effective maximum is never below effective default. A large
default can therefore raise the maximum above 600,000 ms; there is no absolute
cap.

**PORT-ASYNC-008 — Listener-only signal.** Store listeners in a set: duplicate
function registration is idempotent, unsubscribe/delete is idempotent, and
clear removes all. Emit iterates the live set rather than a snapshot. A
listener removed before its turn is skipped; a listener added during emission
may be visited in that same emission according to insertion order. An exception
propagates immediately and prevents later listeners from running.

**PORT-ASYNC-009 — Lazy lock facade.** Defer loading the lock provider until
the first lock, synchronous lock, unlock, or check request, then reuse that
provider. Forward paths and option records without normalization. A provider
load failure leaves it unset so a later request retries. Acquisition returns
the provider's release operation; explicit unlock remains a separate
compatibility path.

**PORT-ASYNC-010 — Cleanup registry.** Store cleanup callbacks in a process-
global set and return idempotent unregistration. A run snapshots the set,
invokes callbacks in snapshot order to obtain their asynchronous completions,
and waits for all concurrently. Registration changes after the snapshot do not
affect that run; the registry is not cleared. An asynchronously failed callback
causes fail-fast aggregate rejection while siblings continue. A callback that
throws synchronously while the invocation list is being constructed can
prevent later callbacks from being invoked.

### Native session lease

**PORT-LOCK-001 — Rooted nonblocking acquisition.** Reject unsupported
cross-process locking before opening or creating any filesystem object.
Otherwise require a pre-existing direct session directory and a simple lock
leaf, open a rooted parent view, derive and retain a direct parent descriptor
through that view, and open or exclusively create the direct lock entry
through a rooted view bound back to that descriptor. Before and after
owner-only chmod and after the nonblocking operating-system lock succeeds,
require the retained-parent, rooted-parent, and textual-parent identities plus
the held/rooted/textual lock identities to match one regular file with exactly
one link. Close each rooted parent view after its bounded operation. A healthy
lock held through the v1.0.6 path-opened protocol remains ordinary contention;
never break or replace its inode. Return contention separately from unsafe
identity and unsupported-platform outcomes. Derive authoritative parent and
lock identities from opened `File.Stat` handles before comparing their direct
textual paths; a plain Windows path stat is not retained as identity because
its file ID may be loaded lazily after a replacement.

**PORT-LOCK-002 — Mutation-boundary verification and release.** Retain the
locked file handle, delete-share-capable parent handle, parent identity, and
file identity for the lease lifetime. Serialize verification against close.
Verification requires an open lease, opens a short-lived rooted parent view,
binds that view to the retained parent handle, then revalidates the held
regular handle, single-link count, same direct rooted and textual lock entry,
and same rooted and textual parent immediately before the protected mutation.
Close the rooted view before returning from verification. Any missing, linked,
symlinked, replaced, or closed identity fails closed. Close is idempotent:
unlock before closing the file, then close the retained parent handle. Leave
the lock file in place so an older owner cannot remain locked on an unlinked
inode while a new process acquires a replacement.

**PORT-LOCK-003 — Rename-while-held lifetime.** Open native Windows retained
parent handles and lock files with delete sharing so the owning session
directory can be renamed while the acquired operating-system lease remains
held; never retain a non-delete-shared pathname root across that rename. Unix
descriptor semantics provide the same lifetime. Pass lock verification as the
immediate verifier of `PORT-FS-019`; that verification must close its
short-lived rooted view before detach. Retain the lease through a committed
detach and release it only after the live source name is unreachable.
Verification against the old textual parent intentionally fails after rename
and is not a post-commit requirement. The lock file moves with the detached
directory and remains subject to cleanup through that detached owner.

**PORT-LOCK-004 — Existing-only acquisition.** A destructive management flow
acquires the same nonblocking operating-system lease only through an already
existing direct, regular, single-link lock identity. A missing parent or lock
remains missing; acquisition does not create either object, chmod the lock, or
change its contents. Apply every rooted parent/file identity check and the same
lease lifetime, verification, contention, unsupported-platform, and release
rules as `PORT-LOCK-001` through `PORT-LOCK-003`. It must contend with the
v1.0.6 path-opened lock on the same inode.

## Notification and sleep prevention

**PORT-NOTIF-001 — Hook-before-channel order.** Read the global preferred
notification channel, await all notification hooks, and only then select a
terminal channel. A hook failure rejects the notification and prevents channel
output and method analytics.

**PORT-NOTIF-002 — Explicit channels.** Recognize `auto`, `iterm2`,
`iterm2_with_bell`, `kitty`, `ghostty`, `terminal_bell`, and
`notifications_disabled`. The combined iTerm channel emits iTerm notification
then bell. Disabled emits nothing and reports `disabled`. Any other value emits
nothing and reports `none`.

**PORT-NOTIF-003 — Titles and Kitty identity.** The compatibility default title
is `AgentX`. Apply it to Kitty and Ghostty payloads; pass iTerm the
original optional title. Kitty receives a fresh integer obtained by flooring a
uniform random value multiplied by 10,000, so IDs range from 0 through 9,999
and may collide.

**PORT-NOTIF-004 — Automatic terminal selection.** For `iTerm.app`, Kitty, and
Ghostty, emit their matching protocol. For Apple Terminal, query whether the
front profile has the bell boolean exactly false; if so, emit a terminal bell
and report `terminal_bell`, otherwise emit nothing and report
`no_method_available`. Every other terminal reports `no_method_available`.

**PORT-NOTIF-005 — Apple Terminal probe.** Outside Apple Terminal return false
without spawning. Otherwise query the front window's profile name, trim it,
and stop false when empty. Export terminal preferences, require zero exit,
parse the property list lazily, select `Window Settings[profile]`, and return
true only for an exact boolean-false `Bell` field. Log and collapse every
exception to false.

**PORT-NOTIF-006 — Channel error compatibility.** A synchronous failure in an
explicit channel is caught and reports `error`. The `auto` branch returns its
asynchronous selection operation without awaiting it inside the surrounding
catch; a terminal-adapter exception in that branch therefore rejects the
whole notification instead of reporting `error` or analytics.

**PORT-NOTIF-007 — Method observation.** After a completed channel attempt,
emit one notification-method event containing configured channel, returned
method, and detected terminal. This event is observational and cannot make an
already-emitted terminal notification authoritative.

**PORT-SLEEP-001 — Reference-count ownership.** Every start increments a
process-global count. Transition from zero to one attempts inhibition and
starts renewal. Every stop decrements only when positive; whenever the result
is zero, stop renewal and kill the inhibitor. Force-stop resets the count to
zero and performs the same cleanup. Extra stops are harmless.

**PORT-SLEEP-002 — macOS-only inhibitor.** On non-macOS hosts, counting still
occurs but spawn and renewal are no-ops. On macOS, launch `caffeinate` with
`-i -t 300`, ignored standard streams, inherited environment, and no shell.
Unreference the child so it cannot keep the client alive.

**PORT-SLEEP-003 — Renewal and self-healing.** Maintain one unreferenced
240,000 ms interval. While owners remain, each tick force-kills the current
child and starts a new 300-second child. An early child exit or spawn error
clears the stored identity but waits until the next renewal tick to retry.

**PORT-SLEEP-004 — Child identity race.** Capture each spawned child's identity
in its error and exit listeners and clear the global child only if it is still
that child. Kill clears the global identity before sending `SIGKILL`, so the
old child's later exit cannot erase a newly spawned child. Spawn and kill
failures are diagnostic-only.

**PORT-SLEEP-005 — Shutdown registration.** Register one cleanup callback on
the first actual macOS spawn attempt and retain that registration for process
life. It force-stops all owners. A future start after force-stop reuses the same
registration rather than adding a duplicate.

## Retention cleanup and background housekeeping

**PORT-CLEAN-001 — Retention cutoff.** Read `cleanupPeriodDays`, defaulting to
30 only when absent, and compute `now - days * 24h`. Zero, negative, and
fractional valid values retain their arithmetic meaning; this layer does not
clamp them.

**PORT-CLEAN-002 — Timestamp-name conversion.** Take the filename portion
before its first dot, convert a trailing timestamp whose colon and decimal
separators were replaced by hyphens back to ISO form, and parse a date. An
invalid date compares false against the cutoff and is retained.

**PORT-CLEAN-003 — Error and MCP log cleanup.** In the current project's cache,
delete old entries from `errors` based on timestamp-shaped names without first
requiring a regular-file entry. Enumerate immediate `mcp-logs-` directories,
apply the same deletion, and try to remove each empty directory. A missing
directory is quiet; other per-entry failures are logged and do not stop its
siblings. Despite the historical aggregate name, this operation does not
visit the legacy `messages` cache directory.

**PORT-CLEAN-004 — Cleanup-result compatibility.** A result has `messages` and
`errors` counters and combination is component-wise addition. In timestamped
log cleanup, `errors` counts successfully deleted error-log entries and
`messages` counts successfully deleted MCP-log entries; deletion failures are
only logged. In session-oriented cleanup, `messages` counts deleted artifacts
and `errors` counts failed operations. Consumers must not interpret these
fields as one uniform metric across all cleaners.

**PORT-CLEAN-005 — Session artifact traversal.** Under every immediate project
directory, delete old regular `.jsonl` and `.cast` files by modification time.
For each immediate session directory, inspect `tool-results`; delete old
regular files directly beneath it and one further directory level beneath
each tool directory, regardless of extension. Ignore symlink entries. Try to
remove emptied tool, result, session, and project directories in that order.
Directory-read and per-file errors are isolated as specified by their local
counter; removal of a nonempty directory is quiet.

**PORT-CLEAN-006 — Specialized retention targets.** Plan cleanup deletes old
regular `.md` files and removes an empty plan directory. File-history and
session-environment cleanup delete whole immediate subdirectories recursively
when the subdirectory modification time is old. Debug cleanup deletes old
regular `.txt` files except the name `latest` and deliberately retains the
debug directory. These operations follow links only where host `stat`, rather
than directory-entry type, is explicitly used.

**PORT-CLEAN-007 — Validation safety gate.** Before the aggregate background
cleanup, collect settings validation errors. If any error exists and raw
settings explicitly contain `cleanupPeriodDays`, skip every aggregate cleanup
instead of silently applying 30 days. An unrelated validation error triggers
this protection because the intended retention value cannot be trusted.

**PORT-CLEAN-008 — Aggregate cleanup order.** Run, sequentially: project error
and MCP logs; sessions/tool results; plans; file history; session environments;
debug logs; image caches; paste caches; stale agent worktrees; and, for exact
user type `ant`, AgentX package cache. Emit worktree-removal analytics only
when the removed count is positive. An uncaught failure from a stage prevents
later stages.

**PORT-CLEAN-009 — AgentX package-cache throttle.** Use a marker in the
configuration home. Skip when its modification time is less than 24 hours old.
Otherwise attempt a zero-retry, non-realpath lock and skip immediately when
held. There is no second marker check after lock acquisition.

**PORT-CLEAN-010 — Package-cache selection.** Stream package-cache index
entries whose key contains `@agentx-ai/agentx-`, group by the substring
before the last `@`, sort each group newest-first, and remove every entry older
than 24 hours or at index five and beyond. Remove index entries concurrently;
do not perform a full content-store verification or garbage collection.
Successful completion writes the current ISO time to the marker and records
duration/removal count. Failure logs and records failure without advancing the
marker. Always attempt unlock and ignore unlock failure.

**PORT-CLEAN-011 — Native-version cleanup throttle.** A separate marker and
the same 24-hour, zero-retry lock pattern guard recurring native-version
cleanup. On success, write the marker after cleanup. On failure, log and leave
the marker unchanged. Always attempt unlock. Installer-triggered native
cleanup remains unthrottled under the `NINST-*` contract.

**PORT-CLEAN-012 — Housekeeping startup order.** Invoke magic-doc initialization
and skill-improvement initialization without awaiting; conditionally initialize
memory extraction; initialize automatic memory consolidation; start plugin and
marketplace update without awaiting; and conditionally register the deep-link
protocol only when that build feature and interactive mode are both active.
Then schedule slow cleanup and the optional recurring interval.

**PORT-CLEAN-013 — User-activity deferral.** Schedule the slow operation for
600,000 ms after housekeeping starts with an unreferenced timer. At invocation,
if interactive activity occurred in the preceding 60,000 ms, schedule another
600,000 ms unreferenced attempt and return. Otherwise run aggregate cleanup at
most once per process invocation; set its one-shot guard false before awaiting
it. Recheck recent activity after aggregate cleanup and defer native cleanup
again when necessary.

**PORT-CLEAN-014 — Slow-operation failure and completion.** If aggregate
cleanup rejects, its one-shot guard remains false and the timer callback's
asynchronous failure is not locally caught. After unthrottled native cleanup
completes there is no further slow timer. Long-running exact-`ant` sessions
add an unreferenced 24-hour interval whose ticks fire package-cache cleanup and
throttled native cleanup concurrently without awaiting either.

## Graceful shutdown

**PORT-SHUT-001 — One-time handler setup.** Memoize setup process-wide. Pin the
process-exit integration with a permanent no-op subscriber so short-lived child
or terminal subscribers cannot unload shared signal handlers on runtimes with
the known listener-removal defect.

**PORT-SHUT-002 — Signal routing.** On SIGINT, do nothing in raw argument modes
containing exact `-p` or `--print`; those surfaces own cancellation. Otherwise
record the signal and request code zero. SIGTERM requests 143. On non-Windows,
SIGHUP requests 129. The first asynchronous shutdown entry owns cleanup.

**PORT-SHUT-003 — Orphan terminal check.** On a non-Windows TTY input, maintain
one unreferenced 30,000 ms interval. Skip a tick while high-priority scroll
drain is active. If output is not writable or input is not readable, clear the
interval, record `orphan_detected`, and request shutdown code 129.

**PORT-SHUT-004 — Process fault observers.** Observe uncaught exceptions and
unhandled rejections without directly requesting shutdown or rethrowing. The
diagnostic record includes error name and up to 2,000 message characters;
rejections also include up to 4,000 stack characters when available. Analytics
receives only a coarse error-name category. A non-error rejection is converted
to string and truncated.

**PORT-SHUT-005 — Entry latch.** The asynchronous entry returns immediately
when shutdown is already in progress. On first entry, latch before loading the
hook subsystem. Resolve the SessionEnd budget through that dynamic load before
arming a failsafe; a failed or hung load can therefore reject or hang before
the failsafe exists.

**PORT-SHUT-006 — Failsafe budget.** After hook-budget resolution, arm an
unreferenced failsafe for `max(5,000 ms, SessionEnd budget + 3,500 ms)` carrying
the first call's exit code. The failsafe restores terminal modes, attempts the
resume hint, and force-exits. Normal force-exit clears it.

**PORT-SHUT-007 — Terminal restoration order.** If output is not a TTY, skip
terminal restoration. Otherwise, synchronously and best-effort: disable mouse
tracking; if the retained terminal instance owns an alternate screen, unmount
it there or emit one manual alternate-screen exit on unmount failure; drain
input; detach the instance from later exit callbacks; disable modify-other-
keys and Kitty keyboard protocols; disable focus and bracketed paste; show the
cursor; clear iTerm progress; conditionally clear tab status through the
multiplexer; and clear the title unless title management was disabled. On
Windows clear the process title, otherwise emit the title-clearing sequence.
Ignore a dead terminal at any step.

**PORT-SHUT-008 — Resume hint.** After restoring the main screen, print at most
one dimmed resume hint only for an interactive stdout TTY with persistence
enabled and an actual session file. Prefer the session's custom title;
otherwise use the session identifier. Custom-title compatibility quoting wraps
in double quotes and escapes only backslashes and double quotes. It does not
escape shell interpolation characters, so a hardened rebuild must either
retain this output compatibility knowingly or adopt an explicit safer command-
rendering product change.

**PORT-SHUT-009 — Critical cleanup phase.** Snapshot and run the cleanup
registry before hooks. Race the aggregate against a 2,000 ms timer. Ignore
both failures and timeout. A timeout or fail-fast rejection does not cancel
callbacks; they may continue concurrently with later shutdown phases. Project
returned callback failures only from exact sentinels and shutdown-owned context
state; never invoke callback-owned `Error`, `Is`, `As`, or `Unwrap` behavior.

**PORT-SHUT-010 — SessionEnd phase.** Invoke SessionEnd hooks after critical
cleanup with the requested exit reason, optional state accessors, one timeout
signal, and a per-hook cap equal to the resolved overall budget. Ignore hook
errors and cancellation. The global failsafe remains the ultimate bound if a
hook ignores cancellation.

**PORT-SHUT-011 — Final observers.** Best-effort emit the startup performance
report. If a last main request identifier exists, emit a session-end cache-
eviction hint before analytics shutdown. Race first-party and secondary
analytics shutdown together against 500 ms; loss or failure is ignored and
the underlying operations are not cancelled.

**PORT-SHUT-012 — Final output and exit.** If supplied, synchronously append one
newline to the final diagnostic and write it to standard error after terminal
restoration and observers. Immediately before exit, drain the retained
terminal instance's actual input stream, including a `/dev/tty` override used
when process input is piped. Request process exit with the first asynchronous
call's code.

**PORT-SHUT-013 — Dead-terminal force path.** Clear the failsafe before ordinary
exit. If exit throws in production, send uncatchable kill to the current
process; if it unexpectedly returns, report an unreachable failure. In tests,
rethrow an exit exception and permit a mocked exit to return.

**PORT-SHUT-014 — Synchronous facade.** Set the process's pending exit code
before starting asynchronous shutdown and retain a completion handle. On
failure, log, restore terminal modes again, retry the hint, and force-exit;
suppress a second rejection caused by test interception. A later synchronous
call can overwrite the visible pending exit code and stored completion handle
even though the first asynchronous shutdown and its captured exit code still
own the real sequence.

**PORT-SHUT-015 — Test reset boundary.** Test reset clears the shutdown latch,
resume-hint latch, failsafe, and stored completion handle. It does not remove
installed process listeners, clear the orphan-check interval, reset handler
memoization, clear the cleanup registry, or restore the process exit code.

## Acceptance scenarios

### `PORT-A01` — UNC and special-file validation

Parameterize path spellings as UNC, FIFO, socket, device, missing file,
dangling link, and ordinary file. Assert that UNC and special objects cause no
canonicalization access, failures retain the original path, and only a
successful canonical resolution sets the canonical flag.

### `PORT-A02` — Symlink evidence for an absent write

Build live parent links, dangling file links, a multi-link chain, a circular
chain, and a chain longer than 40. Assert evidence order, relative-target
resolution, deepest-existing recovery only at the original seed, finite
termination, and conservative retention of every discovered target.

### `PORT-A03` — Bounded and reverse file reads

Use empty, smaller, larger, concurrently appended, concurrently truncated,
CRLF, blank-line, and 4,096-byte-boundary multi-byte fixtures. Assert byte
counts, snapshot totals, direct-host-filesystem behavior, replacement at a
tail split, grapheme preservation in reverse chunks, and handle closure.

### `PORT-A04` — Path/cache/temp compatibility

Parameterize whitespace, NUL, tilde, Windows slash-drive, relative and outside
paths, 200/201-code-unit sanitized names, hash collisions, random versus
content-derived temporary names, empty XDG values, and supplied-environment
HOME. Assert exact normalization, layout, and caller-owned creation/cleanup.

### `PORT-A05` — Platform and locale profiles

Run Darwin, Win32, Linux, WSL1, numbered WSL, unreadable procfs, malformed
os-release, unavailable locale data, and mixed VCS marker profiles. Assert
first-result caching, exact insertion order, the macOS/WSL legacy support list,
and cached unavailable locale language.

### `PORT-A06` — Lossy no-throw process results

Parameterize success, nonzero exit with output, signal termination, timeout,
cancellation, missing executable, output overflow, unexpected executor
rejection, preserved/discarded output, omitted options, and explicit empty
options. Assert the exact four-field projection and its intentional inability
to distinguish terminal causes.

### `PORT-A07` — Process discovery on Windows and Unix

Stub process tables with parent zero, parent one, missing records, commands
containing newlines, permission-denied liveness, and malformed numeric output.
Assert traversal depth/order, NUL command separation, failure collapse, and
the requirement to validate interpolated identifiers at an external boundary.

### `PORT-A08` — Buffered overflow at shutdown

Cross the count and UTF-16 size thresholds repeatedly before immediate
callbacks run, then flush and dispose. Assert one ordered sink stream, detached
batch coalescing, no duplicate write from queued callbacks, character-not-byte
measurement, sink-error propagation, and the ability to write after dispose.

### `PORT-A09` — Timer, signal, and input races

Race delay completion with pre- and post-registration cancellation, timeout an
operation that continues mutating state, add/remove signal listeners during
emission, feed one stdin chunk without ending, and toggle idle between timer
start and expiry. Assert cleanup, live-set iteration, timeout without
cancellation, and the documented potentially unbounded waits.

### `PORT-A10` — Notification matrix

Parameterize every configured channel, every detected terminal, missing and
malformed Apple preferences, hook rejection, explicit-channel adapter throw,
and auto-channel adapter throw. Assert hook order, default-title application,
Kitty ID range, the Apple bell-disabled inversion, explicit `error`, auto
rejection, and analytics only after a completed method result.

### `PORT-A11` — Sleep-inhibitor ownership

Run macOS and non-macOS profiles with nested owners, excess stops, spawn error,
early exit, renewal, old-child late exit, and forced shutdown. Assert the
0-to-1 and 1-to-0 transitions, 240/300-second relationship, identity guard,
unreferenced resources, one cleanup registration, and nonfatal failure.

### `PORT-A12` — Retention safety and counter semantics

Use absent, zero, negative, fractional, and invalid explicitly configured
retention; timestamped and invalid names; symlinks; nested tool results; and
per-entry failures. Assert the validation safety gate, exact traversal depth,
removal order, directory retention, and the context-dependent meaning of the
two counters.

### `PORT-A13` — Package-cache lock race

Run two processes against missing, fresh, and stale markers with mixed package
ages and more than five versions. Assert zero-wait loser behavior, no second
marker check, old-or-rank removal, marker advancement only after full success,
failure telemetry, and best-effort unlock.

### `PORT-A14` — Housekeeping under recent activity

Parameterize interactive and headless startup, feature gates, recent activity
before each slow stage, aggregate failure, exact and nonexact user types, and
a 24-hour tick. Assert initializer invocation order, ten-minute unreferenced
deferral, one-shot guard timing, uncaught timer rejection, and concurrent
recurring cleanup calls.

### `PORT-A15` — Signal and dead-terminal shutdown

Race SIGINT, SIGTERM, SIGHUP, and orphan detection while an alternate-screen
instance is active and while stdout is gone. Assert one owner, conventional
codes, print-mode SIGINT exclusion, exact reset ordering, main-screen resume
hint placement, input draining, and the dead-terminal force path.

### `PORT-A16` — Hanging cleanup and hooks

Mix successful, asynchronously failing, synchronously throwing, and hanging
cleanup callbacks with failing and cancellation-ignoring SessionEnd hooks.
Assert snapshot semantics, the synchronous-throw edge, 2,000 ms critical
budget, continued sibling work, computed failsafe, observer ordering, and exit
despite a hook that ignores its signal.

### `PORT-A17` — Shutdown entry and resume compatibility

Make hook-module loading fail or hang, invoke the synchronous facade twice with
different codes, and use session titles containing backslash, quote, dollar,
and command-substitution characters. Assert the pre-failsafe import gap,
first asynchronous owner, overwritten facade handle, exact historical quoting,
one hint, final stderr newline, and first-owner force-exit code.

### `PORT-A18` — Hyperlink and output degradation

Parameterize terminal support, custom labels, control characters, closed
standard streams, EPIPE, other output errors, and backpressure. Assert the raw
URL fallback, exact BEL-terminated OSC 8 form, caller-owned validation, EPIPE
destruction, swallowed non-EPIPE error events, and deliberately ignored
backpressure.

### `PORT-A19` — Read-only inspection and creator exclusivity

Inspect a stable private child, an absent child, a permission-insecure child,
a regular-file replacement, a direct symlink, and a child beneath a replaced
parent. Assert that inspection never creates or chmods an entry, preserves
untrusted content, and returns only a stable direct private-directory owner.
Then race two creator-exclusive acquisitions after both observe absence.
Exactly one returns the identity it created; the other reports existence and
does not acquire, chmod, replace, or remove the winner.

### `PORT-A20` — Verified detach and directory sync

Acquire a parent and direct child, create source and destination replacement
races before the final rename, create an empty destination at the exact syscall
boundary, and inject a failing mutation-boundary verifier. Assert every
pre-commit failure leaves the source and collision identity untouched; the
final-boundary collision proves no ordinary replacing rename is used. Run the
nonmutating preflight and assert the child namespace identity and parent entry
set do not change. On Linux and Windows, treat the same-name collision as an
adapter and kernel-path check only; inject an unsupported-filesystem failure at
the real distinct-name rename and assert no commit, exact intent and pending
receipt retention under the lease, and a retryable `delete_incomplete` result.
On Darwin,
reject a volume lacking `VOL_CAP_INT_RENAME_EXCL` before intent is persisted.
On success, assert the source name is absent, the destination owner retains the
source and parent identities, and the parent is synchronized. Inject a
post-rename verification or sync failure and assert committed state plus the
detached owner remain available for bounded recovery.
Sync a stable owned directory and then a replaced pathname; only the stable
identity reaches the platform sync boundary. On Windows, reject an unavailable
write-through reopen or `FlushFileBuffers` boundary; on unsupported profiles,
reject before mutation rather than treating re-stat as sync. Move an acquired
detached owner away immediately before strict cleanup and assert cleanup fails
identity verification instead of claiming that the moved, retained contents
were removed.

### `PORT-A21` — Native session-lock identity and rename

Hold a lock through the v1.0.6 path-opened protocol and assert rooted
acquisition and existing-only acquisition report contention. After release,
acquire the existing-only rooted lease and assert its contents and mode remain
unchanged; a missing parent or lock remains absent. Race parent replacement,
including replacement between root opening and the first identity comparison,
direct lock replacement, and an added hard link against verification; each
fails closed without chmod or content mutation. On Windows and Unix, rename
the owning directory while both the delete-share-capable parent identity
anchor and lease are held, assert the old textual path is no longer verifiable,
then release successfully from the retained handles. On Windows this scenario
must fail if any pathname root retained for the lease lacks
`FILE_SHARE_DELETE`. On an unsupported profile, assert both acquisition forms
create no lock entry.

### `PORT-A22` — Bounded, mount-safe strict cleanup and ancestry

Move a retained child identity out of its parent, replace that parent, and move
the original child back beneath the replacement at the same textual child
path. Assert self identity remains stable but complete verification rejects the
retained-parent mismatch. Race retained-parent replacement after strict cleanup
opens both roots and assert neither the moved original nor replacement contents
are traversed. Present a nested cross-device mount, Linux same-device bind
mount, or Windows reparse mount and assert both child acquisition and cleanup
fail closed without touching mounted contents. Exercise entry, depth, and
rescan ceilings and assert a bound error is returned with only identity-safe,
retryable partial progress; no enumeration call materializes an attacker-sized
directory in memory. Strict cleanup of a nil owner is an identity failure while
ordinary lifecycle cleanup remains idempotent.

## Provenance

Non-normative evidence was surveyed in the portable filesystem, path, cache,
temporary-file, XDG, platform, locale, process, buffering, timer, signal, lock,
notification, sleep-prevention, retention, housekeeping, and shutdown modules,
plus their session, permission, terminal, task-output, and headless callers.
The contracts above are standalone and do not require those modules or their
implementation language during implementation.
