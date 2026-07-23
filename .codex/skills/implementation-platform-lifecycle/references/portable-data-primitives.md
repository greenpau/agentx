# Portable Data and Bounded-Text Primitives

## Contents

1. [Scope and compatibility units](#scope-and-compatibility-units)
2. [Strict JSON and byte-order marks](#strict-json-and-byte-order-marks)
3. [JSON with comments and array editing](#json-with-comments-and-array-editing)
4. [JSON-lines parsing and tail reads](#json-lines-parsing-and-tail-reads)
5. [Stale-while-revalidate memoization](#stale-while-revalidate-memoization)
6. [Bounded least-recently-used memoization](#bounded-least-recently-used-memoization)
7. [Collections, grouping, lazy values, and hashing](#collections-grouping-lazy-values-and-hashing)
8. [Small text transformations](#small-text-transformations)
9. [Asynchronous sequence composition](#asynchronous-sequence-composition)
10. [Bounded joining and accumulation](#bounded-joining-and-accumulation)
11. [FIFO sequencing and rolling buffers](#fifo-sequencing-and-rolling-buffers)
12. [Error records and classifiers](#error-records-and-classifiers)
13. [Failure and portability boundaries](#failure-and-portability-boundaries)
14. [Acceptance scenarios](#acceptance-scenarios)
15. [Provenance](#provenance)

## Scope and compatibility units

These primitives are shared by settings, transcripts, task output, diagnostics, prompts, and integrations. They carry no authority of their own, but their exact recovery and truncation behavior is observable and can change what higher layers see. Rebuild them once behind a portable data boundary rather than allowing each caller to acquire subtly different parsing or size semantics. Follow [portable-data-primitives.drawio](../assets/portable-data-primitives.drawio) for the principal flows.

**PRIM-001 — Native representation compatibility units.** In the text-only helpers, every string length, offset, search position, and slice uses UTF-16 code units. It is not a byte count or Unicode-scalar count and a boundary may divide a surrogate pair. Byte-oriented JSON-lines paths instead use explicit byte offsets and UTF-8 decoding. The occurrence counter in `PRIM-063` is deliberately structurally generic: its positions and one-unit advances use the supplied receiver's own indexing unit, which is UTF-16 units for a string and bytes for a byte buffer. A port whose native representations use other units must add compatibility adapters rather than universalizing either unit.

**PRIM-002 — Standard JSON profile.** Strict JSON parsing and serialization follow the ECMAScript-compatible JSON profile: objects, arrays, strings, JSON-number syntax mapped through compatible double-precision numeric behavior, booleans, and null. Parsing retains array-element order. Parsed objects use ordinary own-key ordering: canonical array-index names enumerate in ascending numeric order, then other string names in source creation order; a duplicate name retains its last parsed value without moving from its first-created position. Serialization visits only enumerable own string-keyed object properties under the same index-then-insertion ordering, and symbol-keyed properties are ignored. Arrays retain index order and serialize holes as null. Modern well-formed string serialization escapes isolated UTF-16 surrogates rather than emitting ill-formed Unicode. These helpers do not invent an extended wire format.

**PRIM-003 — Serialization replacement and failure table.** At the serialization root, undefined, a function, or a symbol produces no serialized string. As an array element, any of those becomes null; as an object-property value, the property is omitted. NaN, positive infinity, and negative infinity become null in arrays, object properties, and at the root; negative zero becomes `0`. A BigInt value throws unless an explicitly installed compatibility conversion has changed its behavior. A reachable cycle throws. Ordinary maps and sets with no enumerable own string properties serialize as empty objects rather than their logical entries. A port must reproduce this table where serialized text becomes a key, persistent record, or wire value.

**PRIM-004 — Serialization hooks and key stability.** Before ordinary object traversal, invoke an object's compatibility `toJSON` hook with the current property key; its return value replaces the object and its throw propagates. Built-in date-like values use that path, and user hooks can make serialization stateful. Property insertion order, numeric normalization, omission/replacement rules, and hooks therefore affect serialized equality. The TTL key contract in `PRIM-040` deliberately inherits these collisions and side effects rather than defining structural identity.

**PRIM-009 — Compatibility trim set.** References to compatibility trimming mean removal from both ends of the ECMAScript whitespace and line-terminator set: TAB U+0009, vertical tab U+000B, form feed U+000C, space U+0020, no-break space U+00A0, ogham space U+1680, U+2000 through U+200A, line separator U+2028, paragraph separator U+2029, narrow no-break space U+202F, medium mathematical space U+205F, ideographic space U+3000, byte-order-mark character U+FEFF, LF U+000A, and CR U+000D. It is broader than ASCII trimming and is independent of the one-leading-mark operation in `PRIM-010`.

## Strict JSON and byte-order marks

**PRIM-010 — Leading mark removal.** The shared text helper removes exactly one leading U+FEFF character and changes nothing else. It does not remove U+FEFF later in the text, strip arbitrary whitespace, or inspect raw byte signatures. Byte-oriented JSON-lines parsing separately recognizes the three-byte UTF-8 signature `EF BB BF`.

**PRIM-011 — Safe strict parse result.** Null input, absent input, and the empty string return null without parsing or logging. Any nonempty input has one leading U+FEFF removed and is parsed as strict JSON. A valid JSON null and any invalid document both return the same null value; callers that must distinguish them need a stricter boundary.

**PRIM-012 — Parse-error logging.** Invalid strict JSON logs the parser failure only when the call's logging flag is true. The default is true. Parsing never throws across this safe boundary. Large-input and first-call cache behavior below can affect whether a repeated failure is logged.

**PRIM-013 — Small-input parse cache.** Inputs whose UTF-16 length is at most 8,192 use one 50-entry least-recently-used cache keyed only by the complete, unmodified input string. Larger inputs bypass lookup and insertion. The key therefore includes a leading U+FEFF even though parsing strips it.

**PRIM-014 — Cached success, failure, and identity.** Cache a discriminated success-or-failure record rather than the returned value, so JSON null and invalid JSON remain representable and invalid text is not reparsed. A successful small-input object or array is returned by the same reference on every hit; mutating it therefore mutates what later callers observe until eviction, deletion, or clear. Large inputs bypass the LRU and produce a fresh parse identity on each call. Callers that need isolation must clone explicitly. The logging flag is deliberately absent from the key. The first parse of a particular small string decides whether an invalid document is logged; later hits neither parse nor log, even if their flag differs. Expose the underlying clear, size, delete, nonpromoting peek, and membership controls described by `PRIM-052` through the safe parser. The administrative peek returns the internal tagged success-or-failure record—not the parser's public unwrapped value—because that record is the actual LRU value.

## JSON with comments and array editing

**PRIM-020 — Comment-aware parse.** Null, absent, and empty input return null. Otherwise remove one leading U+FEFF and invoke the comment-aware parser with its specified default recovery options. Return the parser's value, including a partially specified value if that parser reports errors out of band rather than throwing. A thrown failure is logged and becomes null. Do not silently replace this with strict JSON parsing.

**PRIM-021 — Empty-array creation.** Adding an item to empty content or content containing only the compatibility trim set in `PRIM-009` serializes a new one-element JSON array with four-space indentation. No original whitespace or byte-order mark is preserved.

**PRIM-022 — Comment-preserving insertion.** For nonempty content, remove one leading U+FEFF and parse with the comment-aware parser. If the specified top-level value is an array, insert at index zero when empty and at the former length otherwise. Generate an array-insertion edit with spaces and indentation width four, then apply it to the mark-free original so unaffected comments and formatting survive.

**PRIM-023 — Array-edit fallback.** If an array was specified but the editor returns no edits, append the new item to an in-memory copy and serialize the complete array with four-space indentation. If the specified top-level value is not an array, replace the complete document with a one-element serialized array. A thrown parse or edit failure is logged and takes that same replacement path. These fallbacks intentionally discard prior comments and non-array data. If serializing the replacement itself fails—for example because the new value is cyclic—that final failure propagates because it occurs while already handling the earlier path.

## JSON-lines parsing and tail reads

**PRIM-030 — Line-isolated fallback parser.** The portable fallback scans for LF (`0A` or `\n`) without allocating a split array. Remove a leading text U+FEFF or byte signature `EF BB BF`, decode byte lines as UTF-8, apply the complete compatibility trim set from `PRIM-009` to each line, skip blanks, parse each remaining line independently as strict JSON, and silently skip malformed lines. CRLF works because trimming removes the CR. Because trimming is per line, boundary U+FEFF characters on later lines are also removed even though the explicit BOM step affects only the beginning of the full input. Preserve valid values in encounter order, including null.

**PRIM-031 — Accelerated parser equivalence.** An optional incremental JSON-lines accelerator may parse text or bytes directly. If its first call has no error, declares completion, or reports a read offset at or beyond input length, return its values. Otherwise retain its valid prefix, advance from the reported offset to the next LF, resume immediately after that LF, concatenate newly specified values, and repeat while errors leave further complete lines. Acceleration must preserve the fallback's ordered best-effort result for ordinary complete lines.

**PRIM-032 — Recovery progress and intentional hardening divergence.** Each specified accelerator step searches for a later LF before retrying; if none exists, stop with values already specified. After a retry, stop when it has no error, declares completion, or reaches input length. The reference does not verify that a faulty accelerator's reported read offset advances; repeated or regressing offsets can duplicate recovery or loop. An implementation should add a monotonic-progress guard and bounded termination as an intentional hardening divergence, and must record that malformed-accelerator behavior changes while ordinary complete-line equivalence remains unchanged.

**PRIM-033 — Whole-file read.** Stat the JSON-lines file first. At sizes up to and including 100 MiB (`100 × 1,024 × 1,024` bytes), read the whole file as bytes and pass it to the JSON-lines parser. Stat, open, and read failures propagate to the owning persistence boundary; they are not converted to an empty history.

**PRIM-034 — Tail-bounded read.** For a larger file, open it and repeatedly fill a 100 MiB buffer beginning exactly 100 MiB before the statted end, tolerating short reads until full or end-of-file. Parse only bytes actually read. If an LF exists and is not the final byte read, discard through that first LF because the tail begins in a potentially partial record. If no such LF exists, parse the complete captured tail; malformed partial content will be skipped by line isolation.

**PRIM-035 — Concurrent file change.** The stat chooses a branch but does not create a snapshot. In the at-most-100-MiB branch, the later whole-file read consumes the file as it exists when that read runs; growth after stat can therefore return more than 100 MiB, and shrinkage can return less. In the larger-file branch, positional reads start 100 MiB before the previously observed end and never request beyond that old end: shrinkage may yield fewer accumulated bytes, while growth beyond the old end is excluded. Parse only bytes actually obtained. Callers requiring a transactional snapshot or an unconditional memory ceiling must add their own immutable-file, lock, or bounded-open protocol.

## Stale-while-revalidate memoization

**PRIM-040 — Shared key derivation and lifetime.** Synchronous and asynchronous time-to-live memoizers derive one key by standard JSON serialization of the complete rest-argument array. Their default lifetime is 300,000 milliseconds. Freshness is `now - timestamp <= lifetime`; refresh begins only when the difference is strictly greater. Apply `PRIM-002` through `PRIM-004` exactly: undefined, functions, symbols, NaN, infinities, and explicit null collide as array-element null; negative zero collides with zero; unsupported object properties may disappear; object order and `toJSON` affect the key; cycles and ordinary BigInt reject before invocation. These are compatibility collisions, not structural argument identity.

**PRIM-041 — Synchronous cold miss.** On a missing key, record the current clock value, invoke the wrapped synchronous operation, then store its value with that earlier timestamp and `refreshing = false`. A thrown operation stores nothing. Because timing begins before computation, a slow computation consumes part or all of its freshness lifetime.

**PRIM-042 — Synchronous stale hit.** A stale nonrefreshing entry is marked refreshing and its old value is returned immediately. Schedule one microtask that invokes the wrapped operation. Success replaces the entry with the new value and completion-time timestamp only if the cache still contains the identical stale entry. An ordinary failure is passed to logging and then deletes only that identical entry. While refreshing, every caller receives the stale value and schedules no sibling refresh. Apply `PRIM-081`: if failure normalization throws or hard-fail logging terminates, the deletion statement is not reached.

**PRIM-043 — Synchronous clear race.** Clearing removes every entry. An already queued refresh may still execute, but its identity guard prevents it from repopulating a cleared cache or overwriting a newer cold-miss entry. Clearing is invalidation, not cancellation of arbitrary wrapped side effects.

**PRIM-044 — Asynchronous cold-miss deduplication.** The asynchronous memoizer keeps a separate in-flight map. The first miss invokes the operation and registers its promise; concurrent misses for the same key await that work rather than invoking siblings. On fulfillment, cache the result with the timestamp captured before invocation only if the same promise remains registered. In every completion path, remove only that identical promise. A synchronous throw or rejection propagates and stores no value; cold-miss failure is not logged here.

**PRIM-045 — Asynchronous stale hit and synchronous-throw latch.** A stale nonrefreshing entry is marked refreshing, then the asynchronous operation is invoked directly. If invocation returns a promise, the caller receives the old value immediately; fulfillment replaces only the identical stale entry with a completion-time timestamp, while ordinary promise rejection is passed to logging and then deletes only that entry. Calls made while it is refreshing receive the stale value. This background refresh is not entered in the cold-miss in-flight map. Preserve the specified synchronous-throw edge: if the nominally asynchronous operation throws before returning a promise, the current memoized call rejects, no rejection handler is attached, and the identical cache entry remains permanently marked refreshing. Later calls keep receiving that stale value and never retry until clear or other explicit invalidation removes it. Failure normalization that throws and hard-fail logging similarly prevent the deletion after a promise rejection, as specified by `PRIM-081`.

**PRIM-046 — Asynchronous clear race.** Clear both cached entries and in-flight promises. Existing work is not cancelled, but identity checks prevent its later fulfillment, rejection cleanup, or finalizer from mutating a cache or in-flight slot created after clear. A caller that arrives after clear starts fresh work even if an invalidated operation still runs.

## Bounded least-recently-used memoization

**PRIM-050 — LRU execution.** Derive a string key through the caller-provided resolver. A hit promotes the entry and returns it; a miss invokes the wrapped synchronous operation, stores the result, and evicts least-recently-used entries until the configured maximum is met. The default maximum is 100. A thrown operation stores nothing.

**PRIM-051 — Non-nullish result precondition.** The declared caller contract excludes both null and undefined, but the function performs no explicit runtime validation. Its hit test uses only undefined as the absence sentinel. A null result would be distinguishable at that test if the backing LRU accepts it, whereas undefined may be rejected, discarded, or remain indistinguishable according to that dependency; both values are outside the supported result domain and must not be used to infer portable behavior. Wrap nullable or ambiguous result domains in an always-non-null tagged record, as the strict JSON parser does.

**PRIM-052 — Cache-control surface.** Expose clear, current size, delete-by-key, nonpromoting peek-by-key, and membership test. Administrative inspection must not alter recency. The ordinary memoized call does alter recency on a hit.

## Collections, grouping, lazy values, and hashing

**PRIM-053 — Intersperse order.** Transform a dense sequence `[a0, a1, …]` to `[a0, separator(1), a1, separator(2), a2, …]`. The callback receives the original zero-based index and is never called for index zero. Callback evaluation is interleaved with traversal, so a thrown callback aborts without a result. In the compatibility array profile, unassigned sparse slots are skipped while retained elements keep their original indices.

**PRIM-054 — Count and stable uniqueness.** Counting uses array iteration, so it invokes the predicate once for every numeric position through length minus one; a sparse hole is visited as undefined, unlike the skipped-hole behavior of `PRIM-053`. Increment only when the result is compatibility-truthy: false, null, undefined, positive or negative numeric zero, BigInt zero, NaN, and the empty string are false; every object and array, every nonempty string, and other values are true. Predicate failure propagates. Stable uniqueness consumes any iterable and retains the first occurrence under SameValueZero equality: positive and negative zero are equal, every NaN compares equal to NaN, primitives compare by value, and objects by identity. Output order is first encounter order.

**PRIM-055 — Ordered set algebra.** Sets use SameValueZero equality and deterministic insertion order. Difference iterates the first set and retains its elements absent from the second, preserving first-set order. Intersection testing returns false immediately if either set is empty, otherwise scans the first set until a member of the second is found. The “every” predicate means the first set is a subset of the second and is vacuously true for an empty first set. Union inserts all of the first set, then only new members of the second, preserving that order.

**PRIM-056 — Null-prototype grouping.** Consume the input iterable once. Invoke the key selector with each item and a zero-based monotonically increasing index. Store groups in a dictionary with no inherited properties, creating an array on first observation of a key and appending later items in encounter order. Property keys follow the compatibility profile's string-or-symbol key coercion. Names such as `__proto__`, `constructor`, and `toString` are ordinary safe keys. Within every group, item order is encounter order. Enumeration of group keys follows own-key rules rather than pure group encounter order: canonical array-index string keys appear first in ascending numeric order, then other strings in first-creation order, then symbols in first-creation order. Iterator or selector failure propagates with no returned partial object.

**PRIM-057 — Nullish lazy factory.** Return a zero-argument accessor whose first call invokes a factory and caches its result. Later calls reuse any non-null, non-undefined value, including false, zero, and the empty string. A null or undefined result is treated as still absent and causes the factory to run again on the next call. A thrown factory stores nothing. No reentrancy, synchronization, cancellation, or cross-process guarantee is added.

**PRIM-058 — Externally resolvable promise.** Construct one ordinary promise and synchronously return it with its exact resolve and reject functions. Resolution assimilates a supplied promise-like value; rejection accepts an optional reason; only the first settlement wins according to the shared promise profile. Creating the triple starts no background task and adds no cancellation semantics.

**PRIM-059 — Hash profiles.** The portable signed 32-bit text hash starts at zero and, for each UTF-16 code unit, applies `hash = signed32(31 × hash + unit)`. Despite its historical name, the multiplier is 31. General content hashing selects the runtime's accelerated noncryptographic string hash when present and renders its integer in base-ten; otherwise it emits lowercase SHA-256 hex over compatibility UTF-8 text. That fallback converts every isolated UTF-16 surrogate to U+FFFD before UTF-8 encoding. Pair hashing either seed-chains the accelerated hash (`hash(a)` becomes the seed for `hash(b)`) or incrementally hashes compatibility UTF-8 for `a`, one zero byte, then separately encoded `b`; surrogate halves divided between the two inputs are each replaced and are never recombined. Outputs are opaque and profile-dependent, not stable across those two algorithms and never authorization or cryptographic-integrity evidence. The fallback pair encoding is not injective when inputs contain U+0000, so it is suitable only for change detection.

## Small text transformations

**PRIM-060 — Literal regular-expression escaping.** Prefix each occurrence of `. * + ? ^ $ { } ( ) | [ ] \` with one backslash and leave every other character unchanged, producing text safe for literal insertion into the specified regular-expression grammar.

**PRIM-061 — First-unit capitalization.** Uppercase only the first UTF-16 code unit's one-character string and concatenate the untouched remainder. Do not lowercase the remainder. Empty input remains empty; Unicode case expansion follows the host's compatible uppercase mapping.

**PRIM-062 — Plural selection.** Select the singular form only when the numeric count is exactly 1. Every other value—including zero, negative one, fractions, and non-finite numbers—selects the supplied plural or the singular plus `s` default.

**PRIM-063 — First-line and occurrence scans.** First-line extraction returns string text before the first LF or the whole string when none exists; it does not strip CR. Occurrence counting accepts any receiver with a positional search operation, performs its first search at the caller's optional start position (default zero), and after every match searches again at the matched position plus one receiver unit. For a string receiver those positions are UTF-16 units. For a byte-buffer receiver they are bytes, and the string needle is encoded as UTF-8 by default before byte search; the plus-one advance remains one byte. A multi-unit or multi-byte needle can therefore count overlapping matches. The needle must be nonempty; an empty needle is outside the contract because the specified search can cease to advance at the receiver's end.

**PRIM-064 — Full-width input normalization.** Digit normalization maps only U+FF10 through U+FF19 to ASCII `0` through `9` by subtracting hexadecimal FEE0. Space normalization maps only U+3000 to U+0020. Neither operation applies general Unicode compatibility normalization or changes any other character.

**PRIM-065 — Line-count truncation.** Split only on LF. If the resulting line count is at most the caller's maximum, return the original text byte-for-byte. Otherwise retain the first `maximum` entries, join them with LF, and append U+2026 with no extra newline. The caller must supply a nonnegative integer; the primitive does not repair invalid limits.

## Asynchronous sequence composition

**PRIM-066 — Last yielded value.** Consume an asynchronous generator through completion and return its last yielded value, including an actual undefined value. If it yields nothing, reject with the diagnostic `No items in generator`. A generator failure propagates; its final return value is ignored.

**PRIM-067 — Final return value.** Repeatedly request the next generator step, discard every yielded value, and return the value on the first step marked done. A generator failure propagates. The consumer does not invoke a separate close operation merely because it has observed completion.

**PRIM-068 — Concurrent generator merge.** Accept an ordered list of asynchronous generators and a concurrency cap whose default is unbounded. Start generators from the front while active count is less than the cap. Keep at most one pending next-step request per active generator. Await whichever step settles first. For a yielded non-undefined value, schedule that same generator's next step before publishing the value downstream. Suppress yielded undefined values. When a generator completes, start one waiting generator. Thus output is completion-ordered, not source-ordered, while each individual generator's retained values preserve its order.

**PRIM-069 — Merge edge behavior.** A positive fractional cap effectively admits its mathematical ceiling because admission tests integer active count against the fraction; zero, negative, or NaN starts nothing and completes without consuming or closing inputs. A rejected next-step request rejects the merged sequence and does not automatically cancel or close sibling generators or their pending requests. Early downstream cancellation likewise has no specified sibling-cleanup loop. Array-to-generator conversion yields every value in array iteration order; generator-to-array conversion consumes to completion and appends every yielded value.

## Bounded joining and accumulation

**PRIM-070 — Joined-text bound.** Join entries incrementally with a comma default delimiter and a default maximum of 33,554,432 UTF-16 code units. Add the delimiter only when the accumulated result is nonempty, not merely when an earlier array entry existed. Thus leading empty entries do not themselves cause a delimiter before the next nonempty entry.

**PRIM-071 — Join truncation marker.** Before each addition, test whether delimiter plus complete entry fits. If not, reserve room for the literal `...[truncated]`; when positive room remains, append the delimiter, that many units of the entry, and the marker. Otherwise append only the marker and return. The marker-only branch may make the result exceed the requested maximum; the maximum bounds retained payload, not necessarily final diagnostic text.

**PRIM-072 — End-truncating accumulator.** Maintain retained prefix, truncation flag, and total received count. Decode each byte-buffer append independently as compatibility UTF-8 with replacement, with no streaming decoder state carried to the next append. A multibyte sequence divided across two appends therefore becomes replacement characters rather than being reassembled. Count the resulting UTF-16 units and increment total before every capacity check. Retain complete input while it fits; on overflow retain exactly the prefix that reaches the maximum and latch truncation. Once latched at capacity, later appends update total but never change retained content.

**PRIM-073 — Accumulator rendering.** An untruncated accumulator renders exactly its retained content. A truncated accumulator appends LF plus the literal template `... [output truncated - NKB removed]`, where `N = floor(((total received - maximum) / 1,024) + 0.5)`. The numerator is nonnegative on this branch, so this is nearest-integer rounding with exact halves rounded upward; do not substitute ties-to-even rounding. The marker lies outside the maximum and the counter is compatibility-named “bytes” even though it counts decoded UTF-16 units.

**PRIM-074 — Accumulator control.** Clearing empties content, resets truncation false, and resets total to zero. Expose retained length, truncation state, and total received count independently. Capacity is fixed at construction and defaults to 33,554,432 units.

## FIFO sequencing and rolling buffers

**PRIM-075 — Per-wrapper FIFO execution.** A sequential wrapper owns an in-memory FIFO of argument sequence, result resolvers, rejection resolver, and invocation receiver/context. Every call synchronously enqueues one item and triggers the processor. At most one processor and one wrapped asynchronous invocation are active for that wrapper. Await each invocation before removing the next item, apply the original receiver, and resolve or reject only the corresponding caller. A rejection does not stop later queued work. A never-settling invocation blocks the queue; there is no timeout, cancellation, capacity bound, priority, durability, or cross-process exclusion in this primitive.

**PRIM-076 — Circular-buffer insertion.** A positive integer capacity is a caller precondition; the reference performs no explicit validation. Under that precondition, construct a fixed-capacity rolling buffer with head zero and logical size zero. Add writes at head, advances head modulo capacity, and increments size only until capacity. Once full, each add overwrites the oldest element. Bulk add is precisely repeated single add in input order. The native array constructor rejects most negative, fractional, NaN, or infinite capacities. Capacity zero constructs but is broken compatibility state: the first write makes head NaN, logical size remains zero, and projections remain empty. A port that rejects zero eagerly is an intentional fail-fast divergence.

**PRIM-077 — Circular-buffer projection.** A non-full buffer's oldest physical index is zero; a full buffer's oldest index is head. Complete projection visits logical size entries from that index modulo capacity. Recent projection computes `available = min(requested count, logical size)` and emits the final `available` logical entries from oldest to newest. A nonnegative integer count is a caller precondition and the reference does not validate it: negative or NaN count yields no loop iterations, while a positive fractional count can perform ceiling-many iterations using fractional property indexes and consequently project undefined values. Clearing truncates physical storage, resets head and size, and keeps capacity; later adds rebuild storage under the same rules. Reported length is logical size, not backing-array extent. Eager count validation is an intentional fail-fast divergence.

## Error records and classifiers

**PRIM-083 — Named error records.** Provide structured errors with these public fields: base product error sets its name to the concrete subclass name; abort error has exact name `AbortError`; configuration-parse error has message, name `ConfigParseError`, file path, and default configuration; shell failure has fixed message `Shell command failed`, name `ShellError`, stdout, stderr, numeric code, and interrupted flag; teleport failure has ordinary message plus formatted message and name `TeleportOperationError`; telemetry-safe failure has ordinary message, exact name `TelemetrySafeError`, and a separately supplied telemetry message that defaults to the ordinary message only when absent or null. The plain malformed-command subtype adds no fields or name override.

**PRIM-084 — Abort classification.** Classify as abort only an instance of the local abort type, an instance of the model provider's user-abort type, or any actual error instance whose exact name is `AbortError`. A plain object carrying that name is not enough. Subclass-name minification cannot affect the provider check because it uses runtime type identity.

**PRIM-085 — Unknown-error normalization.** Exact-message testing succeeds only for an actual error instance with case-sensitive equal message. Converting to an error returns an existing error unchanged; otherwise create a new error whose message is the compatibility string conversion of the value. Message extraction similarly returns an actual error's message or the compatibility string conversion of anything else.

**PRIM-086 — Filesystem evidence extraction.** Extract an errno code only from a non-null object having a string-valued `code` property; extract its path analogously from a string-valued `path`. Missing-file testing is exact code `ENOENT`. Broad inaccessible-path testing accepts exactly `ENOENT`, `EACCES`, `EPERM`, `ENOTDIR`, or `ELOOP`. These predicates describe caught platform evidence and do not themselves authorize fallback, disclosure, or destructive cleanup.

**PRIM-087 — Short stack projection.** A non-error becomes its compatibility string. An error with a falsy stack becomes its message. An ordinary string-valued stack is split on LF; take its first line as header and identify later frame lines whose compatibility-trimmed form begins `at `. If frame count is at most the default maximum five, return the complete original stack including non-frame lines. If greater, return header plus the first maximum original frame lines joined by LF, omitting other lines. A nonnegative integer maximum and a falsy-or-string stack are caller preconditions; the reference does not validate them. In particular, a truthy non-string stack reaches a missing split operation and throws. Full stacks remain available only to the debug path that owns sensitive logging.

**PRIM-088 — HTTP-client error buckets and record shape.** First derive the normalized message. Unless the caught value is a non-null object with a truthy `isAxiosError` marker, classify `other` and return own fields `kind` and `message` only. For a marked value, read the nested response status without runtime type validation; exact numeric 401 or 403 wins and classifies `auth`, otherwise exact code `ECONNABORTED` classifies `timeout`, exact `ECONNREFUSED` or `ENOTFOUND` classifies `network`, and everything else classifies `http`. Every marked outcome owns `kind`, `message`, and `status`, even when status is absent/undefined or is a nonnumeric hostile value. Classification does not decide retry, credential refresh, or user disclosure.

**PRIM-089 — Error privacy boundary.** Only the deliberately separate telemetry message of a telemetry-safe error may be treated as pre-reviewed for telemetry, and callers must still apply observability policy. Formatted teleport text, file paths, shell streams, default configuration, arbitrary messages, stacks, and stringified caught objects are not telemetry-safe merely because a helper can extract them.

## Failure and portability boundaries

**PRIM-080 — Recovery is explicit.** Safe JSON returns null, JSON-lines skips individual malformed records, array editing replaces malformed content, and an ordinary memoized background-refresh failure attempts logging before invalidation. These are distinct policies. Do not generalize one helper's recovery into a universal “ignore parse errors” rule.

**PRIM-081 — Diagnostic isolation boundary.** For ordinary Error values outside hard-fail mode, sink work is best effort and failures inside the logger's protected body are swallowed. Two specified exceptions are deliberate and observable: conversion of a non-Error value to compatibility text occurs before that protection and may itself throw, and a feature-enabled `--hard-fail` mode prints the normalized failure then terminates the process. Refresh rejection handlers call logging before cache deletion, so either exception prevents that deletion and can leave the entry refreshing. Parser catch blocks likewise cannot promise their normal null fallback if failure logging itself throws or terminates. Never include unrelated file contents, secrets, or complete oversized payloads merely to explain a parser failure.

**PRIM-082 — No hidden authority.** These helpers may bound memory, recover data, or cache computation, but cannot decide whether specified configuration is trusted, whether a file may be read or rewritten, or whether stale data is acceptable to a security decision. The owning settings, transcript, tool, or permission contract makes that decision.

## Acceptance scenarios

### `PRIM-A01` — Invalid JSON and logging-key asymmetry

Parse the same short malformed string first with logging disabled and then enabled. Both calls return null, only one parse occurs, and neither logs. After cache clear, reversing call order produces exactly one logged parser failure. A 9,000-unit malformed string bypasses the cache and follows each call's own logging flag.

### `PRIM-A02` — Null versus invalid JSON

Parse `null`, an invalid token, empty input, and a U+FEFF-prefixed `null`. All four public results are null. Internally, the two valid null documents are cached as successful values, the invalid token as failure, and empty input is not cached.

### `PRIM-A03` — Damaged JSON-lines tail

A file larger than 100 MiB begins the retained window midway through a malformed record, followed by LF, valid record A, a malformed line, valid record B, and no final LF. Tail reading discards the first partial record, parsing returns A then B, and no malformed line aborts the file.

### `PRIM-A04` — Clear during asynchronous cold miss

Caller A starts work for key K. Clear runs before it resolves; caller B then starts new work for K. A still receives its own result, but A cannot populate the cache or delete B's in-flight marker. B's fulfillment alone becomes the cached value.

### `PRIM-A05` — Clear during stale refresh

A stale synchronous value schedules refresh R and returns old data. Clear runs, then a cold miss stores a new value for the same key. R may execute, but its stale-entry identity mismatch prevents both overwrite on success and deletion on failure.

### `PRIM-A06` — Unicode capacity boundary

Use an astral symbol whose encoded representation is one Unicode scalar, two UTF-16 units, and four UTF-8 bytes. A maximum of one unit may split its surrogate pair in joining or accumulation. A conforming port reproduces the specified UTF-16-unit boundary rather than silently changing capacity to scalars or bytes.

### `PRIM-A07` — Marker exceeds requested join maximum

Join `a` and `b` with maximum one. The first entry fits. The second has no room after reserving delimiter and marker, so the result is `a...[truncated]`, longer than one. Tests assert this compatibility quirk rather than assuming the returned diagnostic always fits the payload maximum.

### `PRIM-A08` — Commented array and destructive fallback

Appending to a valid commented array applies a localized insertion and preserves unrelated comments. Appending to a valid object or a document whose editor throws yields a newly serialized one-element array; former content and comments are intentionally gone, and a thrown failure is logged.

### `PRIM-A09` — Set equality and order

The first set contains, in order, NaN, negative zero, object X, and `a`; the second contains another NaN, positive zero, a distinct-but-equal-looking object, and `b`. Difference retains object X then `a`; intersection is true at NaN; union retains the first set's order, does not append the second NaN or zero, then appends the distinct object and `b`.

### `PRIM-A10` — Prototype-looking group key

Group three items under `__proto__`, `toString`, and `__proto__`. The result has no prototype, contains two own groups, and the first group retains its two items in encounter order. No inherited object member is read, invoked, or overwritten.

### `PRIM-A11` — Nullish lazy result

The factory returns null, then zero, then would return one. The first accessor call returns null; the second reruns and caches zero; every later call returns zero without reaching the third factory result. If the first call instead throws, the following call retries.

### `PRIM-A12` — Completion-ordered generator merge

With cap two, generator A yields `a1`, waits, then yields undefined; generator B first yields `b1` sooner and completes. The merged order begins `b1`, then starts the next waiting generator only when B completes. A's undefined is suppressed, but A's next request was already issued before the merged consumer resumed from its prior retained value.

### `PRIM-A13` — Profile-dependent hashing

Hash identical non-ASCII text once under the accelerated profile and once under the SHA-256 fallback. Each profile is deterministic internally, but their serialized hashes need not match. A persisted consumer records or versions the profile, and no permission, signature, or tamper decision relies on either change-detection hash.

### `PRIM-A14` — Queue rejection and stall

Calls A, B, and C enter one sequential wrapper in that order. A rejects, B later fulfills, and C never settles. A's caller rejects, B still begins and resolves normally, then C begins and blocks every later call without reordering or silently timing out.

### `PRIM-A15` — Circular overwrite and reuse

A capacity-three buffer receives A, B, C, then D. Complete projection is B, C, D; the two most recent are C, D. Clear produces empty projection and length zero but retains capacity. Adding E afterward produces exactly E and length one.

### `PRIM-A16` — Abort-shaped impostors

A local abort instance, provider user-abort instance, and ordinary error renamed `AbortError` all classify as abort. A plain object `{name: "AbortError"}` and a non-error string do not. The decision does not depend on constructor-name text.

### `PRIM-A17` — Stack shortening with non-frame lines

An error stack has a header, six `at ` frames, and two interleaved diagnostic lines. With maximum five, return the header and first five original frame lines only. With maximum six, return the entire original stack, including both diagnostic lines, because no truncation branch runs.

### `PRIM-A18` — HTTP classification precedence

A marked HTTP-client error has status 401 and code `ECONNABORTED`; it classifies as auth because status wins. A marked status 500 with `ECONNABORTED` is timeout and retains status 500. An unmarked lookalike with the same fields is other. Retry policy is tested separately by the owning network contract.

### `PRIM-A19` — Ordered array helpers

Intersperse A, B, C calls the separator only with indices one and two and returns A, S1, B, S2, C. Counting a dense sequence with predicate results false, nonempty string, and zero returns one. Stable uniqueness of two references to object X, then distinct object Y, retains X then Y. A sparse compatibility array skips holes during intersperse but preserves the original index passed for later assigned entries.

### `PRIM-A20` — Deferred first settlement

Create an externally resolvable promise without starting work. Resolve it with another pending promise, then call reject and resolve again. Its state follows the adopted promise and ignores both later settlement attempts. A second deferred rejected first retains that exact rejection reason.

### `PRIM-A21` — Parsed-reference cache poisoning

Parse one short object document, mutate a nested member of the returned object, then parse the identical source again. The second result is the exact same object reference and contains the mutation; the parser does not clone or reparse it. A nonpromoting administrative peek at the source key returns a tagged successful cache record whose value member points at that same object, not the unwrapped object as the peek result. Deleting the key or clearing the cache makes the next parse construct a new identity. An otherwise equivalent source longer than 8,192 UTF-16 units bypasses the cache and returns a new identity on each parse.

### `PRIM-A22` — Stat/read growth races

Stat a file just below 100 MiB, grow it beyond 100 MiB before the whole-file read, and observe that the complete later contents are read despite exceeding the nominal threshold. Separately, stat a file above the threshold, append bytes after its old end, and observe that the positional tail branch reads no appended bytes. Neither result is described as a snapshot.

### `PRIM-A23` — Synchronous throw during asynchronous stale refresh

Populate the asynchronous TTL cache, age the entry beyond its lifetime, then configure the wrapped operation to throw before returning a promise. The stale-triggering call rejects instead of returning old data, no background rejection log or deletion occurs, and the entry stays marked refreshing. Every later call returns the old value without another invocation until cache clear; after clear, the next call takes the ordinary cold-miss path.

### `PRIM-A24` — Receiver-relative occurrence positions

Count an overlapping multi-unit needle in a string starting from a nonzero UTF-16 position, then count a text needle in a byte buffer beginning at a nonzero byte offset. Each successful search resumes exactly one unit after the prior match in that receiver's unit system. The buffer case is not converted into UTF-16 indexing, and the string case is not converted into UTF-8 byte indexing.

### `PRIM-A25` — Truncation rounding tie

Render a truncated accumulator with exactly 512 removed units. The marker reports `1KB removed`, not zero. Repeat with 2,560 removed units: the marker reports `3KB removed`; a target language whose default rounding is ties-to-even must not report two. Values immediately below and above each half boundary exercise the same `floor(x + 0.5)` rule.

### `PRIM-A26` — Serialization-key collisions and throws

Invoke a TTL wrapper once each with undefined, a function, a symbol, NaN, positive infinity, and explicit null as the sole argument. Every argument array serializes as `[null]`, so all calls address one cache entry. Negative zero and zero likewise share `[0]`. Object arguments differing only by undefined-valued properties can collide, while insertion-order differences can separate otherwise equal-looking objects. A cyclic argument or ordinary BigInt rejects during key construction and never invokes the wrapped operation.

### `PRIM-A27` — Broad trimming on JSON lines

Treat an array-edit source made only of NBSP, U+3000, and U+FEFF as empty and replace it with a freshly indented one-item array. Parse JSON-lines whose second line is surrounded by U+FEFF and whose CRLF line is surrounded by NBSP: per-line trimming removes all those boundary characters and both valid values survive. An ASCII-only trim implementation fails this fixture.

### `PRIM-A28` — Sparse array contrast

Create a length-three sparse array whose only assigned value C is at index two. Intersperse skips both holes but calls the separator for original index two, yielding separator-two then C. Counting with a predicate that recognizes undefined invokes the predicate three times and returns two. A predicate returning an empty array for every position counts all three because objects are truthy even when logically empty.

### `PRIM-A29` — Independent UTF-8 decoding

Append the leading and trailing bytes of one multibyte character in separate accumulator calls. Each call decodes independently, so retained text and total units contain replacement characters rather than the original scalar. For fallback hashing, an isolated surrogate hashes as UTF-8 for U+FFFD; placing a high surrogate at the end of pair input A and a low surrogate at the beginning of B does not reassemble them across the inserted zero byte.

### `PRIM-A30` — Circular-buffer unsupported inputs

Constructing with capacity zero succeeds in the compatibility runtime, but adding an item leaves reported length zero and all projections empty while head becomes nonnumeric. Negative or fractional capacity fails through native array construction rather than a domain-specific validator. On a valid buffer, negative or NaN recent-count returns empty; a fractional count is outside the precondition and can emit undefined values through fractional indexes. A safer port may reject these eagerly only as a documented fail-fast divergence.

### `PRIM-A31` — Logging interrupts refresh cleanup

Reject a stale refresh with an ordinary Error in the normal profile: logging is attempted and the identical entry is deleted. Reject it with a non-Error whose compatibility string conversion throws: logger normalization throws before its protected body, deletion is skipped, and the entry remains refreshing. In the feature-enabled hard-fail profile, logging terminates before deletion. Tests that cannot safely exercise process termination verify the ordered calls with an isolated subprocess.

### `PRIM-A32` — HTTP result field presence

Classify an unmarked error and assert that its result has no own `status` field. Classify a marked error without a response and assert that it does own `status` with an absent/undefined value. A marked error carrying a string status also retains that string but does not classify as auth; status is evidence forwarded without validation, not proof of a valid response schema.

### `PRIM-A33` — Malformed stack precondition

An ordinary Error with no or empty stack returns its message, and one with a string stack follows frame projection. Assign a truthy object as the stack and observe that projection throws when it attempts string splitting. An implementation that sanitizes this shape must label the fail-safe behavior as an intentional divergence.

### `PRIM-A34` — Faulty accelerator progress divergence

Use an accelerator stub that reports an error and a nonadvancing read offset despite remaining LF-delimited input. The compatibility loop can repeat or duplicate recovery. The hardened implementation records the last accepted offset, terminates or falls back when progress is not strictly monotonic, and reports this fixture as an intentional extension-boundary divergence rather than claiming identical faulty-provider behavior.

### `PRIM-A35` — Numeric-looking group-key enumeration

Create groups in encounter order under keys `10`, `2`, `alpha`, `1`, then symbol S. Items within each group retain encounter order, but own-key enumeration returns `1`, `2`, `10`, `alpha`, then S: array-index strings are numerically ordered before the other string and symbol insertion-order partitions. Prototype-looking non-index strings remain ordinary safe own keys.

## Provenance

The normative behavior was implemented from the shared strict/comment-aware/line-oriented data helpers, time-to-live and least-recently-used caches, byte-order-mark leaf adapter, ordered collection helpers, asynchronous sequence combinators, hashing profiles, lazy/deferred values, sequential execution, rolling buffers, error records/classifiers, and bounded string utilities. Source module names and implementation-language syntax are omitted because a standalone implementation must preserve units, bounds, race guards, recovery choices, ordering, and visible markers using the target language's own libraries.
