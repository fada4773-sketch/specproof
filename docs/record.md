# apitest-gen record: examples in one file

`apitest-gen record` keeps the examples apitest needs in one file,
`examples.record.yaml`. For each tag, the file lists the requests in the order they run, each with
the answer an empty instance gave. The file is the one place the examples
live: `record` writes them into the spec, and it only sends a request when
an answer is missing or no longer fits the schema.

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
the order apitest will run them, to an empty instance, and stores what came
back. Because the requests are stored too, anyone can repeat this with
their own empty instance and gets the same answers. Nothing reads or
changes a shared environment, so no delete can break a later GET and no
create can collide with data that is already there.

The answers are stored once. Later runs reuse them and send nothing,
unless an answer is missing or the schema has changed so that it no
longer fits.

## Workflow

```sh
# 1. once: write an entry for every case of the spec
apitest-gen record -spec openapi.yaml -analyse

# 2. check examples.record.yaml: order, values, links between entries;
#    put the proposed order into defaults.json and into the test (see "Order")

# 3. start an EMPTY instance and record the answers
apitest-gen record -spec openapi.yaml -base-url http://localhost:8080/api

# 4. run the test against an empty instance
go test ./...
```

Commit `examples.record.yaml` and `defaults.json` together with the spec.

Later:

| Situation | Command |
|---|---|
| The spec was regenerated and lost its examples | `apitest-gen record -spec openapi.yaml` (no instance needed) |
| New endpoints in the spec | `apitest-gen record -spec openapi.yaml -analyse`, then step 3 |
| A DTO changed | step 3: only the entries whose answer no longer fits are sent again |
| Record one tag or endpoint again | step 3 with `-refresh Ship` or `-refresh createShip` |

Every run writes the spec in place (or to `-out`). Comments and key order
of the spec and of the record file stay.

## The record file

