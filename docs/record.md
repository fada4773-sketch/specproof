# apitest-gen record: examples in one file

`apitest-gen record` keeps the examples apitest needs in one file,
`examples.record.yaml`. For each tag, the file lists the requests in the order they run, each with
the answer the instance gave and a status. The file is the one place the
examples live: `record` writes them into the spec, and it only sends the
entries whose status says so (`new`, `repeat`). A request that went through
becomes `approved` and is not sent again.

The goal: apitest runs green against an **empty** test environment (a test
container with a fresh database), on every machine, for everyone.

- [The idea](#the-idea)
- [Workflow](#workflow)
- [The record file](#the-record-file)
- [Order](#order)
- [What a run does with each entry](#what-a-run-does-with-each-entry)
- [How the entries become examples in the spec](#how-the-entries-become-examples-in-the-spec)
- [Commands and flags](#commands-and-flags)
- [Findings](#findings)
- [Common situations](#common-situations)
- [Limits](#limits)
- [Moving from the earlier record](#moving-from-the-earlier-record)

## The idea

apitest builds every request from the examples of the spec and compares the
answer with the example. In an empty database, the examples must show exactly
the data the requests before them created: the first dock gets id 1, a
ship refers to that dock, and the list of docks holds what was created up
to that point.

`record` gets there by asking the instance itself. It sends the requests in
the order apitest will run them and stores what came back. Each entry is
sent once: after a successful request its status is `approved`, and later
runs never send it again. The values later entries need come from the
stored answers and requests, so a run that adds one new entry sends that one
request and nothing else. Nothing is created twice, and no create collides
with data an earlier run made.

The answers are stored once. Later runs reuse them and send nothing, unless
you set an entry to `repeat` (or name it with `-refresh`).

## Workflow

```sh
# 1. once: write an entry for every case of the spec
apitest-gen record -spec openapi.yaml -analyse

# 2. check examples.record.yaml: order, values, links between entries;
#    put the proposed order into defaults.json and into the test (see "Order")

# 3. start the instance (empty for the first run) and record the answers
apitest-gen record -spec openapi.yaml -base-url http://localhost:8080/api

# 4. run the test against an empty instance
go test ./...
```

Commit `examples.record.yaml` and `defaults.json` together with the spec.

Later:

| Situation | Command |
|---|---|
| The spec was regenerated and lost its examples | `apitest-gen record -spec openapi.yaml` (no instance needed) |
| New endpoints in the spec | `apitest-gen record -spec openapi.yaml -analyse`, then step 3: only the new entries are sent |
| A DTO changed | `record` reports `RESPONSE_STALE`; set those entries to `status: repeat`, then step 3 |
| Record one tag or endpoint again | `status: repeat` in the file, or step 3 with `-refresh Ship` / `-refresh createShip` |

Every run writes the spec in place (or to `-out`). Comments and key order
of the spec and of the record file stay.

## The record file

```yaml
# Examples for apitest, written by "apitest-gen record".
# ...

Dock:
  - POST /docks:
      status: approved
      body: {capacity: 19, name: North}
      save: {dockId: /id}
      response:
        status: 201
        body: {capacity: 19, id: 1, name: North}
  - GET /docks/{dockId}:
      status: approved
      path: {dockId: '{{dockId}}'}
      response:
        status: 200
        body: {capacity: 19, id: 1, name: North}
  - GET /docks:
      status: approved
      response:
        status: 200
        body:
          - {capacity: 19, id: 1, name: North}
Ship:
  - POST /ships:
      status: approved
      body: {dockId: '{{dockId}}', name: Comet}
      save: {shipId: /id}
      response:
        status: 201
        body: {dockId: 1, id: 1, name: Comet}
Booking:
  - POST /bookings:
      status: new                # record sends this request
      body:
        shipId: '{{shipId}}'
        route: {departure: "2027-01-15T08:00:00Z", originDockId: '{{dockId}}'}
        crew:
          - {pilotName: Ada, role: CAPTAIN}
      save: {bookingId: /id}
      ignore: [createdAt]
      response:
cleanup:
  - DELETE /docks/{dockId}:
      status: approved
      path: {dockId: '{{dockId}}'}
      response:
        status: 204
```

### Sections

The top level holds sections, and each section holds a list of entries. `-analyse` names a
section after the tag of its endpoints (`Dock`, `Ship`, …) and puts the
DELETEs that run after all tags into a last section, `cleanup`. A section
name is only a heading: which endpoint an entry calls comes from the entry
itself. You can rename sections or move an entry into another one. The order
of the entries from top to bottom, across all sections, is the order the
requests are sent in.

### Entries

An entry is a one-key mapping: the endpoint, then its settings.

```yaml
  - POST /docks:          # "METHOD /path" exactly as in the spec ...
      body: {...}
  - createDock:           # ... or the operationId
      body: {...}
  - GET /docks            # an entry without settings needs no colon
```

The method may be written in any case (`post /docks`). The path must be the
path of the spec, with its `{parameters}`.

### Settings of an entry

| Key | Content | Notes |
|---|---|---|
| `status` | `new`, `approved`, `repeat`, `ignore` | Whether `record` sends the request. See [Status](#status). |
| `name` | example name | Needed when an endpoint appears more than once; every entry of that endpoint then needs its own name. Without `name`, the entry is apitest's `default` case. |
| `path` | `{param: value}` | Every path parameter of the endpoint needs a value. |
| `query` | `{param: value}` | Required query parameters need a value; optional ones are sent only if listed. |
| `body` | the request body | JSON content written as YAML, nested objects and lists included. Required when the spec marks the body as required. |
| `save` | `{name: source}` | Values for later entries, from the answer or from the request this entry sent. See [Saved values and placeholders](#saved-values-and-placeholders). |
| `filter` | `{field: value}` | Keeps only the elements of a list answer that match. See [Filtering a list](#filtering-a-list). |
| `ignore` | `[field, ...]` | Fields apitest must not compare, such as time stamps or generated codes. They go into `x-apitest-ignore` of the operation. Names match at every level, and JSON pointers (`/items/*/updatedAt`) work too. |
| `response` | the recorded answer | Written by `record`. |

`response` holds:

| Key | Content |
|---|---|
| `status` | the HTTP status the instance answered |
| `headers` | only the headers a `save` reads from (`{Location: /ships/1}`), so later runs find the value without sending |
| `body` | the answer body, in the order of its fields |

### Status

| Status | Meaning |
|---|---|
| `new` | Not sent yet. `record` sends it, stores the answer and sets `approved`. |
| `approved` | Sent and answered. Never sent again; its stored answer gives the values later entries need and becomes the example. |
| `repeat` | Send it again on the next run, store the new answer, then `approved`. |
| `ignore` | Never sent. apitest still runs the case, so the entry is written into the spec with values that fit the schema: its own values and stored answer where they fit, the rest generated from the schema (see [Ignored entries](#ignored-entries)). The status stays `ignore`, the record file is not changed. A stored answer still gives its saved values. |

`-analyse` gives every new entry `status: new`, and every entry of the file
without status one: `approved` if it has an answer, else `new`. A request
that fails keeps its status, so the next run tries it again.

Since approved entries are not sent again, the instance keeps the data they
created. An entry set to `repeat` must still find the records it refers
to there. A DELETE that ran (an approved entry in `cleanup`) removed its record,
so entries that need that record can only be repeated together with the
entries that create it, or against a fresh instance with every entry they
depend on set to `repeat` too.

### Saved values and placeholders

`save` gives a value a name; `{{name}}` uses it in any later entry: in
`path`, `query`, `filter` and anywhere in `body`, however deeply nested.

| Source | Value |
|---|---|
| `/id`, `/items/0/code` | a JSON pointer into the answer body |
| `/` | the whole answer body |
| `header Location` | a header of the answer; `Location` gives its last path segment, like apitest |
| `request /code` | a JSON pointer into the body this entry sent, after its placeholders were filled in |
| `request /` | the whole body sent |
| `request path dockId` | the value of a path parameter sent |
| `request query zone` | the value of a query parameter sent |

Values from the request are useful when the client chooses a key: a POST
sends `{pilotCode: P001, …}` and answers without a body, and later entries
address the pilot by that code.

```yaml
Pilot:
  - POST /pilots:
      body: {pilotCode: P001, name: Ada}
      save: {pilotCode: request /pilotCode}
  - GET /pilots/{pilotCode}:
      path: {pilotCode: '{{pilotCode}}'}      # takes the value with its type
Mission:
  - POST /missions:
      body: {pilotCode: '{{pilotCode}}', title: 'Flight of {{pilotCode}}'}   # inside a text: "Flight of P001"
```

Quote a value that starts with `{{`, since YAML would read it as a mapping.
A placeholder must be saved by an entry above it. A later `save` with
the same name replaces the value for the entries after it. Without
`-base-url`, the values come from the stored answers and from the requests
in the file, so a run without instance fills in the same values.

### Filtering a list

Some endpoints answer with a list that holds more than the entry is about:
all pilots, all missions of a pilot. `filter` keeps only the elements whose
fields have the given values, and only those are stored:

```yaml
  - GET /missions:
      query: {pilotCode: '{{pilotCode}}'}
      filter: {id: '{{missionId}}'}
      response:
        status: 200
        body:
          items:
            - {id: 2, pilotCode: P001, title: Return}
          total: 2
```

- The list is the answer itself if it is an array, else the one field of
  the answer that is an array (`items` above). An answer with no or with
  several arrays cannot be filtered (`FILTER`).
- A key of `filter` is a field name of the elements (any case) or a JSON
  pointer into them (`/route/originDockId`). Values are compared as text, so
  `7` matches `"7"`. Every key must match.
- `save` reads after the filter: a pointer first looks into the first
  element that matched (`/id`, `/route/originDockId`), then into the
  filtered answer (`/0/id`, `/total`); `/` is that element.
- A filter added to an entry whose answer is already stored filters the
  stored answer on the next run, without sending; the file is updated.
- No element matches: the empty list is stored and reported (`FILTER`).

apitest compares lists by position, and the element may sit anywhere in the
list it gets. So for a response with a filtered entry, `record` sets
`x-apitest-compare-unordered: true`: apitest then looks for each element of
the example anywhere in the answer. Elements apitest gets in addition
are fine; it compares as a subset.

## Order

The answers are recorded in the order of the file. apitest only sees the same
data if it runs the cases in that same order. Two things decide the order:

**1. The order of the file.** `record` sends top to bottom.

**2. The order of apitest.** apitest orders by itself:

- Tags (groups) follow the bindings of path parameters: a tag whose paths
  take a value from another tag's answers runs after it. Then come the tags
  listed in `Config.Tags`, in that order. The rest run alphabetically.
- Within a tag: POST (create), GET with path parameter (read), GET list,
  PUT/PATCH (update), other methods, 4xx examples, authentication cases,
  DELETE.
- DELETEs of tags that others depend on run at the end, in reverse order.
  With `DeleteLast` all DELETEs do.
- `MethodOrder` changes the order of the methods within a tag.

apitest only knows the dependencies of **path parameters**. An id inside a
body (`dockId` in a ship, `shipId` in a booking) is invisible to it, so
without help it would run Booking before Dock (alphabetically) and delete
the dock before a ship needs it.

`-analyse` finds these body references: a body field named like a value
apitest binds (`dockId`) or ending in it (`originDockId`). It sorts the tags
so every body finds its records, puts the DELETEs last, and prints the
settings apitest needs for that order:

```
ORDER
  Bodies refer to records of other tags (an id field named like a value of another tag), so apitest must run
  the tags in this order and every DELETE last. Set it in both places:
    defaults.json:  "$apitest": {"Tags": ["Dock", "Ship", "Booking"], "DeleteLast": true}
    your test:      apitest.Config{Tags: []string{"Dock", "Ship", "Booking"}, DeleteLast: true, …}
```

`Tags` also limits apitest to the tags it lists. The proposal always lists
every tag of the spec.

The bindings of apitest come first. If a tag's path parameters take a
value from another tag (`getDockWithPermit` takes `{dockPermitId}` from
`createDockPermit`), apitest runs the other tag first, whatever the bodies
say. A body reference against such a binding (`createDockPermit` sends
`dockId`) cannot be satisfied by the order of the tags. `-analyse` leaves it
out of the proposal and reports it as `ORDER`, naming the binding. Often
the binding is a guess of apitest (heuristic) and wrong; then declare the
right one with `x-apitest-bind` at that parameter.

If `"$apitest".Tags` itself contradicts the bindings, apitest refuses the
order (`Config.Tags lists "Dock" before "DockPermit", but …`). `record`
then lists the bindings behind it, so you can see which operation takes
which value from which tag.

`record` reads `"$apitest"` from `defaults.json` (`Tags`, `DeleteLast`,
`MethodOrder`, `IncludeOps`, `ExcludeOps`, `IgnoreFields`). After writing
the spec, it compares the file with the order apitest will run. If an entry
runs earlier in apitest than an entry above it in the file, it reports
`ORDER` with both lines. Then either move the entry or change `"$apitest"`
and the test config, then record again.

If you change the order by hand, follow the same rule: an entry only finds
what the entries above it created, and apitest has to run them in that
order too.

## What a run does with each entry

`record` first checks the whole file against the spec: endpoints, names,
parameters, bodies, placeholders, and every request against its schema.
Any problem stops the run before a request is sent.

Then it goes through the entries top to bottom. Only entries with status
`new` or `repeat` (or named by `-refresh`) are sent; the values the others
provide come from their stored answers and requests. Each entry ends in one
of these states:

| State | When | What happens |
|---|---|---|
| `recorded` | Status `new` or `repeat`, or named by `-refresh`, and the instance answered 2xx. | The answer is stored, the status becomes `approved`. |
| `kept` | Status `approved`. | Nothing is sent; the stored answer is written into the spec. |
| `stale` | Status `approved`, but the stored answer no longer fits the schema. | Reported as `RESPONSE_STALE`; the old answer is still written. Set `status: repeat` to send it again. |
| `ignored` | Status `ignore`. | Not sent; written with values that fit the schema, generated where needed (`GENERATED`). |
| `failed` | The instance rejected the request (status other than 2xx, or no answer). | No more requests are sent; see below. |
| `not sent` | An entry to send after a failed one. | Nothing; its status stays. |

**When a request fails**, no further request is sent. The answers recorded
before it are saved in the file with status `approved`, and the spec stays
unchanged. The report shows what was sent and the answer of the instance,
for example `POST /ships answered 422: {"message":"dock 99 does not exist"}`.
Fix the entry and run again: only the entries still `new` or `repeat` are
sent.

**Without `-base-url`** nothing is sent. Stored answers are written into
the spec. An entry to send is a problem (`NEEDS_INSTANCE`), and the run lists
those entries.

## How the entries become examples in the spec

| Entry | Written to |
|---|---|
| `path`, `query` | `example` of the parameter |
| `body` | `example` of the request body's JSON media type |
| `response.body` | `example` of the response with the recorded status |
| `ignore` | `x-apitest-ignore` of the operation, merged with the fields already listed there |
| `filter` | `x-apitest-compare-unordered: true` on the response |
| a path parameter apitest would only guess | `x-apitest-bind` of the parameter (see [Bindings](#bindings)) |

The placeholders are filled in with the saved values of the stored answers.
The spec therefore holds plain values (`dockId: 1`); apitest takes the real
value from the earlier answer anyway, through its bindings.

### Ignored entries

An entry with `status: ignore` is never sent, but apitest runs its case,
so it needs examples that fit the schema. `record` writes:

- each path and query parameter: the value of the entry if it fits its
  schema and has no open placeholder, else a generated one; required query
  parameters without value get one too;
- the body: the body of the entry if it fits, else a generated one (without
  readOnly fields) if the body is required or the entry has one;
- the answer: the stored answer if the entry has one that fits the schema,
  else a generated answer (without writeOnly fields) at the lowest
  documented 2xx.

A generated answer cannot match what the instance returns, so that
response gets `x-apitest-compare: schema # apitest-gen record: generated
answer`: apitest checks it against the schema only. When the entry is
recorded later (`status: new`), its answer replaces the generated one and
`record` removes that extension again; one without the comment is left
alone. The generated values depend on `-seed` only, so every run writes the
same ones. Each ignored entry with generated values is listed as
`GENERATED`, with what was generated and what could not be.

### Bindings

apitest binds a path parameter to an earlier answer through `x-apitest-bind`,
an OpenAPI link, or a guess (`parameter "dockId" is resolved heuristically
from createDock (body /id); make it explicit with x-apitest-bind or links`).
`record` writes every guess as `x-apitest-bind`, so apitest no longer warns
and a later change of the spec cannot change the guess:

```yaml
components:
  parameters:
    DockId:
      name: dockId
      in: path
      x-apitest-bind: {from: createDock, pointer: /id}
```

The source is the `save` of the record file when the same operation saves
the value (`header Location`, `request /code` become `header: Location`,
`pointer: /code, source: request`). Otherwise it is the guess, checked
against the stored answer of the producer: an explicit binding reads only
that one place, while the guess also tries the `Location` header and the
request body, so `record` takes the place the stored answer has the value
at. If the record file takes the value from another operation than apitest
guesses, the guess is kept and a `BIND` note shows both. A parameter several
operations share (at the path, or a `$ref`) gets the binding once when all
of them take the value from the same place, else each operation its own copy.

**Named entries** (an endpoint that appears more than once) are written as
`examples: {<name>: {value: …}}` at each of those places. apitest makes one case per
name, `Dock/createDock/north` and `Dock/createDock/south`, and expects the
response example of the same name.

**Status:** an entry without name is apitest's `default` case, and apitest
expects the lowest documented 2xx for it. If the instance answers another
2xx (201 where the spec documents 200 and 201), `record` reports `STATUS`.
Give the entry a `name` or fix the spec. A status the spec does not document
at all is reported too.

**Shared places:** a parameter declared for the whole path, or a `$ref` to
`components` (parameters, request bodies, responses), serves several
operations. If they need different values, or one of them needs named
examples, the operation gets its own copy: the parameter is declared again
in the operation, or the `$ref` is replaced by a copy of its target. The other
operations keep the shared one. OpenAPI does not allow `example` and
`examples` side by side, so the form not used goes.

Operations without entries in the file keep the examples the spec has.

## Commands and flags

```
apitest-gen record -spec <openapi.yaml> -analyse [-file examples.record.yaml] [-defaults defaults.json]
apitest-gen record -spec <openapi.yaml> [-file examples.record.yaml] [-base-url <url>] [-refresh <entries>] [-out <spec>]
```

### `record -analyse`

Adds an entry for every case apitest runs that the file does not have yet,
in apitest's order (with the proposed order, see [Order](#order)). Nothing
is sent, and the spec stays unchanged.

- Entries already in the file stay as they are, with their values, comments
  and order. New entries are placed after the last entry of their section
  that apitest runs before them. The only changes to an existing entry are a
  `save` that a new entry needs and a `status` if it had none. The YAML is
  written in one style: list items indented, `{a: b}` without inner spaces.
- Every entry gets a status (see [Status](#status)).
- For each entry, it works out which values later requests need and adds them to
  the `save` of the entry that provides them:
  - path and query parameters that apitest binds to an earlier exchange.
    The binding reads the answer (`/id`, `header Location`), or the request when the
    client chose the value (`request /pilotCode`, for a POST that answers
    without the field);
  - id fields in bodies (`dockId`, `originDockId`, `pilotCode`) that refer to
    such a value;
  - parameters and id fields no binding names (a required query parameter
    `pilotCode`): a value already saved under that name, else the latest
    entry before it whose answer or request body has a field of that name,
    at any depth (`/route/originDockId`, `request /crew/0/pilotCode`).

  The name is the parameter's or field's name (`dockId`). A generic `{id}` gets
  the tag's name (`shipId` for tag `Ship`). A name already used for another
  value gets a number (`dockId2`).
- Bodies come from the examples of the spec. Without one, they are generated
  from the schema: `allOf` merged, the first branch of `oneOf`/`anyOf`, no
  readOnly fields, values seeded by `-seed`. Id fields that refer to another
  record get its placeholder.
- Required query parameters come from the spec's example, default or enum,
  else generated. Optional ones are left out.
- If the spec already holds a response example for a case, it becomes the
  stored answer, so that entry needs no request.
- Only regular cases get entries: no 4xx examples, no authentication cases. A
  case apitest cannot send (for example a `multipart/form-data` body) is left
  out (`SKIPPED`).

Check the file afterwards: values that must make sense to the instance
(names, codes, enum combinations) and links `-analyse` could not see (an id
field not named after the value it refers to) are yours to fix.

### `record`

Sends the entries with status `new` or `repeat` and writes the spec, as described in
[What a run does with each entry](#what-a-run-does-with-each-entry).

### Flags

| Flag | Default | Meaning |
|---|---|---|
| `-spec` | (required) | the OpenAPI file |
| `-file` | `examples.record.yaml` | the record file |
| `-analyse` | off | add the missing entries; nothing is sent, the spec stays unchanged |
| `-base-url` | (none) | the instance the requests go to, including the base path, e.g. `http://localhost:8080/api`; empty for the first run, then the one that holds the data of the approved entries; without it nothing is sent |
| `-token-env` | (none) | environment variable holding a bearer token, sent as `Authorization: Bearer …` |
| `-header` | (none) | extra header, `"Name: value"`; repeatable |
| `-refresh` | (none) | send these entries again whatever their status (except `ignore`), comma-separated: `all`, a section or tag (`Ship`), an operationId (`createShip`), `"METHOD /path"`, or `operationId/name` |
| `-defaults` | `defaults.json` | read for `"$apitest"` only (the order of apitest); several files comma-separated |
| `-out` | (in place) | write the spec there instead |
| `-dry-run` | off | send and report as usual, but save neither the record file nor the spec |
| `-seed` | `42` | seed for the values `-analyse` generates |

Exit codes: 0 success, 1 problems (listed under `FINDINGS`), 2 wrong usage.

### Output

```
apitest-gen record: examples.record.yaml → openapi.yaml

ENTRIES (requests to http://localhost:8080/api)
  ── Dock
  #01 POST /docks                        kept
  #02 GET /docks/{dockId}                kept
  ── Ship
  #05 POST /ships                    201 recorded  (status new)
  #06 GET /ships/1                   200 recorded  (status repeat)
  ── Booking
  #07 POST /bookings (main)              kept
  9 entries: 2 recorded, 7 kept; 2 requests sent

FINDINGS
  ...

LINT (the written spec as apitest.Run sees it: 12 of 12 cases can be sent)
  no findings: apitest.Run reports no warnings about the spec

FILES
  examples.record.yaml: 2 answers recorded and saved
  openapi.yaml: 4 examples written, 3 parameters got x-apitest-bind
```

## Findings

Problems (`PROBLEM`) stop the run: nothing is sent, or after a failed
request the spec stays unchanged. Notes (`NOTE`) are information.

| Code | Kind | Meaning | What to do |
|---|---|---|---|
| `UNKNOWN_OPERATION` | problem | An entry names no operation of the spec. | Write `METHOD /path` as in the spec, or the operationId. |
| `DUPLICATE` | problem | An endpoint appears twice without names, a name is used twice, or a named entry has no place for a named example (no body, no parameter). | Give each entry of that endpoint its own `name`. |
| `PARAMETER` | problem | A parameter the endpoint does not have, or a path or required query parameter without value. | Fix `path` / `query`. |
| `BODY` | problem | A required body is missing, the endpoint takes no body, or no JSON body. | Fix `body`. |
| `PLACEHOLDER` | problem | `{{name}}` that no entry above saves. | Add the `save`, or move the entry below the one that saves it. |
| `REQUEST_INVALID` | problem | A request violates its schema (shown with the JSON pointer and the rule). | Fix the value in the file. |
| `NEEDS_INSTANCE` | problem | Entries are to be sent (`new`, `repeat`, `-refresh`) and no `-base-url` is given. | Start the instance and pass `-base-url`. |
| `APPROVED_NO_ANSWER` | problem | An entry is `approved` but has no stored answer. | Set `status: repeat`. |
| `REQUEST_FAILED` | problem | The instance rejected a request; the report shows what was sent and the answer. | Fix the entry (or the data of the instance) and run again. |
| `SAVE_MISSING` | problem | The answer or the request has no value where `save` points, or a placeholder has no value because its entry was not answered. | Fix the source in `save`. |
| `FILTER` | problem / note | Problem: the answer of an entry with `filter` holds no list, or a placeholder of the filter has no value. Note: no element matched, so the empty list is stored; or a stored answer was filtered by a filter added later. | Check the filter and the answer in the record file. |
| `STATUS` | problem | The answer's status is not documented, or an entry without name gets another 2xx than the lowest documented one. | Give the entry a `name`, or document the status. |
| `SHARED` | problem | A shared place of the spec cannot get its own copy. | Declare the parameter or body in the operation. |
| `RESPONSE_SCHEMA` | note | A recorded answer violates the schema; apitest will report it too. | Fix the instance or the spec. |
| `RESPONSE_STALE` | note | The stored answer of an approved entry no longer fits the schema; it is written anyway. | Set `status: repeat` and record it again. |
| `ORDER` | note | apitest runs an entry before one above it in the file. | Move the entry, or set `"$apitest"` and the test config. |
| `NOT_RUN` | note | apitest does not run the case of an entry (`ExcludeOps`, `IncludeOps`, `Tags`, `x-apitest-skip`); its request still shapes the data of later entries. | Usually nothing. |
| `NOT_IN_FILE` | note | apitest runs cases the file has no entry for; they keep the examples of the spec. | `record -analyse` adds them. |
| `SKIPPED` | note | `-analyse` left out a case apitest cannot send. | See the reason. |
| `GENERATED` | note | An ignored entry got generated values (or some could not be generated). | Nothing; set the values in the entry to choose them. |
| `BIND` | note | A guessed binding was written as `x-apitest-bind`; or the record file takes the value from another operation than apitest; or the stored answer has no value where apitest guesses. | Check the source; declare `x-apitest-bind` yourself where it is wrong. |

## Lint

After the examples are in place, `record` loads the written spec the way
apitest does and lists what `apitest.Run` would report about it before it
sends a request, each finding with its place and how to fix it:

```
── LINT (the written spec as apitest.Run sees it: 11 of 12 cases can be sent)
   1. HEURISTIC  paths./moons/{moonName}.get.parameters[moonName]
      parameter "moonName" is resolved heuristically from createMoon (request body /name); make it explicit with x-apitest-bind or links
      fix: declare where the value comes from: x-apitest-bind: {from: <operationId>, pointer: /field} …
  1 findings: 1 HEURISTIC
```

| Kind | Meaning |
|---|---|
| `NOT_BUILDABLE` | apitest cannot send the case: a value is missing. |
| `EXAMPLE_SCHEMA` | An example violates its schema. |
| `HEURISTIC` | apitest only guesses where a parameter comes from (only for operations without entry, since `record` writes the others). |
| `BINDING` | An `x-apitest-bind` or a link cannot be used. |
| `VALIDATION` | The spec breaks a rule of OpenAPI. |
| `AUTH` | An operation with security documents neither 401 nor 403. |

The lint does not change the exit code: the spec is written already.
`apitest-gen check` remains the check for CI.

## Common situations

**Time stamps and generated codes.** A field the server sets differently on
every run (`createdAt`, `updatedAt`, an order number) cannot match a stored
answer. Fields with format `date-time`, `date` or `uuid`, and readOnly fields,
are only checked for presence by apitest. List any other such field under
`ignore`.

**The server answers with `Location` only.** Save from the header:

```yaml
  - POST /ships:
      body: {name: Comet, dockId: '{{dockId}}'}
      save: {shipId: header Location}
```

**Two records of the same kind.** Give each entry a name and its own saved
value:

```yaml
Dock:
  - POST /docks:
      name: north
      body: {name: North, capacity: 4}
      save: {northId: /id}
  - POST /docks:
      name: south
      body: {name: South, capacity: 6}
      save: {southId: /id}
Booking:
  - POST /bookings:
      body:
        shipId: '{{shipId}}'
        route: {originDockId: '{{northId}}', targetDockId: '{{southId}}'}
```

**Large nested bodies (`allOf`, `$ref`, nested objects and lists).** The body
in the file is plain JSON written as YAML. Whatever the schema composes is one
object here. `-analyse` builds it from the merged schema; short objects
are written on one line to keep the file compact. Placeholders work at any
depth (`crew[0].pilotId`).

**Data the API cannot create** (lookup tables, a tenant). Put it into the
seed of the test container, so `record` and apitest find it in the
"empty" instance, and use its values in the file directly.

**A colleague runs `record`.** Without `-base-url`, they get exactly the
examples of the file. With `-base-url`, only the entries still `new` or
`repeat` are sent, against their instance. The approved answers of others
stay and are not sent again.

**The DTO changed.** A new required field in a request makes the entry
`REQUEST_INVALID`: add the field to `body`. A stored answer that no longer
fits the schema is reported as `RESPONSE_STALE`; set `status: repeat` to
record it again. If it still fits (a new optional field), set `repeat` to
show the new field.

**An endpoint you do not want sent** (it sends mail, it deletes something
shared): `status: ignore`. It is never sent; the spec gets examples that fit
the schema, and a generated answer is compared by schema only (see
[Ignored entries](#ignored-entries)).

## Limits

- Request bodies are JSON only. Endpoints with `multipart/form-data` or
  form bodies get no entry.
- The first run should start with an empty instance, so the ids and lists
  are those apitest will see in its empty test environment. Later runs need
  the data of the approved entries, so record against the same instance or
  set the entries the new ones depend on to `repeat`.
- Only regular cases are recorded. 4xx examples and authentication cases are
  written into the spec by hand or by `apitest-gen apply`.
- Authentication: a bearer token (`-token-env`) or headers (`-header`).

## Moving from the earlier record

The earlier `record` read and wrote the data of a running instance and kept
its state in `defaults.json`. None of that is used any more. Remove these
keys from `defaults.json`, because `apply` would otherwise read them as values of
fields with those names: `"params"`, `"seed"`, `"select"`, `"tables"`,
`"bodies"`, `"$recorded"`, `"$suggestions"`. Keep `"$apitest"`. Then run
`record -analyse` and record once against an empty instance.

A record file of an earlier version has no `status`. Such an entry counts as
`approved` if it has an answer, else `new`; `-analyse` writes that status
into it.
