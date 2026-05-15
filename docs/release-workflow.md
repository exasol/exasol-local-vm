# Release Workflow Documentation

## Overview

The release process uses a reusable workflow pattern with two workflows:

1. **build-packages.yml** - Reusable workflow that builds all artifacts
2. **release.yml** - Release workflow that calls build-packages.yml and creates GitHub releases

## How It Works

### Build Packages Workflow (`build-packages.yml`)

**Triggers:**
- Manual: `workflow_dispatch` - Can be triggered manually from Actions tab
- Reusable: `workflow_call` - Can be called by other workflows (like release.yml)

**Jobs:**
1. `build-linux-packages` - Builds Linux VM packages on Ubuntu
2. `build-mac-launcher` - Builds, signs, and notarizes macOS launcher on macOS runner

**Outputs:**
- `linux-packages-artifact` - Artifact name for Linux packages
- `mac-launcher-artifact` - Artifact name for macOS launcher

### Release Workflow (`release.yml`)

**Trigger:**
- Automatically when you push a version tag (e.g., `v1.0.0`, `v2.1.3`)

**Jobs:**
1. `build` - Calls build-packages.yml workflow (inherits secrets)
2. `create-release` - Downloads artifacts and creates GitHub release

**Protected Environment:**
- Uses `environment: release` which requires approval (configure in repository settings)
- Only has `contents: write` permission for creating releases

## Creating a Release

### 1. Prepare Your Code

Ensure all changes are committed and tests pass:

```bash
# Run tests locally
task all

# Commit changes
git add .
git commit -m "Prepare release v1.0.0"
git push
```

### 2. Create and Push Version Tag

```bash
# Create annotated tag
git tag -a v1.0.0 -m "Release version 1.0.0"

# Push tag to trigger release workflow
git push origin v1.0.0
```

### 3. Monitor Release Workflow

1. Go to GitHub Actions tab
2. Find the "Release" workflow run
3. If using protected environment, approve the deployment
4. Wait for build to complete (~30-60 minutes)

### 4. Review Draft Release

1. Go to Releases page
2. Find the draft release for your tag
3. Review release notes and artifacts
4. The workflow automatically publishes the release

## Release Artifacts

Each release includes:

### Linux Packages
- `linux-arm64.tar.xz` - ARM64 VM package
- `linux-x86_64.tar.xz` - x86_64 VM package  

### macOS Launcher
- `mac-runner-aarch64` - Signed binary (raw)
- `mac-runner-aarch64.zip` - Notarized binary (recommended for distribution)
- `mac-runner-aarch64.zip.sha256` - Checksum

## Configuration

### Required Secrets

For signing and notarization, configure these repository secrets:

**Signing:**
- `IOS_PKCS12_IDENTITY_CERTIFICATE_BASE64_ENCODED`
- `IOS_PKCS12_IDENTITY_CERTIFICATE_PASSWORD`
- `IOS_CER_DEVELOPERID_APPLICATION_BASE64_ENCODED`

**Notarization:**
- `IOS_APPSTORECONNECTAPI_ISSUERID`
- `IOS_APPSTORECONNECTAPI_KEYID`
- `IOS_APPSTORECONNECTAPI_AUTHKEY`

See [.github/actions/setup-macos-signing/README.md](../.github/actions/setup-macos-signing/README.md) for details.

### Protected Environment Setup

1. Go to repository **Settings** → **Environments**
2. Create environment named `release`
3. Add required reviewers
4. Configure deployment branches: Only `v*` tags

## Version Numbering

Follow [Semantic Versioning](https://semver.org/):

- **MAJOR** version (v**1**.0.0) - Breaking changes
- **MINOR** version (v1.**1**.0) - New features, backwards compatible
- **PATCH** version (v1.0.**1**) - Bug fixes, backwards compatible

Examples:
- `v1.0.0` - First stable release
- `v1.0.1` - Bug fix release
- `v1.1.0` - New features added
- `v2.0.0` - Breaking changes

## Troubleshooting

### Release Workflow Fails

**Problem:** Build job fails
- Check build-packages workflow - it may have succeeded when run manually but fail in release context
- Ensure all secrets are accessible to the release environment

**Problem:** Notarization timeout
- Apple's notary service can be slow
- Check notarization status at [developer.apple.com](https://developer.apple.com/)
- Workflow waits up to 20 minutes

**Problem:** Cannot create release (403 error)
- Ensure `release` environment has `contents: write` permission
- Check if tag protection rules are blocking

### Draft Release Not Auto-Published

The workflow should automatically publish the release after all artifacts upload successfully. If it remains as draft:
- Check the workflow logs for errors in the "Publish release" step
- Manually publish from the Releases page

## Testing Release Process

To test without creating a real release:

1. Use the build-packages workflow directly (manual trigger)
2. Create a test tag (e.g., `v0.0.0-test`)
3. Delete the tag and draft release after testing

## Comparison with Manual Process

**Before (manual):**
1. Run `task package` locally
2. Build macOS launcher locally
3. Sign manually
4. Create GitHub release manually
5. Upload each artifact manually

**After (automated):**
1. Push version tag
2. Approve deployment (if required)
3. Release created automatically with all artifacts
