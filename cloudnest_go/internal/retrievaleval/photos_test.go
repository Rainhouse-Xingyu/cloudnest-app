package retrievaleval

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestNaturalPhotoCorpusProvenanceAndFixedLabels(t *testing.T) {
	raw, err := os.ReadFile("../../integration/testdata/retrieval/dataset-photos-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != "df42938c7f032ec6afecff297d0033d1b511c6e6bbc209296f8b2a0214e1b515" {
		t.Fatal("pre-inference photo labels/provenance changed")
	}
	d, err := ParsePhotos(raw)
	if err != nil || len(d.Sources) != 6 || len(d.Documents) != 7 || len(d.Queries) != 16 {
		t.Fatal("invalid natural-photo corpus", err)
	}
	for _, change := range []func(*PhotoDataset){
		func(d *PhotoDataset) { d.Sources[0].ID = "../escape" },
		func(d *PhotoDataset) { d.Sources[0].DownloadURL = "https://example.com/photo.jpg" },
		func(d *PhotoDataset) { d.Sources[0].DownloadURL += "?changed=1" },
		func(d *PhotoDataset) { d.Sources[0].SHA256 = d.Sources[1].SHA256 },
		func(d *PhotoDataset) { d.Sources[0].Bytes = 17 << 20 },
		func(d *PhotoDataset) { d.Sources[0].Width = 16000; d.Sources[0].Height = 16000 },
		func(d *PhotoDataset) { d.Sources[0].Author = "" },
		func(d *PhotoDataset) { d.Documents[0].Source = "missing" },
		func(d *PhotoDataset) { d.Queries[0].Relevant = []string{"photo-private-sea"} },
		func(d *PhotoDataset) { d.Queries[0].Relevant = []string{"photo-sea-last", "photo-sea-last"} },
		func(d *PhotoDataset) { d.Queries[0].Relevant = []string{"unknown"} },
		func(d *PhotoDataset) { d.Queries[0].Split = "test" },
	} {
		d, err := ParsePhotos(raw)
		if err != nil {
			t.Fatal(err)
		}
		change(&d)
		modified, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParsePhotos(modified); err == nil {
			t.Fatal("invalid photo manifest accepted")
		}
	}
	if _, err := ParsePhotos(append(raw, []byte(" {}")...)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}
