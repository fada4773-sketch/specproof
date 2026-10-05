# Path conflict: `/book/{id}` and `/book/{class}`

What happens when a spec has two operations on the same path template, here

| operationId | Method and path |
|---|---|
| `getBookById` | `GET /book/{id}` |
| `getBookByClass` | `GET /book/{class}` |
| `getStudentById` | `GET /student/{id}` |
| `getStudentByClass` | `GET /student/{class}` |

each with its PUT and DELETE, and a POST on `/book` and `/student`? This directory tests it with apitest-gen (`check`, `review`, `apply`) and apitest against a small in-memory server. Then it fixes the spec and tests again.

**Short answer:** `/book/{id}` and `/book/{class}` are the **same template** for OpenAPI and for every router. OpenAPI calls such paths identical and invalid. A server can route only one of them: requests for a class (`GET /book/K`) reach the `{id}` handler and get a 400. apitest and `review` both report the conflict, but no default can fix it, only a change of the path. With `/book/class/{class}` all 42 cases pass.

## Files

| File | Content |
|---|---|
| `openapi.original.yaml` | the spec as written by hand: conflicting paths, no examples |
| `openapi.yaml` | the same spec after `apitest-gen apply` (examples and extensions, no links) |
| `openapi.fixed.yaml` | the fixed spec: class operations at `/book/class/{class}` and `/student/class/{class}`, after `apply` |
| `defaults.json` | six extension entries by hand (see [Findings](#findings) 6 to 8); `review` proposes nothing else |
| `global-dict.json` | the dictionary written by `apply` |
| `server/server.go` | in-memory API. `New()` serves only `/book/{id}` (a Go `ServeMux` panics if both templates are registered); `NewFixed()` also serves `/…/class/{class}` |
| `api_test.go` | `TestFixed`: the fixed spec against `NewFixed()` |
| `conflict_test.go` | `TestConflict` (build tag `conflict`): the conflicting spec against `New()`, fails on purpose |

## The spec

`PUT /book/{class}` moves all books of a class to the class in the body; `DELETE /book/{class}` deletes all books of the class. All operations need a bearer token and document 401.

<details><summary><code>openapi.original.yaml</code></summary>

```yaml
openapi: 3.0.3
info:
  title: School
  version: "1"
security:
  - bearer: []
paths:
  /book:
    post:
      operationId: createBook
      tags: [Book]
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: "#/components/schemas/Book" }
      responses:
        "201":
          description: created
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Book" }
        "400": { $ref: "#/components/responses/BadRequest" }
        "401": { $ref: "#/components/responses/Unauthorized" }
  /book/{id}:
    parameters:
      - { name: id, in: path, required: true, schema: { type: integer, minimum: 1 } }
    get:
      operationId: getBookById
      tags: [Book]
      responses:
        "200":
          description: the book
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Book" }
        "404": { $ref: "#/components/responses/NotFound" }
        "401": { $ref: "#/components/responses/Unauthorized" }
    put:
      operationId: updateBook
      tags: [Book]
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: "#/components/schemas/Book" }
      responses:
        "200":
          description: updated
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Book" }
        "404": { $ref: "#/components/responses/NotFound" }
        "401": { $ref: "#/components/responses/Unauthorized" }
    delete:
      operationId: deleteBook
      tags: [Book]
      responses:
        "204": { description: deleted }
        "404": { $ref: "#/components/responses/NotFound" }
        "401": { $ref: "#/components/responses/Unauthorized" }
  /book/{class}:
    parameters:
      - { name: class, in: path, required: true, schema: { type: string, pattern: "^[A-Z]$" } }
    get:
      operationId: getBookByClass
      tags: [Book]
      responses:
        "200":
          description: the books of the class
          content:
            application/json:
              schema: { type: array, items: { $ref: "#/components/schemas/Book" } }
        "401": { $ref: "#/components/responses/Unauthorized" }
    put:
      operationId: updateBookClass
      tags: [Book]
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: "#/components/schemas/ClassChange" }
      responses:
        "200":
          description: the books, moved to the new class
          content:
            application/json:
              schema: { type: array, items: { $ref: "#/components/schemas/Book" } }
        "401": { $ref: "#/components/responses/Unauthorized" }
    delete:
      operationId: deleteBookClass
      tags: [Book]
      responses:
        "204": { description: all books of the class deleted }
        "401": { $ref: "#/components/responses/Unauthorized" }
  /student:
    post:
      operationId: createStudent
      tags: [Student]
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: "#/components/schemas/Student" }
      responses:
        "201":
          description: created
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Student" }
        "400": { $ref: "#/components/responses/BadRequest" }
        "401": { $ref: "#/components/responses/Unauthorized" }
  /student/{id}:
    parameters:
      - { name: id, in: path, required: true, schema: { type: integer, minimum: 1 } }
    get:
      operationId: getStudentById
      tags: [Student]
      responses:
        "200":
          description: the student
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Student" }
        "404": { $ref: "#/components/responses/NotFound" }
        "401": { $ref: "#/components/responses/Unauthorized" }
    put:
      operationId: updateStudent
      tags: [Student]
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: "#/components/schemas/Student" }
      responses:
        "200":
          description: updated
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Student" }
        "404": { $ref: "#/components/responses/NotFound" }
        "401": { $ref: "#/components/responses/Unauthorized" }
    delete:
      operationId: deleteStudent
      tags: [Student]
      responses:
        "204": { description: deleted }
        "404": { $ref: "#/components/responses/NotFound" }
        "401": { $ref: "#/components/responses/Unauthorized" }
  /student/{class}:
    parameters:
      - { name: class, in: path, required: true, schema: { type: string, pattern: "^[A-Z]$" } }
    get:
      operationId: getStudentByClass
      tags: [Student]
      responses:
        "200":
          description: the students of the class
          content:
            application/json:
              schema: { type: array, items: { $ref: "#/components/schemas/Student" } }
        "401": { $ref: "#/components/responses/Unauthorized" }
    put:
      operationId: updateStudentClass
      tags: [Student]
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: "#/components/schemas/ClassChange" }
      responses:
        "200":
          description: the students, moved to the new class
          content:
            application/json:
              schema: { type: array, items: { $ref: "#/components/schemas/Student" } }
        "401": { $ref: "#/components/responses/Unauthorized" }
    delete:
      operationId: deleteStudentClass
      tags: [Student]
      responses:
        "204": { description: all students of the class deleted }
        "401": { $ref: "#/components/responses/Unauthorized" }
components:
  securitySchemes:
    bearer: { type: http, scheme: bearer }
  responses:
    BadRequest:
      description: invalid input
      content:
        application/json:
          schema: { $ref: "#/components/schemas/Problem" }
    NotFound:
      description: not found
      content:
        application/json:
          schema: { $ref: "#/components/schemas/Problem" }
    Unauthorized:
      description: no or invalid token
      content:
        application/json:
          schema: { $ref: "#/components/schemas/Problem" }
  schemas:
    Book:
      type: object
      required: [Title, Class]
      properties:
        Id: { type: integer, readOnly: true }
        Title: { type: string, minLength: 1, maxLength: 60 }
        Class: { type: string, pattern: "^[A-Z]$" }
    Student:
      type: object
      required: [Name, Class]
      properties:
        Id: { type: integer, readOnly: true }
        Name: { type: string, minLength: 1, maxLength: 60 }
        Class: { type: string, pattern: "^[A-Z]$" }
    ClassChange:
      type: object
      required: [Class]
      properties:
        Class: { type: string, pattern: "^[A-Z]$" }
    Problem:
      type: object
      required: [Message]
      properties:
        Message: { type: string }
```
</details>

The fix changes only two lines:

```diff
-  /book/{class}:
+  /book/class/{class}:
-  /student/{class}:
+  /student/class/{class}:
```

## Commands

Build the CLI once, from the repository root:

```sh
go install ./cmd/apitest-gen
```

All further commands run in `examples/path-conflict`. To start from the hand-written spec, as below:

```sh
cp openapi.original.yaml openapi.yaml
rm defaults.json global-dict.json
```

### 1. `check`: what keeps apitest from running?

```sh
apitest-gen check -spec openapi.yaml
```

```text
check: 24 of 42 cases can be sent, 18 problems
  NOT_BUILDABLE   Book/createBook/default: required body without example: no value for /Class
  …
apitest-gen check: 18 problems
```

The spec has no examples, so no case with a body can be sent. `check` does not report the conflict; it only looks at what keeps a case from being **sent**, and both conflicting operations can be sent.

### 2. `review`: fixes into `defaults.json`

```sh
apitest-gen review -spec openapi.yaml
```

```text
model: 2 resources; the examples of each follow one record through the run
  Book: schemas Book; keys Class, Id; list getBookByClass; read getBookById; create createBook; update updateBook; delete deleteBookClass, deleteBook
  Student: schemas Student; keys Class, Id; list getStudentByClass; read getStudentById; create createStudent; update updateStudent; delete deleteStudentClass, deleteStudent
  ORDER                 the examples follow apitest's default order; if the test sets MethodOrder, DeleteLast or Tags, copy them into "$apitest" in defaults.json
review: 8 suggestions; 0 defaults proposed, 0 values to choose, 0 defaults to correct, 6 fixed by apply, 2 to fix in the spec
  SPEC     paths./book/{class}: path "/book/{class}" conflicts with "/book/{id}" (same template); servers cannot route both
  SPEC     paths./student/{class}: path "/student/{class}" conflicts with "/student/{id}" (same template); servers cannot route both
  APPLY    paths./book.post.requestBody: required body without example: no value for /Class
  APPLY    paths./book/{class}.put.requestBody: required body without example: no value for /Class
  APPLY    paths./book/{id}.put.requestBody: required body without example: no value for /Class
  APPLY    paths./student.post.requestBody: required body without example: no value for /Class
  APPLY    paths./student/{class}.put.requestBody: required body without example: no value for /Class
  APPLY    paths./student/{id}.put.requestBody: required body without example: no value for /Class
defaults.json: created (empty), nothing to add
```

`review` first shows the resource model: `Book` and `Student`, identified by `Class` and `Id`, created by the POST. apitest binds `{id}` and `{class}` to `createBook`/`createStudent` by its heuristic (the POST on the collection, the field of the same name). `review` proposes no binding for that: the generator gives the examples exactly the values the heuristic takes, so no `links` are needed in the spec.

**Review:** the missing body examples need nothing, `apply` writes them. The conflict (`SPEC`) is the point of this example; it is fixed in step 6.

Three decisions were added by hand after the first test of the fixed spec (see [Findings](#findings) 6 to 8):

```json
{
  "updateBookClass.x-apitest-compare": "schema",
  "updateBookClass.x-apitest-verify": false,
  "updateStudentClass.x-apitest-compare": "schema",
  "updateStudentClass.x-apitest-verify": false,
  "deleteBookClass.x-apitest-order": 10,
  "deleteStudentClass.x-apitest-order": 10
}
```

### 3. `apitest-gen`: dictionary and spec

```sh
apitest-gen -spec openapi.yaml
```

```text
dictionary global-dict.json created: 4 DTOs, 8 fields, 2 parameters; values: 10 new, 0 reused, 0 kept, 0 invalid, 0 repaired, 0 without value
spec openapi.yaml: 20 examples added, 0 replaced, 0 with defaults, 0 kept, 0 incomplete; 6 extensions, 0 bindings; 0 dictionary values from defaults
records: 2 resources, 2 records (generated); 2 updates on the way; 8 examples from the records, 0 parameters copied into their path
  RECORD                Book: #1 Class="K" Id=981
  RECORD                Student: #1 Class="B" Id=922
verify: 6 defaults entries and the examples of 2 resources checked against the written spec, no problems
```

The cases are played in the order apitest runs them. The book `createBook` creates is the record the reads, the update and the deletes address (their keys are bound to it), so every example shows it: `{id}` is its `Id`, `{class}` its `Class`, and `updateBook` sends a new `Title`. In apitest's default order the reads run before the update and expect the title of the POST; with `MethodOrder: POST, PUT, GET, DELETE` they would expect the new one.

Before anything is saved, `apitest-gen` loads the written spec, builds and orders the cases the way apitest does and plays them again on the records (`verify`): every parameter must address a record, every read and list must show the record as it is at that point, every extension must be set. With a problem nothing is written.

The spec gets examples and the six extensions, no `links`.

### 4. `review` and `check` again

```sh
apitest-gen review -spec openapi.yaml
apitest-gen check -spec openapi.yaml
```

```text
review: 2 suggestions; 0 defaults proposed, 0 values to choose, 0 defaults to correct, 0 fixed by apply, 2 to fix in the spec
  SPEC     paths./book/{class}: path "/book/{class}" conflicts with "/book/{id}" (same template); servers cannot route both
  SPEC     paths./student/{class}: path "/student/{class}" conflicts with "/student/{id}" (same template); servers cannot route both
defaults.json: nothing added
check: 42 of 42 cases can be sent, 0 problems
```

Everything can be sent now. Only the conflict is left, and it needs a change of the spec.

### 5. apitest against the conflicting spec

```sh
go test -tags conflict -run TestConflict -v .
```

Result: **34 of 42 passed, 8 failed** (report: `apitest-report/conflict.md`, 2 spec findings: the two conflicts).

| Case | Result |
|---|---|
| `01_Book/createBook/default` | DATA_MISMATCH: the written resource cannot be read back: GET /book/K returns 400 |
| `02_Book/getBookByClass/default` | FAILED: status code 400 is not documented in the spec, expected 200 |
| `03_Book/getBookById/default` | PASSED |
| `04_Book/updateBookClass/default` | FAILED: status code 400 is not documented in the spec, expected 200 |
| `05_Book/updateBook/default` | PASSED |
| `18_Book/deleteBook/default` | PASSED |
| `21_Book/deleteBookClass/default` | FAILED: status code 400 is not documented in the spec, expected 204 |
| `22_Student/createStudent/default` | DATA_MISMATCH: the written resource cannot be read back: GET /student/B returns 400 |
| `23_Student/getStudentByClass/default` | FAILED: status code 400 is not documented in the spec, expected 200 |
| `24_Student/getStudentById/default` | PASSED |
| `25_Student/updateStudentClass/default` | FAILED: status code 400 is not documented in the spec, expected 200 |
| `26_Student/updateStudent/default` | PASSED |
| `39_Student/deleteStudent/default` | PASSED |
| `42_Student/deleteStudentClass/default` | FAILED: status code 400 is not documented in the spec, expected 204 |
| the 28 `unauthorized` and `invalid-token` cases | PASSED |

### 6. The fixed spec

```sh
apitest-gen -spec openapi.fixed.yaml
apitest-gen review -spec openapi.fixed.yaml
apitest-gen check -spec openapi.fixed.yaml
go test -run TestFixed -v .
```

```text
dictionary global-dict.json updated: 4 DTOs, 8 fields, 2 parameters; values: 0 new, 0 reused, 10 kept, 0 invalid, 0 repaired, 0 without value
spec openapi.fixed.yaml: 0 examples added, 0 replaced, 2 with defaults, 18 kept, 0 incomplete; 0 extensions, 0 bindings; 0 dictionary values from defaults
records: 2 resources, 2 records (generated); 2 updates on the way; 2 examples from the records, 0 parameters copied into their path
  RECORD                Book: #1 Class="K" Id=981
  RECORD                Student: #1 Class="B" Id=922
verify: 6 defaults entries and the examples of 2 resources checked against the written spec, no problems
review: 0 suggestions; 0 defaults proposed, 0 values to choose, 0 defaults to correct, 0 fixed by apply, 0 to fix in the spec
nothing to review
defaults.json: nothing added
check: 42 of 42 cases can be sent, 0 problems
ok  	github.com/fada4773-sketch/specproof/examples/path-conflict
```

The same `defaults.json` serves both specs: the operationIds did not change. Nothing is left to review, and **all 42 cases pass** (report: `apitest-report/fixed.md`). The report lists 12 spec findings: the parameters apitest binds by its heuristic, which is intended here.

The same spec also passes with `MethodOrder: POST, PUT, GET, DELETE` and `DeleteLast: true` (and `"$apitest": {"MethodOrder": ["POST", "PUT", "GET", "DELETE"], "DeleteLast": true}` in `defaults.json`): then `getBookById` runs after `updateBook` and expects the new title.

## Findings

| # | What happened | Why | Fix |
|---|---|---|---|
| 1 | apitest and `review` report `path "/book/{class}" conflicts with "/book/{id}" (same template); servers cannot route both`. `check` does not. | The OpenAPI loader detects equal templates. `check` only reports cases that cannot be sent. | Rename one path. The spec is invalid by OpenAPI; apitest still tests both operations so that the consequences show. |
| 2 | Every class case fails with `status code 400 is not documented in the spec`. | The server has one route for `/book/{…}`. `GET /book/K` reaches the id handler: `id must be a positive number`. A Go `ServeMux` panics when both templates are registered; other routers take the first one or the last one. | Rename the path. |
| 3 | Even `createBook/default` fails: `the written resource cannot be read back: GET /book/K returns 400`. | After a POST, apitest reads the resource with the GET `<collection>/{param}` whose parameter is bound to that POST. Both GETs qualify, and `/book/{class}` comes first. So the conflict also breaks a valid operation. | Rename the path. With a valid spec there is only one such GET. |
| 4 | The `{id}` cases and all authentication cases pass. | The server routes `{id}`; the 401 comes from the middleware before routing. | – |
| 5 | apitest binds `{class}` to `createBook` → `/Class` by its heuristic; `review` proposes no binding. | The heuristic matches the parameter `class` to the body field `Class`; the generator gives the examples the values it takes. | – (no `links` in the spec) |
| 6 | Fixed spec, `updateBookClass/default`: `EXAMPLE_MISMATCH` at `/0/Class`, expected `"K"`, actual `"X"`. | The PUT moves the books to the class in the body. The generated response example takes `Book.Class` from the dictionary and cannot know that. | `"updateBookClass.x-apitest-compare": "schema"`. Setting `"updateBookClass.Class": "X"` is not possible, see 9. |
| 7 | Then `DATA_MISMATCH: data was not stored as sent: GET /book/class/X differs`. | After a PUT, apitest compares the sent body with the GET on the same path, and that GET returns a list. This is open point OP-09 in the plan. | `"updateBookClass.x-apitest-verify": false` until apitest compares with list elements. |
| 8 | Then `deleteBook/default` gets 404 instead of 204. | `deleteBookClass` ran first (both are DELETEs of the same group) and deleted every book of the class, also the one `deleteBook` needs. | `"deleteBookClass.x-apitest-order": 10`: delete by class last. |
| 9 | `defaults.json` keys ignore case: `"updateBookClass.Class"` and `"updateBookClass.class"` are the same key. | The body field `Class` and the path parameter `class` have the same name. | Use another key form (`Dto.Name`) or, as here, an extension. |
| 10 | The `{id}` example is `981`, the `Id` of the record. | The examples of the book `createBook` creates. At run time the id the server returns is used, bound by the heuristic. | – |

## Recommendation

Do not put two parameters of different meaning on the same template. Any of these works:

| Instead of | Use | Note |
|---|---|---|
| `GET /book/{class}` | `GET /book/class/{class}` | used here; the smallest change |
| `GET /book/{class}` | `GET /book?class=K` | a filter on the collection; no new path |
| `GET /book/{class}` | `GET /class/{class}/book` | if a class is a resource of its own |

Then run `review` again: the conflict disappears from the `SPEC` list.
