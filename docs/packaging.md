# Packaging

Linux packages follow the Visor bundle shape so Conductor can be installed beside it.

Visor (`hypermesh-visor`) builds on pull requests with `packaging/package.sh` and [nfpm](https://github.com/goreleaser/nfpm). A raw binary, a `.deb`, and a `.rpm` are named `hypermesh-visor-x86_64` and `hypermesh-visor-aarch64`, plus `checksums.txt`. The package version comes from `Cargo.toml`. A `v*` tag that matches that version is the only publish path (`softprops/action-gh-release`). Other builds use `<version>~ci.<sha>`. There is no Homebrew formula and no package signature; the checksum file is the integrity check.

Conductor uses the same asset names and the same `~ci.<sha>` version, read from `VERSION`. `packaging/package.sh` writes:

- `dist/hypermesh-conductor-x86_64` and `.deb` / `.rpm`
- `dist/hypermesh-conductor-aarch64` and `.deb` / `.rpm`
- `dist/checksums.txt`

The deb and rpm install `/usr/bin/conductor`. CI builds those packages and does not publish a release or create a tag. Darwin and Windows raw binaries are built as extra client artifacts. They are not part of the Linux bundle.

## Dropping Conductor into the Visor package

Visor's `packaging/nfpm.yaml` installs several binaries in one package. Once a Conductor build exists, that file can add:

```yaml
  - src: ${CONDUCTOR_BIN}
    dst: /usr/bin/conductor
    file_info:
      mode: 0755
```

`packaging/package.sh` in Visor would set `CONDUCTOR_BIN` to the static `hypermesh-conductor-<arch>` binary from this repository's `dist/`. Alternatively the Visor package can depend on the `hypermesh-conductor` deb so the two packages upgrade separately. Either path uses the same `/usr/bin/conductor` location.
