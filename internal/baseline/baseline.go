// Package baseline records the findings a repo has decided to live with.
package baseline

import (
	"encoding/json"
	"os"
	"sort"

	"github.com/SergeAx/scrutus/internal/core"
)

const FileName = "baseline.json"

type Entry struct {
	ID   string `json:"id"`
	Rule string `json:"rule"`
}

type File struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`

	index map[string]string
}

func Load(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	f.index = make(map[string]string, len(f.Entries))
	for _, entry := range f.Entries {
		f.index[entry.ID] = entry.Rule
	}
	return &f, nil
}

// Has ignores the rule on purpose: a comment whose text changed gets a new id
// and surfaces again, which is the ratchet the baseline exists for.
func (f *File) Has(id string) bool {
	if f == nil {
		return false
	}
	_, ok := f.index[id]
	return ok
}

func Write(path string, results []core.Result) error {
	f := File{Version: 1}
	for _, r := range results {
		if r.Action == core.ActionKeep {
			continue
		}
		f.Entries = append(f.Entries, Entry{ID: r.Finding.ID, Rule: r.Rule})
	}
	sort.Slice(f.Entries, func(i, j int) bool { return f.Entries[i].ID < f.Entries[j].ID })

	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}
