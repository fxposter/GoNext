# Pristine frontend snapshot

Unmodified copies of `go/ast`, `go/parser`, `go/printer`, `go/types`,
`go/internal/scannerhooks`, `go/internal/srcimporter` and
`internal/types/errors` from the Go 1.26.4 GOROOT, kept only as reference
material for reviewing the GoNext changes in `../frontend`:

    diff -ru internal/frontend_original internal/frontend

Two mechanical transformations were applied so that the diff shows only the
real modifications:

  - `*_test.go`, `testdata/` and generator/legacy files (`generate.go`,
    `gotype.go`, `badlinkname.go`) are excluded, exactly as in the fork;
  - import paths are rewritten to the `gonext/internal/frontend/...` form,
    exactly as in the fork.

The `go.mod` file in this directory is a sentinel that keeps the snapshot out
of `go build ./...`; the snapshot is not meant to compile on its own.
