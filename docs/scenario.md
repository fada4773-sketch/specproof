# Scenario mode: integration test from a tree, and regression check

The scenario mode tests an API with a generated **scenario**: the requests
of a test, in the order they run, with as many records and calls per
endpoint as the test needs. Two checks use it:

| | Data from | Runs against | Writes | Question |
|---|---|---|---|---|
| **Integration test** (`apitest-gen tree`) | connected records of a running instance, fetched with GET only, as `tree.yaml` says | an empty instance (a container the pipeline starts) | only there | does the API store, read, list, change and delete these records as the spec says? |
| **Regression check** (`apitest-gen regression`) | real records of a stage, drawn anew for every deployment | the stage | **never**, GET only | did the deployment change what the stage answers? |

The integration test rebuilds real, connected data: you say which records
to fetch (five docks of two planets, the ships of each dock, the pilot of
each ship) and how they hang together; apitest-gen fetches them from a
stage and writes the steps that create the same records in an empty
instance, read and list them, change and delete them. `order` at the top
of `tree.yaml` is the plan of the test: the order of the creates and where
values of their bodies come from. The first run proposes it, apitest-gen
checks every change you make, and from then on every step follows it.
The answers of a run are kept next to the steps (a **golden snapshot** in
the test file), and the examples of the spec are taken from a passed run.

apitest starts nothing: every command works against the instance under
`-base-url`. In `go test`, `apitest.RunScenario` takes a `Handler` instead.

