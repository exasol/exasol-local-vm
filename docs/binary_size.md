# Provider Binary Size

The macOS provider embeds the xz-compressed, workload-neutral VM payload (disk
image plus boot assets). Payload compression is therefore the dominant lever;
code stripping is marginal.

## Decisions implemented

- **Embedded payloads packed at `xz -9 --extreme`** (was `-6`):
  `package-mac.sh` (`vm-package.tar.xz`) and `build-mac-launcher.sh`
  (`init-assets.tar.xz`). Build-time cost only; launch-time decompression is
  unaffected.
- **macOS provider built with `-trimpath -ldflags="-s -w"`** in
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
| Prune the kernel module tree to the virtio guest set (`linux-virt`) | Tens of MB off the initramfs | Medium | Medium | Needs a tested module allowlist; a missing module breaks boot. | Candidate. |
| `CGO_ENABLED=0` | — | Low | — | **Impossible**: `vz/v3` binds Apple's Virtualization.framework and requires cgo. | Not applicable. |
| `-gcflags=all=-l` (disable inlining) | — | Low | Medium | Slows the xz-decompression hot path for a code-size gain negligible against the payload. | Rejected. |
| UPX / executable packing | — | Medium | High | Invalidates the macOS code signature + virtualization entitlement (notarization breaks). | Rejected. |
| Alternative compilers (TinyGo) / libc / linker | — | High | High | Cannot build the `vz` cgo bindings or the embed-heavy provider; toolchain risk. | Rejected. |
| Dependency replacement for size only | — | Med-High | Medium | `vz/v3`, `ulikunitz/xz`, `x/crypto` are functional deps; code dwarfed by payload. | Rejected. |

## References

- [`go build`](https://pkg.go.dev/cmd/go#hdr-Compile_packages_and_dependencies) · [`cmd/link`](https://pkg.go.dev/cmd/link) · [`embed`](https://pkg.go.dev/embed)
- [`xz(1)`](https://man7.org/linux/man-pages/man1/xz.1.html) — compression levels and memory use
- [`Code-Hex/vz`](https://github.com/Code-Hex/vz) — macOS Virtualization bindings (requires cgo)
- [Shrink your Go binaries with this one weird trick](https://words.filippo.io/shrink-your-go-binaries-with-this-one-weird-trick/) · [UPX](https://upx.github.io/)
