//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"

	"cloudenest_go/model"
)

func TestShareNamesFollowCurrentReadAccess(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "visible-share-name.txt", []byte(t.Name()))
	base := fmt.Sprintf("/api/files/%d/collaborators", file.ID)
	f.json(t, "POST", base, f.alice, map[string]any{"user_id": f.bobID, "permission": "read"}, 200)
	code := fixtureShare(t, f, f.bob, file.ID)
	read := func(wantName *string) {
		t.Helper()
		var list struct {
			Items []struct {
				model.Share
				FileName *string `json:"file_name"`
			}
		}
		body := f.json(t, "GET", "/api/shares", f.bob, nil, 200)
		decode(t, body, &list)
		if len(list.Items) != 1 || list.Items[0].ShareCode != code || list.Items[0].FileID == nil || *list.Items[0].FileID != file.ID {
			t.Fatal("share list lost its legacy fields")
		}
		got := list.Items[0].FileName
		if (got == nil) != (wantName == nil) || got != nil && *got != *wantName {
			t.Fatal("share name did not follow read access")
		}
		if strings.Contains(body.Body.String(), "passcode_hash") {
			t.Fatal("share list exposed a password hash")
		}
	}
	read(&file.Filename)
	f.json(t, "DELETE", fmt.Sprintf("%s/%d", base, f.bobID), f.alice, nil, 200)
	f.json(t, "PUT", fmt.Sprintf("/api/files/%d/rename", file.ID), f.alice, map[string]string{"name": "revoked-secret-name.txt"}, 200)
	read(nil)
	list := f.json(t, "GET", "/api/shares", f.bob, nil, 200)
	if strings.Contains(list.Body.String(), "revoked-secret-name") {
		t.Fatal("share list leaked a name after revocation")
	}
	f.json(t, "GET", "/api/public/shares/"+code, "", nil, 404)
	f.json(t, "DELETE", "/api/shares/"+code, f.bob, nil, 200)
	// Owners still get their current filename, and trashed files lose the name.
	fixtureShare(t, f, f.alice, file.ID)
	ownerList := f.json(t, "GET", "/api/shares", f.alice, nil, 200)
	if !strings.Contains(ownerList.Body.String(), "revoked-secret-name.txt") {
		t.Fatal("owner name missing")
	}
	f.json(t, "POST", "/api/files/batch-delete", f.alice, map[string]any{"file_ids": []int64{file.ID}}, 200)
	if strings.Contains(f.json(t, "GET", "/api/shares", f.alice, nil, 200).Body.String(), "revoked-secret-name") {
		t.Fatal("trash still exposed share filename")
	}
}
