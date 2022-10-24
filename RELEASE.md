Release Process
===

# Perquisites
- goreleaser. For now, use the fork:
```shell
go install github/triarius/goreleaser@latest
```
- ghch. For now, use this fork:
```shell
go install github.com/buildkite/ghch/cmd/ghch@latest
```

# Process
1. Choose a new tag, e.g. v3.3.1
```shell
git tag -f v3.3.1
git push --tags
```
2. Create binaries
```shell
make release
```
3. Generate changelog
```
ghch --format=markdown --from=<old_version> --next-version=v3.3.1
```
4. Create a new release on github. Select the tag (e.g. v3.3.1) for the release.
5. Use the text from the previous command as a starting point for the change log.
6. Upload the binaries in `dist` to the release.
7. Publish the release.
