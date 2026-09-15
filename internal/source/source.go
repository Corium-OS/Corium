// Package source locates a Corium configuration on a node.
//
// cloud-init is the primary way a node is configured, but it is not the only
// one: bare metal without a seed device, PXE, and pre-baked appliances all need
// a configuration and have no datasource. Sources are tried in priority order
// and the first one that produces a document wins, which keeps the common case
// (cloud-init) unchanged while giving the others somewhere to put their answer.
package source

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// ErrNotFound reports that a source has nothing to offer on this node. It is
// the normal outcome for most sources on most nodes and is not an error the
// operator needs to see.
var ErrNotFound = errors.New("no configuration from this source")

// Source produces a Corium configuration document.
type Source interface {
	// Name identifies the source in logs.
	Name() string

	// Load returns the document, or ErrNotFound if this source does not apply.
	Load(ctx context.Context) ([]byte, error)
}

// Result is a document and the source that produced it.
type Result struct {
	Source   string
	Document []byte
}

// Default returns the standard source chain, highest priority first.
//
// The ordering answers "who should win when two of these disagree", and the
// principle is that the more specific and more recent an answer is, the more it
// should be trusted:
//
//  1. /etc/corium/config.yaml -- an operator sat down and wrote this on this
//     machine. Nothing should override it.
//  2. cloud-init -- the platform's answer for this instance.
//  3. the kernel command line -- whoever booted the machine, typically PXE.
//  4. /usr/share/corium/config.yaml -- baked into the image, the same on every
//     node that runs it, so it loses to anything machine-specific.
func Default() []Source {
	return []Source{
		File("/etc/corium/config.yaml"),
		CloudInit(),
		KernelCmdline(),
		File("/usr/share/corium/config.yaml"),
	}
}

// Resolve tries each source in order and returns the first document found.
//
// A source that fails for a reason other than absence stops the search rather
// than being skipped. An unreachable config URL means the operator's intent is
// unknown, and guessing by falling through to a baked-in default is how a node
// silently joins the wrong cluster.
func Resolve(ctx context.Context, sources []Source) (*Result, error) {
	for _, src := range sources {
		document, err := src.Load(ctx)

		switch {
		case err == nil:
			slog.Info("configuration found", "source", src.Name())

			return &Result{Source: src.Name(), Document: document}, nil
		case errors.Is(err, ErrNotFound):
			slog.Debug("source did not apply", "source", src.Name())
		default:
			return nil, fmt.Errorf("reading configuration from %s: %w", src.Name(), err)
		}
	}

	return nil, ErrNotFound
}
