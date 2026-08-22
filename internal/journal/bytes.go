package journal

import "bytes"

// bytesReader wraps a byte slice for the JSON decoder without importing bytes
// at every call site.
func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