- [Files](#files)
- [Integration test](#integration-test)
  - [Workflow](#workflow)
  - [The tree file](#the-tree-file)
  - [Order: the plan of the test](#order-the-plan-of-the-test)
  - [What the proposal holds](#what-the-proposal-holds)
  - [Later runs: your file stays](#later-runs-your-file-stays)
  - [Fetching](#fetching)
  - [The scenario](#the-scenario)
  - [tree run](#tree-run)
  - [In go test: apitest.RunScenario](#in-go-test-apitestrunscenario)
- [Regression check](#regression-check)
  - [The sample file](#the-sample-file)
  - [regression pre](#regression-pre)
  - [regression post](#regression-post)
  - [Findings](#findings)
- [Tokens](#tokens)
- [In a pipeline](#in-a-pipeline)
- [Command reference](#command-reference)
- [Limits](#limits)

## Files

Files with `_gen` in the name are written by a tool on every run and never
edited by hand; you review them as a git diff. Files without `_gen` are
yours; a tool may propose a first version, then it keeps what you wrote.

```
starport/
├── api/openapi.yaml                  # the spec; "tree run -update" writes its examples
└── apitest/
    ├── tree/
    │   ├── tree.yaml                 # yours: which records, how they hang together
    │   ├── tree-data_gen.yaml        # generated: the records fetched (data of the stage)
    │   ├── tree-test_gen.yaml        # generated: the steps that rebuild them, and the answers of "tree run -update"
    │   ├── tree-review_gen.md        # generated: next command, plan, a section per step, one line per request and step
    │   ├── tree-review_gen.html      # generated: every request and step with its bodies, in the order it ran; the fetch as a tree
    │   ├── tree_test.go              # yours, optional: the same test in go test
    │   └── out/                      # reports, not committed
    └── regression/
        ├── regression-sample.yaml    # proposed by "regression init", then yours
        └── out/                      # snapshots and reports, not committed
```

| File | Written by | Read by | Commit |
|---|---|---|---|
| `tree.yaml` | `tree` (first proposal, later only additions), you | `tree` | yes |
| `tree-data_gen.yaml` | `tree -base-url` | `tree` (to build again without fetching) | only if the data of that instance may be in git |
| `tree-test_gen.yaml` | `tree` (the steps; the answers stay), `tree run -update` (the answers) | `tree run`, `RunScenario` | yes |
| `tree-review_gen.md` | every step: `tree`, `tree -base-url`, `tree run`, `tree run -update` | you | yes |
| `tree-review_gen.html` | the same steps | you (a browser); the next step reads its timelines back | like `tree-data_gen.yaml`: it holds the answers of the stage |
| `regression-sample.yaml` | `regression init`, then you | `regression pre` | yes |
| `regression/out/*` | `regression pre`, `post`, `diff` | `regression post`, the pipeline | no: real data of a stage |

The scenario holds the values of the fetched records (names, numbers), so
fetch from an instance whose data may be in git, or check the diff.

**The review.** `tree-review_gen.md` grows with the workflow: each step
writes its own section and keeps the others. On top the next command, then
the **plan** (the order with the sources of the bodies, and the tree with
the records of the last fetch, the writes of each node and where its body
comes from), then a status table (open, done, failed, outdated). The
sections follow folded (`<details>`); a failed or outdated one is open.
Each section ends with its **timeline**, one line per request or step in
the order it ran, without bodies: the fetch the requests that failed (and
how many it sent), the test its steps in the order they will run with
their checks, decisions and gaps, the run every step with status, time
and result (a step that did not pass marked ✗), `-update` the steps whose
answers became examples, in the order they ran.

**The page.** `tree-review_gen.html`, next to it, is one self-contained
file with every line of every step, in the same order: a click on a line
opens the body sent and the answer as a tree in which every object and
list folds (built when opened, long lists 100 at a time), so a fetch of
thousands of requests stays fast. On load everything is closed; search
(step, path, result) and "only failed" narrow the lines. Headers are not
listed, tokens and secret fields are masked. Each run writes the page
anew and keeps the timelines of the steps it did not run (it reads them
back from the page). The button "Mismatches (n)" on top gathers the
steps of every timeline whose answer does not hold what it should, by
kind: data mismatch (the answer lacks what a step sent, `check: {equals:
…}`), schema violation, difference from the golden snapshot. Each shows
its message, the differences (pointer, expected, actual) or the schema
violations, and as raw JSON the body sent, the answer and, for a data
mismatch, the body of the step it was compared with.

**The fetch as a tree.** Above the requests of the fetch the page shows
the fetch as it ran, folded per node and record above it (`docks under
planets2`): the values of path, query, header and body as `tree.yaml`
writes them (`.code`, `planets.code`, `$moonId`) and as they were sent
(a value that could not be read with its reason), the conditions of the
records (the node's `where` and `any`, the `where` and `prefer` of the
file), `pick` and how the candidates were ordered, every request with
status (one taken from the cache marked; a click opens body and answer),
the notes of the fetch about it, and every candidate: its key, the
record, and its outcome — picked (with the record id and the nodes below
it), left out by `where` (the clause, the value it was tested with and
the value the record has: `where capacity: >=5; the record has 4`), by
`require` (each require with its values, requests and why it did not
hold), dropped as the source of `order`, or not needed once `pick` had
its records. A list lists up to 200 candidates. On top, open on load,
the nodes that took nothing or fewer than `pick` asks for; search narrows
the tree too.

| Section | Written by | Holds |
|---|---|---|
| 1. Tree | `tree` (every run) | scope, the nodes with pick, where, require and writes, the params without value and who uses them, `ignore`, the changes of this run, what to check before the fetch; the problems when tree.yaml is wrong; with `-dry-run` the requests the fetch would send |
| 2. Fetch | `tree -base-url` | the records per node with their keys, the notes of the fetch, records of nodes the tree no longer has, the requests that failed in the order sent |
| 3. Test | `tree` with records | the steps by kind, the seed the empty instance must hold, decisions and gaps outside the steps; the problems when the records do not fit `order`; every step in the order it will run, with its decisions and gaps |
| 4. Run | `tree run` | the verdict, the counts, the steps that did not pass in the order they ran; every step in that order |
| 5. Update | `tree run -update` | the answers and the examples written, or why not; the steps that gave the examples, in the order they ran |

A step that changes what a later section was built from marks that section
**outdated**: a tree proposed anew or merged with changes marks fetch,
scenario, run and update; a scenario that differs from the one before marks
run and update; records of nodes the tree no longer has mark the fetch.
The mark goes when that step runs again.

## Integration test

### Workflow

```sh
# 1. propose tree.yaml (tree and order) from the spec; -dry-run shows the plan and what a fetch would send
apitest-gen tree -spec api/openapi.yaml -file apitest/tree/tree.yaml -dry-run

# 2. edit tree.yaml: order, which records (pick, where, require), how they hang together;
#    check it again with -dry-run: a wrong order stops with the line and the reason

# 3. fetch the records from a running instance (GET only) and build the test
apitest-gen tree -spec api/openapi.yaml -file apitest/tree/tree.yaml -base-url https://starport.dev.example.com/api -token-env STARPORT_DEV_TOKEN

# 4. run it against an empty instance; -update writes the answers into the test and the examples into the spec
apitest-gen tree run -spec api/openapi.yaml -file apitest/tree/tree.yaml -base-url http://localhost:8080/api -token-env STARPORT_TOKEN -update

# in CI: the same without -update compares with the answers
apitest-gen tree run -spec api/openapi.yaml -file apitest/tree/tree.yaml -base-url http://localhost:8080/api -token-env STARPORT_TOKEN -junit out/tree.xml
```

**Step by step.** For a large spec, start with a few tags and add more
later; the scope is kept in `tree.yaml`, each run adds the endpoints of the
new tags at their place:

```sh
apitest-gen tree -spec api/openapi.yaml -file apitest/tree/tree.yaml -tags Planet,Moon,Garden -exclude-tags Admin
apitest-gen tree -spec api/openapi.yaml -file apitest/tree/tree.yaml -tags Person          # adds Person
apitest-gen tree -spec api/openapi.yaml -file apitest/tree/tree.yaml -base-url … -node beds  # fetch only beds again
```

```yaml
scope:
  tags: [Planet, Moon, Garden, Person]
  exclude:
    tags: [Admin, Internal*]           # * matches any text
    ops: [purgeAll, "DELETE /cache"]   # operationIds or METHOD /path
  deps: false                          # true: also the reads of other tags that give needed values
```

A value of another tag that is not in the scope yet becomes `$name` under
`params`. When that tag comes in later, the run replaces `$name` by the
node that gives it (`$name` → `persons.name`) and removes the entry, as
long as you did not set a value. `-with-deps` (`deps: true`) takes the
lists of such tags in at once. `-node <name>` fetches one node and what
hangs below it again and keeps the other records of the fetch before.
A read of the record another one names (the person of a garden) hangs
below that record, also when the file reads the same request elsewhere;
a merge adds it there, under a free name. An exclusion added later
(`-exclude-tags`, `-exclude-ops`) is checked against the whole file: what
it excludes is named, and `-fix` sets it off (see the checks of
[order](#order-the-plan-of-the-test)).

`-dry-run` sends nothing, also with `-base-url`, and writes neither data
nor test: it proposes or merges `tree.yaml` as usual (so you can edit it),
checks it, prints the plan and the requests a fetch would send (templates),
and, with records fetched before, the steps of the test; `tree run
-dry-run` lists the steps of the test.

Step 3 without `-base-url` builds the test again from
`tree-data_gen.yaml`, for a change of the write blocks that needs no new
data. After a change of the spec, run step 1 (or 3) again: `tree.yaml`
gets the new endpoints at their place and keeps everything else.

### The tree file

A node is a request and the records it answers with. The records are
fetched, filtered and picked; every child node (`each`) runs once per
picked record. The write block says how the integration test creates,
changes and deletes a record of the node in an empty instance.

```yaml
order:           # the plan: creates top down, deletes bottom up
  - pilots
  - docks
  - ships: {pilotId: shipPilot.id}           # the body's pilotId <- the pilot read below the ship
params:          # values no record of the tree gives: set them (a value of the stage)
  reportNo: R-17 # getReport
  season:        # getHarvest; not set yet: its node is left out
seed: 1          # random picks repeat with the same seed
budget: 2000     # a fetch sends at most this many requests
tree:
  planets:
    call: GET /planets                       # listPlanets
    pick: [mars, venus]                      # exactly these (by the first key)
    key: [/code]
    write:
      create: none                           # no create: the instance holds planets (seed)
    each:
      docks:
        call: GET /planets/{code}/docks      # listDocks
        path: {code: .code}                  # .field: the planet this runs under
        page: {param: page, size: {pageSize: 50}, max: 3}
        where:
          capacity: ">=5"
          status: [OPEN, RESERVED]           # one of them
          any: {name: present, label: present}
        require:                             # each must answer 2xx for a candidate
          ships: GET /docks/{dockId}/ships dockId=.id
        candidates: 20                       # check require for at most 20 candidates
        pick: 5                              # 5 at random (seeded); {first: 5}; all
        key: [/id]
        save: {etag: header ETag}
        write:
          create: POST /planets/{code}/docks   # createDock; code=planets.code follows from the node
          update:
            call: PUT /docks/{dockId}        # updateDock; dockId=.id: the key of the dock
            change: {capacity: 9}            # or auto: one field changed
          patch: PATCH /docks/{dockId}       # patchDock
          delete: DELETE /docks/{dockId}     # deleteDock
        each:
          dock: GET /docks/{dockId} dockId=.id                      # getDock
          ships:
            call: GET /docks/{dockId}/ships  # listShips
            path: {dockId: .id}
            pick: all
            key: [/id]
            write:
              create: POST /docks/{dockId}/ships
              delete: DELETE /ships/{shipId}
            each:
              ship: GET /ships/{shipId} shipId=.id
              log: GET /ships/{shipId}/log shipId=.id
              shipPilot: GET /pilots/{pilotId} pilotId=.pilotId     # the pilot a ship names
  pilots:
    call: GET /pilots
    pick: 3
    key: [/id]
    write:
      create: POST /pilots
    each:
      flown:
        call: POST /ships/search            # a POST that only reads
        read: true
        body: {pilotId: .id}
  report: GET /reports/{reportNo} reportNo=$reportNo                # getReport
  harvest:
    call: GET /harvest                      # getHarvest
    query: {season: $season}                # a required query parameter without example
ignore:                                     # never proposed again
  - GET /health
  - POST /maintenance/run                   # runMaintenance · no node reads the records it writes
```

**Short form.** A node or write that is only a call with path values is
one line: `GET /docks/{dockId} dockId=.id`. Use the long form for anything
else. A write leaves out what follows from its node: a path value the node
reads with (`code=.code` of the planet above becomes `code=planets.code`),
the key of the record (`dockId` at the docks with key `/id`: `.id`), and
`body: from <the node itself>`; write a value only where it differs. In a one-line mapping (`{call: …}`) a path with `{…}` is no valid
YAML; write such a mapping as a block.

**Values.** `.field` reads the record the node runs under; in a write
block it reads the record itself. `<node>.field` reads the record of a
node above (`planets.code`), or, for a node that is not above, its first
record: that node must be fetched before (a root further up, or earlier in
the tree), so a read can take values across tags
(`permit: GET /docks/{dockId}/pilots/{pilotId}/permit dockId=.id pilotId=pilots.id`). A field is a dot path (`route.originId`,
`items.0.id`). `$name` is the value of `params` at the top of the file:
for a value no record gives. A value of `params` may itself be a
reference (`bookId: books.id`): it reads as if it stood in place of
`$name`, so the node it names must be above or fetched before; the name is
the node's name in the tree, not the resource name. A node whose `$name` is not set yet is left
out of the fetch (noted once, and listed in the review). A text with
`{{…}}` composes references, in a node, a write and in `params`:
`berthKey: '{{planets.code}}-{{docks.number}}'` sends `mars-7` (quote it,
YAML reads a value that starts with `{` as a mapping); in the scenario a
part that is the key of a created record becomes its placeholder inside the
text. A reference alone (`dockId: '{{.id}}'`) keeps its type. A `{{name}}`
that is no reference stays as it is. Every other value
is sent as it is. A list as a query value
(`status: [OPEN, CLOSED]`) sends one request per value.

**Where for the whole file.** A `where` at the top of `tree.yaml` (next
to `scope` and `params`) holds for every record of a resource, at every
node that reads one, on every level: the lists, the searches, the reads
of one record, the records a field names (the person of a garden) and the
`require` of other nodes (persons that require gardens count only the
gardens that hold it). A record whose create takes a value of a record
the `where` leaves out goes too, the next candidate is taken (with
`Person.name: [Ada]` and `- gardens: {personId: gardenPerson.id}` a
garden of another person cannot be rebuilt; the review says "1 without a
record of gardenPerson"). The key is `Resource.field`, or a mapping per
resource; the tests are those of a node's `where`:

```yaml
where:
  Garden.name: [Rose, Mint]   # only these gardens, wherever they are read
  Garden:
    code: [rose, mint]
    size: ">=2"
```

A node's records are of a resource when its answer (the item of a list)
is the main schema of that resource or the schema of that name; views of
a record (`GET /gardens/{gardenId}/details`) are not. Resource and field
are checked against the spec; a condition no node of the tree applies is
noted after the fetch.

Without a match a `where` takes nothing: `pick: 3` under a moon with one
such garden gives that one garden (the review says `1 of 3 picked …
left out by where: status: OPEN ×4 (e.g. 12 has "CLOSED")`: per
clause how many it left out, with the value of the first). The same note
comes for `pick: all` and for a read of one record when nothing passes.

**Prefer.** `prefer` at the top, in the same form, does not filter: a
pick takes the records that hold it first and fills the rest with others
(at random by `seed`, or the first). The nodes above come along: a planet
and a moon below which a preferred garden is are picked before the
others (up to three levels down; a candidate that leads to more prefers
first).

```yaml
prefer:
  Garden.name: [mygarden]   # gardens pick: 3 → mygarden and 2 more
```

`where` and `prefer` combine: the where filters, the prefer orders what
is left. A pick of keys (`pick: [..]`) and `pick: all` take what they
name. A prefer no fetched record holds is noted.

| Key of a node | Meaning |
|---|---|
| `call` | `METHOD /path` or the operationId; a node only reads: GET, or POST with `read: true` |
| `path`, `query`, `header` | parameter values |
| `body`, `read` | the body of a POST that only reads (`read: true`) |
| `items` | JSON pointer of the list in the answer (`/content`; `""` for the answer itself); by default the schema decides: an array, or a page with one list of objects |
| `page` | `param` (page number or offset), `size: {name: n}`, `max` pages (default 1), `start`, `step`, `next` (JSON pointer of a cursor in the answer), `in: body` (with `param` a JSON pointer into the body) |
| `pick` | a number (at random, seeded), `{first: n}`, `all`, or a list of keys (by the first key) |
| `min` | in `require`: the answer must list at least this many elements (that hold its `where`) |
| `where` | conditions on fields, all must hold: `present` (there and not empty: not null, `""`, `[]` or `{}`), `absent` (missing, null or empty), a value, a list (one of), `>=`, `<=`, `>`, `<`, `!=` (numbers, else text: ISO dates compare); `any:` holds alternatives. A value may be a reference: `id: docks.pilotId` (or `.pilotId`) holds for the record the dock names, a reference to a list for one of its values |
| `require` | named requests per candidate (`.field` is the candidate); each must answer 2xx and pass its own `where` |
| `candidates` | how many candidates `require` checks at most |
| `key` | JSON pointers that identify a record (default `/id`); records are matched by them |
| `save` | values under a name: `/pointer`, or `header <Name>` |
| `fields` | where a field of the bodies built from the record comes from when the record holds it elsewhere: `pilotId: .pilotDetails.pilotId` (also `<node>.field`, `$name` or a value); over the record, `order` and the search for a reference (see Bodies) |
| `write` | `create` (or `none`), `update`, `patch`, `delete`, their variants by another key (`update-by-name: PUT /gardens/name/{name} name=.name`, `delete-by-code`, `patch-by-…`), and actions under any other name (`open: POST /gardens/{gardenId}/open gardenId=.id`) |
| `each` | the child nodes |
| `off` | the node and its children are not fetched |

| Key of a write | Meaning |
|---|---|
| `call`, `path`, `query`, `header` | the request; in the short form `POST /path p=value` |
| `body` | a body sent as it is (values may be references), or `from <node>`: build it from the record of that node (above or below, or the node itself) first; the fields it lacks come from the record and the reads about it. Without `body` the same holds for the record itself |
| `change` | update and patch: `{field: value}`, or `auto` (default) for one changed field |
| `expect` | the status; default the lowest documented 2xx |
| `save` | values of the answer under a name, as in a node: `/pointer` or `header <Name>`. A `$name` of the tree with that name reads the saved value in the scenario (see below) |
| `onSeed` | `true`: an update, patch or action also runs on a record of the seed (a node without create: the instance holds the record), on the first record of the fetch |
| `off` | not used |

**A value of a write, read later.** A value the instance gives only when it
changes something (the receipt number an update answers with) is saved by that
write, and read with `$name`:

```yaml
params:
  receiptNo: R-1042           # a receipt number of the stage: the fetch reads it
tree:
  docks:
    call: GET /planets/{code}/docks
    path: {code: .code}
    write:
      create: none
      update:
        call: PUT /docks/{dockId}
        path: {dockId: .id}
        save: {receiptNo: header X-Receipt-No}
        onSeed: true        # the docks are seed: change one of them
  receipt: GET /receipts/{receiptNo} receiptNo=$receiptNo
```

The fetch sends no write: it reads the receipt with the value of `params`.
The scenario sends `{{receiptNo}}` and runs the read right after the first
step that saves it (the review says so). When no write that saves it runs,
the value of `params` stands in.

A node without a write block of its own creates like another node of the
same resource (the pilot below a ship like the list of pilots), as long as
that create names no node the first one lacks above it. `create: none` says
the API cannot create the records.

Node names are unique in the file: they name the records (`docks2`) and
the references (`docks.id`).

### Order: the plan of the test

`order` at the top of `tree.yaml` names every node that creates records
(a write block with `create`), in the order the test creates them; the
deletes run the other way round, the updates in this order too. Nodes
without a create of their own (reads only, `create: none`, a node that
creates like another node of the same resource) have no line of their
own, but their steps can be placed (below). A line
may name where values of the create's body come from: `field: node.key`,
a node below the created record (the pilot read below a ship) or above
it; a list field takes the key of every record of that node (`docks:
dockList.code`). A field inside an object of the body is named by its
path (`crew.pilotId`): the proposal, the check and `-fix` find such
references as they find those on top (also the key inside an object
named after a resource, `berth.dock.id`), and the create sets the value
in that object, the other fields of it as the record has them. Fields
inside lists of objects are not followed.

```yaml
order:
  - pilots
  - docks
  - ships: {pilotId: shipPilot.id}
  # the pilot inside an object of the body:
  # - ships: {crew.pilotId: shipPilot.id}
```

**Steps besides the creates.** A line `node.step` places a step of a
node: `read` (a node that reads one record), `list` (a node that reads a
list), `update`, `patch`, a variant (`update-by-code`) or an action
(`open`). The steps of the lines run in the order of `order`; the steps
no line names run after them, in their usual place (reads and lists,
then updates, 401 checks, deletes bottom up). Deletes always run at the
end.

```yaml
order:
  - pilots
  - docks
  - docks.update        # before the ships are created
  - ships: {pilotId: shipPilot.id}
  - shipPilot.read      # right after the ships
  - docks.list
```

A read placed after an update or patch of its record checks what the
change sent.

**Proposed once.** The first run writes `order` from what the creates
need (the values their paths take by name, the records their fields refer
to) and every source it finds; nothing is decided out of sight later. A
file without `order` gets one on the next run. A create that comes in
later (the spec grew) gets a line after the creates it takes values of,
marked `# new: check its place`, and the run says so.

**Checked on every run**, before anything is sent (also with
`-dry-run`), with the line:

- every node of `order` is a node of the tree with a create, once;
- every node with a create has a line;
- a create comes after the creates of the records it takes values of
  (`order: ships comes before docks, but the create of ships takes a value
  of docks; move docks above ships`);
- every source is a node below or above the created one, its records
  have the field, and the body of the create has the field;
- a step line names a step the node has, and comes after the creates of
  its records and of the records above them (`order: docks.update comes
  before docks, but it needs the records docks creates; move docks above
  docks.update`);
- a body field of a create that holds the key of a record the test
  creates (the `pilotId` of a ship, when the test creates pilots) has a
  source: else the create would send the key of the stage, which the
  record has not in the empty instance (`order: ships.pilotId holds the id
  of a pilot the test creates, but order gives it no source`). The fix
  names the node below that reads the pilot by `.pilotId`, or a node above
  of that resource; without one it adds the read below the ships
  (`pilot: GET /pilots/{pilotId} pilotId=.pilotId`) and names it, then
  fetch again with `-node ships`. The `why:` line names the create that
  sends the field (with its line) and the nodes that create the records it
  names. When the spec does not require the field, the problem also names
  the other way: a `body` of the create without it (sent as written); then
  the read below (`shipPilot`) can be `off: true` and does not come back;
- a source of such a field reads records of that resource (`pilotId:
  shipDock.id` is named, the fix takes the pilot below);
- a node whose records the test creates has their `id` in `key` when the
  create does not require it: the instance gives the id, an empty
  instance another one than the stage (456 there, 1 here). The fix adds
  it (`key: [/slot] → key: [/slot, /id]`), the proposal has it. The
  create then leaves the id out of its body and saves the new one: from
  the answer (also spelled `Id`), from the `Location` header, else by a
  step `<record>.find` right after it that reads the node's request and
  takes the element whose other keys the create sent
  (`/[slot={{bookings1.slot}}]/id`); later steps (`.id` below the record,
  its updates and deletes) take the saved one;
- a path value that is a value of the stage (`shipId=7`, or `$shipId`
  with a value in params) where the path takes the key of a record the
  test creates: the fix names the record itself (`.id` in a write) or the
  node above of that resource. A `$name` a write saves is no value of the
  stage;
- a line of a node the tree no longer has, and a source that names one
  (`pilotId: crew.id`): the fix removes them, the check then names the
  new source;
- a source on a node that is not fetched (off, or excluded by scope): the
  fix takes a node that is fetched, or removes it when the field names no
  record the test creates.

The reads `-fix` adds respect the scope: the read by the key when the
scope allows it, else the list of the resource with a where on the key
(`pilot: GET /pilots` with `where: {id: ships.pilotId}`). When the scope
allows no read of these records at all, the test cannot take the one a
record names: the fix sets `off: true` at their create (the instance must
hold them, as seed), or you take a read of them out of `scope.exclude`.

**Checked against the scope.** `exclude` (tags and operations) holds for
the whole file, also for nodes written before the exclusion (a merge
leaves the file's nodes alone): a node whose request is excluded, a
write and a require are named, and the fix sets `off: true` with the
reason as comment (a require is removed). A `where` the generator added
for a node that reads the field (`pilotId: present # shipPilot reads it`)
goes when that node is gone or not fetched: it would only leave records
out. Tags only narrow what is proposed; a read of another tag in the
file (deps, or added by hand) is no problem.

Most problems end with `(-fix does it)`: `apitest-gen tree … -fix` makes
the change in `tree.yaml`, one after the other, until none is left that
it can make. A change that comes back under new names (a node added that
does not solve it) stops after a few rounds; the check names what is left.

When the test is built, a step that takes a value a later step saves
stops the build with the lines to swap.

**Followed strictly.** With `order` the test creates in that order and
takes body values only from `body from` and the sources of `order`, for
the create and for its updates and patches; a field of a created record
that its create set (from a source, or from its path: the moon of a
garden created at `/moons/{moonId}/gardens`) has that value in later
steps. Nothing is guessed: a value of the stage that is the key of a
record the test creates stays as it is; where the check above cannot see
it, the review names it as a hint with the source to add. Records that do
not fit the plan (no record of the source node below a created one)
stop the build with the reason; a record whose field is empty (a ship
without a pilot) needs no source.
Without `order` (files of an earlier version, until the next run adds
it) apitest-gen orders the creates and finds the values itself.

### What the proposal holds

The first `apitest-gen tree` builds `tree.yaml` from the resources of the
spec (the model apitest-gen uses for its examples):

- a root per resource with a list that needs no path parameter, and a
  root per other read without path parameters;
- below a list node: the read of a record (`dock`), other reads that take
  its key (`log`, `profile`), the resources that belong to it (`ships`
  below `docks`) and the records its fields refer to (`shipPilot` from
  `pilotId`);
- every other read below the node that gives its path parameters, across
  tags and roots: `{pilotId}` is given by a node of pilots (the model maps
  it there, or it is the resource name and a key: `pilotId` = Pilot + `id`,
  also `originPilotId`, or a field has its name: `{isbn}`; a generic name
  like `{id}` when the word before it names the resource or its schema:
  `/Dock/{id}/Settings`). One kind of record listed below several parents
  (the docks of a planet and of a moon, one schema `Dock`) counts as one: a
  read by its id hangs below the first of those nodes, even without a
  `GET /Dock/{id}` of its own. With values of
  several nodes it hangs below the one fetched last and reads the others by
  name (`dockId=docks.id`). Repeated while new nodes give new values;
- `key` from the model, `pick: 3`, `page` for a `page`/`offset` parameter;
  a list node is named after the last word of its path, in the plural of
  its resource when that word is the singular (`GET /Mechanic`:
  `mechanics`, its read of one `mechanic`) or names another resource; a
  name already taken falls back to the operationId without its verb
  (`bedsByGardenCode`);
- a read with a value no node gives (`{reportNo}`, a required query
  parameter without example) is in the tree as well: below the nodes that
  give its other values, else at the top, with `$reportNo`; `params` lists
  each such value once, with the operations that use it, and the example
  or first enum value of the spec (`season: spring # … one of spring,
  summer, autumn, winter`). Set it and fetch. A value of `{id}` after the
  name of another resource (`/pilots/{id}/…` below the docks) never comes
  from the record above; the key of an update or delete never from an
  unrelated node, it stays `$name`. A key the client chooses in the path
  of a create (`POST /bookings/{berth}/{slot}`) comes from the record. A
  path parameter no field names (`/docks/{dockNumber}`) holds the key the
  references to the resource name (`Ship.DockId` → `Dock.Id`);
- a reference (`personId`) is read by the key it holds: the read by id,
  not the first read of the spec. So the scenario can create a record
  after the records it refers to, every list node whose records the API
  creates reads them below it, also one placed by its values: a resource
  with a list only gets `GET /pilots` with `where: {id: docks.pilotId}`;
  a list of keys named after a resource (`docks: [D-1, D-2]`, `pilotIds`,
  `dockCodes`) reads one record per key (`dockCode=.docks`, `pick: all`).
  When the resource a field names cannot be created, the resource with
  the same key named after it that can is read (`dockCode` of a list
  of docks and of the dock its create answers). These nodes get no
  `where` or `require` of their own, and their records create like the
  list of their resource;
- one write block per resource, at its list node (else at the first node
  that reads its records): its create (`none` without), update (PUT), patch
  (PATCH) and delete; the create at the path of the list (`POST
  /gardens/{gardenId}/beds` at the beds, `POST /gardens/code/{code}/beds`
  at the beds by code), never at the read of one record. A record written by several keys (`PUT
  /gardens/{gardenId}`, `PUT /gardens/name/{name}`, `PUT
  /gardens/code/{code}`) gets `update` by its id and `update-by-name`,
  `update-by-code` in the same block, deletes and patches alike; its reads
  by the other keys are `gardenByName` and `gardenByCode`, and `key`
  starts with the id (`[/id, /code, /name]`). Path parameters come from a
  node above that gives them, else a field of the record itself (`pilotId` of a book), else any
  node of the tree, else `$name`. Create, update and patch always get
  `body: from <read>`: the read of the record that covers most fields of
  the request (`from profile`), else the node itself;
- an action (a POST, PUT or PATCH whose path ends in a word after a
  parameter, `POST /gardens/{gardenId}/open`) goes into the write block
  of the record of its first path parameter, under its own name; two
  actions of one name there are named by their operationIds
  (`link-to-person`, `link-to-moon`). The scenario runs it once on a record it created,
  after update and patch; actions that end what a record can do
  (`archive`, `cancel`, `close`, …) run last;
- every other write goes to the node whose records it writes: a node of
  its resource, the node that reads its path (`PUT /docks/{dockId}` to
  `GET /docks/{dockId}`), a node whose key its path takes, or whose records
  cover its body. A POST that answers with a list and creates nothing is a
  search: a node with `read: true`. Its body takes the fields a node gives
  by their name, like parameters: `pilotId` from the pilots, a field named
  after a resource its key of the same type (`planet: .code`); it hangs
  below the node fetched last of those. Fields of the records it answers
  with and paging fields are left out. A write without such a node goes to
  `ignore` with the reason;
- the order of the nodes of a level: a node after the nodes whose records
  its subtree reads by name (`<node>.field`, as the fetch needs it), and
  after the list of a resource its records refer to (the pilots before
  the planets whose docks name them, the docks before the bookings that
  list them); else as found. When these needs go round, only the reads by
  name count.

**The operationIds.** An operationId is read as words:
`getGardensByPersonName` is the verb, the subject (gardens) and the filter
after `By` (person name → `Person.name`; `For`, `Of`, `In` alike, `And`
separates filters, `With` names what the answer must have). The parameter
that takes the filter is the one named like it (`personName`, `name`), else
the only free query parameter; a generic name (`id`) only when the word
before it names no other record (`GetSettingsByPilot` at
`/Dock/{id}/Settings/pilot/{pilotCode}` takes the pilot in `pilotCode`, the
`id` is a dock's). So the read hangs below the persons
(`query: {name: .name}`), even when the parameter is only called `name`;
`getPersonsByGardenName` hangs below the gardens, the other way round. A
filter of a resource outside the scope becomes `$name`.

**where and require.** List nodes get the conditions their children and
operationIds suggest, each with the reason as comment, so you see what was
guessed:

| What | Proposed |
|---|---|
| a read below the node for a filter its operationId names | `require` it with `min: 1`; the field it reads `present` |
| a read about the record that answers 404 without it (`details`) | `require` it |
| the first list of records that belong to the record (`beds` of a garden) | `require` it with `min: 1` |
| a field a child reads to name another record (`personId`) | `where: {personId: present}` |
| state words of the node's operationId (`getOpenGardens`) | `where: {status: OPEN}` |
| `With` (`getGardensWithBeds`) | the field `present`, or `require` the list with `min: 1` |
| actions of the write block, when the create sends the state | the states they start from: `open` and `archive` → `status: [DRAFT, CLOSED]` |

A node with `require` checks at most 20 candidates (`candidates: 20`). A
read with a `$name` is no `require`: until the value is set it would drop
every candidate. Delete what you do not want; a later run does not add
conditions to nodes your file has.

Each call gets its operationId as a comment. Names are unique in the file:
a name that is taken becomes the operationId (`getPilot`), only then a
number. The proposal is a start:
check the picks, add `where`, `require` and fixed keys.

### Later runs: your file stays

`apitest-gen tree` with an existing file changes nothing you wrote. It adds
what the file does not name yet, nowhere: not in a node, a require, a write
block or `ignore`.

| Case | What happens |
|---|---|
| a new read the proposal hangs below a node | added below the node of your file with the same call, `# … · new in <version>` |
| a new root that needs no value | added at the top of `tree`, so the nodes of the file can read it |
| a `$name` without value that a node gives now (a tag added later) | replaced by the reference to that node; the `params` entry goes when nothing uses it |
| a new write of a resource | added to the write block of that node |
| a new read whose parent your file does not have | below the nodes of your file that give its values (`pilotId=.pilotId` below the ships when the list of pilots is ignored), else at the top; a value none gives becomes `$name` with a new entry in `params` |
| a write of a node your file does not have | to the first node of your file of the same resource |
| a create, update or patch without `body` | gets `body: from <read>` as in the proposal |
| an `unplaced` section of an earlier version | removed; its reads are placed as above, values written there go into `params` |
| a node you do not want | set `off: true`, or put the operation into `ignore`; a deleted node comes back |
| a path changed, the operationId stayed | the call follows the path, the rest stays; listed |
| an operation the spec no longer has | stays, `# gone: not in the spec <version>`; listed; the node (and what hangs below it) is not fetched until you remove the mark |

The comment after each call is rewritten with the operationId, unless you
wrote your own comment there. The changes are printed and listed in the
review.

### Fetching

With `-base-url`, apitest-gen sends the requests of the tree, root first:
**only GET requests, and POSTs marked `read: true`**; a write block is never
sent. Per node and record above it:

1. send the request, page by page (`page`), once per value of a list in
   the query; the elements of the answers are the candidates (an answer
   that is no list is one record);
2. drop candidates that fail `where`;
3. order them: the keys of `pick`, else at random by `seed`, else as answered;
4. take candidates until `pick` is reached, checking `require` for each
   (at most `candidates`);
5. run the child nodes for each record taken.

A path value that is a reference to a list (`dockCode=.docks`) sends one
request per value; an empty list sends none, and the note says which
`where` lets only records with values through. The same request is sent once. A request that fails is noted, and its
branch is left out; the fetch goes on. When `budget` is used up, the rest
is not fetched. A node without records, candidates `where` or `require`
left out, keys `pick` names that are not there, and every failed request
are printed and listed in the review.

A problem names the line of `tree.yaml` and, below it, the change to make
there; `(-fix does it)` marks the changes `apitest-gen tree -fix` makes
itself (comments and the rest of the file stay):

```
line 23: order: ships.pilotId: the body takes an integer, shipPilot.name is a string
  change line 23: pilotId: shipPilot.name → pilotId: shipPilot.id (-fix does it)
line 48: ship: ".docks.id" reads the field "docks" of the record of ships above, but docks is a node
  change line 48: .docks.id → docks.id (-fix does it)
line 5: order: ships: {pilotId: shipPilot.id}: ships1 has no record of shipPilot below it (the fetch took none under it: see its notes)
  change line 28: ships: add where: {pilotId: present}, then fetch again with -node ships (-fix does it)
```

`-fix` makes: a leading dot before a node name (`.docks.id` → `docks.id`);
in `order` a line listed twice, a line that creates nothing, a delete or
create step (removed), a node name in other case, a create moved above
the line that takes its values, a source the body has not or whose node is
neither below nor above (removed), a source field the records lack or of
another type (→ the key of that node of the right type); after a build, a
`where: {field: present}` at the node whose records lack the source below
them (then fetch again). The others name the change to make by hand. A
reference to a field the record lacks lists the fields it has.

The top of `tree.yaml` explains the syntax in a few lines (fields,
nodes, `/`, `$name`, `{{…}}`, `order`); a run puts it there once, also in
place of the text of an earlier version.

### The scenario

From the tree and the records, apitest-gen writes the steps that rebuild
the records in an empty instance:

- **Records.** Every record of a node with a create is created once; a
  record fetched twice (the same pilot below two ships) once. A read of
  the same resource below a record (`dock` below `docks`) is the same
  record. Records of nodes without a create are **seed**: the instance must
  hold them; the review lists them with their keys.
- **Several creates.** When nodes with a create of their own hold the
  same records (the beds of a garden by id and by code), the records take
  turns, so each create runs; one that never runs is listed in the review.
- **Order of the creates.** A record comes after the records its create
  refers to, else in the order of the fetch: the pilot of a ship is created
  before the ship, though it hangs below it.
- **Values.** The ids of the stage do not exist in the empty instance:
  every value that is the key of a record the scenario creates becomes its
  saved value (`{{pilots2.id}}`). A reference in the tree (`.id`,
  `docks.id`) names the record; a body field counts when its name names
  the record (`pilotId`, `originDockId`), or when the tree reads the record
  by that field (`docks: [D-1]` with `dockCode=.docks` below, also each
  value of a list). A value that fits several records
  stays, and the review says so.
- **Bodies.** The record `from` names first, then the record itself, then
  its reads below it (the same resource, and every read that takes its key:
  `details`, `profile`), a field taken from the first that has it; cut to
  the schema of the request: no readOnly
  fields, nested objects and lists by their schemas. A required field the
  record lacks is generated and listed. A reference field the record
  lacks (`pilotId`, the last word `id`) is searched in it: the same name
  deeper (`pilotDetails.pilotId`), a name that ends with it
  (`currentPilotId`, also deeper), the `id` of an object named after it
  (`currentPilot.id`), in that order; objects only, no lists, at most 6
  levels and 5000 fields, no field the body takes under its own name. It is
  taken when the fields of the best kind hold one value (noted with the
  path); several values are noted and none is taken: `fields` of the node
  decides. A source in `order` or `fields` comes first. The fields of the
  record the body leaves out are noted at the step with their reason
  (`readOnly`, `null` in the record, not in the schema of the request).
  `unique` marks fields like `code`
  or `email` and client-chosen keys, for `-unique`.
- **Saved keys** come from the answer of the create, else the request
  body (a client-chosen key), else the `Location` header. An `ETag` is
  saved when a write takes `If-Match`, and sent with the update, the patch
  and the delete.
- **Reads.** Per list node one list per record above it, that must hold
  the records the scenario created (and the seed records, when the list is
  read with values of the stage alone); per other node one read per record,
  that must hold what its create sent (`check.equals`).
- **Changes.** Per node the update and the patch of its first created
  record, each followed by a read that must hold what was sent; the
  variants by other keys too (`gardens1.update-by-code`), each with a
  change of its own. Unique fields of the create are marked again, so a
  run with `-unique` sends the same new values. A write block at the read
  of one record below its list (`garden: GET /gardens/{gardenId}` below
  `gardens`) works on the records the list node creates: update and
  delete take their values from that read. A record of the seed is
  changed only with `onSeed: true` at the write.
- **Saved values of writes.** A step that uses a value a write saves
  (`$receiptNo`) runs right after the first step that saves it; without
  such a step it takes the value of `params`.
- **401.** Per reading operation that needs a token and documents 401, the
  first read again without a token.
- **Deletes** of the created records in reverse order, each node's first
  followed by a read that expects 404 (if documented). With variants the
  records of a node take them in turn (`gardens3.delete`,
  `gardens2.delete-by-code`, `gardens1.delete-by-name`), each first use
  with its read for 404; a variant without a record left is listed in the
  review. A record stays when
  a record that refers to it is not deleted.

```yaml
order: [pilots, docks, ships]
records:
  pilots1: {resource: pilots}
  docks1: {resource: docks}
  ships1: {resource: ships, refs: {docks: docks1, pilots: pilots1}}
steps:
  - id: pilots1.create # rebuilds pilots1 (GET /pilots)
    call: POST /pilots
    body: {license: L-A, name: Ada}
    save: {pilots1.id: /id}
    expect: 201
  - id: docks1.create # rebuilds docks1 (GET /planets/mars/docks?page=1&pageSize=50)
    call: POST /planets/{code}/docks
    path: {code: mars}
    body: {capacity: 5, name: South}
    save: {docks1.etag: header ETag, docks1.id: /id}
    expect: 201
  - id: ships1.create # rebuilds ships1 (GET /docks/106/ships)
    call: POST /docks/{dockId}/ships
    path: {dockId: '{{docks1.id}}'}
    body: {name: South-1, pilotId: '{{pilots1.id}}'}
    save: {ships1.id: header Location}
    expect: 201
  - id: planets3.docks # lists docks, it holds the record created
    call: GET /planets/{code}/docks
    path: {code: mars}
    query: {pageSize: 50}
    expect: 200
    check: {contains: {key: /id, values: ['{{docks1.id}}']}}
  - id: dock1.read # reads docks1, it holds what docks1.create sent
    call: GET /docks/{dockId}
    path: {dockId: '{{docks1.id}}'}
    expect: 200
    check: {equals: docks1.create}
  - id: docks1.update # updates docks1, changes capacity
    call: PUT /docks/{dockId}
    path: {dockId: '{{docks1.id}}'}
    header: {If-Match: '{{docks1.etag}}'}
    body: {capacity: 9, name: South}
    save: {docks1.etag: header ETag}
    expect: 200
```

| Key of a step | Meaning |
|---|---|
| `id` | `<record>.<action>[.<variant>]`; the subtest is `<tag>/<id>` |
| `call` | `METHOD /path` or the operationId |
| `path`, `query`, `header` | parameter values; `{{name}}` is a saved value (alone: with its type; inside a text: as text) |
| `body` | the request body |
| `bodyFrom` | send the body an earlier step sent |
| `save` | `name: source`: `/pointer` into the answer (a part `[field=value]…` finds the element of a list with these values, `{{name}}` saved values in it: `/[slot={{bookings1.slot}}]/id`; in a page its one list), `header Location` (its last path segment), `header ETag`, `request /pointer`, `request path <name>`, `request query <name>` |
| `token` | `none` (401), `invalid` (a manipulated token), `forbidden` (403); left out: the default token |
| `expect` | the status the answer must have |
| `check.equals` | the answer holds what that step sent (fields the answer's schema does not declare and writeOnly ones left out) |
| `check.contains` | a list answer (the answer, or its one list field) holds elements whose `key` has these values |
| `unique` | JSON pointers into the body whose values `-unique` makes new for each run |
| `answer` | the answer of the last run with `-update` (`request`, `status`, `headers`, `body`, `volatile`, `lists`, `problems`), written by it; `golden: {spec, specVersion, created, reread}` on top of the file says where they come from |

### tree run

```sh
apitest-gen tree run -spec api/openapi.yaml -file apitest/tree/tree.yaml -base-url http://localhost:8080/api -token-env STARPORT_TOKEN -update
```

Runs `tree-test_gen.yaml` against an empty instance. With `-update`,
every step must answer as the test expects; then it writes the answers
next to the steps (`answer` per step, `golden` on top; a reading step is
sent twice: fields that change by themselves are volatile; the comments of
the file stay) and the examples into the spec, in place: per operation the
first request and answer, and the read after a delete as named example
under 404. Without `-update`, it compares every answer with the answers of
the file if it has them: status, schema of the spec, the body as a subset
of the kept one, the compared headers and the checks of the steps; a step
without answer then fails (write them again with `-update`). A saved value
takes the type of the field it is filled into, in a body and in a kept
answer: an id saved from a `Location` header is text, an integer field gets
the number. The timeline of the run lists each step once; with `-update`
the review says that every read was sent twice. Building the
test again (`tree`) keeps the answers of the steps it still has. Exit code
0 passed, 1 failed, 2 wrong usage.

| Flag | Meaning |
|---|---|
| `-spec`, `-file` | the spec; the tree file, next to which the test is |
| `-base-url` | the empty instance, with base path |
| `-token`, `-token-env`, `-forbidden-token`, `-forbidden-token-env`, `-header` | see [Tokens](#tokens) |
| `-update` | write the answers into the test and the examples into the spec instead of comparing |
| `-dry-run` | send nothing: list the steps of the test |
| `-no-examples` | with `-update`: leave the spec alone |
| `-scenario` | another test file |
| `-golden` | a golden snapshot file of its own instead of the answers in the test |
| `-settings` | an optional settings file (below) |
| `-errors 401,404` | run only these error steps, in this order (see `ScenarioConfig.Errors`) |
| `-run`, `-skip` | select steps like `go test -run`: `Ship/`, `Ship/ship1.read`; the steps they need run too |
| `-unique` | make the values at `unique` new for this run, for an instance that is not empty |
| `-report`, `-junit` | Markdown report (HTML and JSON next to it); JUnit XML |
| `-allow-remote-writes` | allow writing requests to a host other than localhost |
| `-timeout` | timeout of each request |

`-unique` replaces each value once per run (a pattern of the schema is
respected, an e-mail gets `+<run>` before the `@`), the same original
value always by the same new one, and maps the golden snapshot the same
way.

The optional settings file holds what does not change per run; paths are
relative to it, `env:NAME` reads a variable, a flag or a field set in code
wins:

```yaml
spec: ../../api/openapi.yaml
baseURL: env:STARPORT_BASE_URL
health: /health                       # waited for before the first step
tokens:
  default: env:STARPORT_TOKEN
  forbidden: env:STARPORT_READER_TOKEN  # the 403 steps; without it they are skipped
headers: {X-Tenant: env:STARPORT_TENANT}
report: out/tree-report_gen.md
unique: false
```

### In go test: apitest.RunScenario

`apitest.RunScenario` takes the whole `apitest.Config` of `apitest.Run` and
the files of the scenario mode. Like `Run`, it needs **either** a
`Handler` **or** a `BaseURL` (a Testcontainers container, for example).

```go
func TestTree(t *testing.T) {
	apitest.RunScenario(t, apitest.ScenarioConfig{
		Config: apitest.Config{
			SpecPath:     "../../api/openapi.yaml",
			Handler:      app.NewRouter(app.NewMemoryStore()), // or BaseURL
			Token:        apitest.StaticToken("admin-token"),
			IgnoreFields: []string{"traceId"},
			ReportPath:   "out/tree-report_gen.md",
		},
		Scenario: "tree-test_gen.yaml", // the steps and the answers of -update
	})
}
```

| Field | Meaning |
|---|---|
| `Config` | everything of `apitest.Run`: spec, `Handler` or `BaseURL`, tokens, headers, `Params` (values for parameters a step leaves out), `IgnoreFields`, `CompareMode`, `CaseInsensitive`, `Strict`, `DeviationsPath`, `TolerateErrorCases`, `SkipAuthCases`, `Tags`/`IncludeOps`/`ExcludeOps` (select steps by operation), `NumberCases`, reports, `Hooks`, … `MethodOrder`, `DeleteLast`, `LastInTag` and `ErrorCases` must stay unset: the scenario fixes the order and holds the error steps. |
| `Settings` | the optional settings file |
| `Scenario` | the scenario (the test file) |
| `Golden` | a golden snapshot file of its own; empty takes the answers the scenario file holds, without them status, schema and the checks of the steps only |
| `Errors` | the error steps (status 400 and above) that run, and their order: statuses (`"401"`) or classes (`"4xx"`). Steps no entry matches are left out; consecutive error steps run in the order of the list, at the place the scenario gives them |
| `Unique` | as `-unique` |
| `Update` | write the answers instead of comparing: into `Golden` when set, else into the scenario file |
| `OnStep` | called after each step with what was sent and received, how it ended (`Outcome`, `Message`), the schema violations (`Problems`), the values that differ (`Diffs`) and the step an `equals` check compared with (`Equals`) |

`apitest.RunScenarioStandalone` runs the same outside of `go test`; the
command line uses it.

## Regression check

Before a deployment, `regression pre` draws a sample of real records of
the stage, reads every GET of the spec with them and keeps the answers.
After the deployment, `regression post` reads the same requests with the
same records and compares. Only GET requests are sent, also for drawing
and checking the sample: a `from` or `check` with another method is an
error before anything is sent.

### The sample file

```sh
apitest-gen regression init -spec api/openapi.yaml -out apitest/regression/regression-sample.yaml
```

`init` proposes one set per resource with a list GET: the list as source,
the values a record gives (its key, the fields that refer to other
records), those references as `present`, and the GETs that take the values
as checks. Run again later, it adds sets for new resources and keeps
yours.

```yaml
seed: random                  # a new sample every pre run; a number repeats one
sets:
  ship:                       # up to 5 ships that have a dock and a crew
    from: GET /ships
    query: {pageSize: 200}
    pages: 3                  # page=1,2,3
    where: {dockId: present, year: '>=2020'}
    check: ['GET /docks/{dockId}', 'GET /ships/{shipId}/crew']
    pick: 5                   # at random; fewer if there are fewer
    as: {shipId: /id, dockId: /dockId}
reads:
  exclude: [GET /health]
  GET /ships: {query: {dockId: '{{ship.dockId}}'}}
ignore: [updatedAt]           # never compared
lists: {key: id}              # list elements are matched by this field
probe: 2s                     # read each GET twice, this far apart
```

A candidate is kept when it passes `where` (a field `present`, a value,
or a comparison `>=`, `<=`, `>`, `<`) and every `check` answers 2xx with
its values; then `pick` are chosen at random with the seed, which is
printed and stored. Fewer candidates than `pick`: the sample takes those
there are; none: the GETs that need the set are not read.

Every GET of the spec is read once per record of the set whose `as` gives
all its path parameters (`{shipId}`, `{dockId}`), or once if it has none.

### regression pre

```sh
apitest-gen regression pre -spec api/openapi.yaml -sample apitest/regression/regression-sample.yaml -base-url https://starport.dev.example.com/api -token-env STARPORT_DEV_TOKEN -out apitest/regression/out
```

```
sample  seed 734291
  ship         5 of 143 candidates (left out: 12 GET /ships/{shipId}/crew failed, 2 without dockId)
reads   19 GETs
PASSED: 19 steps (19 PASSED) in 1.2s
```

It writes into `-out`: `regression-scenario_gen.yaml` (the reads),
`pre-snapshot_gen.yaml` (sample, seed and every answer) and
`pre-report_gen.md`/`.html` (status and schema of every read). A read that
already fails before the deployment does not stop it (it is not new);
`-strict` makes it, and an empty set, exit with 1. `-seed` repeats a
sample, `-probe` overrides the probe of the sample file.

### regression post

```sh
apitest-gen regression post -spec api/openapi.yaml -base-url https://starport.dev.example.com/api -token-env STARPORT_DEV_TOKEN -out apitest/regression/out
```

It reads the same requests with the same records (no new sample), checks
them against `-spec`, the spec of the **new** version, writes
`post-snapshot_gen.yaml` and `post-report_gen.md`/`.html`, then compares
with the pre snapshot and writes `diff-report_gen.md` and
`diff-report_gen.html`. Exit code 1 if a finding fails. Reads of
operations the new spec no longer has are left out.

`regression diff -pre … -post …` compares two snapshots on their own.

### Findings

| Finding | Meaning | Fails |
|---|---|---|
| `STATUS_CHANGED` | another status than before | yes |
| `FIELD_CHANGED` | a field has another value or type | yes |
| `FIELD_REMOVED` | a field is gone | yes |
| `FIELD_ADDED` | a new field | with `-fail-on added` |
| `HEADER_CHANGED` | a compared header differs or is gone | yes |
| `ELEMENT_MISSING` | an element of a list is gone (matched by `lists.key`) | yes |
| `ELEMENT_OTHER` | another element came into a list (someone else's data) | with `-fail-on other` |
| `SCHEMA_VIOLATION` | the post answer does not fit the new spec | yes |
| `SLOWER` | more than `-slower` (default `2x,200ms`) | with `-fail-on slower` |
| `STEP_MISSING`, `ERROR` | no answer after the deployment | yes |

Fields in `ignore` of the sample file or `-ignore`, and fields the probe
found volatile, are not compared. Someone else using the stage between pre
and post can cause findings too: run pre right before and post right after
the deployment; the report shows the time between them and every old and
new value.

## Tokens

All commands take the same flags; a flag wins over the settings.

| Flag | Meaning |
|---|---|
| `-token <value>` | bearer token given directly |
| `-token-env <NAME>` | bearer token from an environment variable |
| `-forbidden-token`, `-forbidden-token-env` | a valid token with too few rights, for the 403 steps (`tree run` only) |
| `-header "Name: value"` | another header, repeatable, e.g. an API key |

Tokens are redacted in every report and output. In a pipeline prefer
`-token-env` with a secret variable, so the token is not on the command
line.

## In a pipeline

Azure DevOps, as an example; the steps are plain commands for any CI.

```yaml
stages:
- stage: Integration
  jobs:
  - job: tree
    steps:
    - script: docker run -d --name starport -p 8080:8080 -e DB_URL=memory:// $(IMAGE)
    - script: timeout 60 sh -c 'until curl -fs http://localhost:8080/api/health; do sleep 1; done'
    - script: apitest-gen tree run -spec api/openapi.yaml -file apitest/tree/tree.yaml -base-url http://localhost:8080/api -token-env STARPORT_TOKEN -junit $(Build.ArtifactStagingDirectory)/tree.xml
      env: {STARPORT_TOKEN: $(starportToken)}
    - task: PublishTestResults@2
      condition: always()
      inputs: {testResultsFormat: JUnit, testResultsFiles: $(Build.ArtifactStagingDirectory)/tree.xml}

- stage: DeployDev
  dependsOn: Integration
  jobs:
  - deployment: dev
    environment: dev
    strategy:
      runOnce:
        preDeploy:
          steps:
          - checkout: self
          - script: apitest-gen regression pre -spec api/openapi.yaml -sample apitest/regression/regression-sample.yaml -base-url $(DEV_URL) -token-env STARPORT_DEV_TOKEN -out $(Pipeline.Workspace)/regression
            env: {STARPORT_DEV_TOKEN: $(starportDevToken)}
        deploy:
          steps:
          - script: ./deploy.sh dev $(IMAGE)
        routeTraffic:
          steps:
          - checkout: self
          - script: apitest-gen regression post -spec api/openapi.yaml -base-url $(DEV_URL) -token-env STARPORT_DEV_TOKEN -out $(Pipeline.Workspace)/regression -junit $(Pipeline.Workspace)/regression/regression.xml
            env: {STARPORT_DEV_TOKEN: $(starportDevToken)}
          - task: PublishPipelineArtifact@1
            condition: always()
            inputs: {targetPath: $(Pipeline.Workspace)/regression, artifact: regression-dev}
```

The pipeline starts the empty instance of the integration test (with the
seed the review lists); apitest only gets its URL. The reports and the diff are self-contained HTML files.

## Command reference

```
apitest-gen tree            -spec <openapi.yaml> [-file tree.yaml] [-tags A,B] [-exclude-tags X] [-exclude-ops opId] [-with-deps] [-fix] [-base-url <url> [tokens] [-node name]] [-timeout 10s]
apitest-gen tree run        -spec <openapi.yaml> [-file tree.yaml] -base-url <url> [tokens] [-update] [-no-examples] [-errors 401,404] [-run <pattern>] [-skip <pattern>] [-unique] [-junit <file>]
apitest-gen regression init -spec <openapi.yaml> [-out regression-sample.yaml]
apitest-gen regression pre  -spec <openapi.yaml> -sample <file> -base-url <url> [tokens] [-out dir] [-seed n] [-probe 2s] [-strict] [-junit <file>]
apitest-gen regression post -spec <openapi.yaml> -base-url <url> [tokens] [-out dir] [-pre <snapshot>] [-sample <file>] [-fail-on added,slower,other] [-slower 2x,200ms] [-ignore f,…] [-junit <file>]
apitest-gen regression diff -pre <snapshot> -post <snapshot> [-out dir] [-sample <file>] [-fail-on …] [-ignore …] [-junit <file>]
```

`apitest-gen tree -help` (also `help`, `-h`, `--help`) explains the whole syntax of `tree.yaml` and lists all flags; `apitest-gen regression help` lists its flags.

## Limits

- Request bodies are JSON (or form-encoded). Multipart bodies are not
  generated.
- The integration test needs an empty instance with the seed the review
  lists, or `-unique` for the fields the scenario marks as unique; other
  conflicts (a name the instance already has) show up as failed steps.
- It rebuilds what the API can create. Records that only a background job,
  an import or another service makes are seed: put them into the test
  instance, or switch their nodes off.
- Error steps besides 401 and the read after a delete (409, 412, 422) are
  not generated.
- A read that is no list but answers with an object holding one list of
  objects is read as a list; set `items` to correct it.
- The regression check compares what a stage answers before and after a
  deployment. Changes by other users between pre and post are reported
  like any other change.
