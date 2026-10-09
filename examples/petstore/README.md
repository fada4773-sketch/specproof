# Petstore integration test

Reference test for apitest: the public [Swagger Petstore v3](https://github.com/swagger-api/swagger-petstore) runs locally in a container and is tested against its own frozen spec. This directory is a separate Go module that uses apitest exactly like an external project; the only difference is the `replace` line in `go.mod` that points to the local copy.

## Run

```bash
# from the repository root
make test-public

# or directly
cd examples/petstore
go test -tags=integration -count=1 -v ./...

# single cases; the producers they need run as preconditions
go test -tags=integration -count=1 -v -run 'TestPetstore/pet/getPetById' ./...

# strict run: NOT_BUILDABLE and expired deviations fail the test
APITEST_STRICT=true go test -tags=integration -count=1 ./...
```

The report is written to `apitest-report/TestPetstore.md` (set `APITEST_PETSTORE_REPORT` for another path), the dashboard to `apitest-report/TestPetstore.html`, the results additionally to `apitest-report/TestPetstore.json`.

## Requirements

- Go 1.26 or later
- Docker or Podman. Testcontainers starts `swaggerapi/petstore3:1.0.28` (pinned by digest) and removes it after the run. Without a container runtime the tests are skipped with the reason.

With Podman:

```bash
systemctl --user enable --now podman.socket
export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock
export TESTCONTAINERS_RYUK_DISABLED=true   # only if the Ryuk container does not start
```

## Files

| File | Content |
|---|---|
| `petstore_test.go` | Starts the container, runs `apitest.Run`, checks that no case is `ERROR` and the random token appears in no report. |
| `spec/openapi.yaml`, `spec/SOURCE.md` | Frozen Petstore spec with source, version and checksum. Never edited by hand. |
| `findings.md` | Classification of every finding: API error, spec error or library error. |
| `apitest_deviations.yaml` | The accepted deviations, one entry per classified API or spec error. |

## What apitest finds

The Petstore deviates from its own spec in several places, which is exactly what apitest is supposed to find: undocumented response bodies, an array where the spec documents an object, a header that is not a `date-time`, and a required parameter that the spec calls optional. All findings are listed in [`findings.md`](findings.md). Path parameters (`petId`, `orderId`, `username`) are resolved from the responses of `addPet`, `placeOrder` and `createUser` through the heuristic; the report lists them as spec findings, because the spec has neither `links` nor `x-apitest-bind`.

`TestServedSpecMatchesFrozenSpec` checks that the container serves the same operations and version as `spec/openapi.yaml`. If it fails, the image and the frozen spec are out of sync.

Each `go test` run starts a fresh container, so every run starts from the same state. `make stability` (repository root) runs the test 20 times and compares results and reports.
