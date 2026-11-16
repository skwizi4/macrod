# Contributing

Thanks for looking. macro-collector is a small personal project; issues and pull requests are welcome, but please open an issue before a large change.

## Build and check

You need Go (version in `go.mod`) and `staticcheck` (`make tools`). No cgo: SQLite is `modernc.org/sqlite`.

```bash
make build   # bin/macrod and bin/macroctl
make check   # go vet + staticcheck + tests — must pass before a pull request
```

`scripts/check-public.sh` must pass too (CI runs it): no home paths, keys, tokens or personal e-mail addresses in the tree.

## Rules

- **Tests never touch the network.** They read recorded responses from `testdata/`. To refresh fixtures from the live APIs (needs keys, run by hand): `make record`. Third-party content in fixtures is trimmed to what the test reads (see `docs/data-sources.md`).
- **Secrets only in the environment** (`.env`, see `.env.example`). Never in code, config or commits.
- **Store levels, transform later.** Sources store raw levels; every transformation lives in `internal/transform`.
- **One writer.** All SQLite writes go through the store's single writer goroutine.
- **New data source:** check the provider's official documentation for the endpoint and its terms of use (key, rate limits, redistribution), add a row to `docs/data-sources.md`, and keep it out of the public demo unless the licence allows it.
- **Golden files:** export tests compare against `testdata/export/*`. Regenerate with the test's `-update` flag only when the output change is intended, and review the diff.

## Style

- Go standard library first, no ORM. Code and comments in English.
- Wrap errors with context: `fmt.Errorf("fred fetch %s: %w", id, err)`.
- Logs via `log/slog`, every record with a `source` field.
- Small commits with a clear message, e.g. `fred: retry on 429`.

## License

By contributing you agree that your contribution is licensed under the MIT License (see `LICENSE`).
