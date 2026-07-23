# Readable Identifier Generation and Canonical Lexicon

## Contents

1. [Purpose and ownership](#purpose-and-ownership)
2. [Random selection protocol](#random-selection-protocol)
3. [Identifier formats](#identifier-formats)
4. [Canonical adjective lexicon](#canonical-adjective-lexicon)
5. [Canonical noun lexicon](#canonical-noun-lexicon)
6. [Canonical verb lexicon](#canonical-verb-lexicon)
7. [Acceptance scenarios](#acceptance-scenarios)
8. [Provenance](#provenance)

## Purpose and ownership

Readable slugs are observable identifiers for plans, generated team names, and default remote-control titles. They are not security tokens and do not replace UUIDs or durable task/session identifiers. Preserve their exact token tables because duplicate entries affect selection probability and deterministic random-source tests.

**SC-ID-001 — Canonical source.** The three ordered token tables below are normative data. Read tokens left-to-right and top-to-bottom, separated by ASCII whitespace. Preserve spelling, order, and duplicate positions. Counts are 219 adjective positions, 409 noun positions, and 109 verb positions.

**SC-ID-002 — Caller ownership.** The generator returns a candidate only. A plan-name owner may test filesystem/session collisions and retry; team and bridge callers may accept the first candidate. The generator itself keeps no registry, performs no collision check, and persists nothing.

## Random selection protocol

**SC-ID-010 — Entropy draw.** For every token selection, request exactly four bytes from a cryptographically secure operating-system random source. Interpret them as one unsigned 32-bit integer in big-endian order.

**SC-ID-011 — Index mapping.** Select position `integer mod table_length`. Preserve this modulo mapping, including its tiny modulo bias; do not substitute floating-point random selection or rejection sampling when deterministic parity is required.

**SC-ID-012 — Independent order.** Each component consumes a new four-byte draw. The full slug draws adjective, then verb, then noun. The short slug draws adjective, then noun. An entropy-source failure propagates and produces no partial slug.

**SC-ID-013 — Table precondition.** A selectable table is nonempty. The canonical tables satisfy this. There is no fallback word, seed, user locale, case transformation, or profanity filter.

## Identifier formats

**SC-ID-020 — Full slug.** Return the chosen adjective, verb, and noun joined with two literal ASCII hyphens: `adjective-verb-noun`. Tokens are already lowercase ASCII and contain no spaces or hyphens.

**SC-ID-021 — Short slug.** Return the chosen adjective and noun joined with one literal ASCII hyphen: `adjective-noun`.

**SC-ID-022 — Duplicate weighting.** Duplicate token positions are intentional observable weight. For example, choosing either occurrence of a repeated token yields the same visible word but occupies a distinct modulo result. Do not deduplicate any table.

## Canonical adjective lexicon

```text
abundant ancient bright calm cheerful clever cozy curious dapper dazzling
deep delightful eager elegant enchanted fancy fluffy gentle gleaming golden
graceful happy hidden humble jolly joyful keen kind lively lovely
lucky luminous magical majestic mellow merry mighty misty noble peaceful
playful polished precious proud quiet quirky radiant rosy serene shiny
silly sleepy smooth snazzy snug snuggly soft sparkling spicy splendid
sprightly starry steady sunny swift tender tidy toasty tranquil twinkly
valiant vast velvet vivid warm whimsical wild wise witty wondrous
zany zesty zippy breezy bubbly buzzing cheeky cosmic cozy crispy
crystalline cuddly drifting dreamy effervescent ethereal fizzy flickering floating floofy
fluttering foamy frolicking fuzzy giggly glimmering glistening glittery glowing goofy
groovy harmonic hazy humming iridescent jaunty jazzy jiggly melodic moonlit
mossy nifty peppy prancy purrfect purring quizzical rippling rustling shimmering
shimmying snappy snoopy squishy swirling ticklish tingly twinkling velvety wiggly
wobbly woolly zazzy abstract adaptive agile async atomic binary cached
compiled composed compressed concurrent cryptic curried declarative delegated distributed dynamic
eager elegant encapsulated enumerated eventual expressive federated functional generic greedy
hashed idempotent immutable imperative indexed inherited iterative lazy lexical linear
linked logical memoized modular mutable nested optimized parallel parsed partitioned
piped polymorphic pure reactive recursive refactored reflective replicated resilient robust
scalable sequential serialized sharded sorted staged stateful stateless streamed structured
synchronous synthetic temporal transient typed unified validated vectorized virtual
```

## Canonical noun lexicon

```text
aurora avalanche blossom breeze brook bubble canyon cascade cloud clover
comet coral cosmos creek crescent crystal dawn dewdrop dusk eclipse
ember feather fern firefly flame flurry fog forest frost galaxy
garden glacier glade grove harbor horizon island lagoon lake leaf
lightning meadow meteor mist moon moonbeam mountain nebula nova ocean
orbit pebble petal pine planet pond puddle quasar rain rainbow
reef ripple river shore sky snowflake spark spring star stardust
starlight storm stream summit sun sunbeam sunrise sunset thunder tide
twilight valley volcano waterfall wave willow wind alpaca axolotl badger
bear beaver bee bird bumblebee bunny cat chipmunk crab crane
deer dolphin dove dragon dragonfly duckling eagle elephant falcon finch
flamingo fox frog giraffe goose hamster hare hedgehog hippo hummingbird
jellyfish kitten koala ladybug lark lemur llama lobster lynx manatee
meerkat moth narwhal newt octopus otter owl panda parrot peacock
pelican penguin phoenix piglet platypus pony porcupine puffin puppy quail
quokka rabbit raccoon raven robin salamander seahorse seal sloth snail
sparrow sphinx squid squirrel starfish swan tiger toucan turtle unicorn
walrus whale wolf wombat wren yeti zebra acorn anchor balloon
beacon biscuit blanket bonbon book boot cake candle candy castle
charm clock cocoa cookie crayon crown cupcake donut dream fairy
fiddle flask flute fountain gadget gem gizmo globe goblet hammock
harp haven hearth honey journal kazoo kettle key kite lantern
lemon lighthouse locket lollipop mango map marble marshmallow melody mitten
mochi muffin music nest noodle oasis origami pancake parasol peach
pearl pebble pie pillow pinwheel pixel pizza plum popcorn pretzel
prism pudding pumpkin puzzle quiche quill quilt riddle rocket rose
scone scroll shell sketch snowglobe sonnet sparkle spindle sprout sundae
swing taco teacup teapot thimble toast token tome tower treasure
treehouse trinket truffle tulip umbrella waffle wand whisper whistle widget
wreath zephyr abelson adleman aho allen babbage bachman backus barto
bengio bentley blum boole brooks catmull cerf cherny church clarke
cocke codd conway cook corbato cray curry dahl diffie dijkstra
dongarra eich emerson engelbart feigenbaum floyd gosling graham gray hamming
hanrahan hartmanis hejlsberg hellman hennessy hickey hinton hoare hollerith hopcroft
hopper iverson kahan kahn karp kay kernighan knuth kurzweil lamport
lampson lecun lerdorf liskov lovelace matsumoto mccarthy metcalfe micali milner
minsky moler moore naur neumann newell nygaard papert parnas pascal
patterson pearl perlis pike pnueli rabin reddy ritchie rivest rossum
russell scott sedgewick shamir shannon sifakis simon stallman stearns steele
stonebraker stroustrup sutherland sutton tarjan thacker thompson torvalds turing ullman
valiant wadler wall wigderson wilkes wilkinson wirth wozniak yao
```

## Canonical verb lexicon

```text
baking beaming booping bouncing brewing bubbling chasing churning coalescing conjuring
cooking crafting crunching cuddling dancing dazzling discovering doodling dreaming drifting
enchanting exploring finding floating fluttering foraging forging frolicking gathering giggling
gliding greeting growing hatching herding honking hopping hugging humming imagining
inventing jingling juggling jumping kindling knitting launching leaping mapping marinating
meandering mixing moseying munching napping nibbling noodling orbiting painting percolating
petting plotting pondering popping prancing purring puzzling questing riding roaming
rolling sauteeing scribbling seeking shimmying singing skipping sleeping snacking sniffing
snuggling soaring sparking spinning splashing sprouting squishing stargazing stirring strolling
swimming swinging tickling tinkering toasting tumbling twirling waddling wandering watching
weaving whistling wibbling wiggling wishing wobbling wondering yawning zooming
```

## Acceptance scenarios

### `SC-ID-A01` — Deterministic full slug

Inject three four-byte draws representing unsigned values `a`, `v`, and `n`. The result is adjective[`a mod 219`], verb[`v mod 109`], and noun[`n mod 409`] in that order with literal hyphens. Exactly twelve entropy bytes are consumed.

### `SC-ID-A02` — Deterministic short slug

Inject two four-byte draws. The result uses adjective then noun, consumes exactly eight bytes, and never consults the verb table.

### `SC-ID-A03` — Duplicate probability

Find two positions containing the same adjective and drive the random source to each index. Both yield the same visible component and both remain valid distinct outcomes. An implementation that deduplicates the table fails parity.

### `SC-ID-A04` — Entropy failure

The second draw of a full slug fails. The operation propagates the entropy failure, returns no slug, performs no persistence, and leaves collision policy to the caller.

## Provenance

The canonical lexicon and selection protocol are implemented behavioral data. Their source declaration syntax is irrelevant; store the ordered tokens in any immutable representation that preserves indices and duplicates.

