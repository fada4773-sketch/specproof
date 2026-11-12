# apitest-gen tree: the files, and keeping tests and examples current

This page is for developers who work on an API that is tested with
`apitest-gen tree`. It answers three questions:

1. [Which file does what?](#the-files)
2. [When does the spec (`openapi.yaml`) change?](#when-the-spec-changes)
3. [What do I do when the API spec changes?](#walkthrough-the-spec-changes)
   And [how do the examples stay current?](#keeping-the-examples-current)

The full reference of every key and flag is in
[scenario.md](scenario.md); this page explains how the pieces fit
together in daily work. The examples use the fictional Starport API
(planets, docks, ships, pilots).

## The files

```
starport/
├── api/openapi.yaml               # the spec: yours; its examples are written by "tree run -update"
└── apitest/tree/
    ├── tree.yaml                  # yours: what to fetch and how a record is written
    ├── tree-data_gen.yaml         # generated: the records fetched from a stage
    ├── tree-test_gen.yaml         # generated: the test steps, plus the answers of the last -update
    ├── tree-review_gen.md         # generated: what happened, what to do next
    ├── tree-review_gen.html       # generated: every request with its bodies, for the browser
    ├── settings.yaml              # yours, optional: base URL, tokens, headers per environment
    └── tree_test.go               # yours, optional: the same test in go test
```

**Rule of thumb:** a file with `_gen` in its name is written by a tool and
never edited by hand; you review it as a git diff. A file without `_gen`
is yours: a tool may propose a first version, then it only adds to it and
keeps what you wrote.

| File | What it holds | Written by | Changes when | Commit |
|---|---|---|---|---|
| `api/openapi.yaml` | the API contract; the `examples` of requests and answers | you; the examples by `tree run -update` | you change the API; `-update` finds answers that differ from the examples | yes |
| `tree.yaml` | the plan: which records to fetch (`call`, `pick`, `where`, `require`, `page`, `each`), how each one is created, updated, patched and deleted (`write`), the order of the creates (`order`), values no record gives (`params`), what to leave out (`scope`, `ignore`), results that do not fail (`tolerate`) | `apitest-gen tree` (first proposal, later only additions), you | the spec has new or changed endpoints; you want other records | yes |
| `tree-data_gen.yaml` | the records fetched from a running instance, with their ids of that stage | `apitest-gen tree -base-url …` | you fetch again | only if the data of that stage may be in git |
| `tree-test_gen.yaml` | the steps that rebuild the records in an **empty** instance (create, read, list, update, delete, 401, read after delete), with placeholders for ids (`{{ships1.id}}`); after `-update` also the answer of every step (`answer`, `golden` on top) | `apitest-gen tree` (the steps), `tree run -update` (the answers) | `tree.yaml` or the records change; the API answers differently | yes |
| `tree-review_gen.md` / `.html` | the next command, the plan, the notes (`error`, `warn`, `info`), every request and step with its result | every `tree` and `tree run` command | every run | `.md` yes; `.html` like the data file |
| `settings.yaml` | spec path, base URL, health path, tokens (`env:NAME`), headers, report | you | an environment changes | yes (no secrets: use `env:`) |

### tree.yaml: the plan

```yaml
order:                              # creates top down, deletes bottom up
  - pilots
  - docks
  - ships: {pilotId: shipPilot.id}  # the ship's pilotId comes from the pilot read below it
tree:
  docks:
    call: GET /planets/{code}/docks
    path: {code: mars}
    pick: 2                         # two docks of mars
    key: [/id]
    write:
      create: POST /planets/{code}/docks
      update: {call: PUT /docks/{dockId}, change: {capacity: 9}}
      delete: DELETE /docks/{dockId}
    each:
      ships:
        call: GET /docks/{dockId}/ships
        path: {dockId: .id}
        pick: all
        key: [/id]
        write:
          create: POST /docks/{dockId}/ships
          delete: DELETE /ships/{shipId}
        each:
          shipPilot: GET /pilots/{pilotId} pilotId=.pilotId
```

You edit this file. Everything the test does follows from it and from the
spec.

### tree-data_gen.yaml: the records

The answers of the fetch: which docks, ships and pilots the stage had, with
their stage ids. The test is built from them. You only fetch again when
you want other records or the records lack something new (a field the
stage did not have before).

### tree-test_gen.yaml: the test

```yaml
steps:
  - id: docks1.create              # rebuilds docks1
    call: POST /planets/{code}/docks
    path: {code: mars}
    body: {capacity: 5, name: South}
    save: {docks1.id: /id}
    expect: 201
  - id: dock1.read                 # reads docks1, it holds what docks1.create sent
    call: GET /docks/{dockId}
    path: {dockId: '{{docks1.id}}'}
    expect: 200
    check: {equals: docks1.create}
```

`apitest-gen tree run` and `apitest.RunScenario` (in `go test`) run these
steps. After `tree run -update` each step also carries the answer of that
run; later runs compare with it, leaving out fields that change by
themselves (ids, timestamps).

Error steps besides 401 and the read after a delete are not in this
file: `ScenarioConfig.Errors` (`-errors 409,404,401,invalid`) generates
them when the test runs (see
[Generated error steps](scenario.md#generated-error-steps)).

## When the spec changes

Only **one** command writes into `openapi.yaml`:

```sh
apitest-gen tree run -spec api/openapi.yaml -file apitest/tree/tree.yaml -base-url http://localhost:8080/api -token-env STARPORT_TOKEN -update
```

and only when **every step passed**. It writes in place (comments and key
order stay):

- per operation the request and answer of its first successful step, as
  the default example (named `default` when the operation also has error
  examples);
- the read after a delete as a named example `afterDelete` under 404.

Steps without a token, with a manipulated token and the generated error
steps give no example. If nothing differs, the run says
`every example is up to date` and leaves the file alone. `-no-examples`
writes the answers into the test but leaves the spec alone.

| Command | `tree.yaml` | `tree-data_gen.yaml` | `tree-test_gen.yaml` | `openapi.yaml` |
|---|---|---|---|---|
| `apitest-gen tree … -dry-run` | proposes or adds endpoints | – | – | – |
| `apitest-gen tree …` (without `-base-url`) | adds endpoints | – | built again from the records | – |
| `apitest-gen tree … -base-url <stage>` | adds endpoints | fetched again | built again | – |
| `apitest-gen tree run …` | – | – | – | – |
| `apitest-gen tree run … -update` | – | – | answers written | **examples written** |
| `apitest.RunScenario` in `go test` (also with `Update`) | – | – | `Update`: answers written | – |

A run without `-update` (the CI run) never writes anything. It fails when
the API answers differently than the answers in the test file.

## Walkthrough: the spec changes

A developer changes the Starport API in one pull request:

- `Ship` gets a new required field `callsign`;
- a new endpoint `GET /ships/{shipId}/crew` lists the crew of a ship;
- `GET /docks/{dockId}/ships` moves to `GET /docks/{dockId}/fleet` (same
  operationId `listShips`);
- `DELETE /pilots/{pilotId}` is removed.

### 1. Change the API and the spec

Change the code and `api/openapi.yaml` as usual. Do not edit the examples
by hand; step 4 writes them.

### 2. Bring tree.yaml up to date

```sh
apitest-gen tree -spec api/openapi.yaml -file apitest/tree/tree.yaml -dry-run
```

It keeps everything you wrote and only adds; the changes are printed and
listed in `tree-review_gen.md`:

| Change in the spec | What happens in `tree.yaml` | What you do |
|---|---|---|
| new endpoint `GET /ships/{shipId}/crew` | added below the ships node, `# … · new in <version>` | keep it, or set `off: true`, or move it to `ignore` |
| path moved, operationId stayed | the `call` follows the new path | check the diff |
| operation removed | stays with `# gone: not in the spec <version>`; not fetched any more | delete the node or the write by hand |
| new required field `callsign` | nothing in `tree.yaml`; the next build takes it from the records, or generates a value and lists it | see step 3 |
| new tag you want to test | nothing until you add it | `-tags Crew` adds its endpoints |

Read the notes of section 1 of `tree-review_gen.md`. A plan that does not
work (a create before the record it refers to) stops with the line and the
reason; `-fix` proposes the change.

### 3. Build the test again, fetch only if needed

The records of the last fetch are enough when the change does not need new
data:

```sh
apitest-gen tree -spec api/openapi.yaml -file apitest/tree/tree.yaml
```

Fetch again when the records lack something the new spec needs, for
example a stage that already runs the new version has `callsign` on its
ships. Fetch one node and what hangs below it:

```sh
apitest-gen tree -spec api/openapi.yaml -file apitest/tree/tree.yaml -base-url https://starport.dev.example.com/api -token-env STARPORT_DEV_TOKEN -node ships
```

If no stage has the new field yet, the build generates a value that fits
the schema and lists it in the review. To send a fixed value, set it in
`fields` of the node in `tree.yaml`; a value is sent as it is:

```yaml
      ships:
        call: GET /docks/{dockId}/ships
        fields: {callsign: SP-1}
```

Check section 3 of the review: the steps of the new endpoint, and no
`error` notes.

### 4. Run against an empty instance and write the answers and examples

Start the new version of the API with an empty database (a container),
then:

```sh
apitest-gen tree run -spec api/openapi.yaml -file apitest/tree/tree.yaml -base-url http://localhost:8080/api -token-env STARPORT_TOKEN -update
```

If a step fails, the API or the plan is wrong; `tree-review_gen.html`
shows the request and the answer. Fix it and run again. Nothing is
written until every step passes.

### 5. Review the diff and commit everything together

```sh
git diff api/openapi.yaml apitest/tree/
```

Look for:

- `openapi.yaml`: the new example of `GET /ships/{shipId}/crew`, the ship
  examples with `callsign`. Timestamps and ids in examples change on
  every `-update`; that is expected.
- `tree-test_gen.yaml`: the new steps, the answers with `callsign`.
- `tree.yaml`: the additions of step 2 and your edits.

Commit the API change, the spec and the tree files in **one** pull
request, so the CI run sees code and test of the same version.

### 6. CI

CI runs the test without `-update` against a fresh container and fails if
the API answers differently than the committed answers:

```sh
apitest-gen tree run -spec api/openapi.yaml -file apitest/tree/tree.yaml -base-url http://localhost:8080/api -token-env STARPORT_TOKEN -errors 409,404,401,invalid -junit out/tree.xml
```

Or in `go test`:

```go
func TestTree(t *testing.T) {
	apitest.RunScenario(t, apitest.ScenarioConfig{
		Config:   apitest.Config{SpecPath: "../../api/openapi.yaml", Handler: router, Token: apitest.StaticToken(token)},
		Scenario: "tree-test_gen.yaml",
		Errors:   []string{"409", "404", "401", "invalid"},
	})
}
```

## Keeping the examples current

The examples in the spec are the output of a passed run, not something you
write. They stay current when the run that produces them is part of every
change:

1. **Never edit examples by hand.** Change `tree.yaml` (which records,
   which values, `change` of an update) and run `-update`. A hand-edited
   example is overwritten by the next `-update`.
2. **Every change of the API or the spec runs steps 2 to 5.** A pull
   request that changes `openapi.yaml` without `tree-test_gen.yaml`
   changing too is a sign that step 4 was skipped.
3. **CI compares, it does not write.** The run without `-update` fails as
   soon as the API answers differently than the committed answers: a
   changed field, a new status, a missing record. The fix is step 4 on the
   developer's machine, not a change in CI.
4. **CI can catch endpoints the test does not know yet.** `-dry-run` adds
   new endpoints of the spec to `tree.yaml`; if that changes the file, the
   tree is behind the spec:

   ```sh
   apitest-gen tree -spec api/openapi.yaml -file apitest/tree/tree.yaml -dry-run
   git diff --exit-code apitest/tree/tree.yaml
   ```

   Do not use `git diff --exit-code` on `openapi.yaml` after an `-update`
   in CI: timestamps and generated ids differ on every run.
5. **Removed endpoints:** the `# gone` mark in `tree.yaml` stays until you
   delete the node. Delete it in the same pull request that removes the
   endpoint.
6. **Before a release**, fetch the records again from a current stage
   (`-base-url` without `-node`), so the test works with today's data
   shapes, and run step 4.

## See also

- [scenario.md](scenario.md): the full reference of `tree.yaml`, the
  test file, every flag, tokens, the regression check and a pipeline.
- `apitest-gen tree -help`: the syntax of `tree.yaml` and all flags.
