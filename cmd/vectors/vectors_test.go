package main

import (
	"os"
	"testing"
)

// TestWriteVectors regenerates testdata/vectors.json when
// GITCHAIN_WRITE_VECTORS=1, for machines where freshly built binaries are
// blocked from running.
func TestWriteVectors(t *testing.T) {
	if os.Getenv("GITCHAIN_WRITE_VECTORS") != "1" {
		t.Skip("set GITCHAIN_WRITE_VECTORS=1 to regenerate")
	}
	write("../../testdata/vectors.json")
}
