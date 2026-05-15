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

## Notarization Secrets (Optional)

For Apple notarization (removes "unverified developer" warnings), add these additional secrets:

### `IOS_APPSTORECONNECTAPI_ISSUERID`
The Issuer ID from App Store Connect API

### `IOS_APPSTORECONNECTAPI_KEYID`
The Key ID from App Store Connect API

### `IOS_APPSTORECONNECTAPI_AUTHKEY`
The contents of the .p8 private key file from App Store Connect

**To obtain these:**
1. Go to [App Store Connect](https://appstoreconnect.apple.com/)
2. Navigate to **Users and Access** → **Keys** (under Integrations)
3. Click **Generate API Key** or select existing key
4. Choose **App Manager** or **Developer** role
5. Download the `.p8` file (can only download once!)
6. Note the **Issuer ID** (at top of page) and **Key ID** (in the key row)
7. For the auth key secret, paste the entire contents of the .p8 file

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

## Notarization

The build workflow automatically notarizes the launcher binary with Apple if the notarization secrets are configured. This removes "unverified developer" warnings when users download and run the launcher.

Notarization happens after signing and produces a `.zip` file that contains the notarized binary. Both the raw binary and the notarized zip are included in the build artifacts.

See [Apple's notarization documentation](https://developer.apple.com/documentation/security/notarizing_macos_software_before_distribution) for more details.
