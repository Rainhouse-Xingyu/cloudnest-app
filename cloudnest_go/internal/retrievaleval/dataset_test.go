package retrievaleval

import (
	"encoding/json"
	"os"
	"testing"
)

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