```yaml
# Examples for apitest, written by "apitest-gen record".
# ...

Dock:
  - POST /docks:
      body: {capacity: 19, name: North}
      save: {dockId: /id}
      response:
        status: 201
        body: {capacity: 19, id: 1, name: North}
  - GET /docks/{dockId}:
      path: {dockId: '{{dockId}}'}
      response:
        status: 200
        body: {capacity: 19, id: 1, name: North}
  - GET /docks:
      response:
        status: 200
        body:
          - {capacity: 19, id: 1, name: North}
Ship:
  - POST /ships:
      body: {dockId: '{{dockId}}', name: Comet}
      save: {shipId: /id}
      response:
        status: 201
        body: {dockId: 1, id: 1, name: Comet}
Booking:
  - POST /bookings:
      body:
        shipId: '{{shipId}}'
        route: {departure: "2027-01-15T08:00:00Z", originDockId: '{{dockId}}'}
        crew:
          - {pilotName: Ada, role: CAPTAIN}
      save: {bookingId: /id}
      ignore: [createdAt]
      response:            # empty: record sends this request
cleanup:
  - DELETE /docks/{dockId}:
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
| `name` | example name | Needed when an endpoint appears more than once; every entry of that endpoint then needs its own name. Without `name`, the entry is apitest's `default` case. |
| `path` | `{param: value}` | Every path parameter of the endpoint needs a value. |
| `query` | `{param: value}` | Required query parameters need a value; optional ones are sent only if listed. |
| `body` | the request body | JSON content written as YAML, nested objects and lists included. Required when the spec marks the body as required. |
| `save` | `{name: source}` | Values for later entries, from the answer or from the request this entry sent. See [Saved values and placeholders](#saved-values-and-placeholders). |
| `filter` | `{field: value}` | Keeps only the elements of a list answer that match. See [Filtering a list](#filtering-a-list). |
| `ignore` | `[field, ...]` | Fields apitest must not compare, such as time stamps or generated codes. They go into `x-apitest-ignore` of the operation. Names match at every level, and JSON pointers (`/items/*/updatedAt`) work too. |
| `response` | the recorded answer | Written by `record`. Empty (`response:`) means "send this request and store the answer". |

`response` holds:

| Key | Content |
|---|---|
| `status` | the HTTP status the instance answered |
| `headers` | only the headers a `save` reads from (`{Location: /ships/1}`), so later runs find the value without sending |
| `body` | the answer body, in the order of its fields |

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
- `save` reads the filtered answer: `/0/id` is the first element that
  matched.
- An entry sent again is compared with its stored answer after filtering.
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

Then each entry is in one of these states:

| State | When | What happens |
|---|---|---|
| `kept` | The answer is stored and still fits the schema. | Nothing is sent; the stored answer is written into the spec. |
| `recorded` | No answer yet, the answer no longer fits the schema, or `-refresh` names the entry. | Sent; the answer is stored in the file. |
| `sent` | Comes before an entry that is recorded. | Sent again to build the data the later entry needs; the stored answer stays. |
| `differs` | Like `sent`, but the instance answered otherwise than stored. | Reported as `DIFFERS`; the stored answer stays. |
| `stale` | The answer no longer fits the schema and no `-base-url` is given. | Reported as `RESPONSE_STALE`; the old answer is still written. |
| `failed` | The instance rejected the request (status other than 2xx, or no answer). | The run stops; see below. |
| `not sent` | Comes after a failed entry. | Nothing. |

Entries after the last one to record are not sent. The run sends the
first entry through the last one that needs an answer, because each request
can depend on the data the ones before it created. That is why the
instance must be **empty**: a database that still holds the data of an
earlier run gives other ids, and `DIFFERS` tells you so.

**When a request fails**, the run stops. The answers recorded before it are
saved in the file, and the spec stays unchanged. The report shows what was sent
and the answer of the instance, for example
`POST /ships answered 422: {"message":"dock 99 does not exist"}`. Fix the
entry and run again with a fresh, empty instance.

**Without `-base-url`** nothing is sent. Stored answers are written into
the spec. A missing answer is a problem (`NEEDS_INSTANCE`), and the run lists
the entries that need one.

## How the entries become examples in the spec

| Entry | Written to |
|---|---|
| `path`, `query` | `example` of the parameter |
| `body` | `example` of the request body's JSON media type |
| `response.body` | `example` of the response with the recorded status |
| `ignore` | `x-apitest-ignore` of the operation, merged with the fields already listed there |
| `filter` | `x-apitest-compare-unordered: true` on the response |

The placeholders are filled in with the saved values of the stored answers.
The spec therefore holds plain values (`dockId: 1`); apitest takes the real
value from the earlier answer anyway, through its bindings.

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

- Entries already in the file stay as they are. New entries are placed after
  the last entry of their section that apitest runs before them. The only
  change to an existing entry is a `save` that a new entry needs.
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
    entry before it whose answer or request body has a field of that name.

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

Records what is missing and writes the spec, as described in
[What a run does with each entry](#what-a-run-does-with-each-entry).

### Flags

| Flag | Default | Meaning |
|---|---|---|
| `-spec` | (required) | the OpenAPI file |
| `-file` | `examples.record.yaml` | the record file |
| `-analyse` | off | add the missing entries; nothing is sent, the spec stays unchanged |
| `-base-url` | (none) | the **empty** instance the requests go to, including the base path, e.g. `http://localhost:8080/api`; without it nothing is sent |
| `-token-env` | (none) | environment variable holding a bearer token, sent as `Authorization: Bearer …` |
| `-header` | (none) | extra header, `"Name: value"`; repeatable |
| `-refresh` | (none) | record these answers again, comma-separated: `all`, a section or tag (`Ship`), an operationId (`createShip`), `"METHOD /path"`, or `operationId/name` |
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
  #01 POST /docks                    201 sent
  #02 GET /docks/1                   200 sent
  ── Ship
  #05 POST /ships                    201 recorded  (no answer yet)
  #06 GET /ships/1                   200 recorded  (no answer yet)
  ── Booking
  #07 POST /bookings (main)              kept
  9 entries: 2 recorded, 4 sent, 3 kept; 6 requests sent

FINDINGS
  ...

FILES
  examples.record.yaml: 2 answers recorded and saved
  openapi.yaml: 4 examples written
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
| `NEEDS_INSTANCE` | problem | Answers are missing and no `-base-url` is given. | Start an empty instance and pass `-base-url`. |
| `REQUEST_FAILED` | problem | The instance rejected a request; the report shows what was sent and the answer. | Fix the entry (or the instance) and run again with a fresh instance. |
| `SAVE_MISSING` | problem | The answer or the request has no value where `save` points, or a placeholder has no value because its entry was not answered. | Fix the source in `save`. |
| `FILTER` | problem / note | Problem: the answer of an entry with `filter` holds no list, or a placeholder of the filter has no value. Note: no element matched, so the empty list is stored. | Check the filter and the answer in the record file. |
| `STATUS` | problem | The answer's status is not documented, or an entry without name gets another 2xx than the lowest documented one. | Give the entry a `name`, or document the status. |
| `SHARED` | problem | A shared place of the spec cannot get its own copy. | Declare the parameter or body in the operation. |
| `RESPONSE_SCHEMA` | note | A recorded answer violates the schema; apitest will report it too. | Fix the instance or the spec. |
| `RESPONSE_STALE` | note | A stored answer no longer fits the schema; without `-base-url` it is written anyway. | Record it again with `-base-url`. |
| `DIFFERS` | note | An entry sent again answered otherwise than stored. It is compared the way apitest compares: fields of the stored answer, date-time and uuid only for presence. | Is the instance really empty? A value that changes on every run belongs in `ignore`. |
| `ORDER` | note | apitest runs an entry before one above it in the file. | Move the entry, or set `"$apitest"` and the test config. |
| `NOT_RUN` | note | apitest does not run the case of an entry (`ExcludeOps`, `IncludeOps`, `Tags`, `x-apitest-skip`); its request still shapes the data of later entries. | Usually nothing. |
| `NOT_IN_FILE` | note | apitest runs cases the file has no entry for; they keep the examples of the spec. | `record -analyse` adds them. |
| `SKIPPED` | note | `-analyse` left out a case apitest cannot send. | See the reason. |

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
examples of the file. With `-base-url`, only missing or outdated answers
are recorded, against their own empty instance. The stored answers of
others stay.

**The DTO changed.** A new required field in a request makes the entry
`REQUEST_INVALID`: add the field to `body`. A changed answer is recorded
again automatically when its stored version no longer fits the schema. If it still
fits (a new optional field), record it with `-refresh` to show the new field.

## Limits

- Request bodies are JSON only. Endpoints with `multipart/form-data` or
  form bodies get no entry.
- The instance must be empty at the start of every recording run. Recording
  against an instance with data gives other ids and lists (`DIFFERS`).
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
