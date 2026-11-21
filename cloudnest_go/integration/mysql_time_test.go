//go:build integration

package integration

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	mysqldsn "github.com/go-sql-driver/mysql"
)

func TestMySQLTimePinsEveryPooledUTCConnection(t *testing.T) {
	f := newFixture(t)
	pool, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(6)
	var connections []*sql.Conn
	defer func() {
		for _, c := range connections {
			c.Close()
		}
	}()
	for range 6 {
		c, err := pool.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, c)
		var zone string
		var delta int64
		if err := c.QueryRowContext(context.Background(), "SELECT @@session.time_zone,TIMESTAMPDIFF(SECOND,UTC_TIMESTAMP(),NOW())").Scan(&zone, &delta); err != nil {
			t.Fatal(err)
		}
		if zone != "+00:00" || delta != 0 {
			t.Fatalf("UTC driver got session %s with %d second offset", zone, delta)
		}
	}
	for _, c := range connections {
		c.Close()
	}
	connections = nil
	// A fresh physical connection must get the same session contract.
	pool.SetMaxIdleConns(0)
	var delta int64
	if err := pool.QueryRow("SELECT TIMESTAMPDIFF(SECOND,UTC_TIMESTAMP(),NOW())").Scan(&delta); err != nil || delta != 0 {
		t.Fatalf("reconnected session drifted: %d %v", delta, err)
	}
}

func TestMySQLTimeFileDefaultsMatchTheActualInstant(t *testing.T) {
	f := newFixture(t)
	before := time.Now().Add(-2 * time.Second)
	response := f.json(t, "POST", "/api/files/folders", f.alice, map[string]any{"name": "timezone-folder"}, 200)
	var body struct {
		Item model.File `json:"item"`
	}
	decode(t, response, &body)
	// The native browser refreshes the list after creating a folder. The create
	// response has not reloaded MySQL's defaults, so exercise that actual path.
	listed := f.json(t, "GET", "/api/files?parent_id=0", f.alice, nil, 200)
	var listing struct {
		Items []model.File `json:"items"`
	}
	decode(t, listed, &listing)
	if len(listing.Items) != 1 || listing.Items[0].ID != body.Item.ID {
		t.Fatal("created folder missing from refreshed list")
	}
	body.Item = listing.Items[0]
	if body.Item.CreateTime.Before(before) || body.Item.CreateTime.After(time.Now().Add(2*time.Second)) {
		t.Fatalf("default timestamp is not the current instant: %s (now %s)", body.Item.CreateTime, time.Now())
	}
	if !body.Item.UpdateTime.Equal(body.Item.CreateTime) {
		t.Fatal("initial timestamps disagree")
	}
	// Connecting with the new contract cannot reinterpret or rewrite old cells.
	const legacy = "2020-02-03 04:05:06"
	if err := f.db.Exec("UPDATE file SET create_time=?,update_time=? WHERE id=?", legacy, legacy, body.Item.ID).Error; err != nil {
		t.Fatal(err)
	}
	db, err := store.NewMySQL(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	var original string
	if err := db.Raw("SELECT CAST(create_time AS CHAR) FROM file WHERE id=?", body.Item.ID).Scan(&original).Error; err != nil || original != legacy {
		t.Fatalf("opening a connection changed history: %s %v", original, err)
	}
	// The database's ON UPDATE default must follow the same convention as its
	// insertion default, without rewriting the original creation timestamp.
	f.json(t, "PUT", "/api/files/"+fmt.Sprint(body.Item.ID)+"/rename", f.alice, map[string]any{"name": "timezone-renamed"}, 200)
	var updated model.File
	if err := db.First(&updated, body.Item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.UpdateTime.Before(before) || updated.UpdateTime.After(time.Now().Add(2*time.Second)) {
		t.Fatal("ON UPDATE timestamp shifted", updated.UpdateTime)
	}
	if updated.CreateTime.Format("2006-01-02 15:04:05") != legacy {
		t.Fatal("renaming rewrote the historical creation timestamp")
	}
}

func TestMySQLTimeExplicitFixedProfileKeepsItsLocation(t *testing.T) {
	f := newFixture(t)
	parsed, err := mysqldsn.ParseDSN(f.cfg.MySQLDSN)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Loc, err = time.LoadLocation("Etc/GMT-8")
	if err != nil {
		t.Fatal(err)
	}
	cfg := f.cfg
	cfg.MySQLDSN = parsed.FormatDSN()
	db, err := store.NewMySQL(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	var zone string
	if err := db.Raw("SELECT @@session.time_zone").Scan(&zone).Error; err != nil || zone != "+08:00" {
		t.Fatalf("fixed profile not installed: %s %v", zone, err)
	}
	file := model.File{UserID: f.aliceID, Filename: "fixed-profile", IsFolder: 1}
	if err := db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&file, file.ID).Error; err != nil {
		t.Fatal(err)
	}
	if age := time.Since(file.CreateTime); age < -2*time.Second || age > 2*time.Second {
		t.Fatalf("fixed-profile default shifted: %s", file.CreateTime)
	}
	_, offset := file.CreateTime.Zone()
	if offset != 8*3600 {
		t.Fatal("driver location was replaced", offset)
	}
}
