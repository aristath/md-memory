package memory

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gofrs/flock"
	"go.yaml.in/yaml/v3"
	"time"
)

const indexFormat = 1

// Diagnostic identifies a recoverable problem without hiding healthy records.
type Diagnostic struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}
type EpisodeTime struct {
	Description string `json:"description,omitempty"`
	Earliest    string `json:"earliest,omitempty"`
	Latest      string `json:"latest,omitempty"`
}

// Record is the derived search/graph view. Source Markdown is authoritative.
type Record struct {
	ID          string      `json:"id"`
	Source      string      `json:"source"`
	Kind        Destination `json:"kind"`
	Revision    string      `json:"revision"`
	Pending     bool        `json:"pending,omitempty"`
	Unconfirmed bool        `json:"unconfirmed,omitempty"`
	Title       string      `json:"title"`
	Aliases     []string    `json:"aliases,omitempty"`
	Description string      `json:"description,omitempty"`
	Relations   []Relation  `json:"relations,omitempty"`
	Redirects   []string    `json:"redirects,omitempty"`
	Time        EpisodeTime `json:"time"`
	Body        string      `json:"body"`
	Diagnostics []string    `json:"diagnostics,omitempty"`
}
type Catalog struct {
	Records     []Record     `json:"records"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

func nodeStrings(nodeName string, d *Document) []string {
	return stringsFromNode(metadataField(&d.Frontmatter, nodeName))
}
func stringsFromNode(node *yaml.Node) []string {
	var values []string
	seen := map[*yaml.Node]bool{}
	var visit func(*yaml.Node)
	visit = func(n *yaml.Node) {
		n = dereference(n)
		if n == nil || seen[n] {
			return
		}
		seen[n] = true
		if n.Kind == yaml.SequenceNode {
			for _, child := range n.Content {
				visit(child)
			}
		} else if n.Kind == yaml.ScalarNode && n.ShortTag() != "!!null" {
			var value string
			if n.Decode(&value) == nil && value != "" {
				values = append(values, value)
			}
		}
	}
	visit(node)
	return values
}

func recordFromDocument(source string, d *Document, kind Destination) Record {
	metadata := d.Metadata()
	r := Record{ID: d.ID, Source: source, Kind: kind, Revision: d.Revision, Pending: d.Pending, Unconfirmed: d.Unconfirmed, Title: metadata.Title, Body: d.Body, Relations: metadata.Relations, Diagnostics: d.Diagnostics()}
	if r.Title == "" {
		r.Title = strings.Join(nodeStrings("name", d), " ")
	}
	r.Aliases = nodeStrings("aliases", d)
	r.Description = strings.Join(nodeStrings("description", d), " ")
	r.Redirects = nodeStrings("redirects", d)
	for _, name := range []string{"entities", "topics", "broader"} {
		for _, target := range nodeStrings(name, d) {
			r.Relations = append(r.Relations, Relation{Type: name, Target: target})
		}
	}
	if kind == Stories {
		seen := map[string]bool{}
		for cursor := 0; cursor < len(r.Body); {
			match := storyReference.FindStringSubmatchIndex(r.Body[cursor:])
			if match == nil {
				break
			}
			target := r.Body[cursor+match[2] : cursor+match[3]]
			if !seen[target] {
				seen[target] = true
				r.Relations = append(r.Relations, Relation{Type: "contains", Target: target})
			}
			cursor += match[1]
		}
	}
	timeNode := dereference(metadataField(&d.Frontmatter, "time"))
	if timeNode != nil && timeNode.Kind == yaml.MappingNode {
		r.Time = EpisodeTime{Description: strings.Join(stringsFromNode(metadataField(timeNode, "description")), " "), Earliest: strings.Join(stringsFromNode(metadataField(timeNode, "earliest")), " "), Latest: strings.Join(stringsFromNode(metadataField(timeNode, "latest")), " ")}
	} else {
		r.Time.Description = strings.Join(stringsFromNode(timeNode), " ")
	}
	if r.Title == "" {
		r.Title = d.ID
		if r.Title == "" {
			r.Title = filepath.Base(source)
		}
	}
	return r
}

// Scan refreshes byte revisions on every call. Cached parsing never masks direct
// edits, and a damaged cache is rebuilt from the readable source files.
func (s *Store) Scan(ctx context.Context) (Catalog, error) { return s.scan(ctx, false) }

// Rebuild ignores cached parsing and derives a fresh catalogue from source data.
func (s *Store) Rebuild(ctx context.Context) (Catalog, error) { return s.scan(ctx, true) }

func (s *Store) scan(ctx context.Context, rebuild bool) (Catalog, error) {
	if s == nil {
		return Catalog{}, errors.New("store is nil")
	}
	root, err := os.OpenRoot(s.path)
	if err != nil {
		return Catalog{}, err
	}
	defer root.Close()
	var coordination []Diagnostic
	if lockErr := regularFile(root, ".lock"); lockErr == nil {
		lock := flock.New(filepath.Join(s.path, ".lock"))
		defer lock.Close()
		acquired, err := lock.TryRLockContext(ctx, 10*time.Millisecond)
		if err != nil {
			return Catalog{}, err
		}
		if !acquired {
			return Catalog{}, ctx.Err()
		}
	} else {
		coordination = append(coordination, Diagnostic{".lock", "scan could not coordinate with writers: " + lockErr.Error()})
	}
	cached := map[string]Record{}
	cacheName := filepath.Join(".index", "catalog.json")
	var envelope struct {
		Format   int    `json:"format"`
		Checksum string `json:"checksum"`
		Records  []byte `json:"records"`
	}
	if !rebuild {
		if data, err := readFile(root, cacheName); err == nil && json.Unmarshal(data, &envelope) == nil && envelope.Format == indexFormat && dataRevision(envelope.Records) == envelope.Checksum {
			var records []Record
			if gob.NewDecoder(bytes.NewReader(envelope.Records)).Decode(&records) == nil {
				for _, r := range records {
					cached[r.Source] = r
				}
			}
		}
	}
	result := Catalog{Records: []Record{}, Diagnostics: coordination}
	for _, destination := range destinations {
		directory := string(destination)
		entries, err := fs.ReadDir(root.FS(), directory)
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{directory, err.Error()})
			continue
		}
		prefix, _ := destination.prefix()
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			pendingID := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "."), ".md.pending")
			if entry.Name() == "."+pendingID+".md.pending" && ValidID(pendingID) && pendingID[:2] == prefix {
				source := filepath.Join(directory, pendingID+".md")
				if _, err := readFile(root, source); err == nil {
					continue
				}
				// Batch creations stay hidden until the whole batch commits. Ordinary
				// retained creations remain discoverable, even while publication is blocked.
				if _, err := root.Lstat(filepath.Join(pendingName(source), "batch")); !errors.Is(err, fs.ErrNotExist) {
					continue
				}
				doc, err := readCopy(root, pendingID, source)
				if err != nil {
					result.Diagnostics = append(result.Diagnostics, Diagnostic{source, err.Error()})
					continue
				}
				result.Records = append(result.Records, recordFromDocument(source, doc, destination))
				continue
			}
			if strings.HasPrefix(entry.Name(), ".") || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			source := filepath.Join(directory, entry.Name())
			data, err := readFile(root, source)
			data, err = batchView(root, source, data, err)
			if err != nil {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{source, err.Error()})
				continue
			}
			revision := dataRevision(data)
			r, ok := cached[source]
			if !ok || r.Revision != revision {
				d := Parse(data)
				d.Revision = revision
				id := strings.TrimSuffix(entry.Name(), ".md")
				if ValidID(id) && id[:2] == prefix {
					d.ID = id
				}
				r = recordFromDocument(source, d, destination)
			}
			if issue := batchReadIssue(root, source); issue != nil {
				r.Unconfirmed = true
				r.Revision = ""
				r.Diagnostics = append(r.Diagnostics, issue.Error())
				result.Diagnostics = append(result.Diagnostics, Diagnostic{source, issue.Error()})
			}
			if r.ID == "" {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{source, "record has no valid filename ID"})
			}
			result.Records = append(result.Records, r)
		}
	}
	sort.Slice(result.Records, func(i, j int) bool { return result.Records[i].Source < result.Records[j].Source })
	var encoded bytes.Buffer
	_ = gob.NewEncoder(&encoded).Encode(result.Records)
	records := encoded.Bytes()
	envelope.Format = indexFormat
	envelope.Checksum = dataRevision(records)
	envelope.Records = records
	payload, _ := json.Marshal(envelope)
	if existing, err := readFile(root, cacheName); err != nil || string(existing) != string(payload) {
		err := ensureDirectory(root, ".index")
		if err == nil {
			temporary := cacheName + "." + dataRevision(payload) + ".tmp"
			err = root.WriteFile(temporary, payload, 0600)
			if err == nil {
				err = root.Rename(temporary, cacheName)
			}
		}
		s.note(cacheName, err)
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{cacheName, "derived index unavailable: " + err.Error()})
		}
	} else {
		s.note(cacheName, nil)
	}
	return result, nil
}

func words(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}
func clip(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	for maximum > 0 && !utf8.RuneStart(value[maximum]) {
		maximum--
	}
	return strings.Clone(value[:maximum])
}

type Query struct {
	Text           string      `json:"text,omitempty"`
	Kind           Destination `json:"kind,omitempty"`
	RelatedTo      string      `json:"related_to,omitempty"`
	RelationType   string      `json:"relation_type,omitempty"`
	IncludeRetired bool        `json:"include_retired,omitempty"`
	Offset         int         `json:"offset,omitempty"`
	Limit          int         `json:"limit,omitempty"`
}
type Match struct {
	ID               string      `json:"id,omitempty"`
	Source           string      `json:"source"`
	Kind             Destination `json:"kind"`
	Revision         string      `json:"revision"`
	Pending          bool        `json:"pending,omitempty"`
	Unconfirmed      bool        `json:"unconfirmed,omitempty"`
	Title            string      `json:"title"`
	Aliases          []string    `json:"aliases,omitempty"`
	Excerpt          string      `json:"excerpt"`
	Redirects        []string    `json:"redirects,omitempty"`
	Score            int         `json:"score"`
	Reasons          []string    `json:"reasons,omitempty"`
	Diagnostics      []string    `json:"diagnostics,omitempty"`
	DetailsTruncated bool        `json:"details_truncated,omitempty"`
}
type SearchResult struct {
	Matches     []Match      `json:"matches"`
	Next        int          `json:"next,omitempty"`
	Truncated   bool         `json:"truncated"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

func pageRange(offset, limit, length int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if offset > length {
		offset = length
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	end := offset + limit
	if end > length {
		end = length
	}
	return offset, end
}
func boundedDiagnostics(diagnostics []Diagnostic) []Diagnostic {
	if len(diagnostics) > 20 {
		diagnostics = append(append([]Diagnostic{}, diagnostics[:19]...), Diagnostic{"diagnostics", "additional diagnostics omitted; inspect affected records or refresh in smaller sections"})
	}
	for i := range diagnostics {
		diagnostics[i].Path = clip(diagnostics[i].Path, 256)
		diagnostics[i].Message = clip(diagnostics[i].Message, 512)
	}
	return diagnostics
}

func searchCatalog(c Catalog, q Query) SearchResult {
	matches := []Match{}
	normalizedText := " " + strings.Join(words(q.Text), " ") + " "
	tokens := words(q.Text)
	if len(tokens) > 64 {
		tokens = tokens[:64]
	}
	for _, r := range c.Records {
		if !q.IncludeRetired && len(r.Redirects) > 0 && !r.Unconfirmed || q.Kind != "" && r.Kind != q.Kind {
			continue
		}
		if q.RelatedTo != "" {
			found := false
			for _, edge := range r.Relations {
				if edge.Target == q.RelatedTo && (q.RelationType == "" || edge.Type == q.RelationType) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		score := 0
		reasons := []string{}
		names := append([]string{r.Title}, r.Aliases...)
		for _, name := range names {
			lower := strings.Join(words(name), " ")
			if lower != "" && strings.Contains(normalizedText, " "+lower+" ") {
				score += 20
				reasons = append(reasons, "name or alias: "+clip(name, 96))
			}
		}
		title := strings.ToLower(strings.Join(names, " "))
		description := strings.ToLower(r.Description)
		body := strings.ToLower(r.Body + " " + r.Time.Description + " " + r.Time.Earliest + " " + r.Time.Latest)
		for _, token := range tokens {
			if strings.Contains(title, token) {
				score += 5
			}
			if strings.Contains(description, token) {
				score += 3
			}
			if strings.Contains(body, token) {
				score++
			}
		}
		if len(tokens) > 0 && score == 0 {
			continue
		}
		excerpt := r.Description
		if excerpt == "" {
			excerpt = r.Body
		}
		if len(reasons) > 4 {
			reasons = reasons[:4]
		}
		aliases := []string{}
		for _, alias := range r.Aliases {
			if len(aliases) == 8 {
				break
			}
			aliases = append(aliases, clip(alias, 96))
		}
		redirects := []string{}
		detailsTruncated := len(r.Redirects) > 8 || len(r.Aliases) > 8 || len(r.Title) > 160 || len(r.Diagnostics) > 4
		for _, redirect := range r.Redirects[:min(8, len(r.Redirects))] {
			value := clip(redirect, 128)
			detailsTruncated = detailsTruncated || value != redirect
			redirects = append(redirects, value)
		}
		warnings := []string{}
		for _, warning := range r.Diagnostics[:min(4, len(r.Diagnostics))] {
			warnings = append(warnings, clip(warning, 512))
		}
		matches = append(matches, Match{ID: r.ID, Source: r.Source, Kind: r.Kind, Revision: r.Revision, Pending: r.Pending, Unconfirmed: r.Unconfirmed, Title: clip(r.Title, 160), Aliases: aliases, Excerpt: clip(excerpt, 512), Redirects: redirects, Score: score, Reasons: reasons, Diagnostics: warnings, DetailsTruncated: detailsTruncated})
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].Source < matches[j].Source
	})
	start, end := pageRange(q.Offset, q.Limit, len(matches))
	result := SearchResult{Matches: append([]Match{}, matches[start:end]...), Truncated: end < len(matches), Diagnostics: boundedDiagnostics(c.Diagnostics)}
	if result.Truncated {
		result.Next = end
	}
	return result
}
func (s *Store) Search(ctx context.Context, q Query) (SearchResult, error) {
	catalog, err := s.Scan(ctx)
	if err != nil {
		return SearchResult{}, err
	}
	return searchCatalog(catalog, q), nil
}

// ReadSource exposes only Markdown sources in the four record directories. This
// also keeps records with damaged filenames inspectable without inventing IDs.
func (s *Store) ReadSource(source string) ([]byte, error) {
	if s == nil {
		return nil, errors.New("store is nil")
	}
	parts := strings.Split(filepath.ToSlash(source), "/")
	if len(parts) != 2 || parts[1] == "" || strings.HasPrefix(parts[1], ".") || !strings.HasSuffix(parts[1], ".md") {
		return nil, fmt.Errorf("invalid record source %q", source)
	}
	if _, err := Destination(parts[0]).prefix(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(s.path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	data, err := readRecordFile(root, source)
	if err == nil {
		return data, nil
	}
	if _, claimErr := root.Lstat(filepath.Join(pendingName(source), "batch")); errors.Is(claimErr, fs.ErrNotExist) {
		if staged, _, base, stageErr := stagedData(root, source); stageErr == nil && base == "" {
			return staged, nil
		}
	}
	return data, err
}
