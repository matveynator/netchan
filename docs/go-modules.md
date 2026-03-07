# Go Modules compatibility

This repository is a single-module Go library.

## Module path

The canonical module path is:

```txt
github.com/matveynator/netchan
```

Consumers should import the package as:

```go
import "github.com/matveynator/netchan"
```

## Versioning policy

Releases are published with SemVer tags in the `vMAJOR.MINOR.PATCH` format.

`@latest` resolves to the highest SemVer tag, which guarantees deterministic installation through Go modules and public proxies.
