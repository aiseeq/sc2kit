# sc2kit

Go library for StarCraft II bots: client, game loop and lazily parsed observation over the official protocol; goal and status — `README.md`. Public, MIT: nothing personal and no names or data of work (client) projects (decisions `public-repo-names`).

- `api/` is generated from `proto/` (pinned upstream commit in `proto/UPSTREAM`): change `proto/` and run `make generate`, never edit `api/` by hand.
- Consumers — the other `go.mod` files in `~/git` that require `github.com/aiseeq/sc2kit` (`grep -l aiseeq/sc2kit ~/git/*/go.mod`). There are no tags: they take the latest commit as a pseudo-version on their own `graft commit` (`modules.latest`). A change of the public API — check that they build.
- Trunk `main`. Commit — `graft commit -m "…"`: gate is go vet, gofmt, `go test ./...` and glint.
