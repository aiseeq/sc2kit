# sc2kit

Go library for building StarCraft II bots (AI Arena oriented).

Goal: complete, lazily-parsed access to everything the SC2 client streams —
raw observation, feature layers, unit orientation/animation state, score,
events — so a bot never has to say "the library didn't expose that".

Status: early scaffolding, API not stable yet.

## Install

```
go get github.com/aiseeq/sc2kit
```

## Protocol bindings

`api/` is generated from the official [Blizzard/s2client-proto](https://github.com/Blizzard/s2client-proto)
files vendored under `proto/` (pinned commit — see `proto/UPSTREAM`, license — `proto/PROTOCOL_LICENSE`).
Regenerate with `make generate` (needs `protoc` and `protoc-gen-go`).

## License

MIT. The vendored protocol definitions in `proto/` are © Blizzard Entertainment,
MIT-licensed (`proto/PROTOCOL_LICENSE`).
