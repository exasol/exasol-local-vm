# Launcher Binary Size

The macOS and Windows launchers each `go:embed` the full xz-compressed VM
payload (disk image + boot/init assets), so the binary is a small Go stub plus a
large embedded payload. The payload is one-to-three orders of magnitude bigger
than the code, so payload compression is the dominant lever and code stripping
is a marginal one.

## Decisions implemented

- **Embedded payloads packed at `xz -9 --extreme`** (was `-6`):
  `package-mac.sh` (`vm-package.tar.xz`) and `build-mac-launcher.sh`
  (`init-assets.tar.xz`). Build-time cost only; launch-time decompression is
  unaffected.
- **macOS launcher built with `-trimpath -ldflags="-s -w"`** in
  `build-mac-launcher.sh` — strips local paths, symbol table, and DWARF data.
  Panic stack traces remain usable.

## Technique tradeoffs and decisions

Impact is a ballpark estimate as a fraction of total binary size, pending
measurement against a real build (no artifacts were available when this was
written). Because the binary is almost entirely the embedded payload,
payload-level techniques scale with the whole binary while code-level techniques
act only on the small Go-code portion.

| Technique | Est. impact | Effort | Risk | Tradeoff | Decision |
|---|---|---:|---:|---|---|
| Embed payload at `xz -9 --extreme` | ~2–5% (a few % off the compressed image) | Low | Low | Slower, more memory-hungry CI builds; user launch unaffected. | **Done.** |
| `-trimpath` + `-ldflags="-s -w"` | ~2–5 MB absolute, <1–2% of total | Low | Low | Removes local paths and symbol/DWARF data; panic traces still usable. | **Done.** |
| Trim/zero free space + prune VM image before imaging | ~10–30% (largest lever) | Medium | Low-Medium | Needs image-build changes and boot re-testing; shrinks the source the compressor sees. | Candidate. |
| De-duplicate the two macOS embeds (`vm-package` vs `init-assets`) | 0% to large, depends on overlap | Low | Low | Audit only; avoids embedding the same bytes twice. | Candidate. |
| Recompress DB container `gzip -9` → `zstd -19` / `xz -9e` (`download-db-container.sh`) | ~10–30% of the DB blob (likely the largest component) | Low | Low | Swap compressor on the largest payload; `podman load` auto-detects format. | Candidate. |
| Squash/flatten DB container layers before `podman save` | Bigger than the compressor swap on layer-heavy images; stacks with the above | Medium | Low-Medium | Drops inter-layer duplication/whiteouts and compresses better; needs re-test that the flattened image loads/runs. | Candidate. |
| Prune the kernel module tree to the virtio guest set (`linux-virt`) | Tens of MB off the initramfs | Medium | Medium | Needs a tested module allowlist; a missing module breaks boot. | Candidate. |
| Replace `podman` with `crun` + drop `iptables` in the guest | Tens of MB of guest tooling | Med-High | Medium | Guest runs one known container; reworks the run/network path. | Candidate. |
| `CGO_ENABLED=0` | — | Low | — | **Impossible** on macOS: `vz/v3` binds Apple's Virtualization.framework and requires cgo. Valid only for the pure-Go Windows launcher. | Not applicable (macOS). |
| `-gcflags=all=-l` (disable inlining) | — | Low | Medium | Slows the xz-decompression hot path for a code-size gain negligible against the payload. | Rejected. |
| UPX / executable packing | — | Medium | High | Invalidates the macOS code signature + virtualization entitlement (notarization breaks). | Rejected. |
| Alternative compilers (TinyGo) / libc / linker | — | High | High | Cannot build the `vz` cgo bindings or embed-heavy launchers; toolchain risk. | Rejected. |
| Dependency replacement for size only | — | Med-High | Medium | `vz/v3`, `ulikunitz/xz`, `x/crypto` are functional deps; code dwarfed by payload. | Rejected. |

## References

- [`go build`](https://pkg.go.dev/cmd/go#hdr-Compile_packages_and_dependencies) · [`cmd/link`](https://pkg.go.dev/cmd/link) · [`embed`](https://pkg.go.dev/embed)
- [`xz(1)`](https://man7.org/linux/man-pages/man1/xz.1.html) — compression levels and memory use
- [`Code-Hex/vz`](https://github.com/Code-Hex/vz) — macOS Virtualization bindings (requires cgo)
- [Shrink your Go binaries with this one weird trick](https://words.filippo.io/shrink-your-go-binaries-with-this-one-weird-trick/) · [UPX](https://upx.github.io/)
