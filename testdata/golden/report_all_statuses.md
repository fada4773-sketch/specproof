# apitest-Report: Bookstore 1.0

| | |
|---|---|
| Spec | `/specs/bookstore.yaml` (OpenAPI 3.0.3) |
| Base URL | `http://127.0.0.1:1234` |
| Start | 2026-09-30T10:00:00Z |
| Duration | 1.234s |
| Go / apitest | go1.27.1 / (devel) |
| Strict | yes |
| Result | ❌ failed |

<details open><summary><b>⚠️ Warnings (1)</b></summary>

- The spec declares a base path.

</details>

## Summary

| Status | Count |
|---|---:|
| `PASSED` | 1 |
| `FAILED` | 1 |
| `SCHEMA_VIOLATION` | 1 |
| `EXAMPLE_MISMATCH` | 1 |
| `DATA_MISMATCH` | 1 |
| `ERROR` | 1 |
| `DEVIATION` | 1 |
| `NOT_BUILDABLE` | 1 |
| `SKIPPED` | 1 |
| **Total** | 9 |

2 more cases were not selected by `go test -run`.

<details><summary><b>📊 Coverage: 3 of 4 operations (75 %)</b></summary>

- Operations with an executed case: 3 of 4 (75 %)
- Named examples executed: 1 of 2 (50 %)

| Uncovered operation | Reason |
|---|---|
| `upload` | media type not supported |

</details>

<details><summary><b>🔎 Spec findings (1)</b></summary>

| Location | Finding |
|---|---|
| `paths./x.get` | example does \| not match |

</details>

<details open><summary><b>❌ Errors (5)</b></summary>

### ❌ Author/createAuthor/wrong — FAILED

| | |
|---|---|
| Request | `POST /authors` |
| Expected | 201 |
| Actual | 200 |

expected 201, got 200

<details><summary>Request</summary>

```json
{
  "name": "Ada"
}
```
</details>

<details><summary>Response (200)</summary>

```json
{}
```
</details>

<details><summary>Reproduce</summary>

```bash
curl -X POST "$BASE_URL/authors" -H "Authorization: Bearer $TOKEN" -d '{"name":"Ada"}'
```
</details>

### ❌ Book/getBook/default — SCHEMA_VIOLATION

| | |
|---|---|
| Request | `GET /books/1` |
| Expected | 200, Schema |
| Actual | 200 |

response violates the schema

**Schema errors**

- /price: value must be a number

### ❌ Author/createAuthor/mismatch — EXAMPLE_MISMATCH

| | |
|---|---|
| Request | `POST /authors` |
| Expected | 201, body ⊇ example |
| Actual | 201, body differs |

response differs from the example

**Differences**

| Pointer | Expected | Actual |
|---|---|---|
| `/name` | `"Ada"` | `"Bob"` |
| `/x` | `1` | `(missing)` |

### ❌ Book/updateBook/default — DATA_MISMATCH

| | |
|---|---|
| Request | `PUT /books/1` |

GET returns different values

<details><summary>Response</summary>

```text
aaaaaaaaaa
```

*Truncated, original size 70000 bytes.*
</details>

<details><summary>Response (GET /books/1, 200)</summary>

```json
{
  "title": "Go"
}
```
</details>

### ❌ Author/listAuthors/default — ERROR

| | |
|---|---|
| Request | `GET /authors` |

timeout after 1s

</details>

<details open><summary><b>🟡 Deviations (1)</b></summary>

Entries of `/specs/deviations.yaml`:

| # | Case | Deviation | Reason | Ticket | Valid until | Used | State |
|---:|---|---|---|---|---|---:|---|
| 1 | `Author/getAuthor/default` | 200 → 404 | seed data missing | API-1 | 2026-12-31 | 1 | active |
| 2 | `Book/*/default` | EXAMPLE_MISMATCH at /title | trimmed |  | 2026-10-05 | 1 | ⚠️ expires soon |
| 3 | `Shelf/*/default` | 404 → 500 | old |  | 2026-01-01 | 0 | ❗ expired |
| 4 | `X/y/z` | 400 → 200 | unused |  | 2027-01-01 | 0 | unused, can be removed |

### 🟡 Author/getAuthor/default — DEVIATION

| | |
|---|---|
| Request | `GET /authors/1` |

allowed by API-1

</details>

<details><summary><b>⏭ Not run (2)</b></summary>

| Case | Status | Reason |
|---|---|---|
| `Book/upload/default` | `NOT_BUILDABLE` | no value for /file |
| `Book/deleteBook/default` | `SKIPPED` | x-apitest-skip: later |

</details>

<details><summary><b>✅ Passed (1)</b></summary>

| Case | Request | Status |
|---|---|---|
| `Author/createAuthor/valid` (precondition) | `POST /authors` | 201 |

</details>

