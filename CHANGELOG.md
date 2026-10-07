# Changelog

All notable changes are documented here. The project follows
[Semantic Versioning](https://semver.org/); before 1.0 minor versions may
contain breaking changes, which are listed here.

## Unreleased

### Moved

- The project lives at `github.com/fada4773-sketch/specproof`. The library
  is the package `apitest` in the subdirectory `apitest/`: import
  `github.com/fada4773-sketch/specproof/apitest`; the API is unchanged
  (`apitest.Run`, `apitest.Config`, …). The versions below were released
  under the previous module path `github.com/fada4773-sketch/apitest`.

### Features

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
  `ignore` leaves the entry out (not sent, not written into the spec). The
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
