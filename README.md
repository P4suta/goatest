# Goatest

A scope-aware assurance runner for Go tests, fuzzing, mutation testing, and durable reports.

## Installation

```sh
git clone https://github.com/P4suta/goatest.git
cd goatest
mise install go
mise x -- go install ./cmd/goatest
```

## Usage

```sh
mise x -- go mod download all
goatest init
goatest plan ./...
```

## Documentation

[Read the documentation](docs/README.md).

## License

Licensed under either license:

- [Apache License, Version 2.0](LICENSE-APACHE)
- [Massachusetts Institute of Technology (MIT) license](LICENSE-MIT)

Contributions use both licenses unless you state other terms.
