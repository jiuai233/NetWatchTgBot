package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"netwatch/internal/config"
	"netwatch/internal/model"
)

func TestRunnerQueueBackfillAndConfigSync(t *testing.T) {
	var failMode atomic.Bool
	failMode.Store(true)

	var received []model.AgentReport
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failMode.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		var rep model.AgentReport
		if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		received = append(received, rep)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(model.ReportResponse{
			OK:        true,
			ConfigMD5: "md5-v2",
			AgentConfig: &model.AgentSyncConfig{
				IntervalSec: 15,
				LocalTargets: []model.TargetDef{
					{Name: "CT", Tag: "gd", Addr: "127.0.0.1:80"},
				},
			},
		})
	}))
	defer srv.Close()

	cfgPath := filepath.Join(t.TempDir(), "agent.yaml")
	cfg := &config.Config{
		Mode:      "agent",
		MasterURL: srv.URL,
		LocalCollector: config.LocalCollect{
			Enabled:     true,
			NodeID:      "n-test",
			IntervalSec: 30,
		},
	}
	runner := NewRunner(cfg, cfgPath, "v0.1.0")

	t0 := time.Now().Add(-2 * time.Minute)
	runner.enqueue(model.AgentReport{NodeID: "n-test", Timestamp: t0, CPUPct: 11})
	runner.enqueue(model.AgentReport{NodeID: "n-test", Timestamp: t0.Add(30 * time.Second), CPUPct: 22})

	// First flush fails -> both reports stay queued with backoff
	runner.flush(context.Background())
	if runner.queueLen() != 2 {
		t.Fatalf("expected 2 queued reports after failure, got %d", runner.queueLen())
	}

	// Recover server & clear backoff timer -> flush sends both in order and syncs config
	failMode.Store(false)
	runner.mu.Lock()
	runner.nextRetryAt = time.Time{}
	runner.mu.Unlock()

	runner.flush(context.Background())
	if runner.queueLen() != 0 {
		t.Fatalf("expected empty queue after recovery, got %d", runner.queueLen())
	}
	if len(received) != 2 || received[0].CPUPct != 11 || received[1].CPUPct != 22 {
		t.Fatalf("expected ordered backfill [11, 22], got %+v", received)
	}
	if runner.currentInterval() != 15*time.Second {
		t.Fatalf("expected synced interval 15s, got %s", runner.currentInterval())
	}
	reloaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.LocalCollector.IntervalSec != 15 || len(reloaded.LocalCollector.LocalTargets) != 1 {
		t.Fatalf("expected persisted synced config, got %+v", reloaded.LocalCollector)
	}
}

func TestUpdaterVerifyChecksumSuccessAndMismatch(t *testing.T) {
	payload := []byte("fake-new-agent-binary-v0.2.0")
	sum := sha256.Sum256(payload)
	validHash := hex.EncodeToString(sum[:])

	asset := ExpectedAssetName("linux", "amd64")
	var badHash atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/releases":
			_ = json.NewEncoder(w).Encode([]githubRelease{
				{
					TagName: "v0.2.0",
					Assets:  []githubReleaseAsset{{Name: asset}, {Name: checksumFileName}},
				},
			})
		case fmt.Sprintf("/owner/repo/releases/download/v0.2.0/%s", checksumFileName):
			h := validHash
			if badHash.Load() {
				h = "0000000000000000000000000000000000000000000000000000000000000000"
			}
			_, _ = fmt.Fprintf(w, "%s  %s\n", h, asset)
		case fmt.Sprintf("/owner/repo/releases/download/v0.2.0/%s", asset):
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	u := NewUpdater(filepath.Join(dir, "agent.yaml"), "v0.1.0")
	u.githubAPIBase = srv.URL
	u.downloadOrigin = srv.URL

	tag, err := u.findLatestTag(context.Background(), "owner/repo", asset, false)
	if err != nil || tag != "v0.2.0" {
		t.Fatalf("expected v0.2.0, got tag=%s err=%v", tag, err)
	}

	binPath, err := u.downloadAndVerify(context.Background(), "owner/repo", "v0.2.0", asset, "")
	if err != nil {
		t.Fatalf("expected downloadAndVerify success, got %v", err)
	}
	if _, err := os.Stat(binPath); err != nil {
		t.Fatalf("expected verified temp binary at %s: %v", binPath, err)
	}

	// Mismatch must fail and clean up temp binary
	badHash.Store(true)
	if _, err := u.downloadAndVerify(context.Background(), "owner/repo", "v0.2.0", asset, ""); err == nil {
		t.Fatal("expected sha256 mismatch error")
	}
	if _, err := os.Stat(binPath); !os.IsNotExist(err) {
		t.Fatalf("expected failed binary to be deleted, stat err=%v", err)
	}
}
