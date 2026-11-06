package songya

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// RecallPolicy belongs to the store, never to a requesting application.
type RecallPolicy struct {
	DirectLimit     int           `json:"direct_limit" yaml:"direct_limit"`
	MinScore        float64       `json:"min_score" yaml:"min_score"`
	SearchKinds     []Destination `json:"search_kinds" yaml:"search_kinds"`
	ReturnKinds     []Destination `json:"return_kinds" yaml:"return_kinds"`
	ConnectionDepth int           `json:"connection_depth" yaml:"connection_depth"`
	ConnectedLimit  int           `json:"connected_limit" yaml:"connected_limit"`
	RelationTypes   []string      `json:"relation_types" yaml:"relation_types"`
}

func DefaultRecallPolicy() RecallPolicy {
	return RecallPolicy{DirectLimit: 5, MinScore: 0.42, SearchKinds: []Destination{Memories, Entities, Stories, Topics}, ReturnKinds: []Destination{Memories, Entities, Stories, Topics}, ConnectionDepth: 1, ConnectedLimit: 5, RelationTypes: []string{"after", "caused_by", "part_of", "related_to", "possibly_same_as"}}
}

// Decode fields independently so a damaged option does not erase healthy ones.
func (p *RecallPolicy) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("recall must be an object")
	}
	var issues []error
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, v := n.Content[i].Value, n.Content[i+1]
		var err error
		switch key {
		case "direct_limit", "connected_limit", "connection_depth":
			var text string
			err = v.Decode(&text)
			var x int
			if err == nil {
				x, err = strconv.Atoi(strings.TrimSpace(text))
			}
			maximum := 1000
			if key == "connection_depth" {
				maximum = 50
			}
			if err == nil && (x < 0 || x > maximum) {
				err = fmt.Errorf("%s must be between 0 and %d", key, maximum)
			}
			if err == nil {
				switch key {
				case "direct_limit":
					p.DirectLimit = x
				case "connected_limit":
					p.ConnectedLimit = x
				case "connection_depth":
					p.ConnectionDepth = x
				}
			}
		case "min_score":
			var text string
			err = v.Decode(&text)
			var x float64
			if err == nil {
				x, err = strconv.ParseFloat(strings.TrimSpace(text), 64)
			}
			if err == nil && !(x >= -1 && x <= 1) {
				err = fmt.Errorf("min_score must be between -1 and 1")
			}
			if err == nil {
				p.MinScore = x
			}
		case "search_kinds", "return_kinds":
			var names []string
			err = v.Decode(&names)
			if err == nil {
				kinds := []Destination{}
				seen := map[Destination]bool{}
				for _, name := range names {
					kind := Destination(strings.TrimSpace(strings.ToLower(name)))
					if _, e := kind.prefix(); e != nil {
						issues = append(issues, e)
						continue
					}
					if !seen[kind] {
						kinds = append(kinds, kind)
						seen[kind] = true
					}
				}
				if len(names) == 0 || len(kinds) > 0 {
					if key == "search_kinds" {
						p.SearchKinds = kinds
					} else {
						p.ReturnKinds = kinds
					}
				}
			}
		case "relation_types":
			var names []string
			err = v.Decode(&names)
			if err == nil {
				types := []string{}
				seen := map[string]bool{}
				for _, name := range names {
					name = relationType(name)
					if name == "" {
						issues = append(issues, fmt.Errorf("unknown relation type %q", name))
						continue
					}
					if !seen[name] {
						types = append(types, name)
						seen[name] = true
					}
				}
				if len(names) == 0 || len(types) > 0 {
					p.RelationTypes = types
				}
			}
		}
		if err != nil {
			issues = append(issues, fmt.Errorf("recall.%s: %w", key, err))
		}
	}
	return errors.Join(issues...)
}

type Configuration struct {
	Embeddings EmbeddingConfig `json:"embeddings" yaml:"embeddings"`
	Recall     RecallPolicy    `json:"recall" yaml:"recall"`
}

func LoadConfiguration(path string, fallback ...EmbeddingConfig) (Configuration, error) {
	config, _, err := configurationSnapshot(path, fallback...)
	return config, err
}
func configurationSnapshot(path string, fallback ...EmbeddingConfig) (Configuration, []byte, error) {
	config := Configuration{Recall: DefaultRecallPolicy()}
	if len(fallback) > 0 {
		config.Embeddings = fallback[0]
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	if err != nil {
		config.Embeddings = config.Embeddings.defaults()
		return config, data, err
	}
	var doc yaml.Node
	if err = yaml.Unmarshal(data, &doc); err != nil {
		config.Embeddings = config.Embeddings.defaults()
		return config, data, err
	}
	var issues []error
	if len(doc.Content) > 0 {
		n := doc.Content[0]
		if n.Kind != yaml.MappingNode {
			issues = append(issues, fmt.Errorf("configuration must be an object"))
		} else {
			for i := 0; i+1 < len(n.Content); i += 2 {
				switch n.Content[i].Value {
				case "embeddings":
					issues = append(issues, n.Content[i+1].Decode(&config.Embeddings))
				case "recall":
					issues = append(issues, n.Content[i+1].Decode(&config.Recall))
				}
			}
		}
	}
	config.Embeddings = config.Embeddings.defaults()
	return config, data, errors.Join(issues...)
}

// Configuration is reloaded per operation.
func (s *Store) Configuration() (Configuration, error) {
	config, _, err := s.ConfigurationSnapshot()
	return config, err
}

// ConfigurationSnapshot couples the effective values to the exact source bytes
// used for optimistic settings saves, including incomplete readable settings.
func (s *Store) ConfigurationSnapshot() (Configuration, []byte, error) {
	path := s.configPath
	if path == "" {
		path = filepath.Join(s.path, "config.json")
	}
	return configurationSnapshot(path, s.embeddings)
}

// UseConfiguration selects a configuration file at construction, before sharing
// the Store with goroutines. All later operations read its current contents.
func (s *Store) UseConfiguration(path string) { s.configPath = path }
