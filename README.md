# Grouse

Like `git diff`, but for the output of Hugo sites.

## Build instructions

FYI: This project uses gomodules.

To build, do:

```sh
./scripts/build.sh
```

To run,

```
cd your-hugo-directory
grouse <ref> [<ref>]
```

## Tests

Tests!? Yay! Tests!

```
go test ./...
```

## Next steps before shipping / productionizing
- Tests? For a start, let's just do integration tests by unzipping zip files and then ensuring that the output makes sense.
- Check through all command output, switch printlns to logs

## Other things that would be nice for the future (roughly ordered)
- Support optional `--command` argument (so you can use it with different static site generators, not just hugo)
- Cleanup temp files, unless debug option specified or something
- Maybe set things up so that running without a second arg just takes the current working directory as-is, so you don't need to commit before diffing.
