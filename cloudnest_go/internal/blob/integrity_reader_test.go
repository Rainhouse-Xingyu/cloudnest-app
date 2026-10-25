package blob

import (
	"cloudenest_go/model"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifiedReaderServesOriginalDescriptorAfterPathReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	payload := []byte("original")
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	size := int64(len(payload))
	object := model.Storage{FilePath: path, FileSize: &size, FileHash: fmt.Sprintf("%x", sha256.Sum256(payload)), State: Active}
	reader, err := OpenVerifiedStorageObject(context.Background(), object)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := os.Rename(path, path+".held"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replaced"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil || string(got) != "original" {
		t.Fatal("verification and serving reopened different paths", string(got), err)
	}
	if _, err := OpenVerifiedStorageObject(context.Background(), object); err == nil {
		t.Fatal("replacement incorrectly verified")
	}
}

func TestVerifiedReaderRejectsLegacyDigestWithoutRewritingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy")
	payload := []byte("legacy bytes")
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	size := int64(len(payload))
	legacy := model.Storage{FilePath: path, FileSize: &size, FileHash: fmt.Sprintf("%x", md5.Sum(payload)), State: Active}
	if file, err := OpenVerifiedStorageObject(context.Background(), legacy); err == nil {
		file.Close()
		t.Fatal("legacy MD5 mislabeled as SHA-256")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(payload) {
		t.Fatal("read validation rewrote legacy data", err)
	}
}
