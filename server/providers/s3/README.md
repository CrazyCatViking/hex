# Optional S3 provider

This package is not wired into Hex's default deployment. Existing local and
Azure deployments are unchanged. It uses the AWS Go SDK v2 directly, never an
S3 filesystem mount.

```go
import (
    "context"
    s3store "github.com/crazycatviking/hex/server/providers/s3"
)

sites, err := s3store.New(context.Background(), s3store.Config{
    Bucket: "hex-sites",
    Region: "us-east-1",
    Endpoint: "https://objects.example.com", // empty for AWS S3
    Prefix: "production/sites",             // trusted deployment configuration
    AccessKeyID: accessKey,
    SecretAccessKey: secretKey,
    SessionToken: sessionToken, // optional
    UsePathStyle: true,         // often needed by compatible servers
})
// Handle err before assigning sites to Hex Config.Sites and Config.Publisher.
```

`New(ctx, Config) (*Store, error)` does not contact the bucket or create it.
Provision the bucket, policies and browser CORS separately. If no explicit
credentials are supplied, the AWS default credential chain is used. Region
falls back to `us-east-1` when neither config nor the AWS environment supplies
one. `Config.Credentials` accepts an `aws.CredentialsProvider`; `HTTPClient`
accepts an `aws.HTTPClient`; `Client` accepts a supplied `*s3.Client` and
overrides SDK connection/authentication options. A supplied client must support
the configured bucket and endpoint; its checksum behavior remains unchanged.

The Store implements `ObjectStore`, `SitePublisher`, `SiteDirectory`,
`SiteMetadataReader` and `DirectUploader`. No core interfaces were changed.
Use another instance with a different bucket/prefix for Hex Config.Files;
site and file storage need not share credentials or infrastructure.

## Layout and semantics

- Object keys are relative to Prefix; Prefix is a trusted root, optionally
  ending in one slash. Absolute paths, dot segments, empty segments,
  backslashes and NULs are rejected rather than cleaned. IAM/bucket policy
  must independently enforce tenant isolation; a prefix is not an IAM boundary.
- Sites use `public/sites/<site>/<file>` under that root. Discovery recognizes
  only sites with a top-level `index.html`, as the local provider does.
  Metadata is read from `.hex-site.json`, limited to 64 KiB. Missing metadata
  returns nil. Lists paginate, return root-relative keys and include server
  JSON records; MD5 is not inferred from ETags (which are not reliably MD5).
- `Open` / `ReadSiteFile` return streaming bodies the caller must close.
  Internal metadata reads always close their bodies. Missing keys map to
  `hex.ErrNotFound`; other service errors remain wrapped and inspectable.
  Missing buckets and access denial are not treated as missing keys.
- Writes spool to an OS temporary file, validate declared site-file length
  before sending, and use a seekable body for SDK retries. Provide adequate
  temporary disk capacity. This is not a filesystem mount. Cancellation is
  checked between source reads; a source blocked inside Read must itself be
  cancelable. Cleanup failures are returned, even if the object was uploaded.
- Delete performs HEAD before S3's idempotent DELETE to report missing keys.
  There is a HEAD/DELETE race. Site deletion lists and deletes files
  sequentially and is idempotent for an absent site; concurrent additions
  may survive. Versioned buckets retain old versions/delete markers.

## Direct publishing and limitations

Upload targets use protocol `http`, signed HTTP PUT, one exact object key per
URL. `PresignExpires` defaults to 15 minutes (valid range: one second to seven
days); temporary credentials may expire sooner. URLs are bearer credentials:
do not log them or expose them to other users. Endpoints must be reachable by
publishing clients. URLs cannot be individually revoked and can be replayed
until expiration; deleting a site does not invalidate outstanding URLs.

Targets return required signed headers, including Content-MD5 when supplied
(validated as a base64 16-byte digest) and Content-Length. The AWS SDK signs
positive Content-Length, but omits zero Content-Length from the signature;
zero-length targets therefore lack signed length enforcement. S3-compatible
servers must implement signature/header validation and MD5 verification.
Without MD5, presigning uses unsigned payloads and does not guarantee content
integrity. Browser CORS must allow PUT and required headers; browsers control
Content-Length themselves. Compatibility should be verified against your
chosen service before production use.

There is **no atomic deployment**, deployment transaction, snapshot isolation,
multipart upload, rollback, or static gateway in this package. Each object is
replaced independently; readers can observe mixed deployments and partial
failures. Wire an object-serving HTTP gateway separately rather than mounting
S3. Keep the bucket private and authorize reads through the application's
existing site access policy. Provider credentials need ListBucket, GetObject,
PutObject and DeleteObject on their intended scope; HEAD uses GetObject.

Tests use fake HTTP responses and a local S3 wire server, with no real cloud
credentials or resources:

```sh
go test ./server/providers/s3
go vet ./server/providers/s3
```
