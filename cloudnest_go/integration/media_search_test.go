//go:build integration

package integration

import (
	"fmt"
	"net/url"
	"testing"

	"cloudenest_go/model"
)

func TestMediaSearchPagesLegacyMetadataAndCurrentAuthorization(t *testing.T) {
	f := newFixture(t)
	marker := fmt.Sprintf("media-%d-", f.aliceID)
	wanted := map[int64]bool{}
	var legacy model.File
	for i := 0; i < 55; i++ {
		// Synthetic bytes exercise metadata classification, not image decoding.
		row := f.upload(t, f.alice, fmt.Sprintf("%s%02d.PNG", marker, i), []byte(fmt.Sprintf("synthetic media metadata %d", i)))
		wanted[row.ID] = true
		if i == 0 {
			legacy = row
		}
	}
	if err := f.db.Model(&legacy).Update("file_type", "").Error; err != nil {
		t.Fatal(err)
	}
	peer := f.upload(t, f.bob, marker+"shared.heic", []byte("synthetic shared still"))
	private := f.upload(t, f.bob, marker+"private.jpg", []byte("synthetic private still"))
	_ = private
	grant := model.FilePermission{FileID: peer.ID, UserID: f.aliceID, Permission: "read"}
	if err := f.db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	wanted[peer.ID] = true
	f.upload(t, f.alice, marker+"document.txt", []byte("not an image"))
	motion := f.upload(t, f.alice, marker+"motion.mov", []byte("not a still"))
	if err := f.db.Model(&motion).Update("file_type", "").Error; err != nil {
		t.Fatal(err)
	}
	fixtureFolder(t, f, f.alice, marker+"folder.jpg", 0)
	deleted := f.upload(t, f.alice, marker+"deleted.webp", []byte("deleted image metadata"))
	f.json(t, "DELETE", fmt.Sprintf("/api/files/%d", deleted.ID), f.alice, nil, 200)
	type response struct {
		Items      []model.File `json:"items"`
		Pagination struct {
			Page  int
			Limit int
			Total int64
		} `json:"pagination"`
	}
	base := "/api/files/search?media_kind=image&limit=20&name=" + url.QueryEscape(marker)
	seen := map[int64]bool{}
	for page := 1; page <= 3; page++ {
		w := f.json(t, "GET", fmt.Sprintf("%s&page=%d", base, page), f.alice, nil, 200)
		var result response
		decode(t, w, &result)
		expected := 20
		if page == 3 {
			expected = 16
		}
		if result.Pagination.Total != 56 || result.Pagination.Page != page || result.Pagination.Limit != 20 || len(result.Items) != expected || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("media page bounds", page, result.Pagination, len(result.Items))
		}
		for _, row := range result.Items {
			if !wanted[row.ID] || seen[row.ID] || row.IsFolder != 0 {
				t.Fatal("media page exposed private, duplicate, or non-image row", row.ID)
			}
			seen[row.ID] = true
		}
	}
	if len(seen) != len(wanted) {
		t.Fatal("media pagination omitted authorized rows")
	}
	// Revocation is applied by the next database query, not a client-side list filter.
	if err := f.db.Delete(&grant).Error; err != nil {
		t.Fatal(err)
	}
	var denied response
	decode(t, f.json(t, "GET", base, f.alice, nil, 200), &denied)
	if denied.Pagination.Total != 55 {
		t.Fatal("revoked media still visible", denied.Pagination.Total)
	}
	var legacyResult response
	decode(t, f.json(t, "GET", "/api/files/search?limit=200&name="+url.QueryEscape(marker), f.alice, nil, 200), &legacyResult)
	if legacyResult.Pagination.Total != 58 {
		t.Fatal("legacy search changed", legacyResult.Pagination.Total)
	}
	var png response
	decode(t, f.json(t, "GET", base+"&file_type=png", f.alice, nil, 200), &png)
	if png.Pagination.Total != 54 {
		t.Fatal("exact type does not intersect media scope", png.Pagination.Total)
	}
	var motions response
	decode(t, f.json(t, "GET", "/api/files/search?media_kind=live_photo_motion&name="+url.QueryEscape(marker), f.alice, nil, 200), &motions)
	if motions.Pagination.Total != 1 || len(motions.Items) != 1 || motions.Items[0].ID != motion.ID {
		t.Fatal("legacy motion omitted or non-movie exposed", motions.Pagination.Total)
	}
	for _, q := range []string{"media_kind=video", "media_kind=unknown", "media_kind=image&parent_id=-1", "media_kind=image&page=9223372036854775807"} {
		f.json(t, "GET", "/api/files/search?"+q, f.alice, nil, 400)
	}
	if err := f.db.Create(&model.UserPathPermission{UserID: f.bobID, Method: "GET", Path: "/api/files"}).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "GET", base, f.bob, nil, 403)
}
