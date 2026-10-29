package retrievaleval

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestExpandedCorpusKeepsOriginalSourcesAndPrivateLabels(t *testing.T) {
	original, err := os.ReadFile("../../integration/testdata/retrieval/dataset-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(original)) != "2cc46826ef494188d63fd1b6ac1e1a65c7ea62feaa24b23a05641f156a86d759" {
		t.Fatal("original frozen corpus changed")
	}
	base, err := Parse(original)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../integration/testdata/retrieval/dataset-stress-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	expanded, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(expanded.Documents) != 48 || len(expanded.Queries) != 24 || expanded.Gates != base.Gates {
		t.Fatal("stress corpus/gates unexpectedly changed")
	}
	for i, doc := range base.Documents {
		if expanded.Documents[i] != doc {
			t.Fatal("old source was modified", doc.ID)
		}
	}
	for _, query := range expanded.Queries {
		if len(query.Relevant) > 5 {
			t.Fatal("Recall@5 gate impossible for query", query.ID)
		}
	}
}

func TestDatasetFixedLabelsAndValidation(t *testing.T) {
	raw, err := os.ReadFile("../../integration/testdata/retrieval/dataset-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline.Documents) != 30 || len(baseline.Queries) != 24 {
		t.Fatal("unexpected baseline size")
	}
	for _, change := range []func(*Dataset){
		func(d *Dataset) { d.Documents[0].Fixture = "../images/beach-synthetic.png" },
		func(d *Dataset) { d.Documents[0].Fixture = "/images/beach-synthetic.png" },
		func(d *Dataset) { d.Documents[0].Fixture = "images/../images/beach-synthetic.png" },
		func(d *Dataset) { d.Documents[0].ID = d.Documents[1].ID },
		func(d *Dataset) { d.Queries[0].Relevant = []string{"private-migration"} },
		func(d *Dataset) { d.Queries[0].Relevant = []string{"missing"} },
		func(d *Dataset) { d.Queries[0].Relevant = []string{"photo-last", "photo-last"} },
		func(d *Dataset) { d.Queries[0].Split = "test" },
		func(d *Dataset) { d.Gates.Recall5 = 0 },
	} {
		d, err := Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		change(&d)
		changed, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(changed); err == nil {
			t.Fatal("invalid manifest accepted", string(changed))
		}
	}
	if _, err := Parse(append(raw, []byte(" {}")...)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}
