# Changelog

All notable changes are documented here. The project follows
[Semantic Versioning](https://semver.org/); before 1.0 minor versions may
contain breaking changes, which are listed here.

## Unreleased

### Features

- Scenario mode, next to the examples mode of `apitest.Run`
  ([docs/scenario.md](docs/scenario.md)):
  - `apitest.RunScenario(t, apitest.ScenarioConfig{…})` runs a scenario:
    the requests of a test in the order they run, with any number of
    records and calls per endpoint, values saved from earlier answers
    (`{{ship1.shipId}}`, also from headers such as `Location` and `ETag`),
    checks (`equals`, `contains`) and authentication steps. It compares
    every answer with a golden snapshot (`kind: golden`). `ScenarioConfig` embeds the whole
    `Config` of `Run` (either `Handler` or `BaseURL`, tokens, `Params`,
    `IgnoreFields`, `CompareMode`, deviations, `TolerateErrorCases`,
    `SkipAuthCases`, `Tags`, reports, hooks, …) and adds `Settings`,
    `Scenario`, `Golden`, `Errors` (which error steps run, and their
    order: `"401"`, `"404"`, `"4xx"`), `Unique`, `Update` and `OnStep`.
    `RunScenarioStandalone` runs it outside of `go test`.
  - `apitest-gen tree`: an integration test from connected records.
    `tree.yaml` says which records to fetch and how they hang together: a
    node is a request (`GET /docks/{dockId} dockId=.id`), its records are
    filtered (`where`: present (not empty: `""`, `[]` and `{}` count as
    absent), values, one of, comparisons, `any`;
    `require`: requests that must answer 2xx per candidate, `candidates`),
    picked (`pick`: a number at random by `seed`, `{first: n}`, `all`, or
    fixed keys), paged (`page`: number, offset or cursor, in query or
    body) and every child node (`each`) runs per record; `.field` and
    `<node>.field` read records above; a POST that only reads is allowed
    with `read: true`; `budget` limits the requests; `<node>.field` may
    also read a node fetched before that is not above (its first record),
    so values are used across tags. The `write` block of a
    node says how a record is created (`none`: seed), updated, patched and
    deleted. `apitest-gen tree` proposes the file from the spec (roots,
    reads below them, resources that belong to them, records their fields
    refer to; every other read below the node that gives its path
    parameters, across tags: `{bookId}` below the books, by the model, by
    resource name + key or by field name; a value no node gives becomes
    `$name`, listed once under `params` at the top of the file to be set
    (a value or a reference to a node such as `books.id`),
    and the read still gets its place in the tree; write blocks with
    `body: from <read>` for every create, update and patch, from the read
    that covers the request best; every other write at the node whose
    records it writes, a search as a node with `read: true`, the rest in
    `ignore` with the reason; operationIds as comments) and, run again,
    keeps the file and only adds what it does not name yet at its place
    (below the nodes of the file that give its values; values the file
    lacks into `params`), follows renamed paths and marks operations the
    spec lost. Large specs step by step: `-tags` (kept as `scope` in the
    file, each run adds the endpoints of new tags at their place),
    `-exclude-tags` and `-exclude-ops` (patterns with `*`), `-with-deps`
    for the reads of other tags that give needed values; a `$name` a new
    tag gives is replaced by the reference to its node; `-node` fetches
    one subtree again. The operationIds are read as words
    (`getGardensByPersonName`: the filter Person.name, so the read hangs
    below the persons even when the parameter is only `name`), and list
    nodes get `where` and `require` with the reason as comment: from such
    filters (`min: 1`, new key of `require`), from reads that answer 404,
    from lists of records below, from fields that name other records, from
    state words (`getOpenGardens`) and from actions; actions
    (`POST /gardens/{gardenId}/open`) are write kinds of their own name,
    run once on a created record, those that end a record last. A record
    updated or deleted by several keys (`PUT /gardens/{gardenId}`,
    `/gardens/name/{name}`, `/gardens/code/{code}`) gets one write block
    with `update` (by its id) and variants `update-by-name`,
    `update-by-code` (also `patch-by-…`, `delete-by-…`); the reads by the
    other keys are `gardenByName`, `gardenByCode`, the `key` starts with
    the id. The scenario runs every update variant on the first created
    record, each with its own change and read after it (with `-unique` the
    updates send the new unique values of the create), and gives the
    deletes to the created records in turn, each with a read for 404. With
    `-base-url` it fetches the records (GET only) into
    `tree-data_gen.yaml` and writes `tree-scenario_gen.yaml`: the creates
    in the order the references need (the ids of the stage replaced by
    saved values, bodies cut to the request schema, required fields
    generated, `ETag`/`If-Match`), reads and lists with checks, update and
    patch with a read after them, 401 steps and the deletes in reverse
    order. `tree-review_gen.md` grows with the workflow: every step
    (`tree`, `tree -base-url`, `tree run`, `tree run -update`) writes its
    own section (the tree with its nodes, params and what to check; the
    records per node; the steps, the seed the empty instance needs and
    every decision; the run with the steps that did not pass; the golden
    snapshot and the examples) and keeps the others, with a status table,
    the next command and sections marked outdated by a later change. `apitest-gen tree run` runs the scenario against an
    empty instance; `-update` writes the golden snapshot and the examples
    of the spec. A `where` at the top of `tree.yaml`
    (`Garden.name: [Rose, Mint]`, or `Garden: {code: [rose, mint]}`)
    holds for every record of that resource at every node of the fetch,
    `require` included; resource and field are checked against the spec.
    The proposal reads a reference by the key the field holds
    (`personId` by id, though the spec lists the read by name first),
    puts a create at the list of its path (`POST /gardens/code/{code}/beds`
    at the beds by code, never at the read of one record), names a second
    variant by the same key after its operationId
    (`update-by-moon-and-name`), keeps abbreviations whole
    (`update-by-url`), takes a key the client chooses in the path of a
    create from the record (`POST /bookings/{berth}/{slot}`), reads the key
    of a write from the references to it (`/docks/{dockNumber}` holds the
    Id ships refer to), never takes `{id}` of another resource from the
    record above (`/pilots/{id}/…` below the docks), never guesses the key
    of an update or delete from an unrelated node, and gives a value of
    `params` the example or first enum value of the spec (the others in
    the comment). A resource with a list and a create of its own is no view
    of the record above it: its `Id` is its own. Two actions of one name
    at a node (`link` to a person and to a moon) stay together, named by
    their operationIds (`link-to-person`, `link-to-moon`); an action goes
    to the record of its first path parameter. A list at a singular path
    or below the name of another resource is named in the plural of its
    resource (`mechanics`), a taken name falls back to the operationId
    without its verb (`bedsByGardenCode`). Several nodes with a create of
    their own for the same records (beds by garden id and by code) take
    turns in the scenario, so every create runs; one that never runs is
    listed in the review. A path parameter named like a key field of the
    record (`repairNo`) counts as its key, also where path and schema have
    different names. Proposing for a large spec is about eight times
    faster.
    `prefer` in the same form does not filter: a pick takes those records
    first and fills the rest with others, and the nodes above a preferred
    record (its planet and moon) are picked first too.
    One kind of record listed below several parents (the docks of a planet
    and of a moon, one schema) counts as one: a read by its generic `{id}`
    (`/Dock/id/{id}/Settings`, `/Dock/{id}/Settings/pilot/{pilotCode}`)
    hangs below those docks with `id=.id`, even without a read of one dock
    by id, instead of becoming `$id`; an operationId filter
    (`…ByPilot`) no longer takes such an `{id}` for the pilot's.
    A text with `{{…}}` composes references (`berthKey:
    '{{planets.code}}-{{docks.number}}'`), in nodes, writes and `params`.
    A write's `save` (`receiptNo: header X-Receipt-No`) gives `$receiptNo` its
    value in the scenario, and the step that reads it runs right after
    that write; `onSeed: true` lets an update, patch or action change a
    record of the seed.
    Dependencies: a list node whose records the API creates reads the
    records they refer to below it, also when it is placed by its values
    (before: only nodes built from the resource, and only by a read by
    key): a resource with a list only by `where: {id: docks.pilotId}` (a
    `where` value may now be a reference), a list of keys named after a
    resource (`docks: [D-1]`, `pilotIds`) one read per key (a path
    reference to a list sends one request per value); a body field the
    tree reads a record by is its placeholder in the scenario, so the
    creates run in the right order. The nodes of a level are ordered by
    what they need (references by name, then the lists their records
    refer to), no longer as the spec lists the resources. A search
    (`read: true`) takes the body fields a node gives by name
    (`pilotId`, `planet: .code`) and hangs below that node, instead of
    `body: {}` at the top.
    **The plan of the test: `order`.** `tree.yaml` starts with `order`,
    the nodes that create records in the order the test creates them
    (deletes the other way round, updates in this order) and where values
    of their bodies come from (`- ships: {pilotId: shipPilot.id}`). The
    first run proposes it with every source it finds; a create that comes
    in later gets a line after what it needs, marked `# new: check its
    place`. Every run checks it before anything is sent (unknown or
    duplicate nodes, a create without line, a create before the records
    it takes values of, a source that is neither below nor above, a field
    the records or the body lack) and stops with the line and the reason.
    With `order` the test follows it strictly: creates in its order, body
    values only from `body from` and its sources (also for updates and
    patches), no guessing; records that do not fit stop the build. Files
    without `order` work as before.
    **Keys of the stage are found before the fetch:** a body field of a
    create that holds the key of a record the test creates (`pilotId` of a
    ship, the test creates pilots) needs a source in `order`, else the
    create would send the key of the stage, which the empty instance does
    not have; `-fix` names the node below that reads it or the node above
    of that resource, or adds the read below
    (`pilot: GET /pilots/{pilotId} pilotId=.pilotId`) and names it (then
    fetch again with `-node`). Also named, with fix: a source that reads
    another resource, and a path value of a node or write that is a value
    of the stage (`shipId=7`, `$shipId`) where the path takes the key of a
    created record. The proposal and a merge that writes `order` add the
    sources they find, also of a node above. A record whose field is empty
    needs no source.
    **Scope checked against the whole file:** a node, write or require
    whose request `exclude` names (also one added later) is named; `-fix`
    sets `off: true` with the reason (a require is removed). The reads
    `-fix` adds respect the scope (the read by key, else the list with a
    where on the key); with no read in scope the create of those records
    goes off (seed). `-fix` also removes a line of `order` without node, a
    source naming a missing node or one that is not fetched, and a `where`
    kept for a node that is gone or not fetched; a change that comes back
    under new names stops. **Merge:** the read of the record another one
    names hangs below it even when the file reads that request elsewhere
    (it was mapped to that node, the source and the where were lost and the
    merged file failed its own check), under a free name (two nodes of one
    name broke the file before); new `scope` and `params` sections go below
    `order`, so the explanation at the top no longer moves on every run.
    **Where at the top:** a record whose create takes a value of a record
    that `where` leaves out is left out too (the next candidate is taken,
    noted), instead of stopping the build.
    `order` places reads, lists, updates, patches and actions too
    (`- docks.update`, `- shipPilot.read`, `- docks.list`): those steps
    run at that place, the steps not named after the list as before. The
    check names a step before the creates of its records or of the records
    above them, a step the node lacks, a delete (they run at the end) and
    a step that takes a value a later step saves; a read placed after an
    update checks what the update sent.
    **Shorter `tree.yaml`:** a head of three lines, and writes leave out
    what follows from their node (a path value the node reads with, the
    key of the record, the body from the node itself):
    `create: POST /planets/{code}/docks`.
    **One test file:** `tree-test_gen.yaml` holds the steps and the
    answers of the last `tree run -update` (`answer` per step, `golden`
    on top), replacing `tree-scenario_gen.yaml` and
    `tree-golden_gen.yaml`; building the test again keeps the answers of
    the steps it still has. `ScenarioConfig.Golden` is optional: empty
    takes the answers of the scenario file, and `Update` writes them
    there (in place, the comments stay).
    **Review:** the next command and the plan (order, tree with records,
    writes, body sources) on top, the steps folded, and every request of
    the fetch and of the run with its bodies in full (secrets masked).
    **`-dry-run`** for `tree` and `tree run`: sends nothing, writes no
    data or test, shows the plan, the requests a fetch would send and the
    steps of the test.
  - `apitest-gen regression init|pre|post|diff`: a check before and after a
    deployment that only sends GET requests. `pre` draws real records of a
    stage by the rules of `regression-sample.yaml` (a new sample every
    time; `where`, `check`, `pick`), reads every GET with them and keeps
    the answers; `post` reads the same with the same records against the
    new spec and reports what changed (`STATUS_CHANGED`, `FIELD_CHANGED`,
    `ELEMENT_MISSING`, `SCHEMA_VIOLATION`, `SLOWER`, …) as Markdown, HTML
    and JUnit; fields that change by themselves are found by reading
    twice (`probe`).
  - `apitest-gen tree -help` (also `help`, `-h`, `--help`, after `run`
    and other flags) explains the whole syntax of `tree.yaml` (top-level
    keys, values and references, keys of a node and of `write`, `order`,
    messages) and lists the flags. A problem names the line and below it
    the change to make (`change line 23: pilotId: shipPilot.name →
    pilotId: shipPilot.id`); `apitest-gen tree -fix` makes the changes
    marked `(-fix does it)` in `tree.yaml`, comments kept: a leading dot
    before a node name (`.docks.id` → `docks.id`, now found before the
    fetch), lines of `order` listed twice, creating nothing or a delete
    step, creates moved above the lines that take their values, sources
    the body has not or that hang neither below nor above, a source field
    the records lack or of another type than the body field (new check)
    → the key of that node; after a build the `where: {f: present}` at
    the node whose records lack the source below them (then fetch again).
    A source of `order` without a record names all records that lack it
    once; a path value that is an empty list says so and which `where`
    lets only records with values through; a reference to a missing
    field lists the fields of the record. Problems are sorted by line.
    The top of `tree.yaml` explains the syntax in a few lines; a run puts
    it there once (it stood there again with each new `order`), and a
    merge no longer takes sources of the proposal whose node hangs
    elsewhere in the file.
  - The review of `apitest-gen tree` stays small: each section of
    `tree-review_gen.md` ends with its timeline, one line per request or
    step in the order it ran (the fetch only the failed requests; the
    test its steps in the order they will run, with the decisions and
    gaps of each; the run every step, a failed one marked; `-update` the
    steps that gave the examples). The bodies moved to
    `tree-review_gen.html`: one self-contained page, every line opens its
    request and answer as a tree in which every object and list folds,
    built when opened (long lists 100 at a time), closed on load, with
    search and "only failed"; each run keeps the timelines of the steps
    it did not run.
  - `tree-review_gen.html` shows the fetch as a tree: per node and record
    above it the values sent and where they come from, the conditions
    (`where`, `any`, the `where` and `prefer` of the file), `pick`, every
    request (cached ones marked), and per candidate why it was taken or
    not (the failed clause with the value of the record, each `require`
    with its requests), with the nodes below a record; the nodes that took
    nothing on top.
  - `apitest-gen tree`: the note of a node that took fewer records than
    asked names per `where` clause how many candidates it left out, with
    the value of the first, and the first reason of `require`; it also
    comes for `pick: all` and a read of one record that took nothing
    (before: no note).
  - `apitest-gen tree`: the `where` of the file holds at a node with
    `items` for the records at items (before: only where the spec marks
    the list itself).
  - Scenario mode: a saved value takes the type of the field it is filled
    into, in a request body and in the kept answer of the golden snapshot
    (as parameters already did). An id saved from a `Location` header is
    text; compared with an integer field that is not `readOnly`
    (`shipId` of a log) a run after `-update` failed with
    `EXAMPLE_MISMATCH`, and a body sent it as a string.
  - `apitest-gen tree`: one record read by two nodes of its resource whose
    `key` lists differ (`[/id]` and `[/id, /name]`) was created twice,
    with the same body; the keys both nodes name now decide. A create
    that still sends the same request and body as an earlier one is noted
    at its step.
  - Token flags for both: `-token`, `-token-env`, `-forbidden-token`,
    `-forbidden-token-env`, `-header`. The settings file of a run is
    optional (spec, base URL, health, tokens, headers, report, unique).

## v0.2.0


### Breaking changes

- Reports are only written when `Config.ReportPath` is set; the default
  `apitest-report/<test name>.md` is gone. `ReportJSON` needs `ReportPath`.
  `DisableReports` still turns reports off where a path is set.
- New order: every regular case (2xx) of the run first, DELETEs included,
  then the 4xx and 5xx examples, `conflict` and authentication cases, the
  `not-found` cases last. A wrong answer to one of those (a DELETE without
  token that the API accepts, an upsert with an unknown key) no longer
  changes what the regular cases check. The authentication cases of a
  DELETE now run after it; the regular DELETE is no longer skipped when an
  unauthorized DELETE succeeded (FR-ORDER-07). `AfterGroup` runs after the
  last case of the group, in the second phase.
- `conflict` sends the body twice (the first answer makes sure the record
  exists) and deletes the records it created with the regular DELETE of
  the tag.

### Moved

- The project lives at `github.com/fada4773-sketch/specproof`. The library
  is the package `apitest` in the subdirectory `apitest/`: import
  `github.com/fada4773-sketch/specproof/apitest`; the API is unchanged
  (`apitest.Run`, `apitest.Config`, …). The versions below were released
  under the previous module path `github.com/fada4773-sketch/apitest`.

### Features

- `apitest-gen record`: every schema under `components/schemas` gets an
  `example` from the values bodies and answers of the record file hold at
  it (nested ones too; an object cut to the properties the schema
  declares), else one generated from the schema, marked
  `# apitest-gen record: generated example` and replaced once an entry
  holds a value. An existing example that fits stays. Schemas apitest reads
  the example of (a parameter or request body without example) are left
  out and listed (`GENERATED`), so no case changes. The FILES line counts
  the schema examples.
- `not-found` cases no longer get skipped for missing values: a required
  parameter without value (no example, or its producer failed or did not
  run) gets one generated from its schema, and the request is sent.
  Keys and parameters with a `pattern` get a matching value too: the key
  of a not-found case the highest one (`^\d{3}$` → `999`), other
  parameters a generated one.
- HTML report: new layout with fixed rows (key figures, status, response
  times and status codes side by side), tags and slowest requests as full
  width tables, error cases with the matrix above the list, fixed table
  columns with shortened long paths and names (full text on hover and in
  the details), dark scrollbars.
- Error cases: `Config.ErrorCases` generates `not-found` (an unknown key in
  the last path parameter of every operation that documents 404; the key
  from `x-apitest-not-found` or the schema) and `conflict` (the body of a
  POST that documents 409 sent a second time). Named request examples paired
  with a 5xx response example now test that error too.
  `Config.TolerateErrorCases` reports error cases that get another answer
  (generated ones and named 404/409/5xx examples) as the new status
  `TOLERATED` instead of failing the test.
- HTML report: a self-contained dashboard (`<name>.html`, next to the
  Markdown report; `Config.DisableHTMLReport` turns it off) with key
  figures, status ring, tags, response time histogram and percentiles,
  status codes, slowest requests, error case analysis (expected → received)
  and every case with search, filters and details; light and dark theme.
- The Markdown report is redesigned: a result line, a Mermaid chart of the
  statuses, key figures, tags, response times, status codes, slowest
  requests and the error case analysis. The JSON report gives each case
  `kind`, `expected`, `errorCase` and `tolerated`.
- `Config.LastInTag`: operations (operationId or `"METHOD /path"`) whose
  cases run after all other cases of their tag, whatever their method, in
  the order listed; the tag's DELETEs still follow, bindings still win.
  An unknown operation stops the run. `"$apitest"` of the defaults takes
  it too, so `apply` and `record` follow the same order.
- `Config.CaseInsensitive`: the example comparison (and the read-back after
  writes) ignores the case of strings and field names; an exact field name
  wins, `IgnoreFields` match in any case too. The schema check stays
  case-sensitive.
- `apitest-gen record`: entries with `status: ignore` are still never sent,
  but they are now written into the spec with values that fit the schema:
  their own values and stored answer where they fit, the rest generated
  (`-seed`). A generated answer gets `x-apitest-compare: schema` with the
  comment `apitest-gen record: generated answer`, removed again once the
  entry has a real answer. The status stays `ignore`. Note `GENERATED`.
- `apitest-gen record`: every path parameter apitest would bind by guessing
  is written as `x-apitest-bind` (source from the record file's `save`, or
  the guess checked against the stored answer), once at a shared parameter;
  note `BIND`. apitest no longer warns "resolved heuristically" for them.
- `apitest-gen record`: after writing, a lint of the written spec (section
  `LINT`) lists what `apitest.Run` would report before sending a request
  (`NOT_BUILDABLE`, `EXAMPLE_SCHEMA`, `HEURISTIC`, `BINDING`,
  `VALIDATION`, `AUTH`), each with place, message and fix.

- `apitest-gen record` is rebuilt around one file, `examples.record.yaml`,
  the one place the examples live. Per tag it lists the requests in the
  order they run, each with its path and query parameters, its body, the
  values it saves for later entries (`save: { dockId: /id }`, used as
  `"{{dockId}}"`), the fields apitest must not compare (`ignore`) and the
  recorded answer. `record -analyse` adds an entry for every case of the
  spec the file lacks, in apitest's order, bodies from the spec's examples
  or generated from the schema; it links id fields of bodies
  (`shipId`, `originDockId`) to the entries that create those records and
  proposes the `Tags` and `DeleteLast` apitest needs for that order. `record`
  without `-analyse` writes every entry into the spec (parameters, request
  body, response at its status; `examples.<name>` for an endpoint that
  appears more than once; a copy of a shared `$ref` where operations need
  different examples) and sends only the requests whose answer is missing,
  no longer fits the schema or is named by `-refresh`, to an empty instance
  (`-base-url`); the entries before them are sent again to build their
  data. Without `-base-url` nothing is sent, so the same file gives everyone
  the same examples. It reports entries apitest runs in another order
  (`ORDER`) and answers that changed (`DIFFERS`). This replaces the record
  run that read and wrote the data of a shared instance: `"params"`,
  `"seed"`, `"select"`, `"tables"`, `"bodies"`, `"$recorded"`,
  `"$suggestions"`, `-read-only`, `-all`, `-show-bodies` and
  `record-log.html` are gone.
- `apitest-gen record`: `save` also takes values from the request an entry
  sent (`request /pilotCode`, `request /`, `request path <name>`,
  `request query <name>`), for keys the client chooses. `-analyse` adds to
  each entry the saves later entries need: bindings that read the request
  of the producer, and parameters and id fields no binding names, matched
  by name to a value saved before or to the latest earlier entry with such a
  field.
- `apitest-gen record`: every entry has a `status`. `new` and `repeat` are
  sent; a request that went through becomes `approved` and is never sent
  again, its stored answer and request give the values later entries need;
  `ignore` is never sent (see above for how it is written). The
  entries before one to record are no longer sent again, so nothing is
  created twice; `DIFFERS` is gone. An approved answer that no longer fits
  the schema is `RESPONSE_STALE` (set `repeat`), an approved entry without
  answer `APPROVED_NO_ANSWER`. `-analyse` gives every new entry `status: new`
  and every entry without status one (`approved` with an answer, else
  `new`), and finds values no binding names at any depth of earlier
  answers and request bodies.
- `apitest-gen record`: with a `filter`, `save` reads the element the filter
  kept first (`/id`), then the filtered answer (`/0/id`); a filter added
  after the answer was stored filters the stored answer.
- `apitest-gen record`: `filter: {field: value}` keeps only the elements of a
  list answer (an array, or the one array field of an object) that match;
  keys are field names or JSON pointers, values may use placeholders. The
  response gets `x-apitest-compare-unordered: true`, so apitest finds the
  elements anywhere in the list.
- The resource model of `apitest-gen` tells more shapes of a spec apart:
  - a DTO with a longer stem that a path names (`DockDetailRead` with
    `/DockDetail/{id}`) is a resource of its own instead of part of `Dock`,
    so the read of a detail no longer counts as a read of the dock;
    `Upsert` bodies create and update; a suffix after `With`, `And`, `Of`,
    `By` stays (`ShipsWithDetails`);
  - a list parameter (`/Dock/Phase/{phase}`) filters and is no key;
  - the parent is the last resource of the chain of keys a path starts
    with, the longest one wins (`/Planet/{p}/Moon/{m}/Garden`: Moon); a
    filter like `/Ship/Planet/{p}` is no parent;
  - relations: fields that hold the key of a resource above
    (`MoonCode`, `PlanetCode`) or of any other resource by its name
    (`PilotId`); generated and created records take the key of the first
    record of that resource, updates keep it;
  - `/Pilot/{code}` returning a `PilotCard` addresses the `PilotCard`;
  - a path parameter no segment names takes the one resource with a key of
    that name (`/view/{planetCode}/{moonCode}`);
  - an update whose body belongs to no resource changes the record read at
    its path; a DELETE takes the resource read or updated at its path;
  - a resource created below different resources is split per place
    (`PlanetPerson`, `MoonPerson`);
  - a read that names one more record (`/Planet/{p}/Report/booking/{b}`)
    is a resource of its own; a read without a key of its resource or of a
    resource above (the dock of a ship) belongs to no resource;
  - `"$model"` takes `"params"`, which maps a path parameter whose name is
    no field (`{"Ship": {"params": {"registry": "ShipCode"}}}`).
- A write of no resource (`PUT /Dock/{code}/Ship/{s}/link`) changes the
  records in its path; the examples after it leave out the fields it may
  change (`SIDE_EFFECT`) instead of showing stale values.
- A key the server assigns to a created record is left out of the examples
  that read it (`CREATED_KEY`), unless the read DTO requires it.
- Path parameters of every resource in a path get the key of its record
  (before only the first one); a deleted record is replaced by one the
  test created. Keys in a POST path that ends with a key belong to the
  created record; keys of a default body fit the path parameters that hold
  them later.
- `mandatoryFields` also rejects an empty object `{}`.
- `SNAPSHOT_MISMATCH` is one message per record and read: both requests as
  sent, the keys of the record, every differing field with both values and
  the `IgnoreFields` entry that fixes it. `SNAPSHOT_SHORT` lists requests,
  counts, checks and reasons on lines of their own; multi-line messages are
  indented.
- `"$snapshot"` runs in the order of `defaults.json`; `review` writes the
  entries in the order of the paths in the spec, with an empty `"seed"`.
- `"seed"` in a `"$snapshot"` entry keeps fields of the chosen elements,
  one set per record. A later `"from"` takes them as placeholders
  (`{code}`, `{Book.code}`) and is sent once per set; the elements of all
  answers are searched together. Elements without a value in a seed field
  are not chosen.
- `apitest-gen -debug` saves `global-dict.json` even if the run fails; the
  spec stays unchanged.
- `apitest-gen -ignorelinting` lists fetched data and examples that violate
  their schema as `LINT_IGNORED` instead of stopping the run.
- `"$snapshot"` validation: elements that `mandatoryFields` or
  `equalFields` reject are no longer checked against the schema, so an
  empty `{}` in a list no longer stops the run.
- Records that come from the `followingDetails` of another resource no
  longer crash the snapshot (nil pointer when a read returned another
  value), and the request they came from is not sent a second time.
- With `-base-url` nothing is taken from the examples of the spec: existing
  examples are replaced (as with `-overwrite`) and the requests of the
  snapshot no longer fill placeholders from parameter examples.
- `apitest-gen` (preview), a command line tool next to the library:
  `apitest-gen dict -spec openapi.yaml -dict global-dict.json` creates or
  updates a global dictionary with one node per DTO field and parameter.
  Constraints come from the spec; values are generated deterministically
  (`-seed`), shared by fields with the same name and write-protected on
  later runs (`-repair` regenerates values that no longer fit).
  Values for `pattern` come from a generator that builds candidates from
  the regex syntax tree and checks each one against the original ECMA-262
  pattern the way apitest validates it, lookaheads included; unsolvable
  patterns are reported as `PATTERN_PENDING`, never filled wrongly.
  Readable values come from gofakeit by field name and DTO (City, Zip,
  FirstName, User.Name, "Brave Garden" for Garden.Name, versions, phones, …),
  matched by whole words. Compound names (`PlanetCode`) share one value
  across DTOs and parameters, generated for the strictest place first;
  generic names (`Name`, `Id`) do not.
- `apitest-gen apply` (the default command) updates the dictionary and
  writes missing or invalid examples into the spec, in place or with `-out`:
  parameter examples, request bodies and 2xx responses, at the `$ref`
  target for shared objects, never next to a `$ref`, never next to named
  `examples`. Comments and key order are kept; a second run changes nothing.
  `defaults.json` sets values everywhere (`Name`, `Dto.Name`,
  `operationId.name`), generic path ids (`/pilots/{id}` or `pilotId` derived
  from the path), extensions (`operationId.x-apitest-verify`) and bindings
  (`{"bind": "createPilot", "pointer": "/Id"}`, written as `x-apitest-bind`
  or `links`). A default that violates a schema stops the run before
  anything is written; the written spec is loaded again before it replaces
  the original.
- `apitest-gen discover -defaults defaults.json -base-url <url>` fetches
  sources such as `{"from": "GET /Planet", "pick": "/[Active=true]/Code"}`
  from a running environment, in the order their placeholders need
  (`GET /Planet/{planetCode}/Moon`), and writes the values to a file
  for `apply -defaults defaults.json,defaults.qa.resolved.json`. `apply
  -base-url` does the same in memory. Only GET requests are sent; the token
  comes from an environment variable (`-token-env`) and is never printed.
- `apitest-gen apply` replaces every invalid example apitest validates: also
  in error responses and in `components.schemas` (missing ones are not
  added there). Invalid curated named examples are kept and reported as
  `EXAMPLE_NAMED_INVALID`.
- `defaults.json` values for fields that hold a DTO or a list of DTOs
  (`"Ship.Pilot": {"Name": "Ada"}`) are merged into the generated value and
  into existing examples; fields left out keep their values, a list sets
  the length, readOnly/writeOnly fields are left out where they are not
  allowed. Previously such keys were ignored.
- `"#/components/schemas/<Dto>": {…}` in `defaults.json` sets a DTO itself,
  also a free one: its schema example and every body, field and list
  element of that type. A key that names a DTO instead of a field is
  reported with this key as a hint.
- `apitest-gen review` evaluates what apitest and apitest-gen would report
  (spec findings such as heuristic bindings, missing 401/403, invalid
  examples and links, cases that cannot be sent, values the generator
  cannot create, wrong or unused defaults) and writes the fixes as data
  into `defaults.json` (created if missing): values, the ids used so far
  and `$snapshot`. It prints the resource model and proposes no bindings:
  the examples follow the bindings apitest finds by itself. No comments
  are written; the reasons are printed. The next `apitest-gen` run takes
  the entries into dictionary and spec. Keys in `$rejected` are not
  proposed again.
- `defaults.json`: `null` marks a value still to be filled in
  (`DEFAULT_TODO`); `apply` creates an empty defaults file if it is missing.
- `apitest-gen` (apply) verifies before it saves: the new spec is written
  to a temporary file and loaded like apitest does, and every entry of the
  defaults is checked against it (key matched and written, binding present
  with its producer and a pointer that finds a value in the producer's
  example, extension set, written examples valid). With a problem nothing
  is written, the problems are listed and the exit code is 1.
  `DEFAULT_UNUSED`, `SHARED_PARAM_CONFLICT` and `BIND_NOT_WRITTEN` now stop
  the run.
- Examples follow the data through the run. `apitest-gen` detects the
  resources of the spec (`BookRead`, `BookUpdate` → `Book`, keys from the
  path parameters, lists, reads, creates, updates, deletes, `Article`
  below `Book`) and gives each resource records: fetched from a running
  instance with `-base-url` (GET only; `"$snapshot": {"Book": {"from":
  "GetBooks", "count": 3}}`), or generated and kept in
  `global-dict.json` (`records`). The cases are played in the order
  apitest runs them, with the Config from `"$apitest"` (`MethodOrder`,
  `DeleteLast`, `Tags`, …). An update sends new values and changes the
  record; every path parameter, body, response and list example shows the
  record as it is at its case, so a GET after a PUT expects what the PUT
  sent. Lists with their own key parameter show only matching records,
  records created by a POST are followed through the bindings apitest
  uses, `Response.Id` is the key, and a `Message` field reads
  `Successfully updated Book` or `Error while updating Book`. Key defaults
  select the record (`"/Book/id/{id}": 8`), keys fit the patterns of their
  path parameters, and a parameter object shared by paths of different
  records is copied into the path (`PARAM_INLINED`). `"$model"` corrects
  the model. No links and no extensions are written for this.
- `"from"` in `"$snapshot"` takes the request itself
  (`"/BookPreset/Tier/A1?bookCode=abc"`), an operationId still works.
  `review` proposes the request with every parameter it knows (key of a
  record of the last run, default, record field of the same name), keeps
  unknown ones as `{tier}` and writes the path template, the filled values
  and their source into `"$comment"`. A list with a path parameter of
  unknown meaning is no automatic source any more.
- `"validation"` in `"$snapshot"` selects the records: `mandatoryFields`
  (set, not empty), `equalFields` (`{"Book.Author": "tom"}`) and
  `followingDetails` (`"/book/{id}/details"` must answer for the element,
  with its values in the placeholders). The list is searched until `count`
  elements pass. The answers of the detail requests become data too:
  fields of the same resource, records of another resource, or the example
  of an operation of no resource. Field paths may name fields, DTOs and
  resources (`Book.Author`, `BookRead.BookDetail.Author`). A path or
  request that fits nothing in the spec stops the run. `review` writes the
  block empty.
- A `"$snapshot"` entry without `from` generates the records of that
  resource and fetches nothing, also with `-base-url` (`GENERATED`); for
  endpoints apitest does not test that only need a valid example. Lists
  below it are generated too.
- The model takes an object with one list for a page only with a list
  field like `items` or a DTO named `…Page`/`…List`; a Pilot with its
  Ships stays a read of the Pilot.
- `verify` also plays the cases of the written spec, built and ordered by
  apitest's own code, on the start records and checks every example
  (`EXAMPLE_STALE`, `PARAM_NO_RECORD`); a snapshot that fails, is short or
  disagrees with itself stops the run too. Nothing is written then.
- Removed: bound parameters taking the producer's example and response
  examples following the path inside `apply`; the records do both,
  consistently.
- A path default for a parameter object shared by several paths selects
  the record when the parameter holds a record key; otherwise it is
  reported as `SHARED_PARAM_CONFLICT`.
- `review` lists a missing body example once per operation, not once per
  case.
- New `links` are written in block style, one link per line, instead of
  one long flow mapping.
- `examples/path-conflict`: what happens with `/book/{id}` next to
  `/book/{class}`, tested with `check`, `review`, `apply` and apitest,
  before and after the fix.
- Inline schemas outside any DTO (e.g. a response `{type: object,
  properties: …}`) get generated values instead of `EXAMPLE_INCOMPLETE`.
- Applied defaults are kept in `global-dict.json` (`DICT_FROM_DEFAULTS`),
  except operation-scoped ones, so the dictionary shows the values the spec
  uses and a second run reports nothing.
- Free objects (`type: object` without `properties`) get `{}`, or entries
  from a typed `additionalProperties`, `minProperties` and `required`,
  instead of `NO_VALUE`.
- `apitest-gen check -spec openapi.yaml` (and `apply -check`) reports every
  case apitest could not send (`NOT_BUILDABLE`, with the reason) and every
  example that violates its schema, with exit code 1 for CI. It uses
  apitest's own case building, bindings and request preparation; values
  from the defaults count like `Config.Params`.

### Fixed

- `apitest-gen record -analyse` no longer proposes a `Tags` order that
  apitest refuses ("Config.Tags lists … before …, but … depends on …
  through bindings"): the bindings of path parameters come first, and a body
  reference against them is reported as `ORDER` with the binding instead.
  When `"$apitest".Tags` itself contradicts the bindings, the error lists
  the bindings behind it.
- `apitest-gen`: a key longer than 128 characters (a long path) is written in
  its usual form also as the key of a list item (`- GET /long/path:`), with a
  mapping or list as its value, and as the first key of a mapping value.
  Before, the explicit form `? key` / `: value` stayed in these places or was
  turned into invalid YAML ("Invalid child element in a block mapping").
- `apitest-gen`: a request body or list item written inline as `allOf` of
  several `$ref`s (as code generators write a type with `x-go-type`, e.g.
  `[{$ref: ShipBase}, {$ref: ShipExtra}, {x-go-type: ShipCreate}]`)
  belongs to the resource its path names. Before, such a POST was no
  create and such a GET no list: the POST body did not become the record,
  and the examples of POST response, GET and list did not show it.
- `apitest-gen`: an inline `allOf` of several `$ref`s with fields of its
  own (`[{$ref: ShipBase}, {$ref: ShipExtra}, {properties: {Registry: …}}]`)
  is recognized as well; a field only such a body or list element declares
  belongs to the record, so the list after the POST shows the value it sent.
  A property declared again in another part of an `allOf` (to add a
  description or readOnly) keeps its first declaration instead of
  replacing it, which made apply stop with `EXAMPLE_INCOMPLETE`.
- `apitest-gen`: the body of an update no longer contains the readOnly
  fields of a nested DTO (the `Id` of a nested `Person`), which made apply
  stop with `EXAMPLE_INVALID`; the record keeps them after the update, so
  the following GET examples still show them.

## v0.1.10

### Changed

- `Config.DisableWarnings` also drops the spec findings from the report.
- The report sections are collapsible (`<details>`) with counts in their
  titles; warnings, errors and deviations are open, the rest is closed.
- Deviation entries accept case names as copied from `go test -v` or an IDE:
  a leading test name, the `NumberCases` prefix and spaces are handled.
- A failed case names deviation entries that match its name but accept
  another result, e.g. `accepts 200 → 400, the result is 200 → 404`.
- `make update-golden` only passes `-update` to the report package.

## v0.1.9

### Changed

- The `invalid-token` case sends the first 100 characters of the real token
  (its first half if it is shorter) instead of a token with an inverted
  signature, so APIs that only decode tokens behind a validating gateway
  reject it as well.
- `Config.SkipAuthCases` leaves the listed cases out completely; they no
  longer appear as `SKIPPED` subtests or in the report.
- `Config.DisableWarnings` also drops the warnings from the report.

### Features

- `Config.TamperToken` sets how the `invalid-token` token is derived;
  `apitest.TruncateToken` (default) and `apitest.TamperSignature` (the
  previous behaviour) are provided.

## v0.1.8

### Features

- `Config.MethodOrder` orders the regular cases of a group by method, e.g.
  `{"POST", "PUT", "GET"}`; DELETE stays last, bindings still win.
- `Config.DeleteLast` runs the DELETEs of all groups after every other case,
  so a DELETE cannot remove seed data that a later group uses through
  `Params`.
- `Config.NumberCases` prefixes subtest names with their position
  (`07_Organization/createOrganization/valid`); numbers are stable under
  `-run`, case names in deviations, hooks and results stay unchanged.
  `CaseResult.Number` and the JSON report carry the number.
- `Config.ReportPassedDetails` shows passed cases with request, response,
  headers and `curl` command in the report.
- `Config.DisableWarnings` keeps warnings out of the `go test` output; the
  report still lists them.

## v0.1.7

### Report

- Every failed case shows the response headers, also when the response has
  no body, e.g. to see whether a 401 came from the API or from a proxy
  (`WWW-Authenticate`, `Server`, `Via`).
- The `curl` command contains the request headers as sent, including the
  ones set in `Hooks.BeforeRequest`.
- Header values are masked if the name looks secret (`X-Api-Key`,
  `X-Signature`, `X-Session-Id`, names containing a `Redact` field), in
  addition to `Authorization`, `Cookie` and `Set-Cookie`.

## v0.1.6

### Features

- `Config.SkipAuthCases` reports the listed authentication cases
  (`AuthUnauthorized`, `AuthInvalidToken`, `AuthForbidden`) as `SKIPPED`,
  e.g. `invalid-token` where no proxy validates tokens.

## v0.1.0 – v0.1.5

First public version.

### Features

- `apitest.Run` derives test cases from the examples of an OpenAPI 3.0/3.1
  spec (Swagger 2.0 is converted) and runs each case as a subtest named
  `<Tag>/<operationId>/<example>`.
- Three checks per response: status code (undocumented codes fail), schema
  (types, formats, required fields, `additionalProperties`, headers, content
  type) and the expected example (`subset`, `exact`, `schema`; arrays
  optionally unordered).
- Parameters from bindings (`x-apitest-bind`, OpenAPI `links`, heuristic),
  `Config.Params` and examples; values from `Location` headers; bound keys
  follow the value actually stored after a PUT.
- GET check after every write and 404 check after DELETE, optionally with
  polling for asynchronous processing.
- Resource groups ordered by their dependencies; cycles and contradicting
  `Config.Tags` are reported before the first request; dependents of a
  failed producer are skipped with the cause.
- Authentication cases: without token, with a manipulated token and with a
  token that lacks rights (`x-apitest-forbidden`). An unauthorized DELETE
  that succeeds skips the regular DELETE.
- Accepted deviations with reason and expiry date, exact matching, warnings
  before expiry, report of unused entries.
- `go test -run` on a single case runs its producers as preconditions.
- Markdown report after every group (partial report on abort or deadline),
  optional JSON report, redaction of tokens, manipulated tokens, API keys in
  URLs and error messages, `curl` commands with `$TOKEN` placeholders.
- `Config.DisableReports` turns off all report files; results are then only
  reported through `go test` and the returned `Result`.
- Specs without any `security` get `Config.Token` as bearer token for every
  operation, reported as a spec finding. Whitespace, line breaks and a
  `Bearer ` prefix are removed from tokens before sending.
- `Config.Params` keys of the form `"<operationId>.<name>"` set a value for
  one operation only.
- `pattern` keywords are ECMA-262 regular expressions: patterns that Go's
  `regexp` does not support (lookahead, lookbehind, backreferences) are
  evaluated with an ECMAScript-compatible engine with a one-second timeout.
- The binding heuristic matches field names case-insensitively, finds the
  collection POST behind literal path segments, by the resource name or by a
  body field, uses a POST of another tag if it is named after the resource,
  and never adds a binding that would create a cycle.
- The `invalid-token` case changes the token much more: a JWT keeps its
  header and claims, gets the extra claim `"apitest": "invalid-token"` and an
  inverted signature; opaque tokens get every second character changed.
  Previously only one bit of the signature was flipped.
- A heuristic binding whose successful producer returns no value falls back
  to `Config.Params` with a warning instead of skipping its dependents.
- A failed `unauthorized` or `invalid-token` case that the API answered
  with 2xx explains what was sent (no token, or the manipulated token),
  since the report redacts both tokens.
- Readiness check, per-request timeout, no retries, protection against
  writing requests to non-local hosts, expiry check for static JWTs.

### Quality

- Component tests against a built-in test API with 13 fault switches; each
  switch changes exactly the expected cases.
- Reference test against the Swagger Petstore v3 in a container
  (`examples/petstore`), with six classified deviations.
- Loading and planning the GitHub REST description (1,231 operations) takes
  about 2 s.
