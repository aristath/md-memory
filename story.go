package songya

import (
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var storyReference = regexp.MustCompile(`\[\[([mets]_[0-9a-z]{12})\]\]`)

type StoryPage struct {
	User          string       `json:"user,omitempty"`
	UserTruncated bool         `json:"user_truncated,omitempty"`
	ID            string       `json:"id"`
	Revision      string       `json:"-"`
	Text          string       `json:"text,omitempty"`
	Base64        string       `json:"base64,omitempty"`
	Offset        int          `json:"offset"`
	Next          int          `json:"next,omitempty"`
	Truncated     bool         `json:"truncated"`
	Unconfirmed   bool         `json:"unconfirmed,omitempty"`
	Diagnostics   []Diagnostic `json:"diagnostics,omitempty"`
}

// Compose expands exact [[record_id]] references in stories. Referenced memory
// bodies are canonical content; ordinary prose is never interpreted as a link.
func (s *Store) Compose(ctx context.Context, id string, offset, limit int) (StoryPage, error) {
	catalog, err := s.Scan(ctx)
	if err != nil {
		return StoryPage{}, err
	}
	records := catalogMap(catalog)
	original, exists := records[id]
	if !exists {
		return StoryPage{}, fmt.Errorf("story %s is unavailable", id)
	}
	if original.Kind != Stories {
		return StoryPage{}, fmt.Errorf("%s is not a story", id)
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 4096
	}
	if limit > 16384 {
		limit = 16384
	}
	diagnostics := append([]Diagnostic{}, catalog.Diagnostics...)
	// Discard the prefix while streaming; a large offset must not allocate a
	// correspondingly large buffer. Extra bytes preserve UTF-8 page boundaries.
	ceiling := limit + 2*utf8.UTFMax
	validUTF8 := true
	skip := offset
	visits := 0
	var renderErr error
	var output strings.Builder
	write := func(value string) {
		validUTF8 = validUTF8 && utf8.ValidString(value)
		if skip >= len(value) {
			skip -= len(value)
			return
		}
		value = value[skip:]
		skip = 0
		remaining := ceiling - output.Len()
		if remaining > 0 {
			if len(value) > remaining {
				value = value[:remaining]
			}
			output.WriteString(value)
		}
	}
	unconfirmed := original.Unconfirmed
	active := map[string]bool{}
	type position struct {
		id    string
		depth int
	}
	lengths := map[position]uint64{}
	var render func(string, int)
	render = func(current string, depth int) {
		if output.Len() >= ceiling || ctx.Err() != nil || renderErr != nil {
			return
		}
		visits++
		if visits > 100000 {
			renderErr = fmt.Errorf("story expansion exceeds the traversal budget; retrieve a nested story separately")
			return
		}
		if depth > 32 {
			diagnostics = append(diagnostics, Diagnostic{current, "recursive story reference retained"})
			write("[[" + current + "]] [recursive reference]\n")
			return
		}
		resolution := resolve(records, current)
		unconfirmed = unconfirmed || resolution.Unconfirmed
		if resolution.Unconfirmed {
			diagnostics = append(diagnostics, resolution.Diagnostics...)
		}
		if resolution.Ambiguous || len(resolution.Targets) != 1 {
			diagnostics = append(diagnostics, resolution.Diagnostics...)
			diagnostics = append(diagnostics, Diagnostic{current, "reference needs resolution; replacements: " + strings.Join(resolution.Targets[:min(10, len(resolution.Targets))], ", ")})
			if r, ok := records[current]; ok {
				write(r.Body)
			} else {
				write("[[" + current + "]] [missing record]\n")
			}
			return
		}
		current = resolution.Targets[0]
		if active[current] {
			diagnostics = append(diagnostics, Diagnostic{current, "recursive story reference retained"})
			write("[[" + current + "]] [recursive reference]\n")
			return
		}
		key := position{current, depth}
		if length, known := lengths[key]; known && length <= uint64(skip) {
			skip -= int(length)
			return
		}

		r := records[current]
		if r.Kind != Stories {
			lengths[key] = uint64(len(r.Body))
			write(r.Body)
			return
		}
		active[current] = true
		defer delete(active, current)
		startSkip, startOutput, startDiagnostics := skip, output.Len(), len(diagnostics)
		cursor := 0
		for {
			location := storyReference.FindStringSubmatchIndex(r.Body[cursor:])
			if location == nil {
				break
			}
			for i := range location {
				location[i] += cursor
			}
			if output.Len() >= ceiling || ctx.Err() != nil || renderErr != nil {
				return
			}
			write(r.Body[cursor:location[0]])
			render(r.Body[location[2]:location[3]], depth+1)
			cursor = location[1]
		}
		write(r.Body[cursor:])
		// Only completed, unambiguous expansions have reusable lengths. Keying by
		// depth keeps the recursion boundary identical across separate page requests.
		if output.Len() < ceiling && len(diagnostics) == startDiagnostics && renderErr == nil && ctx.Err() == nil {
			lengths[key] = uint64(startSkip-skip) + uint64(output.Len()-startOutput)
		}
	}
	render(id, 0)
	if err := ctx.Err(); err != nil {
		return StoryPage{}, err
	}
	if renderErr != nil {
		return StoryPage{}, renderErr
	}
	text := output.String()
	offset -= skip
	start := 0
	// Offsets are byte offsets. A manually supplied offset inside a rune advances
	// to the next boundary. Following Next always preserves the entire narrative.
	for validUTF8 && start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	end := min(start+limit, len(text))
	for validUTF8 && end > start && end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	if validUTF8 && end == start && start < len(text) {
		_, size := utf8.DecodeRuneInString(text[start:])
		end = start + size
	}
	if end > int(^uint(0)>>1)-offset {
		return StoryPage{}, fmt.Errorf("composed offset exceeds the byte range; retrieve a nested story separately")
	}
	page := StoryPage{User: clip(original.User, 160), UserTruncated: len(original.User) > 160, ID: id, Revision: original.Revision, Text: text[start:end], Offset: offset + start, Truncated: end < len(text), Unconfirmed: unconfirmed, Diagnostics: boundedDiagnostics(diagnostics)}
	if !utf8.ValidString(page.Text) {
		page.Base64 = base64.StdEncoding.EncodeToString([]byte(page.Text))
		page.Text = ""
	}
	if page.Truncated {
		page.Next = offset + end
	}
	return page, nil
}
