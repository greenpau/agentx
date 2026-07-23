# Retained flex-layout engine contract

This document defines the layout tree used by the terminal renderer: public adapter vocabulary, value resolution, flex measurement and positioning, cache invalidation, pixel rounding, lifecycle, and known compatibility gaps. `YOGA-*` identifiers are stable implementation anchors; the name is historical provenance, not a requirement to use a particular library. Follow the [flex-layout state diagram](../assets/flex-layout.drawio) for the measure-versus-layout and cache-generation branches.

## Contents

1. [Public adapter surface](#public-adapter-surface)
2. [Style defaults and values](#style-defaults-and-values)
3. [Tree, dirtiness, and lifecycle](#tree-dirtiness-and-lifecycle)
4. [Layout entry and cache](#layout-entry-and-cache)
5. [Measurement and flex basis](#measurement-and-flex-basis)
6. [Lines, flex distribution, and container size](#lines-flex-distribution-and-container-size)
7. [Positioning and absolute children](#positioning-and-absolute-children)
8. [Rounding and geometry](#rounding-and-geometry)
9. [Explicit compatibility gaps](#explicit-compatibility-gaps)
10. [Acceptance scenarios](#acceptance-scenarios)

## Public adapter surface

`YOGA-001` — A layout node owns parent/ordered children, style, computed rectangle, computed physical padding/border/margin, optional measurement callback, dirty state, and cache generation. The terminal adapter exposes insertion/removal, one layout call, measurement registration, dirty marking, computed geometry getters, style setters, and `free`/recursive free.

`YOGA-002` — Exposed terminal style vocabulary is exactly:

| Domain | Values |
| --- | --- |
| edges | `all`, `horizontal`, `vertical`, `left`, `right`, `top`, `bottom`, `start`, `end` |
| gaps | `all`, `column`, `row` |
| display | `flex`, `none` |
| flex direction | `row`, `row-reverse`, `column`, `column-reverse` |
| align items/self | `auto`, `stretch`, `flex-start`, `center`, `flex-end` |
| justify content | `flex-start`, `center`, `flex-end`, `space-between`, `space-around`, `space-evenly` |
| wrap | `nowrap`, `wrap`, `wrap-reverse` |
| position | `relative`, `absolute` |
| overflow | `visible`, `hidden`, `scroll` |
| measure mode | `not-defined`, `exactly`, `at-most` |

Width, height, min/max dimensions, flex basis, position, padding, margin, border, and gap accept point values through the terminal adapter; dimensions/positions and min/max also have explicit percent setters where exposed. Width and height support `auto`.

`YOGA-003` — The adapter calls layout with the supplied owner width, a not-defined owner height, and left-to-right direction. Its `height` parameter is intentionally ignored. The measurement adapter forwards only available width and width mode to the terminal text measurer; engine-supplied height and height mode are intentionally not forwarded. Preserve these limitations because terminal text wraps by columns and derives rows.

`YOGA-004` — The underlying engine recognizes additional values—display `contents`, position `static`, align baseline/content distribution, direction inherit/right-to-left, auto margins, percent gap, and a full four-argument measure callback—but the terminal adapter does not expose all of them. Do not accidentally make unsupported style strings silently map to a different exposed value.

## Style defaults and values

`YOGA-010` — New nodes begin with:

```text
direction=inherit; flex-direction=column; justify=flex-start;
align-items=stretch; align-self=auto; align-content=flex-start;
wrap=nowrap; overflow=visible; display=flex; position=relative;
grow=0; shrink=0; basis=auto;
width=auto; height=auto; min/max=not-defined;
all spacing/insets/gaps=not-defined; point scale=1.
```

The terminal adapter's layout entry resolves in left-to-right mode regardless of an inherited style direction.

`YOGA-011` — A stored value has unit `not-defined`, `point`, `percent`, or `auto`. Resolve a percent as `value * ownerSize / 100`; a not-defined owner produces a not-defined result. Numeric NaN and positive/negative infinity become not-defined. A string ending in `%` parses as percent; another numeric string parses as points; an unparsable value becomes not-defined. Auto padding and border resolve to zero; auto margins participate in free-space distribution.

`YOGA-012` — Resolve every padding, border, margin, and ordinary relative-position percentage against owner width, including top and bottom. This follows the compatibility engine rather than intuitive height-relative vertical spacing. Min/max height percentages and explicit height resolve against owner height. Absolute top/bottom insets resolve against the containing height as specified in `YOGA-061`.

`YOGA-013` — Edge fallback order for a physical edge is:

```text
specific physical edge
  -> horizontal or vertical aggregate
  -> all
  -> start for left / end for right in LTR
  -> zero or not-defined
```

This means `all` outranks `start`/`end` in this specified engine. Do not substitute browser-CSS precedence. The computed arrays always contain four physical values in left, top, right, bottom order.

`YOGA-014` — Resolve row/column gap from its specific gutter, then `all`, then zero. Clamp a resolved negative gap to zero. Main gap lies between siblings; cross gap lies between flex lines, never outside the first/last line.

`YOGA-015` — Apply maximum then minimum constraints to each tentative size; min therefore wins if constraints conflict. A missing/not-defined constraint has no effect. Box sizing is border-box: explicit dimensions include padding and border.

## Tree, dirtiness, and lifecycle

`YOGA-020` — Inserting a child assigns its parent and splices it at the requested index, then marks the node dirty. Removing searches by identity, does nothing if absent, otherwise detaches parent and marks dirty. The caller is responsible for not inserting one node into multiple parents.

`YOGA-021` — Dirty marking propagates upward only until it reaches an already-dirty ancestor. Style changes and measurement callback changes mark the node dirty. A measure-only pass never clears dirty; only a full positioning/layout pass clears it. This prevents measurement from reusing a pre-mutation positioned-layout cache.

`YOGA-022` — `free` severs the parent, discards child links, callback, and allocated multi-entry caches, and decrements live-node accounting. It does not recursively free former children. `freeRecursive` walks descendants postorder and frees each. Reset restores default style/tree/cache state but does not replace configuration ownership.

`YOGA-023` — `display:none` recursively sets descendant rectangles to zero and invalidates their layout, measure, and basis caches so unhiding recomputes. `display:contents` makes its own box zero, ignores its margin/padding/position/dimensions, and recursively lifts its flow and absolute descendants into the nearest layout-producing ancestor.

## Layout entry and cache

`YOGA-030` — A layout call increments one global generation, treats defined root owner width/height as exact and absent dimensions as not-defined, performs recursive layout, applies root margin plus left/top relative inset, then rounds the entire tree. The terminal adapter normally makes only width exact per `YOGA-003`.

`YOGA-031` — Cache identity includes available width/height, both measure modes, owner width/height, forced-width and forced-height flags, and whether dimensions alone or full layout is requested. Compare not-defined numeric values as equal to each other. Cache outputs include width and height; never rely on the node's currently mutable rectangle as the cached output.

`YOGA-032` — Maintain distinct last-layout and last-measure entries plus a four-slot rotating input/output cache. A clean node may reuse matching entries across generations. A dirty node may reuse only a matching dimension-only result computed in the current generation; full-layout same-generation reuse is forbidden because it would skip child repositioning. The first write after a dirty cross-generation transition makes stale pre-dirty entries unreachable.

`YOGA-033` — Cache flex basis separately by owner width/height, available main/cross size, cross measure mode, and generation. A same-generation basis is valid even while the node remains dirty because the subtree cannot mutate inside one synchronous layout pass. A prior-generation basis is valid only for a clean node.

`YOGA-034` — On every cache hit, restore the matching output width/height before returning. On every early-return path—measured leaf, empty leaf, hidden subtree, or container measure—commit single-slot and multi-slot outputs. Caches are an optimization: removing them must not change rectangles, only performance.

## Measurement and flex basis

`YOGA-040` — Before measuring a node, resolve its physical box values, let an explicit style dimension replace the corresponding available constraint with exact mode, apply min/max, and subtract padding plus border from finite inner constraints.

`YOGA-041` — A leaf with a measurement callback receives inner width/height and their modes. For an exact axis, the final outer size is the exact value and the callback's value is ignored. Otherwise add padding plus border to the measured value, default a missing measured dimension to zero, and apply min/max. An empty unmeasured leaf is padding plus border unless an exact dimension determines it.

`YOGA-042` — Determine a flow child's flex basis in this priority:

1. defined explicit flex basis resolved against available main size;
2. defined style dimension on the main axis;
3. intrinsic recursive measurement.

Clamp the result through main-axis min/max and at least zero. A row container may pass finite main availability as an at-most width during intrinsic measurement; a column does not constrain intrinsic height merely because parent height is available.

`YOGA-043` — Initial cross measurement respects an exact/known cross constraint. Stretch applies an exact cross size only when the child's cross dimension is auto and neither cross margin is auto. For multiline or intrinsically sized containers, remeasure a stretched child after line cross size is known.

## Lines, flex distribution, and container size

`YOGA-050` — Partition children into flow and absolute lists after processing `none` and `contents`. Absolute children consume no basis, gap, flex factor, line size, or auto-margin free space.

`YOGA-051` — No-wrap or a not-defined main limit creates one line. Otherwise, before adding a child, compare the line's current outer hypothetical main size plus applicable gap plus the child's clamped outer basis with the finite inner main limit. If it exceeds and the line is nonempty, start a new line. Retain original child order; reverse affects positioning, not line membership.

`YOGA-052` — Resolve flexible lengths independently per line using the CSS Flexbox 9.7 loop:

1. choose grow when hypothetical outer main sum is smaller than container inner main, otherwise shrink;
2. freeze inflexible items;
3. compute remaining free space and scale it when the sum of unfrozen factors is between zero and one;
4. distribute positive space by grow factor or negative space by `shrink * flexBasis`;
5. clamp each target by min/max and zero;
6. sum clamp violations, freeze min violators when positive or max violators when negative, and repeat until stable.

Never perform only one proportional pass: frozen constraint violators require redistribution.

`YOGA-053` — For each line, lay out children at their target main size, measure cross size including margins, and in row baseline layout expand the line to maximum ascent plus maximum descent. A container uses baseline layout only for row direction when `align-items:baseline` or some flow child has `align-self:baseline`. Container baselines recurse through the first eligible child on the first line, falling back to own height.

`YOGA-054` — Determine container size as follows:

- exact mode uses the supplied size;
- not-defined and ordinary at-most modes size to content even when content exceeds the at-most value;
- `overflow:scroll` clamps at-most content to the available bound, never below padding plus border;
- a wrapped, multiline, at-most container fills the main boundary at which it wrapped;
- apply min/max to the resulting physical width and height.

This fit-content compatibility is intentional; hidden overflow does not perform the scroll clamp.

## Positioning and absolute children

`YOGA-060` — Position lines with `align-content`: flex start/center/end, stretch, space-between, space-around, or space-evenly. A single nonwrapped, nonbaseline line takes the full cross size. Wrap-reverse mirrors line positions across the cross dimension.

`YOGA-061` — Within each line, first allocate positive main free space across auto main margins. If none exist, apply justify-content. Then position the cross axis using auto cross margins or resolved align-self/items. Two auto cross margins center, leading auto consumes all positive cross free space, trailing auto stays at the leading placement. Baseline uses the line ascent. Apply relative left/right and top/bottom offsets after normal placement, preferring left over right and top over bottom; these flow-child offsets resolve percentages against owner width.

`YOGA-062` — Main-axis reverse mirrors each item's placement inside the container but retains source order and gap calculation. Column-reverse and row-reverse use their respective physical trailing edge. Negative free space may produce negative center/end offsets; spacing distributions use only nonnegative remainder for between-item expansion.

`YOGA-063` — Lay out absolute children after flow positioning. Percent width/height resolve against the parent's padding box (parent size minus border). If width is absent but both left/right exist, derive width from padding-box width minus insets; do the analogous height derivation. Absolute top/bottom percentages resolve against parent height; left/right against parent width. Margins resolve against parent width.

`YOGA-064` — Position an absolute child from a defined leading inset, otherwise trailing inset, otherwise the parent's justify rule on its main axis and align rule on its cross axis, inside padding plus border. Wrap-reverse flips cross flex-start/flex-end behavior. Main reverse flips the no-inset main fallback. Explicit insets outrank justify/align.

`YOGA-065` — Root position is resolved separately after layout: left equals resolved left margin plus left inset, top equals top margin plus top inset. For compatibility, both root inset percentages use owner width. This seeds absolute coordinates for pixel-grid rounding.

## Rounding and geometry

`YOGA-070` — Round after the complete unrounded tree exists. With point scale zero, skip rounding. Otherwise compute child absolute origins from unrounded ancestors and derive rounded width/height from rounded absolute far edge minus rounded absolute near edge; this prevents accumulated sibling drift.

`YOGA-071` — For a measured/text node, floor left/top. If its scaled width/height has a fractional component, ceil the far edge to avoid clipping; otherwise floor consistently. For nontext nodes, use half-up rounding with threshold `0.4999`. Treat fractions within `0.0001` of zero or one as whole. Divide by point scale after rounding.

`YOGA-072` — Geometry helpers use terminal-cell semantics:

- one edge argument fills all; two mean vertical/horizontal; four mean top/right/bottom/left;
- rectangle union treats rectangles as half-open extents `(x+width, y+height)`;
- rectangle clamp converts to inclusive last cells using `size-1` and returns zero width/height when disjoint;
- a point is in bounds iff `0 <= x < width` and `0 <= y < height`;
- scalar clamp applies only provided minimum/maximum bounds.

## Explicit compatibility gaps

`YOGA-080` — The compatibility API stores configuration `errata` and `useWebDefaults` but the specified algorithm does not broadly change behavior from those values. Experimental feature queries always return disabled and setting them is a no-op. Do not claim those switches implement upstream semantics.

`YOGA-081` — Content-box sizing, aspect ratio, always-form-containing-block, style copying, and dirtied callbacks are parity stubs. Aspect-ratio lookup returns the not-defined/NaN sentinel. New-layout lookup always reports true and marking layout seen is a no-op. These are explicit unsupported states, not partially working features.

`YOGA-082` — The underlying engine supports display contents and static enum values, but the terminal adapter maps any exposed nonabsolute position to relative and any exposed nonflex display to none. Align-content, baseline, and space-distribution align values are not in the terminal adapter's public style vocabulary even though internal tests/engine paths may exercise them.

`YOGA-083` — Keep the [general terminal pipeline](../assets/architecture.drawio) and [specialized flex-layout state machine](../assets/flex-layout.drawio) consistent. The latter is normative for invalidation, cache eligibility, freeze-loop recurrence, and rounding order.

## Acceptance scenarios

- **YOGA-A01 — Root adapter.** Call terminal layout with width 80 and height 5;
  verify exact width and undefined owner height because the adapter ignores 5.
- **YOGA-A02 — Edge precedence.** Set left, horizontal, all and start edges to
  different values; remove each winner and verify the documented cascade.
- **YOGA-A03 — Percentage bases.** Give top padding 10 percent; verify it uses
  owner width while explicit height percentage uses owner height.
- **YOGA-A04 — Measurement dirtiness.** Insert a measured leaf into a clean
  tree, run basis measurement then layout, and verify measurement does not clear
  dirtiness or allow stale positioned cache output.
- **YOGA-A05 — Display hiding.** Hide and show a subtree; verify zeroed hidden
  descendants and complete recomputation after unhide.
- **YOGA-A06 — Freeze redistribution.** Create a grow line with one max-width
  violator; verify freeze and redistribution among unfrozen siblings.
- **YOGA-A07 — Multiline wrap.** Wrap three children at finite at-most main
  size; verify breaks include margins/gaps and the container fills the boundary.
- **YOGA-A08 — Overflow modes.** Compare visible, hidden and scroll with
  oversized content under at-most sizing; only scroll clamps to the bound.
- **YOGA-A09 — Absolute fallback.** Place one absolute child with left/right
  and no width and another with no insets; verify derived padding-box width and
  justify/align fallback respectively.
- **YOGA-A10 — Pixel rounding.** Round fractional measured and ordinary boxes
  at scale one; verify text-edge floor/ceil, the `0.4999` half-up threshold and
  absolute-edge width calculation.
- **YOGA-A11 — Explicit unsupported API.** Toggle content-box, aspect ratio,
  experimental features and layout-seen calls; verify explicit unsupported
  behavior rather than false upstream compatibility.

## Non-normative provenance

Evidence was specified from the terminal layout adapter, its node/geometry interface, and the synchronous flex-layout compatibility engine. The source module name, enum integer values, typed-array cache representation, and implementation language are not implementation requirements.
