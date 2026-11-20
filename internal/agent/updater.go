package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultUpdateRepo = "jiuai233/NetWatchTgBot"
	DefaultService    = "netwatch-agent"
	autoUpdateLockTTL = 30 * time.Minute
	maxUpdateBytes    = 128 << 20
	checksumFileName  = "checksums.txt"
)

type githubRelease struct {
	TagName    string               `json:"tag_name"`
	Draft      bool                 `json:"draft"`
	Prerelease bool                 `json:"prerelease"`
	Assets     []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	Name string `json:"name"`
}

type Updater struct {
	mu             sync.Mutex
	configDir      string
	version        string
	githubAPIBase  string
	downloadOrigin string
	client         *http.Client
}

func NewUpdater(cfgPath, version string) *Updater {
	dir := "."
	if cfgPath != "" {
		dir = filepath.Dir(cfgPath)
	}
	return &Updater{
		configDir:      dir,
		version:        version,
		githubAPIBase:  "https://api.github.com",
		downloadOrigin: "https://github.com",
		client:         &http.Client{Timeout: 60 * time.Second},
	}
}

func ExpectedAssetName(goos, goarch string) string {
	name := fmt.Sprintf("netwatch-agent-%s-%s", goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

func (u *Updater) CheckAndSchedule(ctx context.Context, repo, proxy string, force bool) error {
	if runtime.GOOS == "windows" {
		return errors.New("windows self-update is not supported (collector is Linux-only)")
	}
	if !force && (u.version == "" || u.version == "dev") {
		return nil
	}
	if repo == "" {
		repo = DefaultUpdateRepo
	}
	asset := ExpectedAssetName(runtime.GOOS, runtime.GOARCH)
	tag, err := u.findLatestTag(ctx, repo, asset, force)
	if err != nil {
		return err
	}
	if tag == "" {
		return nil
	}
	return u.TriggerTargetUpdate(ctx, repo, proxy, tag)
}

func (u *Updater) TriggerTargetUpdate(ctx context.Context, repo, proxy, tag string) error {
	if runtime.GOOS == "windows" {
		return errors.New("windows self-update is not supported (collector is Linux-only)")
	}
	if tag == "" || tag == u.version {
		return nil
	}
	if repo == "" {
		repo = DefaultUpdateRepo
	}
	u.mu.Lock()
	defer u.mu.Unlock()

	lockPath := filepath.Join(u.configDir, "auto_update.lock")
	now := time.Now().Unix()
	if raw, err := os.ReadFile(lockPath); err == nil {
		if last, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil {
			if time.Duration(now-last)*time.Second < autoUpdateLockTTL {
				return nil
			}
		}
	}

	asset := ExpectedAssetName(runtime.GOOS, runtime.GOARCH)
	binPath, err := u.downloadAndVerify(ctx, repo, tag, asset, proxy)
	if err != nil {
		return err
	}

	exePath, err := os.Executable()
	if err != nil {
		_ = os.Remove(binPath)
		return err
	}
	exePath, _ = filepath.EvalSymlinks(exePath)

	method, err := scheduleDetachedReplace(binPath, exePath, DefaultService, now)
	if err != nil {
		_ = os.Remove(binPath)
		return err
	}
	_ = os.MkdirAll(u.configDir, 0o755)
	_ = os.WriteFile(lockPath, []byte(strconv.FormatInt(now, 10)), 0o600)
	log.Printf("agent update scheduled: %s -> %s (%s)", u.version, tag, method)
	return nil
}

func (u *Updater) findLatestTag(ctx context.Context, repo, assetName string, force bool) (string, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/releases?per_page=20", strings.TrimRight(u.githubAPIBase, "/"), repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "netwatch-agent")

	resp, err := u.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("github releases http %d", resp.StatusCode)
	}
	var releases []githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&releases); err != nil {
		return "", err
	}
	curVer, curOK := parseSemver(u.version)
	var bestTag string
	var bestVer [3]int
	for _, rel := range releases {
		if rel.Draft || rel.Prerelease || !hasAsset(rel, assetName) {
			continue
		}
		v, ok := parseSemver(rel.TagName)
		if !ok {
			continue
		}
		if !force && curOK && compareSemver(v, curVer) <= 0 {
			continue
		}
		if bestTag == "" || compareSemver(v, bestVer) > 0 {
			bestTag = rel.TagName
			bestVer = v
		}
	}
	return bestTag, nil
}

