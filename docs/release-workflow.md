# Release Workflow Documentation

## Overview

The release process uses a reusable-workflow pattern with two workflows:

1. **build-mac.yml** — Reusable workflow that builds the macOS launcher and
   its embedded Linux VM image.
2. **release.yml** — Release workflow that calls the macOS build workflow and
   creates a GitHub release.

The Linux build job is build infrastructure for the macOS appliance. Its VM
package is embedded in the launcher and is not published as a separate host
product.

## How It Works

### Build Mac Launcher Workflow (`build-mac.yml`)

**Triggers:**

- Manual: `workflow_dispatch` — Can be triggered from the Actions tab or via
  `task ci-build-mac-launcher`.
- Reusable: `workflow_call` — Called by `release.yml`.

**Inputs:**

- `skip-linux-build` — Reuse an existing VM package instead of rebuilding it.
- `previous-run-id` — Optionally select the workflow run containing the VM
  package to reuse. Without it, the workflow finds the latest suitable run on
  the same branch.

**Jobs:**

1. `build-disk-images` — Builds the ARM64 VM package on Ubuntu with Podman.
   This job is skipped when `skip-linux-build` is enabled.
2. `build-mac-launcher` — Downloads the VM package, builds and signs the
   launcher, and notarizes its zip on a macOS runner.
3. `test-mac-launcher` — Runs integration tests on a self-hosted ARM64 macOS
   runner with virtualization support.

**Workflow artifacts:**

- `release-packages` — Intermediate VM package consumed by the launcher build
  and available for reuse by later workflow runs.
- `mac-launcher` — Notarized launcher zip and checksum consumed by the release
  workflow.

### Release Workflow (`release.yml`)

**Trigger:**

- Automatically when a version tag is pushed, for example `v1.0.0` or
  `v2.1.3-rc.1`.

**Jobs:**

1. `validate-tag` — Checks the tag matches
   `vMAJOR.MINOR.PATCH[-pre-release]`.
2. `build-mac` — Calls `build-mac.yml` and inherits the signing and
   notarization secrets.
3. `create-release` — Downloads the macOS launcher artifact, creates a draft
   release, uploads the files, and publishes the release. Tags with a suffix
   such as `-rc.1` are marked as prereleases.

**Protected environment:**

- Uses the `release` environment, which can require approval through
  repository environment-protection rules.
- The release job has `contents: write` permission; the build jobs retain
  read-only repository permissions plus the package permission needed for
  their build cache.

## Creating a Release

### 1. Prepare Your Code

Ensure all changes are committed and the applicable checks pass:

```bash
task lint

git add .
git commit -m "Prepare release v1.0.0"
git push
```

Run the macOS launcher build workflow before tagging if you want to verify the
complete signed, notarized, and integration-tested artifact in advance.

### 2. Create and Push Version Tag

```bash
# Create annotated tag
git tag -a v1.0.0 -m "Release version 1.0.0"

# Push tag to trigger release workflow
git push origin v1.0.0
```

### 3. Monitor Release Workflow

1. Go to the GitHub Actions tab.
2. Find the **Release** workflow run for the tag.
3. Approve the `release` environment deployment if its protection rules
   require approval.
4. Wait for the VM build, launcher build, notarization, integration tests, and
   release jobs to complete.

### 4. Review the Release

1. Go to the Releases page.
2. Open the release for the pushed tag.
3. Review the generated notes, launcher zip, and checksum.

The workflow creates the release as a draft while uploading assets and
publishes it automatically after the upload succeeds.

## Release Artifacts

Each release includes:

### macOS Launcher

- `mac-launcher-aarch64.zip` — Signed and notarized launcher bundle.
- `mac-launcher-aarch64.zip.sha256` — SHA-256 checksum for the bundle.

The intermediate `mac-arm64.tar.xz` VM package is embedded in the launcher and
remains a workflow artifact; it is not uploaded to the GitHub release.

## Configuration

### Required Secrets

Configure these repository secrets for signing and notarization:

**Signing:**

- `IOS_PKCS12_IDENTITY_CERTIFICATE_BASE64_ENCODED`
- `IOS_PKCS12_IDENTITY_CERTIFICATE_PASSWORD`
- `IOS_CER_DEVELOPERID_APPLICATION_BASE64_ENCODED`

**Notarization:**

- `IOS_APPSTORECONNECTAPI_ISSUERID`
- `IOS_APPSTORECONNECTAPI_KEYID`
- `IOS_APPSTORECONNECTAPI_AUTHKEY`

See
[the macOS signing action documentation](../.github/actions/setup-macos-signing/README.md)
for details.

### Protected Environment Setup

1. Go to repository **Settings** → **Environments**.
2. Create an environment named `release`.
3. Add required reviewers if releases should require manual approval.
4. Restrict deployment branches and tags according to the repository's
   release policy.

## Version Numbering

Follow [Semantic Versioning](https://semver.org/):

- **MAJOR** version (v**1**.0.0) — Breaking changes.
- **MINOR** version (v1.**1**.0) — New backward-compatible features.
- **PATCH** version (v1.0.**1**) — Backward-compatible bug fixes.

Examples:

- `v1.0.0` — First stable release.
- `v1.0.1` — Bug-fix release.
- `v1.1.0` — Feature release.
- `v2.0.0` — Release containing breaking changes.

## Troubleshooting

### Release Workflow Fails

**Problem: VM package build fails**

- Inspect the `build-disk-images` job in `build-mac.yml`.
- Check the Podman build and registry-cache logs.

**Problem: Reused VM package cannot be found**

- Run `build-mac.yml` once without `skip-linux-build` on the same branch.
- Alternatively, supply `previous-run-id` for a successful run containing the
  `release-packages` artifact.

**Problem: Notarization fails or times out**

- Check the `build-mac-launcher` job's `notarytool` output.
- Verify the App Store Connect issuer ID, key ID, and private key secrets.
- Apple's notary service can be slow; inspect its reported status before
  rerunning the workflow.

**Problem: Release creation returns HTTP 403**

- Ensure the release job still has `contents: write` permission.
- Check the `release` environment and tag-protection rules.

### Release Is Left as a Draft

The workflow should publish the draft after all assets upload successfully. If
it remains a draft:

- Check the **Publish release** step for errors.
- Verify that both expected launcher files were downloaded and uploaded.
- Publish the release manually only after confirming its assets are complete.

## Testing the Release Process

To test the build without creating a release:

1. Run `build-mac.yml` manually from the Actions tab or use
   `task ci-build-mac-launcher` from a pushed branch.
2. Confirm the `mac-launcher` workflow artifact contains the zip and checksum.
3. Confirm the macOS integration-test job passes.

To test the tag-driven release itself, use a valid prerelease tag such as
`v0.0.0-test.1`. This creates a real prerelease; delete the release and tag
afterward if they are only for testing.

## Comparison with Manual Process

**Before (manual):**

1. Build and package the VM locally.
2. Build and sign the macOS launcher locally.
3. Submit the launcher for notarization.
4. Create the GitHub release.
5. Upload the launcher and checksum.

**After (automated):**

1. Push a version tag.
2. Approve the deployment if required.
3. Let the workflow build, sign, notarize, test, and publish the release.
