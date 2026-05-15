# macOS Signing Setup

This action configures code signing for macOS binaries with the `com.apple.security.virtualization` entitlement required for using the macOS Virtualization.framework.

## Required Secrets

To enable code signing in GitHub Actions, add these repository secrets:

### `IOS_PKCS12_IDENTITY_CERTIFICATE_BASE64_ENCODED`
Base64-encoded PKCS12 private key file (.p12)

**To generate:**
```bash
base64 -i your-private-key.p12 | pbcopy
```

### `IOS_PKCS12_IDENTITY_CERTIFICATE_PASSWORD`
The password for the PKCS12 private key

### `IOS_CER_DEVELOPERID_APPLICATION_BASE64_ENCODED`
Base64-encoded Developer ID Application certificate (.cer)

**To generate:**
```bash
base64 -i DeveloperID_Application.cer | pbcopy
```

## Getting the Certificates

1. Enroll in the [Apple Developer Program](https://developer.apple.com/programs/)
2. Go to [Apple Developer Certificates](https://developer.apple.com/account/resources/certificates/)
3. Create a **Developer ID Application** certificate
4. Download the certificate (.cer file)
5. Export your private key from Keychain Access as a .p12 file

## Testing Locally

To test signing locally without GitHub Actions:

```bash
# Set up environment variables
export MACOS_SIGN_KEYCHAIN="/path/to/your/signing.keychain"
export MACOS_SIGN_IDENTITY="Developer ID Application: Your Name (TEAM_ID)"

# Build with signing
task build-mac-launcher IMG_ARCH=aarch64
```

## Verification

After signing, the launcher will be verified to ensure the virtualization entitlement is present:

```bash
codesign -d --entitlements :- release/mac-runner-aarch64
```

You should see:
```xml
<key>com.apple.security.virtualization</key>
<true/>
```

## Optional Notarization

For distribution outside of GitHub releases, you may want to notarize the binary with Apple. This requires additional secrets:

- `MACOS_NOTARY_ISSUER_ID` - App Store Connect API issuer ID
- `MACOS_NOTARY_KEY_ID` - App Store Connect API key ID  
- `MACOS_NOTARY_KEY` - App Store Connect API authentication key (.p8)

See [Apple's notarization documentation](https://developer.apple.com/documentation/security/notarizing_macos_software_before_distribution) for details.
