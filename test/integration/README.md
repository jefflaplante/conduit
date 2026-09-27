# Integration Tests

End-to-end tests that exercise several packages together.

- `auth_test.go` — token creation → gateway validation → API access → rate
  limiting → revocation, over HTTP and WebSocket.

The `*.go.disabled` heartbeat loop tests are kept for reference and are not
compiled.

```bash
go test -v ./test/integration/...
```

The former hybrid web-search suite (`search_test.go`) was removed together
with the unused `internal/search` router (conduit-31jg.38). The live
WebSearch tool lives in `internal/tools/web/` and is covered by its unit
tests.
