# Native fuzzing and property testing

goatest intentionally does not define `Check`, `Draw`, a generator package, a
custom replay token, shrinking, classification, or a state-machine framework.
Those experimental pre-release APIs are not part of v1.

Use native Go fuzzing for corpus-compatible properties:

```go
func FuzzRoundTrip(f *testing.F) {
	f.Add("seed")
	f.Fuzz(func(t *testing.T, input string) {
		encoded := Encode(input)
		decoded, err := Decode(encoded)
		if err != nil || decoded != input {
			t.Fatalf("round trip: %q, %v", decoded, err)
		}
	})
}
```

goatest discovers this as an ordinary `FuzzX` target and executes its registered
seed corpus deterministically during baseline, probe, and mutation evaluation.
The corpus digest is part of the target evidence key, so an unchanged seed kill
or survival can be reused and any corpus change invalidates that evidence.

Exploration remains an explicit Go workflow. Run it separately, inspect the
result, and commit useful inputs to the standard corpus before verification:

```sh
go test ./path/to/package -fuzz=FuzzRoundTrip
goatest verify
```

`go test -fuzz` controls its own random campaign, shrinking, cache, and timeout.
goatest never treats a bounded random search as a proof.

For typed generators, shrinking, or state-machine testing, use an established
library such as Rapid in an ordinary `TestX`. goatest treats it like any other
native test and does not interfere with the library's seed or replay model.

Resource metadata remains orthogonal: use `goatest.Integration(...)` or a
`//goatest:resources ...` directive around the native test that owns the
property.
