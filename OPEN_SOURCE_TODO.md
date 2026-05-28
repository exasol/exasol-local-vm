# Open Source Readiness TODO

This repository is being prepared for public development. The items below are
intentionally deferred until the cleanup branch has stabilized the repository
layout, build flow, and release process.

## Before Public Launch

- Squash or rewrite commit history into a clean initial public history.
- Re-check the repository for secrets, private URLs, internal hostnames, and
  temporary development artifacts.
- Replace private build inputs with public images or documented public
  dependencies.
- Review generated artifacts and ensure only source files and intended assets
  are tracked.
- Finalize high-level user documentation after the runtime and packaging shape
  is stable.
- Revisit dependency update automation once the final dependency surface is
  clear.
- Review CI/CD workflows, release permissions, and signing requirements for
  public repository defaults.
- Enable GitHub repository protections such as branch protection, required
  reviews, Dependabot alerts, secret scanning, push protection, and private
  vulnerability reporting.
