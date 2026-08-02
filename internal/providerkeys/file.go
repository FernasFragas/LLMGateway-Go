package providerkeys

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// File reads each provider's credential from its own file — one path per
// provider, which is exactly the layout a Kubernetes Secret volume produces
// (one file per data key), with the filename in config rather than an
// undocumented convention.
type File struct{}

// NewFile builds the file fetcher. It holds nothing: the path travels with
// each read, and the kubelet updates a mounted Secret in place, so re-reading
// is how rotation arrives.
func NewFile() *File { return &File{} }

// Fetch returns the file's contents with surrounding whitespace removed — a
// trailing newline is what every editor and `kubectl create secret
// --from-file` leaves behind, and a credential sent with one is a credential
// the provider rejects.
func (*File) Fetch(_ context.Context, path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	return strings.TrimSpace(string(raw)), nil
}
