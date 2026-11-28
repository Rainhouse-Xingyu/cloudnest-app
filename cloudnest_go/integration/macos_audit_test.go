//go:build integration && darwin

package integration

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloudenest_go/model"
)

type nativeAuditSeed struct {
	Action        string
	CorruptAction string
	SlowAction    string
	Date          time.Time
	OwnerIDs      []int64
	PeerIDs       []int64
}

func seedNativeAudit(t *testing.T, f *fixture, slow bool) nativeAuditSeed {
	t.Helper()
	seed := nativeAuditSeed{Action: fmt.Sprintf("native_audit_%d", f.aliceID), Date: time.Date(2026, 11, 1, 12, 34, 0, 0, time.UTC)}
	seed.CorruptAction = seed.Action + "_corrupt"
	seed.SlowAction = seed.Action + "_slow"
	for i := 0; i < 55; i++ {
		status := "success"
		if i%5 == 0 {
			status = "failed"
		}
		detail := fmt.Sprintf("Native audit owner row %d", i)
		if i == 54 {
			detail = "=SUM(1,2)"
		}
		row := model.OperationLog{UserID: &f.aliceID, Action: seed.Action, Status: status, Detail: detail, ClientIP: "2001:db8::1", CreateTime: seed.Date}
		if err := f.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		seed.OwnerIDs = append(seed.OwnerIDs, row.ID)
	}
	for i := 0; i < 4; i++ {
		row := model.OperationLog{UserID: &f.bobID, Action: seed.Action, Status: "success", Detail: "synthetic-peer-private", CreateTime: seed.Date}
		if err := f.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		seed.PeerIDs = append(seed.PeerIDs, row.ID)
	}
	system := model.OperationLog{Action: seed.Action, Status: "success", Detail: "synthetic system audit record", CreateTime: seed.Date}
	if err := f.db.Create(&system).Error; err != nil {
		t.Fatal(err)
	}
	corrupt := model.OperationLog{UserID: &f.aliceID, Action: seed.CorruptAction, Status: "success", Detail: "real wire integrity rejection", CreateTime: seed.Date}
	if err := f.db.Create(&corrupt).Error; err != nil {
		t.Fatal(err)
	}
	if slow {
		rows := make([]model.OperationLog, 800)
		for i := range rows {
			rows[i] = model.OperationLog{UserID: &f.aliceID, Action: seed.SlowAction, Status: "success", Detail: fmt.Sprintf("row %d ", i) + string(bytes.Repeat([]byte("s"), 400)), CreateTime: seed.Date}
		}
		if err := f.db.CreateInBatches(&rows, 128).Error; err != nil {
			t.Fatal(err)
		}
	}
	return seed
}

