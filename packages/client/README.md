# @crazycatviking/hex

The JavaScript and TypeScript browser client for Hex file storage, JSON documents, and realtime channels.

## Install

```sh
npm install @crazycatviking/hex
```

Use your project's package manager and commit its lockfile. Bundle the package with your application using tools such as Vite. The Hex CLI initializes configuration and agent skills; it does not install or copy this client.

```ts
import { createHexClient } from '@crazycatviking/hex';

const hex = createHexClient({ site: 'my-app' });
const tasks = hex.db.collection('tasks');
const task = await tasks.create({ title: 'Review the report' });
```

Deployed applications use same-origin `/api/` requests. The hosting gateway handles authentication.

See the [Hex documentation](https://github.com/crazycatviking/hex) and the skill installed by `hex init` or `hex skills` for API examples.

## Package from a checkout

Until a version is published to your registry, create a tarball from the repository root:

```sh
npm ci
just pack-client
```

The prepack script compiles the TypeScript client. Install the resulting tarball into the consuming application:

```sh
npm install /path/to/hex/dist/npm/crazycatviking-hex-0.1.0.tgz
```

Maintainers use `just version-client 0.2.0` to update the package and lockfile, then `just publish-client` to build, test, and publish to npm. See [release recipes](../../docs/releases.md). The package includes compiled JavaScript and TypeScript declarations and has no runtime dependencies.
