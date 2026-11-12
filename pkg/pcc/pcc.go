/*
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package pcc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"
)

const (
	// Volume attribute / StorageClass parameter keys (normalized).
	KeyEnablePCCCache     = "enablepcccache"
	KeyPCCEnabled         = "pccenabled"
	KeyPCCCacheSize       = "pcccachesize"
	KeyPCCIncludePatterns = "pccincludepatterns"
	KeyPCCMaxFileSize     = "pccmaxfilesize"
	KeyPCCRule            = "pccrule"

	defaultPCCBaseDir        = "/var/lib/kubelet/plugins/lustre.csi.storage.gke.io/pcc"
	defaultHighWatermark     = 90
	defaultLowWatermark      = 75
	defaultPurgeInterval     = 30 * time.Second
	defaultMaxTier2LoopBytes = 500 * 1024 * 1024 * 1024 // 500 GiB
	minTier2LoopBytes        = 1 * 1024 * 1024 * 1024   // 1 GiB
)

// Config holds the parsed PCC settings for a volume.
type Config struct {
	Enabled         bool
	CacheSizeBytes  int64
	IncludePatterns string
	MaxFileSize     string
	CustomRule      string
	HighWatermark   int
	LowWatermark    int
}

// ParseConfig extracts PCC configuration from normalized CSI volume context keys.
func ParseConfig(vc map[string]string) (*Config, error) {
	enabledStr := strings.ToLower(strings.TrimSpace(vc[KeyEnablePCCCache]))
	if enabledStr == "" {
		enabledStr = strings.ToLower(strings.TrimSpace(vc[KeyPCCEnabled]))
	}
	if enabledStr != "true" {
		return &Config{Enabled: false}, nil
	}

	cfg := &Config{
		Enabled:         true,
		IncludePatterns: strings.TrimSpace(vc[KeyPCCIncludePatterns]),
		MaxFileSize:     strings.TrimSpace(vc[KeyPCCMaxFileSize]),
		CustomRule:      strings.TrimSpace(vc[KeyPCCRule]),
		HighWatermark:   defaultHighWatermark,
		LowWatermark:    defaultLowWatermark,
	}

	if sizeStr := strings.TrimSpace(vc[KeyPCCCacheSize]); sizeStr != "" {
		qty, err := resource.ParseQuantity(sizeStr)
		if err != nil {
			return nil, fmt.Errorf("invalid pcc-cache-size %q: %w", sizeStr, err)
		}
		val := qty.Value()
		if val < minTier2LoopBytes {
			return nil, fmt.Errorf("pcc-cache-size %q must be at least 1Gi", sizeStr)
		}
		cfg.CacheSizeBytes = val
	}

	return cfg, nil
}

// BuildLctlParam constructs the parameter string passed to `lctl pcc add <mnt> <pccpath> -p "<param>"`.
func (c *Config) BuildLctlParam() string {
	rule := c.CustomRule
	if rule == "" {
		var clauses []string
		if c.IncludePatterns != "" {
			clauses = append(clauses, fmt.Sprintf("fname={%s}", c.IncludePatterns))
		} else {
			// Default project ID 0 matches all standard files on Lustre.
			clauses = append(clauses, "projid={0}")
		}
		if c.MaxFileSize != "" {
			clauses = append(clauses, fmt.Sprintf("size<%s", c.MaxFileSize))
		}
		rule = strings.Join(clauses, "&")
	}

	return fmt.Sprintf("%s rwid=1 roid=1 pccro=1 auto_attach=1 open_attach=1", rule)
}

// CommandRunner abstracts shell execution for unit testing.
type CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func defaultCommandRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

// Manager manages Local SSD PCC storage pools and per-volume PCC lifecycle on a GKE node.
type Manager struct {
	mu          sync.Mutex
	baseDir     string
	runCmd      CommandRunner
	purgerStops map[string]context.CancelFunc
}

// NewManager creates a new PCC Manager.
func NewManager() *Manager {
	return &Manager{
		baseDir:     defaultPCCBaseDir,
		runCmd:      defaultCommandRunner,
		purgerStops: make(map[string]context.CancelFunc),
	}
}

// AttachVolume provisions the Local SSD PCC pool (Tier 1 raw NVMe or Tier 2 loopback ext4 on /var/lib/kubelet)
// and attaches a Read-Only PCC dataset to stagingTargetPath.
func (m *Manager) AttachVolume(ctx context.Context, volumeID, stagingTargetPath string, cfg *Config) error {
	if cfg == nil || !cfg.Enabled {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	poolDir, err := m.ensurePoolLocked(ctx, cfg.CacheSizeBytes)
	if err != nil {
		return fmt.Errorf("failed to ensure Local SSD PCC pool: %w", err)
	}

	volDirName := volumeCacheDirName(volumeID)
	volCacheDir := filepath.Join(poolDir, volDirName)
	if err := os.MkdirAll(volCacheDir, 0o755); err != nil {
		return fmt.Errorf("failed to create volume PCC cache directory %s: %w", volCacheDir, err)
	}

	// Check if already attached on this staging target path.
	if out, err := m.runCmd(ctx, "lctl", "pcc", "list", stagingTargetPath); err == nil {
		if strings.Contains(string(out), volCacheDir) {
			klog.Infof("PCC dataset %s is already attached to %s", volCacheDir, stagingTargetPath)
			m.startPurgerLocked(stagingTargetPath, poolDir, volCacheDir, cfg.HighWatermark, cfg.LowWatermark)
			return nil
		}
	}

	param := cfg.BuildLctlParam()
	klog.Infof("Attaching Lustre RO-PCC dataset to %s: cacheDir=%s param=%q", stagingTargetPath, volCacheDir, param)
	out, err := m.runCmd(ctx, "lctl", "pcc", "add", stagingTargetPath, volCacheDir, "-p", param)
	if err != nil {
		outStr := string(out)
		if !strings.Contains(strings.ToLower(outStr), "file exists") {
			return fmt.Errorf("lctl pcc add %s %s -p %q failed: %w (output: %s)", stagingTargetPath, volCacheDir, param, err, outStr)
		}
		klog.Infof("lctl pcc add returned EEXIST for %s; treating as idempotent success", stagingTargetPath)
	}

	if listOut, listErr := m.runCmd(ctx, "lctl", "pcc", "list", stagingTargetPath); listErr == nil {
		klog.Infof("Verified active PCC configuration on %s:\n%s", stagingTargetPath, string(listOut))
	}

	// Lustre 2.14.0_p259 defaults pcc_dio_attach_threshold to 32 MiB, which switches
	// RO-PCC attachment to kernel O_DIRECT buffers that fail on Linux 6.12+. Raising
	// the threshold to 1 TiB keeps attachment on the standard buffered pcc_copy_data path.
	if _, dioErr := m.runCmd(ctx, "lctl", "set_param", "llite.*.pcc_dio_attach_threshold=1099511627776"); dioErr != nil {
		klog.V(4).Infof("Optional pcc_dio_attach_threshold tuning skipped: %v", dioErr)
	}

	m.startPurgerLocked(stagingTargetPath, poolDir, volCacheDir, cfg.HighWatermark, cfg.LowWatermark)
	return nil
}

// DetachVolume detaches any PCC dataset from stagingTargetPath before unmount.
func (m *Manager) DetachVolume(ctx context.Context, volumeID, stagingTargetPath string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if cancel, ok := m.purgerStops[stagingTargetPath]; ok {
		cancel()
		delete(m.purgerStops, stagingTargetPath)
	}

	poolDir := filepath.Join(m.baseDir, "pool")
	volCacheDir := filepath.Join(poolDir, volumeCacheDirName(volumeID))
	if _, err := os.Stat(volCacheDir); err != nil {
		return
	}

	klog.Infof("Detaching Lustre PCC dataset from %s (cacheDir=%s)", stagingTargetPath, volCacheDir)
	if out, err := m.runCmd(ctx, "lctl", "pcc", "clear", stagingTargetPath); err != nil {
		klog.Warningf("lctl pcc clear %s returned warning: %v (output: %s)", stagingTargetPath, err, string(out))
	}
}

func volumeCacheDirName(volumeID string) string {
	sum := sha256.Sum256([]byte(volumeID))
	return "vol-" + hex.EncodeToString(sum[:8])
}

func (m *Manager) ensurePoolLocked(ctx context.Context, requestedBytes int64) (string, error) {
	poolDir := filepath.Join(m.baseDir, "pool")
	if err := os.MkdirAll(poolDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create pool directory %s: %w", poolDir, err)
	}

	mounted, err := isMountPoint(poolDir)
	if err != nil {
		return "", err
	}
	if mounted {
		return poolDir, nil
	}

	// Tier 1: Check for unmounted raw Google Local NVMe SSDs.
	rawLSSDs, err := m.discoverUnmountedLocalSSDs(ctx)
	if err != nil {
		klog.Warningf("Failed to scan for raw Local SSDs, falling back to Tier 2 loopback pool: %v", err)
	}
	if len(rawLSSDs) > 0 {
		klog.Infof("Tier 1 PCC Pool: discovered %d unmounted Local SSD(s): %v", len(rawLSSDs), rawLSSDs)
		err := m.setupTier1RawPool(ctx, rawLSSDs, poolDir)
		if err == nil {
			return poolDir, nil
		}
		klog.Warningf("Tier 1 raw Local SSD setup failed (%v); falling back to Tier 2 loopback pool", err)
	}

	// Tier 2: Isolated loopback ext4 mounted with -o rw,atime,prjquota on Kubelet's Local SSD (/var/lib/kubelet).
	if err := m.setupTier2LoopbackPool(ctx, poolDir, requestedBytes); err != nil {
		return "", err
	}
	return poolDir, nil
}

func (m *Manager) discoverUnmountedLocalSSDs(ctx context.Context) ([]string, error) {
	out, err := m.runCmd(ctx, "lsblk", "-d", "-n", "-o", "NAME,MODEL")
	if err != nil {
		return nil, err
	}
	mdstatBytes, _ := os.ReadFile("/proc/mdstat")
	mdstat := string(mdstatBytes)

	var candidates []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		devName := fields[0]
		model := strings.Join(fields[1:], " ")
		if !strings.HasPrefix(model, "nvme_card") && !strings.Contains(model, "EphemeralDisk") {
			continue
		}
		if strings.Contains(mdstat, devName) {
			continue
		}
		devPath := "/dev/" + devName
		mntOut, err := m.runCmd(ctx, "lsblk", "-n", "-o", "MOUNTPOINTS", devPath)
		if err != nil || strings.TrimSpace(string(mntOut)) != "" {
			continue
		}
		candidates = append(candidates, devPath)
	}
	sort.Strings(candidates)
	return candidates, nil
}

func (m *Manager) setupTier1RawPool(ctx context.Context, devices []string, poolDir string) error {
	targetDev := devices[0]
	if len(devices) > 1 {
		targetDev = "/dev/md/lustre-pcc"
		if _, err := os.Stat(targetDev); os.IsNotExist(err) {
			if err := os.MkdirAll("/dev/md", 0o755); err != nil {
				return err
			}
			args := append([]string{
				"--create", targetDev,
				"--level=0",
				"--force",
				"--run",
				fmt.Sprintf("--raid-devices=%d", len(devices)),
			}, devices...)
			if out, err := m.runCmd(ctx, "mdadm", args...); err != nil {
				return fmt.Errorf("mdadm create failed: %w (%s)", err, string(out))
			}
		}
	}

	if out, err := m.runCmd(ctx, "mkfs.ext4", "-F", "-O", "project,quota", targetDev); err != nil {
		return fmt.Errorf("mkfs.ext4 on %s failed: %w (%s)", targetDev, err, string(out))
	}
	if out, err := m.runCmd(ctx, "mount", "-t", "ext4", "-o", "rw,atime,prjquota", targetDev, poolDir); err != nil {
		return fmt.Errorf("mount %s at %s failed: %w (%s)", targetDev, poolDir, err, string(out))
	}
	klog.Infof("Mounted Tier 1 raw Local SSD PCC pool (%s) at %s with strict atime and prjquota", targetDev, poolDir)
	return nil
}

func (m *Manager) setupTier2LoopbackPool(ctx context.Context, poolDir string, requestedBytes int64) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(m.baseDir, &stat); err != nil {
		return fmt.Errorf("statfs on %s failed: %w", m.baseDir, err)
	}

	availBytes := int64(stat.Bavail) * int64(stat.Bsize)
	// Never allocate more than 70% of available space on /var/lib/kubelet to avoid Kubelet DiskPressure.
	maxSafeBytes := (availBytes * 70) / 100
	if maxSafeBytes < minTier2LoopBytes {
		return fmt.Errorf("insufficient free space on %s (%d MiB available) for Lustre PCC pool", m.baseDir, availBytes/(1024*1024))
	}

	poolBytes := requestedBytes
	if poolBytes <= 0 {
		poolBytes = availBytes / 2
		if poolBytes > defaultMaxTier2LoopBytes {
			poolBytes = defaultMaxTier2LoopBytes
		}
	}
	if poolBytes > maxSafeBytes {
		klog.Warningf("Capping requested PCC cache size (%d GiB) to 70%% of available Local SSD space (%d GiB)",
			poolBytes/(1024*1024*1024), maxSafeBytes/(1024*1024*1024))
		poolBytes = maxSafeBytes
	}

	imgPath := filepath.Join(m.baseDir, "pcc-pool.img")
	if _, err := os.Stat(imgPath); os.IsNotExist(err) {
		klog.Infof("Creating Tier 2 sparse PCC loopback image %s (capacity=%d GiB) on Local SSD", imgPath, poolBytes/(1024*1024*1024))
		f, err := os.OpenFile(imgPath, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return fmt.Errorf("failed to create loopback image %s: %w", imgPath, err)
		}
		if err := f.Truncate(poolBytes); err != nil {
			f.Close()
			return fmt.Errorf("failed to truncate loopback image %s to %d bytes: %w", imgPath, poolBytes, err)
		}
		f.Close()

		if out, err := m.runCmd(ctx, "mkfs.ext4", "-F", "-O", "project,quota", imgPath); err != nil {
			_ = os.Remove(imgPath)
			return fmt.Errorf("mkfs.ext4 on %s failed: %w (%s)", imgPath, err, string(out))
		}
	}

	// Attach loop device with --direct-io=on (falling back to standard losetup if sector size alignment rejects direct-io).
	loopOut, err := m.runCmd(ctx, "losetup", "--find", "--show", "--direct-io=on", imgPath)
	if err != nil {
		klog.V(4).Infof("losetup --direct-io=on returned %v (%s), retrying without --direct-io", err, string(loopOut))
		loopOut, err = m.runCmd(ctx, "losetup", "--find", "--show", imgPath)
		if err != nil {
			return fmt.Errorf("losetup for %s failed: %w (%s)", imgPath, err, string(loopOut))
		}
	}
	loopDev := strings.TrimSpace(string(loopOut))

	if out, err := m.runCmd(ctx, "mount", "-t", "ext4", "-o", "rw,atime,prjquota", loopDev, poolDir); err != nil {
		_, _ = m.runCmd(ctx, "losetup", "-d", loopDev)
		return fmt.Errorf("mount %s at %s failed: %w (%s)", loopDev, poolDir, err, string(out))
	}

	syncLoopReadahead(loopDev, imgPath)

	klog.Infof("Mounted Tier 2 Local SSD loopback PCC pool (%s -> %s, capacity=%d GiB) at %s with strict atime and prjquota",
		imgPath, loopDev, poolBytes/(1024*1024*1024), poolDir)
	return nil
}

// syncLoopReadahead copies the backing device's readahead window onto the loop device.
//
// losetup does not inherit the backing device's readahead, so the loop device starts at
// the 128 KiB kernel default. GKE tunes the Local SSD RAID-0 array to 16 MiB, so without
// this the PCC pool reads the array in 128 KiB chunks and large sequential reads lose
// roughly half their bandwidth.
//
// Best effort: a PCC pool with a small readahead window is slow but correct, so a failure
// here is logged rather than failing the mount.
func syncLoopReadahead(loopDev, imgPath string) {
	backing, err := readaheadKBForPath(imgPath)
	if err != nil {
		klog.V(4).Infof("Could not read backing device readahead for %s: %v", imgPath, err)
		return
	}

	loopName := filepath.Base(loopDev)
	loopKnob := filepath.Join("/sys/block", loopName, "queue/read_ahead_kb")
	current, err := readIntFromFile(loopKnob)
	if err != nil {
		klog.V(4).Infof("Could not read %s: %v", loopKnob, err)
		return
	}
	if current >= backing {
		return
	}

	if err := os.WriteFile(loopKnob, []byte(strconv.Itoa(backing)), 0o644); err != nil {
		klog.V(4).Infof("Could not raise %s to %d: %v", loopKnob, backing, err)
		return
	}
	klog.Infof("Raised %s readahead from %d KiB to %d KiB to match the backing device",
		loopDev, current, backing)
}

// readaheadKBForPath returns the readahead window of the block device holding path.
func readaheadKBForPath(path string) (int, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0, err
	}
	major, minor := unix.Major(uint64(st.Dev)), unix.Minor(uint64(st.Dev))
	sysDev := fmt.Sprintf("/sys/dev/block/%d:%d", major, minor)

	// Whole disks expose queue/read_ahead_kb directly. Partitions do not, so fall back to
	// the parent disk one level up.
	for _, knob := range []string{
		filepath.Join(sysDev, "queue/read_ahead_kb"),
		filepath.Join(sysDev, "../queue/read_ahead_kb"),
	} {
		if v, err := readIntFromFile(knob); err == nil {
			return v, nil
		}
	}
	return 0, fmt.Errorf("no read_ahead_kb found for device %d:%d backing %s", major, minor, path)
}

func readIntFromFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

func isMountPoint(target string) (bool, error) {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return false, err
	}
	cleanTarget := filepath.Clean(target)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && filepath.Clean(fields[1]) == cleanTarget {
			return true, nil
		}
	}
	return false, nil
}

type cachedFile struct {
	path  string
	atime time.Time
	size  int64
}

func (m *Manager) startPurgerLocked(stagingTargetPath, poolDir, volCacheDir string, highWatermark, lowWatermark int) {
	if _, exists := m.purgerStops[stagingTargetPath]; exists {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.purgerStops[stagingTargetPath] = cancel

	go func() {
		ticker := time.NewTicker(defaultPurgeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.reconcilePurge(ctx, stagingTargetPath, poolDir, volCacheDir, highWatermark, lowWatermark)
			}
		}
	}()
}

func (m *Manager) reconcilePurge(ctx context.Context, stagingTargetPath, poolDir, volCacheDir string, highWatermark, lowWatermark int) {
	usedPct, bytesToFree, err := checkPoolUsage(poolDir, highWatermark, lowWatermark)
	if err != nil || bytesToFree <= 0 {
		return
	}

	klog.Infof("PCC pool %s usage is %d%% (>= %d%% high watermark); scanning %s to free %d MiB down to %d%%",
		poolDir, usedPct, highWatermark, volCacheDir, bytesToFree/(1024*1024), lowWatermark)

	var files []cachedFile
	_ = filepath.WalkDir(volCacheDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}
		files = append(files, cachedFile{
			path:  path,
			atime: time.Unix(stat.Atim.Sec, stat.Atim.Nsec),
			size:  info.Size(),
		})
		return nil
	})

	// Sort by oldest access time (LRU) first.
	sort.Slice(files, func(i, j int) bool {
		return files[i].atime.Before(files[j].atime)
	})

	var freed int64
	for _, f := range files {
		if freed >= bytesToFree {
			break
		}
		if fid, ok := extractFIDFromPCCPath(volCacheDir, f.path); ok {
			_, _ = m.runCmd(ctx, "lfs", "pcc", "detach_fid", "-m", stagingTargetPath, fid)
		}
		if err := os.Remove(f.path); err == nil || os.IsNotExist(err) {
			freed += f.size
			klog.V(4).Infof("Purged LRU PCC file %s (atime=%s, size=%d)", f.path, f.atime.Format(time.RFC3339), f.size)
		}
	}
}

func checkPoolUsage(poolDir string, highWatermark, lowWatermark int) (int, int64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(poolDir, &stat); err != nil {
		return 0, 0, err
	}
	if stat.Blocks == 0 {
		return 0, 0, nil
	}
	totalBytes := int64(stat.Blocks) * int64(stat.Bsize)
	availBytes := int64(stat.Bavail) * int64(stat.Bsize)
	usedBytes := totalBytes - availBytes
	usedPct := int((usedBytes * 100) / totalBytes)
	if usedPct < highWatermark {
		return usedPct, 0, nil
	}
	targetUsedBytes := (totalBytes * int64(lowWatermark)) / 100
	bytesToFree := usedBytes - targetUsedBytes
	if bytesToFree < 0 {
		bytesToFree = 0
	}
	return usedPct, bytesToFree, nil
}

// extractFIDFromPCCPath converts a PCC backend file path (`<volCacheDir>/0004/0000/0bd1/0000/0002/0000/0x200000bd1:0x4:0x0`)
// into its Lustre FID string (`0x200000bd1:0x4:0x0`).
func extractFIDFromPCCPath(volCacheDir, filePath string) (string, bool) {
	base := filepath.Base(filePath)
	if strings.HasPrefix(base, "0x") && strings.Count(base, ":") == 2 {
		return base, true
	}
	return "", false
}
