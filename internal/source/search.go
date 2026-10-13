// SPDX-License-Identifier: 0BSD

package source

import (
	"context"
	"mod-archiver/internal/core"
)

// SearchResult is one hit from a source search.
type SearchResult struct {
	Source    string
	Ref       string // ready-to-paste archive target
	Title     string
	Author    string
	Game      string
	Downloads int64
	URL       string
	Summary   string
}

// SearchCtx is handed to Source.Search.
type SearchCtx struct {
	Query  string
	Limit  int
	Client *core.Client
	Term   *core.Term
	Opts   *Options
}

// Searcher is implemented by sources that can answer text queries.
type Searcher interface {
	Search(ctx context.Context, sc *SearchCtx) ([]SearchResult, error)
}

// SearchAll queries every searchable source. Sources that need missing
// credentials or a game hint report a one-line skip reason instead of
// an error so partial results still print.
func SearchAll(ctx context.Context, sc *SearchCtx, hint string) ([]SearchResult, []string) {
	var out []SearchResult
	var notes []string
	for _, s := range sources {
		if hint != "" && s.Name() != hint {
			continue
		}
		sr, ok := s.(Searcher)
		if !ok {
			continue
		}
		res, err := sr.Search(ctx, sc)
		switch {
		case err != nil:
			notes = append(notes, s.Name()+": "+err.Error())
		case len(res) == 0:
			notes = append(notes, s.Name()+": no hits")
		default:
			out = append(out, res...)
		}
		if ctx.Err() != nil {
			break
		}
	}
	return out, notes
}