func (u *Updater) downloadAndVerify(ctx context.Context, repo, tag, assetName, proxy string) (string, error) {
	checksumURL := buildAssetDownloadURL(u.downloadOrigin, repo, tag, checksumFileName, proxy)
	checksumText, err := u.fetchString(ctx, checksumURL, 1<<20)
	if err != nil {
		return "", fmt.Errorf("download checksums: %w", err)
	}
	expectedHash, ok := parseChecksum(checksumText, assetName)
	if !ok {
		return "", fmt.Errorf("checksum not found for %s", assetName)
	}

	assetURL := buildAssetDownloadURL(u.downloadOrigin, repo, tag, assetName, proxy)
	if err := os.MkdirAll(u.configDir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(u.configDir, "netwatch-agent.update.tmp")
	if err := u.downloadFile(ctx, assetURL, dest); err != nil {
		return "", err
	}
	if err := verifySHA256(dest, expectedHash); err != nil {
		_ = os.Remove(dest)
		return "", err
	}
	return dest, nil
}

func (u *Updater) fetchString(ctx context.Context, rawURL string, limit int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "netwatch-agent")
	resp, err := u.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	return string(b), err
}

func (u *Updater) downloadFile(ctx context.Context, rawURL, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "netwatch-agent")
	resp, err := u.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download %s http %d", rawURL, resp.StatusCode)
	}
	tmp := dest + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, maxUpdateBytes+1))
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if n <= 0 || n > maxUpdateBytes {
		_ = os.Remove(tmp)
		return fmt.Errorf("invalid downloaded size: %d", n)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

// scheduleDetachedReplace runs the replacement outside the current service cgroup
// (via systemd-run when available, or nohup fallback) so systemctl restart does not kill the updater mid-flight.
func scheduleDetachedReplace(tmpBin, targetExe, serviceName string, now int64) (string, error) {
	cmdLine := fmt.Sprintf(
		"sleep 2; mv -f %s %s && chmod 755 %s && (systemctl restart %s 2>/dev/null || rc-service %s restart 2>/dev/null || true)",
		shellQuote(tmpBin), shellQuote(targetExe), shellQuote(targetExe), shellQuote(serviceName), shellQuote(serviceName),
	)
	if _, err := exec.LookPath("systemd-run"); err == nil {
		if _, statErr := os.Stat("/run/systemd/system"); statErr == nil {
			unit := fmt.Sprintf("%s-update-%d", serviceName, now)
			out, err := exec.Command("systemd-run", "--unit="+unit, "/bin/sh", "-c", cmdLine).CombinedOutput()
			if err == nil {
				return "systemd-run:" + unit, nil
			}
			log.Printf("systemd-run failed (%v: %s), falling back to nohup", err, strings.TrimSpace(string(out)))
		}
	}
	nohupCmd := fmt.Sprintf("nohup /bin/sh -c %s >/dev/null 2>&1 &", shellQuote(cmdLine))
	if err := exec.Command("/bin/sh", "-c", nohupCmd).Run(); err != nil {
		return "", err
	}
	return "nohup", nil
}

func buildAssetDownloadURL(origin, repo, tag, asset, proxy string) string {
	raw := fmt.Sprintf("%s/%s/releases/download/%s/%s", strings.TrimRight(origin, "/"), repo, tag, asset)
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		return raw
	}
	if !strings.HasSuffix(proxy, "/") {
		proxy += "/"
	}
	return proxy + raw
}

func parseChecksum(content, assetName string) (string, bool) {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if filepath.Base(name) == assetName && len(fields[0]) == 64 {
			return strings.ToLower(fields[0]), true
		}
	}
	return "", false
}

func verifySHA256(path, expectedHex string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, expectedHex) {
		return fmt.Errorf("sha256 mismatch: got %s want %s", got, expectedHex)
	}
	return nil
}

func hasAsset(rel githubRelease, name string) bool {
	for _, a := range rel.Assets {
		if a.Name == name {
			return true
		}
	}
	return false
}

func parseSemver(tag string) ([3]int, bool) {
	var out [3]int
	s := strings.TrimPrefix(strings.TrimSpace(tag), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return out, false
	}
	for i := 0; i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func compareSemver(a, b [3]int) int {
	for i := 0; i < 3; i++ {
		if a[i] > b[i] {
			return 1
		}
		if a[i] < b[i] {
			return -1
		}
	}
	return 0
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
