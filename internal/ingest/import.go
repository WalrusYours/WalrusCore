package ingest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
)

const (
	importChunk      = 500
	maxImportLine    = 1 << 20
	maxImportReports = 100
)

type LineRejection struct {
	Line   int    `json:"line"`
	Entity string `json:"entity,omitempty"`
	ID     string `json:"id,omitempty"`
	Error  string `json:"error"`
}

type ImportResult struct {
	Lines    int             `json:"lines"`
	Accepted int             `json:"accepted"`
	Rejected int             `json:"rejected"`
	Reports  []LineRejection `json:"reports"` // the first rejections; Rejected has the full count
}

// One stores a single entity. A schema violation comes back as a 400 *Error.
func (s *Service) One(ctx context.Context, typ, id string, attrs map[string]any) error {
	res, err := s.Entities(ctx, []Raw{{Entity: typ, ID: id, Attributes: attrs}})
	if err != nil {
		return err
	}
	if len(res.Rejected) > 0 {
		return &Error{http.StatusBadRequest, "validation_error", res.Rejected[0].Error}
	}
	return nil
}

// Import reads one JSON entity per line (the same shape POST /v1/entities takes) and stores them
// in chunks, so a large backfill does not have to fit in memory. Blank lines are skipped. A bad
// line is reported with its number and does not stop the import.
func (s *Service) Import(ctx context.Context, r io.Reader) (*ImportResult, error) {
	if s.schema.Compiled() == nil {
		return nil, &Error{http.StatusConflict, "schema_missing", "no schema has been pushed yet"}
	}
	res := &ImportResult{Reports: []LineRejection{}}
	report := func(l LineRejection) {
		res.Rejected++
		if len(res.Reports) < maxImportReports {
			res.Reports = append(res.Reports, l)
		}
	}

	var chunk []Raw
	var lines []int
	flush := func() error {
		if len(chunk) == 0 {
			return nil
		}
		out, err := s.Entities(ctx, chunk)
		if err != nil {
			return err
		}
		res.Accepted += out.Accepted
		for _, rej := range out.Rejected {
			report(LineRejection{Line: lines[rej.Index], Entity: rej.Entity, ID: rej.ID, Error: rej.Error})
		}
		chunk, lines = chunk[:0], lines[:0]
		return nil
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxImportLine)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		res.Lines++
		var raw Raw
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&raw); err != nil {
			report(LineRejection{Line: n, Error: "not a valid entity: " + err.Error()})
			continue
		}
		chunk, lines = append(chunk, raw), append(lines, n)
		if len(chunk) == importChunk {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, &Error{http.StatusBadRequest, "validation_error", fmt.Sprintf("could not read the body: %v", err)}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	slices.SortFunc(res.Reports, func(a, b LineRejection) int { return a.Line - b.Line })
	return res, nil
}
