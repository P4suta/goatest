# Security policy

## Reporting a vulnerability

Please do not open a public issue for security problems.

Report vulnerabilities privately through GitHub:
<https://github.com/P4suta/goatest/security/advisories/new>

Include the affected version or commit, reproduction steps, and the impact
you observed. You will get an acknowledgement within a week; fixes are
published as a new release together with a security advisory.

## Supported versions

Only the latest release receives security fixes.

## Scope

goatest runs `go test`, fuzz targets, and mutants from the repository it is
pointed at, and it can write repairs into `_test.go` files and standard fuzz
corpora. Reports about escaping those write boundaries, executing code outside
the target repository, or reading files the tool should not read are in scope.
