# Scenario mode: contract test and regression check

The scenario mode tests an API with a generated **scenario**: the requests
of a test, in the order they run, with as many records and calls per
endpoint as the test needs. Two checks use it:

| | Where | Data | Writes | Question |
|---|---|---|---|---|
| **Contract test** | an empty instance (a container the pipeline starts) | records the scenario creates | yes | does the API do what the spec says, for every request, error and token? |
| **Regression check** | a shared stage (dev, test) | real records of the stage, drawn anew for every deployment | **never**, GET only | did the deployment change what the stage answers? |

Both run a scenario and keep the answers as a **snapshot**. The contract
test compares with a committed snapshot (the golden snapshot); the
regression check compares the snapshot after a deployment with the one
before. The examples of the spec are taken from the golden snapshot.

apitest starts nothing: every command works against the instance under
`-base-url`. In `go test`, `apitest.RunScenario` takes a `Handler` instead.

- [Files](#files)
- [Contract test](#contract-test)
  - [Settings](#settings)
  - [contract init](#contract-init)
  - [The scenario](#the-scenario)
  - [Overrides](#overrides)
  - [The golden snapshot](#the-golden-snapshot)
  - [contract run](#contract-run)
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
├── api/openapi.yaml                    # the spec; contract init writes its examples
└── apitest/
    ├── contract/
    │   ├── contract-settings.yaml      # yours: spec, base URL, tokens, report
    │   ├── contract-overrides.yaml     # yours: only the exceptions (often empty)
    │   ├── contract-scenario_gen.yaml  # generated: the steps
    │   ├── contract-golden_gen.yaml    # generated: the expected answers
    │   ├── contract-review_gen.md      # generated: what to review
    │   ├── contract_test.go            # yours, optional: the same test in go test
    │   └── out/                        # reports, not committed
    └── regression/
        ├── regression-sample.yaml      # proposed by "regression init", then yours
        └── out/                        # snapshots and reports, not committed
```

| File | Written by | Read by | Commit |
|---|---|---|---|
| `contract-settings.yaml` | you | `contract init`, `contract run`, `RunScenario` | yes |
| `contract-overrides.yaml` | you | `contract init` | yes |
| `contract-scenario_gen.yaml` | `contract init` | `contract run`, `RunScenario` | yes |
| `contract-golden_gen.yaml` | `contract init` | `contract run`, `RunScenario` | yes |
| `contract-review_gen.md` | `contract init` | you | yes |
| `regression-sample.yaml` | `regression init`, then you | `regression pre` | yes |
| `regression/out/*` | `regression pre`, `post`, `diff` | `regression post`, the pipeline | no: real data of a stage |

## Contract test

### Settings

`contract-settings.yaml` says how the test runs, not what it sends. Paths
are relative to the file; a value is written as is or read from the
environment with `env:NAME`. A flag wins over the file.

```yaml
spec: ../../api/openapi.yaml
baseURL: env:STARPORT_BASE_URL     # -base-url overrides it
health: /health                    # waited for before the first step
tokens:
  default: env:STARPORT_TOKEN        # every step without "token"
  forbidden: env:STARPORT_READER_TOKEN  # the 403 steps; without it they are skipped
headers: {X-Tenant: env:STARPORT_TENANT}
learnFrom:                         # optional: realistic values, GET only
  baseURL: https://starport.dev.example.com/api
  token: env:STARPORT_DEV_TOKEN
records: 2                         # records per resource the scenario creates
report: out/contract-report_gen.md # HTML and JSON next to it
unique: false                      # see "contract run -unique"
```

A token written directly in the file works, but the file is committed;
prefer `env:` or the token flags.

### contract init

```sh
apitest-gen contract init -settings apitest/contract/contract-settings.yaml -base-url http://localhost:8080/api -token-env STARPORT_TOKEN -forbidden-token-env STARPORT_READER_TOKEN
```

`init` needs an **empty** instance. It

1. generates the scenario from the spec, with the corrections of
   `contract-overrides.yaml`, and writes `contract-scenario_gen.yaml`;
2. runs it. Every step must answer as the scenario expects. A reading step
   is sent twice in a row: fields that change by themselves are volatile;
3. writes the golden snapshot `contract-golden_gen.yaml`;
4. writes the examples into the spec, in place: per operation the first
   regular request and answer, and one named example per documented error
   the scenario provoked (`duplicate` under 409, `stale` under 412,
   `unknown` under 404);
5. writes `contract-review_gen.md`.

If a step answers otherwise, nothing but the scenario and the review is
written, and the exit code is 1. The review names the step and the
override that would accept the answer. Fix the API or add the override,
then run `init` again. After an intended change of the API, run `init`
again too, and review the diff of the golden snapshot.

| Flag | Meaning |
|---|---|
| `-settings` | the settings file, required |
| `-base-url` | the instance, with base path |
| `-token`, `-token-env`, `-forbidden-token`, `-forbidden-token-env`, `-header` | see [Tokens](#tokens) |
| `-scenario`, `-golden`, `-overrides`, `-review` | other paths; default next to the settings |
| `-report` | Markdown report; HTML and JSON next to it |
| `-junit` | the results as JUnit XML too |
| `-no-examples` | leave the spec alone |
| `-no-learn` | ignore `learnFrom` |
| `-dry-run` | generate and print the scenario, send and write nothing |
| `-allow-remote-writes` | allow writing requests to a host other than localhost |
| `-timeout` | timeout of each request |

### The scenario

`init` builds the scenario from the resources of the spec (the model
`apitest-gen` uses for its examples) and the bindings apitest resolves:

- **Order.** A resource comes after those it refers to: its parent
  (`/docks/{dockId}/ships`), the creates its path takes values from, and
  the resources its body fields refer to (`dockId` in a ship).
- **Life cycle**, per resource: create every record (`records`), read the
  first and check it holds what was sent, list and check every record is
  there, update and read again, the actions of the resource, then the
  error and authentication steps. The deletes run at the end, in reverse
  order, each resource's last one followed by a read that expects 404.
- **Error steps** from the statuses the spec documents: 409 sends the
  body of the create again; 400/422 leaves a required field out; 412
  sends an out-of-date `If-Match`; 404 reads a key no record has. A 500
  cannot be provoked from the spec; the review lists it.
- **Authentication steps**: every operation that documents 401 without a
  token, the read also with a manipulated token, and every operation that
  documents 403 (or has `x-apitest-forbidden`) with the forbidden token.
- **Values**, in this order: the overrides, a reference to a record the
  scenario created (`{{dock1.dockId}}`), the body of the create (for an
  update), a record learned from `learnFrom`, the example of the spec,
  a generated value. Optional fields are sent only when one of the first
  five has a value.

Each decision carries its reason as a comment: `sure:` where the spec says
so, `guess:` where the generator chose. The review lists the guesses.

```yaml
order: [Dock, Ship]
records:
  dock1: {resource: Dock}
  ship1: {resource: Ship, refs: {Dock: dock1}}
steps:
  - id: dock1.create # sure: create of Dock
    call: POST /docks
    body:
      capacity: 4
      name: North # guess: learned from https://starport.dev.example.com/api
    save: {dock1.dockId: /id}
    expect: 201
  - id: ship1.create # sure: create of Ship
    call: POST /docks/{dockId}/ships
    path: {dockId: '{{dock1.dockId}}'}
    body: {name: Comet, code: SH-0001}
    save: {ship1.shipId: header Location, ship1.etag: header ETag}
    expect: 201
    unique: [/code]
  - id: ship1.read # sure: read ship1, it holds what ship1.create sent
    call: GET /ships/{shipId}
    path: {shipId: '{{ship1.shipId}}'}
    expect: 200
    check: {equals: ship1.create}
  - id: ship.list # sure: list of Ship, it holds the 2 records created
    call: GET /ships
    expect: 200
    check: {contains: {key: /id, values: ['{{ship1.shipId}}', '{{ship2.shipId}}']}}
  - id: ship1.update.stale # sure: 412 documented; an If-Match that is out of date
    call: PUT /ships/{shipId}
    path: {shipId: '{{ship1.shipId}}'}
    header: {If-Match: '"0"'}
    bodyFrom: ship1.update
    expect: 412
  - id: ship1.read.unauthorized # sure: 401 documented; no token
    call: GET /ships/{shipId}
    path: {shipId: '{{ship1.shipId}}'}
    token: none
    expect: 401
```

| Key | Meaning |
|---|---|
| `id` | `<record>.<action>[.<variant>]`; the subtest is `<tag>/<id>` |
| `call` | `METHOD /path` or the operationId |
| `path`, `query`, `header` | parameter values; `{{name}}` is a saved value (alone: with its type; inside a text: as text) |
| `body` | the request body |
| `bodyFrom` | send the body an earlier step sent |
| `save` | `name: source`: `/pointer` into the answer, `header Location` (its last path segment), `header ETag`, `request /pointer`, `request path <name>`, `request query <name>` |
| `token` | `none` (401), `invalid` (a manipulated token), `forbidden` (403); left out: the default token |
| `expect` | the status the answer must have |
| `check.equals` | the answer holds what that step sent (fields the answer's schema does not declare and writeOnly ones left out) |
| `check.contains` | a list answer (the answer, or its one list field) holds elements whose `key` has these values |
| `unique` | JSON pointers into the body whose values `-unique` makes new for each run |

### Overrides

`contract-overrides.yaml` holds only what the generator cannot know. It
survives every `init`; the generated files never need editing.

```yaml
values:                          # by Resource.field, DTO.field, operationId.param or name
  Ship.code: [SH-0001, SH-0002]  # a list: one value per record
  Dock.region: north
links:
  Booking.pilotRef: Pilot        # this field refers to a Pilot
  Booking.note: none             # this one refers to nothing
steps:                           # change a generated step; the keys given replace its own
  dock1.delete: {expect: 409}    # the dock still has ships: documented behaviour
add:                             # steps the generator cannot derive
  - after: ship1.read
    id: ship1.read.server-error
    call: GET /ships/{shipId}
    path: {shipId: '{{ship1.shipId}}'}
    header: {X-Fault: db-down}   # a test switch of the instance
    expect: 500
skip: [ship1.create.forbidden]   # generated steps that must not run
operations:                      # operations without a generated step, by "METHOD /path"
  GET /docks/{dockCode}:
    path: {dockCode: D-7}        # a value for the parameter of this operation
  DELETE /planets/{planetId}:
    path: {planetId: null}       # null: still open
    status: null                 # the status of its step; a DELETE runs only with it
  POST /ships/import:
    skip: true                   # stays without a step; init proposes nothing for it
```

After the overrides, the scenario must still hold together: a skipped
step that saves a value a later step needs is an error that names both.

#### Operations without a step

`init` writes an entry under `operations` for every operation that got no
step, with the reason as a comment above it and the keys it needs, `null`
while open. You fill them in and run `init` again; what you wrote stays,
and a later `init` only adds entries and keys that are missing.

| Reason | Proposed |
|---|---|
| a path parameter no create gives and the spec has no example for | `path: {name: null}` |
| a path parameter from a create that made no record | `path: {name: null}`; often fixing the entry of that create is enough |
| a required query or header parameter without any value | `query`/`header: {name: null}` |
| no 2xx response documented | `status: null` |
| a DELETE outside the life cycle of a record | `status: null` (runs before the records are deleted) |
| another create of a resource whose records use the first one | `status: null` |

With `learnFrom` in the settings, an open parameter gets a value of the
stage when one fits its schema: the field the parameter names (`dockCode` →
`code` of a Dock) of a record read with the list operation. The comment
says where it comes from; a key of the stage may not exist on the empty
instance. A `null` value counts as not given, `skip: true` leaves the
operation out for good.

### The golden snapshot

`contract-golden_gen.yaml` holds the answer of every step. Saved values
are kept as placeholders, so another id generator does not break the
test; volatile fields (found by the second read) and headers such as
`Date` and `ETag` are compared by schema only, like readOnly fields with
`format: date-time` or `uuid`.

```yaml
snapshot:
  kind: contract
  spec: ../../api/openapi.yaml
  specVersion: 1.5.0
  scenario: contract-scenario_gen.yaml
  created: "2026-10-13T14:02:11Z"
  reread: true
steps:
  - id: ship1.create
    op: createShip
    request: POST /docks/{{dock1.dockId}}/ships
    status: 201
    headers: {ETag: '"5f2a9c"', Location: '/api/ships/{{ship1.shipId}}'}
    volatile: [header ETag]
  - id: ship1.read
    op: getShip
    request: GET /ships/{{ship1.shipId}}
    status: 200
    body: {code: SH-0001, createdAt: "2026-10-13T14:02:11Z", dockId: '{{dock1.dockId}}', id: '{{ship1.shipId}}', name: Comet}
    volatile: [/createdAt]
```

### contract run

```sh
apitest-gen contract run -settings apitest/contract/contract-settings.yaml -base-url http://localhost:8080/api -token-env STARPORT_TOKEN -junit out/contract.xml
```

Runs the scenario and compares every answer with the golden snapshot:
status, schema of the spec, the body as a subset of the golden one (as
`Config.CompareMode`), the compared headers and the checks of the steps.
Exit code 0 passed, 1 failed, 2 wrong usage or configuration.

| Flag | Meaning |
|---|---|
| `-errors 401,404,4xx` | run only these error steps, in this order (see `ScenarioConfig.Errors`) |
| `-run`, `-skip` | select steps like `go test -run`: `Ship/`, `Ship/ship1.read`; the steps they need run too |
| `-unique` | make the values at `unique` new for this run, for an instance that is not empty |
| `-junit` | the results as JUnit XML |
| `-report` | Markdown report; HTML and JSON next to it |

`-unique` replaces each value once per run (a pattern of the schema is
respected, an e-mail gets `+<run>` before the `@`), the same original
value always by the same new one, and maps the golden snapshot the same
way.

### In go test: apitest.RunScenario

`apitest.RunScenario` takes the whole `apitest.Config` of `apitest.Run` and
the files of the scenario mode. Like `Run`, it needs **either** a
`Handler` **or** a `BaseURL`. Fields set in the code win over the settings.

```go
func TestContract(t *testing.T) {
	apitest.RunScenario(t, apitest.ScenarioConfig{
		Config: apitest.Config{
			Handler:        app.NewRouter(app.NewMemoryStore()), // or BaseURL
			Token:          apitest.StaticToken("admin-token"),
			ForbiddenToken: apitest.StaticToken("reader-token"),
			IgnoreFields:   []string{"traceId"},
			ReportPath:     "out/contract-report_gen.md",
		},
		Settings: "contract-settings.yaml",
		Scenario: "contract-scenario_gen.yaml",
		Golden:   "contract-golden_gen.yaml",
		Errors:   []string{"401", "403", "404", "409"},
	})
}
```

| Field | Meaning |
|---|---|
| `Config` | everything of `apitest.Run`: spec, `Handler` or `BaseURL`, tokens, headers, `Params` (values for parameters a step leaves out), `IgnoreFields`, `CompareMode`, `CaseInsensitive`, `Strict`, `DeviationsPath`, `TolerateErrorCases`, `SkipAuthCases`, `Tags`/`IncludeOps`/`ExcludeOps` (select steps by operation), `NumberCases`, reports, `Hooks`, … `MethodOrder`, `DeleteLast`, `LastInTag` and `ErrorCases` must stay unset: the scenario fixes the order and holds the error steps. |
| `Settings` | the settings file |
| `Scenario` | the scenario |
| `Golden` | the golden snapshot; empty checks status, schema and the checks of the steps only |
| `Errors` | the error steps (status 400 and above) that run, and their order: statuses (`"401"`) or classes (`"4xx"`). Steps no entry matches are left out; consecutive error steps run in the order of the list, at the place the scenario gives them, where the records they need exist |
| `Unique` | as `-unique` |
| `Update` | write the golden snapshot instead of comparing |
| `OnStep` | called after each step with what was sent and received |

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
| `-forbidden-token`, `-forbidden-token-env` | a valid token with too few rights, for the 403 steps (contract only) |
| `-header "Name: value"` | another header, repeatable, e.g. an API key |

Tokens are redacted in every report and output. In a pipeline prefer
`-token-env` with a secret variable, so the token is not on the command
line.

## In a pipeline

Azure DevOps, as an example; the steps are plain commands for any CI.

```yaml
stages:
- stage: Contract
  jobs:
  - job: contract
    steps:
    - script: docker run -d --name starport -p 8080:8080 -e DB_URL=memory:// $(IMAGE)
    - script: timeout 60 sh -c 'until curl -fs http://localhost:8080/api/health; do sleep 1; done'
    - script: apitest-gen contract run -settings apitest/contract/contract-settings.yaml -base-url http://localhost:8080/api -token-env STARPORT_TOKEN -forbidden-token-env STARPORT_READER_TOKEN -junit $(Build.ArtifactStagingDirectory)/contract.xml
      env: {STARPORT_TOKEN: $(starportToken), STARPORT_READER_TOKEN: $(starportReaderToken)}
    - task: PublishTestResults@2
      condition: always()
      inputs: {testResultsFormat: JUnit, testResultsFiles: $(Build.ArtifactStagingDirectory)/contract.xml}

- stage: DeployDev
  dependsOn: Contract
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

The pipeline starts the empty instance of the contract test; apitest only
gets its URL. The reports and the diff are self-contained HTML files.

## Command reference

```
apitest-gen contract init   -settings <file> [-base-url <url>] [tokens] [-overrides <file>] [-no-examples] [-no-learn] [-dry-run] [-junit <file>]
apitest-gen contract run    -settings <file> [-base-url <url>] [tokens] [-errors 401,404] [-run <pattern>] [-skip <pattern>] [-unique] [-junit <file>]
apitest-gen regression init -spec <openapi.yaml> [-out regression-sample.yaml]
apitest-gen regression pre  -spec <openapi.yaml> -sample <file> -base-url <url> [tokens] [-out dir] [-seed n] [-probe 2s] [-strict] [-junit <file>]
apitest-gen regression post -spec <openapi.yaml> -base-url <url> [tokens] [-out dir] [-pre <snapshot>] [-sample <file>] [-fail-on added,slower,other] [-slower 2x,200ms] [-ignore f,…] [-junit <file>]
apitest-gen regression diff -pre <snapshot> -post <snapshot> [-out dir] [-sample <file>] [-fail-on …] [-ignore …] [-junit <file>]
```

`apitest-gen contract help` and `apitest-gen regression help` list all
flags.

## Limits

- Request bodies are JSON (or form-encoded). Multipart bodies are not
  generated.
- The contract test needs an empty instance, or `-unique` for the fields
  the scenario marks as unique; other conflicts (a name the instance
  already has) show up as failed steps.
- A 500 is never generated; add it with `add:` and a request that
  triggers it.
- A skipped delete keeps its "read after delete" step; skip both.
- The regression check compares what a stage answers before and after a
  deployment. Changes by other users between pre and post are reported
  like any other change.