func TestMacOSClientAudit(t *testing.T) {
	if os.Getenv("CLOUDNEST_TEST_SWIFT") != "1" {
		t.Skip("set CLOUDNEST_TEST_SWIFT=1 for the real native client")
	}
	f := newFixture(t)
	admin := model.Role{RoleName: "admin"}
	if err := f.db.Where("role_name=?", "admin").FirstOrCreate(&admin).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&model.UserRole{UserID: f.aliceID, RoleID: admin.ID}).Error; err != nil {
		t.Fatal(err)
	}
	seed := seedNativeAudit(t, f, true)
	var owner, peer model.User
	if err := f.db.First(&owner, f.aliceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&peer, f.bobID).Error; err != nil {
		t.Fatal(err)
	}
	var redirects atomic.Int32
	other := newNativeTLSServer(f, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirects.Add(1); w.WriteHeader(200) }))
	defer other.Close()
	type stoppedResponse struct {
		status    int
		bytes     int64
		cancelled bool
	}
	stopped := make(chan stoppedResponse, 1)
	var advance sync.Once
	server := newNativeTLSServer(f, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.URL.Query().Get("action")
		if r.URL.Path == "/api/operation-logs/export" {
			if action == seed.Action+"_redirect" {
				http.Redirect(w, r, other.URL+"/capture", 302)
				return
			}
			if action == seed.CorruptAction {
				f.router.ServeHTTP(&publicShareCorruptWriter{ResponseWriter: w}, r)
				return
			}
			if action == seed.SlowAction {
				request := r.Clone(r.Context())
				url := *r.URL
				query := url.Query()
				query.Set("rate_kbps", "16")
				url.RawQuery = query.Encode()
				request.URL = &url
				counter := &publicShareCountingWriter{ResponseWriter: w}
				defer func() { stopped <- stoppedResponse{counter.status, counter.bytes, r.Context().Err() != nil} }()
				f.router.ServeHTTP(counter, request)
				return
			}
		}
		advanced := false
		if r.URL.Path == "/api/operation-logs" && action == seed.Action && r.URL.Query().Get("all") == "0" && r.URL.Query().Get("snapshot_id") == "" {
			advance.Do(func() {
				advanced = true
				// Fixture-only concurrent insert after the real query takes its range,
				// before its bounded JSON is copied to the actual TLS response writer.
				recorder := httptest.NewRecorder()
				f.router.ServeHTTP(recorder, r)
				var page auditCursorResponse
				if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil || recorder.Code != 200 || page.Pagination.Total != 55 {
					t.Error("initial native audit range not captured")
				}
				row := model.OperationLog{UserID: &f.aliceID, Action: seed.Action, Status: "success", Detail: "inserted after native first range", CreateTime: seed.Date}
				if err := f.db.Create(&row).Error; err != nil {
					t.Error(err)
				}
				for key, values := range recorder.Header() {
					w.Header()[key] = values
				}
				w.WriteHeader(recorder.Code)
				_, _ = w.Write(recorder.Body.Bytes())
			})
		}
		if !advanced {
			f.router.ServeHTTP(w, r)
		}
	}))
	defer server.Close()
	directory := t.TempDir()
	certificate := filepath.Join(directory, "server.der")
	if err := os.WriteFile(certificate, server.Certificate().Raw, 0600); err != nil {
		t.Fatal(err)
	}
	packagePath, err := filepath.Abs("../../cloudnest_macos")
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"admin", "ordinary", "restricted"} {
		t.Run(role, func(t *testing.T) {
			user := owner
			if role != "admin" {
				user = peer
			}
			if role == "restricted" {
				if err := f.db.Create(&model.UserPathPermission{UserID: peer.ID, Method: "GET", Path: "/api/files"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			manifest := map[string]any{"origin": server.URL, "certificate": certificate, "username": user.Username, "password": "disposable-fixture-password", "role": role, "action": seed.Action, "corrupt_action": seed.CorruptAction, "slow_action": seed.SlowAction, "date": seed.Date.Format(time.RFC3339), "user_id": user.ID, "owner_id": f.aliceID, "peer_id": f.bobID, "directory": directory}
			data, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "audit-"+role+".json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("swift", "test", "--package-path", packagePath, "--filter", "realBackendAudit")
			command.Env = append(os.Environ(), "CLOUDNEST_SWIFT_AUDIT_FIXTURE="+path)
			output, err := command.CombinedOutput()
			t.Log(string(output))
			if err != nil {
				t.Fatal(err)
			}
			if role == "restricted" {
				return
			}
			name := role + "-audit.csv"
			csvData, err := os.ReadFile(filepath.Join(directory, name))
			if err != nil {
				t.Fatal(err)
			}
			rows, err := csv.NewReader(bytes.NewReader(csvData)).ReadAll()
			if err != nil {
				t.Fatal(err)
			}
			expected := 56
			if role == "ordinary" {
				expected = 4
			}
			if len(rows) != expected+1 {
				t.Fatal("native CSV records", len(rows))
			}
			for _, row := range rows[1:] {
				if len(row) != 8 || row[1] != strconv.FormatInt(user.ID, 10) {
					t.Fatal("native CSV crossed account scope")
				}
			}
			var receipt struct {
				Bytes int    `json:"bytes"`
				SHA   string `json:"sha256"`
				Rows  int    `json:"rows"`
			}
			data, err = os.ReadFile(filepath.Join(directory, role+"-receipt.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.Bytes != len(csvData) || receipt.Rows != expected || receipt.SHA != fmt.Sprintf("%x", sha256.Sum256(csvData)) {
				t.Fatal("native audit receipt mismatch")
			}
			if role == "admin" {
				select {
				case response := <-stopped:
					if response.status != 200 || response.bytes <= 0 || !response.cancelled {
						t.Fatal("native CSV did not cancel the real TCP body", response)
					}
					t.Logf("Native audit CSV cancelled: status=%d bytes=%d parent_cancelled=%v", response.status, response.bytes, response.cancelled)
				case <-time.After(3 * time.Second):
					t.Fatal("native CSV cancellation retained server response")
				}
				if transferAudit(t, f).Usage.Charged != 0 {
					t.Fatal("native CSV cancellation leaked temporary budget")
				}
			}
		})
	}
	if redirects.Load() != 0 {
		t.Fatal("native authenticated audit followed redirect")
	}
	var count int64
	if err := f.db.Model(&model.OperationLog{}).Where("action IN ?", []string{seed.Action, seed.CorruptAction, seed.SlowAction}).Count(&count).Error; err != nil || count != 862 {
		t.Fatal("native audit modified historical records", count, err)
	}
}
