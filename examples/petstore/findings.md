# Petstore findings (TP-L3-02)

Every non-passed case of `TestPetstore` is classified here before it goes
into `apitest_deviations.yaml` or is fixed in the library.

| Class | Meaning | Action |
|---|---|---|
| API | The Petstore behaves differently than its own spec says. | Deviation entry; report upstream (github.com/swagger-api/swagger-petstore). |
| Spec | The spec is wrong or incomplete (undocumented bodies, wrong schema, no examples). | Deviation entry; report upstream. |
| Library | apitest reports something that is not a real deviation. | Fix in apitest. Must be 0 before acceptance (AK-01). |

## Run log

| Date | Image | apitest | Result |
|---|---|---|---|
| 2026-09-30 | `swaggerapi/petstore3:1.0.28` | phase 1 (fixed `Params`, before bindings) | 11 passed, 1 failed, 5 schema violations, 2 not buildable |

The next run with the current version adds the GET checks of writes and
resolves the path parameters through bindings, so new findings are possible.
Entries of `apitest_deviations.yaml` that are no longer needed are reported
as "unused, can be removed".

## Findings

| Case | Status | Observation | Class |
|---|---|---|---|
| `pet/updatePetWithForm/default` | `FAILED` (400) | `POST /pet/10` without query parameters returns 400 "No Name provided". The spec marks `name` and `status` as optional and has no examples, so apitest sends neither (FR-PARAM-02). | API |
| `pet/deletePet/default` | `SCHEMA_VIOLATION` | Returns the text "Pet deleted"; the 200 response documents no content. | Spec |
| `user/logoutUser/default` | `SCHEMA_VIOLATION` | Returns the text "User logged out"; the 200 response documents no content. | Spec |
| `user/updateUser/default` | `SCHEMA_VIOLATION` | Returns the updated user as JSON; the 200 response documents no content. | Spec |
| `user/createUsersWithListInput/default` | `SCHEMA_VIOLATION` at `/` | Returns an array of users; the spec documents a single `User`. | Spec |
| `user/loginUser/default` | `SCHEMA_VIOLATION` | Header `X-Expires-After` is not a `date-time`; the body "Logged in user session: …" is not JSON although the response is sent as JSON. | API |
| `pet/uploadFile/default` | `NOT_BUILDABLE` | `application/octet-stream` bodies are not supported (see "Limits" in the README). | Library (documented limit, not a false alarm) |
| `pet/findPetsByTags/default` | `NOT_BUILDABLE` | The required query parameter `tags` has no example. | Spec |

Library errors: **0**. None of the findings is a false alarm; each one can be
reproduced with the curl command in the report.
