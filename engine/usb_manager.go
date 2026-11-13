// Package engine — usb_manager.go handles strict removable media discovery,
// dynamic drive capacity queries via sysfs, autonomous 2-partition provisioning
// (ORCH_STORAGE + ORCH_BOOT), universal monolithic EFI bootloader generation,
// auto-mount discovery of external ISOs/archives, and payload streaming.
package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type flexibleRM string

func (r *flexibleRM) UnmarshalJSON(b []byte) error {
	raw := strings.TrimSpace(string(b))
	raw = strings.Trim(raw, `"`)
	if raw == "true" || raw == "1" {
		*r = "1"
	} else {
		*r = "0"
	}
	return nil
}

type usbDeviceRecord struct {
	Name       string            `json:"name"`
	Size       string            `json:"size"`
	Tran       string            `json:"tran"`
	Mountpoint string            `json:"mountpoint"`
	FSType     string            `json:"fstype"`
	Type       string            `json:"type"`
	Removable  flexibleRM        `json:"rm"`
	Vendor     string            `json:"vendor"`
	Model      string            `json:"model"`
	Children   []usbDeviceRecord `json:"children,omitempty"`
}

type usbLsblkResponse struct {
	BlockDevices []usbDeviceRecord `json:"blockdevices"`
}

type USBTargetDevice struct {
	Name       string
	DevPath    string
	Size       string
	SizeBytes  uint64
	Vendor     string
	Model      string
	MountPoint string
}

type USBPayload struct {
	DeviceName   string
	FilePath     string
	FileName     string
	SizeBytes    int64
	MountPoint   string
	WasAutoMount bool
}

type USBProvisionResult struct {
	StoragePartition string
	BootPartition    string
	StorageBytes     uint64
	BootBytes        uint64
	StorageMountPath string
}

func QueryBlockDeviceBytes(devName string) (uint64, error) {
	baseName := filepath.Base(devName)

	// 1. Check if exact partition node exists in /sys/class/block
	sizeFile := filepath.Join("/sys/class/block", baseName, "size")
	data, err := os.ReadFile(sizeFile)
	if err != nil {
		// 2. If exact partition not found, fallback to parent disk
		parent := baseName
		for len(parent) > 0 && (parent[len(parent)-1] >= '0' && parent[len(parent)-1] <= '9') {
			parent = parent[:len(parent)-1]
		}
		parent = strings.TrimSuffix(parent, "p")
		sizeFile = filepath.Join("/sys/class/block", parent, "size")
		data, err = os.ReadFile(sizeFile)
		if err != nil {
			return 0, fmt.Errorf("reading block size for %s: %w", devName, err)
		}
	}

	sectors, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing sector count: %w", err)
	}
	return sectors * 512, nil
}

func ProbeRemovableTargets() ([]USBTargetDevice, error) {
	out, err := exec.Command("lsblk", "-J", "-o", "NAME,SIZE,TRAN,MOUNTPOINT,FSTYPE,TYPE,RM,VENDOR,MODEL").Output()
	if err != nil {
		return nil, fmt.Errorf("lsblk target probe failed: %w", err)
	}

	var data usbLsblkResponse
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("parsing lsblk telemetry: %w", err)
	}

	var targets []USBTargetDevice

	for _, dev := range data.BlockDevices {
		// Audit 1C-followup: classify removable/USB disks only by lsblk's own
		// TRAN/RM signals — never by a positional device-name guess
		// (sdc/sdd/sde). On a machine with more than three SATA/SAS disks, an
		// internal data disk can land at sdc or later, and treating it as
		// "definitely USB" risked wiping it as a staging target. Fail closed:
		// if lsblk doesn't say usb/removable, this is not treated as USB.
		isUSB := strings.ToLower(dev.Tran) == "usb" || dev.Removable == "1" || dev.Removable == "true"
		if isUSB && dev.Type == "disk" {
			devPath := "/dev/" + dev.Name
			byteSize, _ := QueryBlockDeviceBytes(dev.Name)
			targets = append(targets, USBTargetDevice{
				Name:       dev.Name,
				DevPath:    devPath,
				Size:       dev.Size,
				SizeBytes:  byteSize,
				Vendor:     strings.TrimSpace(dev.Vendor),
				Model:      strings.TrimSpace(dev.Model),
				MountPoint: dev.Mountpoint,
			})
		}
	}

	return targets, nil
}

func PrepareAutonomousUSBDisk(targetDev USBTargetDevice, logFn func(string)) (*USBProvisionResult, error) {
	if logFn == nil {
		logFn = func(string) {}
	}

	diskPath := targetDev.DevPath
	totalBytes := targetDev.SizeBytes
	if totalBytes == 0 {
		var err error
		totalBytes, err = QueryBlockDeviceBytes(targetDev.Name)
		if err != nil {
			return nil, fmt.Errorf("resolving disk geometry: %w", err)
		}
	}

	minRequired := uint64(14 * 1024 * 1024 * 1024)
	if totalBytes < minRequired {
		return nil, fmt.Errorf("USB drive capacity (%.1f GB) is too small. Minimum 16 GB required", float64(totalBytes)/(1024*1024*1024))
	}

	logFn(fmt.Sprintf("=> [USB PREPARE] Dynamic capacity confirmed: %.2f GB total", float64(totalBytes)/(1024*1024*1024)))

	bootBytes := uint64(1 * 1024 * 1024 * 1024)
	storageBytes := totalBytes - bootBytes
	storageEndMB := storageBytes / (1024 * 1024)

	logFn("=> [USB PREPARE] Unmounting existing partitions on " + diskPath)
	_ = exec.Command("bash", "-c", fmt.Sprintf("umount -l %s* 2>/dev/null || true", diskPath)).Run()

	logFn("=> [USB PREPARE] Purging stale filesystem signatures...")
	_ = exec.Command("wipefs", "-a", "-f", diskPath).Run()

	logFn("=> [USB PREPARE] Writing clean GPT partition table...")
	if out, err := exec.Command("parted", "-s", diskPath, "mklabel", "gpt").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("writing GPT label to %s: %w (%s)", diskPath, err, string(out))
	}

	logFn(fmt.Sprintf("=> [USB PREPARE] Carving Partition 1 [ORCH_STORAGE] (1MiB -> %dMiB)...", storageEndMB))
	if out, err := exec.Command("parted", "-s", diskPath, "mkpart", "ORCH_STORAGE", "ext4", "1MiB", fmt.Sprintf("%dMiB", storageEndMB)).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("creating storage partition: %w (%s)", err, string(out))
	}

	logFn("=> [USB PREPARE] Carving Partition 2 [ORCH_BOOT] (FAT32 Live ESP)...")
	if out, err := exec.Command("parted", "-s", diskPath, "mkpart", "ORCH_BOOT", "fat32", fmt.Sprintf("%dMiB", storageEndMB), "100%").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("creating boot partition: %w (%s)", err, string(out))
	}

	_ = exec.Command("parted", "-s", diskPath, "set", "2", "boot", "on").Run()
	_ = exec.Command("parted", "-s", diskPath, "set", "2", "esp", "on").Run()
	_ = exec.Command("partprobe", diskPath).Run()
	_ = exec.Command("udevadm", "settle", "--timeout=10").Run()
	time.Sleep(1 * time.Second)

	part1 := JoinDiskAndPartition(diskPath, "1")
	part2 := JoinDiskAndPartition(diskPath, "2")

	logFn("=> [USB PREPARE] Formatting Partition 1 as ext4 [ORCH_STORAGE]...")
	if out, err := exec.Command("mkfs.ext4", "-F", "-q", "-L", "ORCH_STORAGE", part1).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("formatting ext4 storage partition %s: %w (%s)", part1, err, string(out))
	}

	logFn("=> [USB PREPARE] Formatting Partition 2 as FAT32 [ORCH_BOOT]...")
	if out, err := exec.Command("mkfs.vfat", "-F32", "-n", "ORCH_BOOT", part2).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("formatting FAT32 boot partition %s: %w (%s)", part2, err, string(out))
	}

	mountDir := "/mnt/orch_usb_storage"
	_ = os.MkdirAll(mountDir, 0755)
	_ = exec.Command("umount", "-l", mountDir).Run()

	logFn("=> [USB PREPARE] Mounting storage vault at " + mountDir)
	if out, err := exec.Command("mount", part1, mountDir).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("mounting storage partition %s to %s: %w (%s)", part1, mountDir, err, string(out))
	}

	return &USBProvisionResult{
		StoragePartition: part1,
		BootPartition:    part2,
		StorageBytes:     storageBytes,
		BootBytes:        bootBytes,
		StorageMountPath: mountDir,
	}, nil
}

func ArmAutonomousUSBBootloader(bootPart, isoPayloadPath, targetDisk string, logFn func(string)) error {
	if logFn == nil {
		logFn = func(string) {}
	}

	bootMount := "/mnt/orch_usb_boot"
	_ = os.MkdirAll(bootMount, 0755)
	_ = exec.Command("umount", "-l", bootMount).Run()

	logFn("=> [UNIVERSAL EFI] Mounting ORCH_BOOT at " + bootMount + "...")
	if out, err := exec.Command("mount", bootPart, bootMount).CombinedOutput(); err != nil {
		return fmt.Errorf("mounting boot partition %s: %w (%s)", bootPart, err, string(out))
	}
	defer func() {
		_ = exec.Command("sync").Run()
		_ = exec.Command("umount", "-l", bootMount).Run()
	}()

	efiBootDir := filepath.Join(bootMount, "EFI", "BOOT")
	_ = os.MkdirAll(efiBootDir, 0755)

	bootGrubDir := filepath.Join(bootMount, "boot", "grub")
	_ = os.MkdirAll(bootGrubDir, 0755)

	targetShim := filepath.Join(efiBootDir, "BOOTX64.EFI")
	targetGrub := filepath.Join(efiBootDir, "grubx64.efi")

	logFn("=> [UNIVERSAL EFI] Injecting autonomous zero-touch orchestration boot configuration...")

	grubCfgContent := `set default="0"
set timeout=2

search --no-floppy --set=storage_part --label ORCH_STORAGE

menuentry "Universal OS Orchestrator: Autonomous Offline Resizer & Installer" {
    set isofile="/os_image.payload"
    loopback loop ($storage_part)$isofile
    linux (loop)/live/vmlinuz boot=live findiso=$isofile components quiet systemd.run="bash -c 'mkdir -p /mnt/vault && for i in $(seq 1 30); do mount -L ORCH_STORAGE /mnt/vault 2>/dev/null && break; sleep 1; done; if [ -f /mnt/vault/transient_resize.sh ]; then bash /mnt/vault/transient_resize.sh > /dev/tty1 2>&1; fi'"
    initrd (loop)/live/initrd.img
}
`

	_ = os.WriteFile(filepath.Join(efiBootDir, "grub.cfg"), []byte(grubCfgContent), 0644)
	_ = os.WriteFile(filepath.Join(bootGrubDir, "grub.cfg"), []byte(grubCfgContent), 0644)
	_ = os.WriteFile(filepath.Join(bootMount, "grub.cfg"), []byte(grubCfgContent), 0644)

	earlyCfg := filepath.Join("/tmp", "early-grub.cfg")
	earlyCfgContent := `search --no-floppy --label --set=root ORCH_BOOT
set prefix=($root)/EFI/BOOT
normal
`
	_ = os.WriteFile(earlyCfg, []byte(earlyCfgContent), 0644)
	defer os.Remove(earlyCfg)

	builtMonolithic := false
	grubModDir := "/usr/lib/grub/x86_64-efi"
	if fi, err := os.Stat(grubModDir); err == nil && fi.IsDir() {
		logFn("=> [UNIVERSAL EFI] Compiling monolithic standalone EFI core via grub-mkimage...")
		mkimageArgs := []string{
			"-d", grubModDir,
			"-O", "x86_64-efi",
			"-o", targetShim,
			"-c", earlyCfg,
			"-p", "/EFI/BOOT",
			"part_gpt", "part_msdos", "fat", "ext2",
			"search", "search_label", "search_fs_file", "search_fs_uuid",
			"normal", "configfile", "echo", "test", "reboot",
			"linux", "boot", "loopback", "iso9660",
		}
		if out, err := exec.Command("grub-mkimage", mkimageArgs...).CombinedOutput(); err == nil {
			if sFi, sErr := os.Stat(targetShim); sErr == nil && sFi.Size() > 0 {
				builtMonolithic = true
				logFn(fmt.Sprintf("=> [UNIVERSAL EFI] Monolithic BOOTX64.EFI compiled successfully (%d KB).", sFi.Size()/1024))
			}
		} else {
			logFn(fmt.Sprintf("=> [WARNING] grub-mkimage failed: %s", string(out)))
		}
	}

	if !builtMonolithic || fileIsEmpty(targetShim) {
		logFn("=> [UNIVERSAL EFI] Staging verified EFI bootloader from host system...")
		candidates := []string{
			"/boot/efi/EFI/kali/grubx64.efi",
			"/boot/efi/EFI/BOOT/BOOTX64.EFI",
			"/boot/efi/EFI/debian/grubx64.efi",
			"/usr/lib/shim/shimx64.efi.signed",
			"/usr/lib/grub/x86_64-efi-signed/grubx64.efi.signed",
		}
		for _, c := range candidates {
			if fi, err := os.Stat(c); err == nil && fi.Size() > 0 {
				if err := copyFile(c, targetShim); err == nil {
					logFn(fmt.Sprintf("=> [UNIVERSAL EFI] Populated BOOTX64.EFI from: %s (%d KB)", c, fi.Size()/1024))
					break
				}
			}
		}
	}

	if _, err := os.Stat(targetShim); err == nil {
		_ = copyFile(targetShim, targetGrub)
	}

	mokSources := []string{
		"/usr/lib/shim/mmx64.efi.signed",
		"/boot/efi/EFI/kali/mmx64.efi",
		"/boot/efi/EFI/debian/mmx64.efi",
	}
	targetMok := filepath.Join(efiBootDir, "mmx64.efi")
	for _, src := range mokSources {
		if _, err := os.Stat(src); err == nil {
			_ = copyFile(src, targetMok)
			break
		}
	}

	_ = exec.Command("sync").Run()

	fi, err := os.Stat(targetShim)
	if err != nil || fi.Size() == 0 {
		return fmt.Errorf("fatal: failed to populate non-zero byte EFI binary at %s", targetShim)
	}

	logFn(fmt.Sprintf("=> [UNIVERSAL EFI] Stack armed: BOOTX64.EFI (%d KB verified)", fi.Size()/1024))
	return nil
}

func fileIsEmpty(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	return info.Size() == 0
}

func StagePayloadWithContext(ctx context.Context, srcPath, destDir string, progressFn func(written, total int64)) (string, error) {
	// Transparently extract ISO if srcPath points to a ZIP file
	scratchDir := filepath.Join(os.TempDir(), "orch_extracted_payload")
	if normalizedPath, err := EnsureRawISO(srcPath, scratchDir, progressFn); err == nil {
		srcPath = normalizedPath
	}

	destPath := filepath.Join(destDir, "os_image.payload")

	src, err := os.Open(srcPath)
	if err != nil {
		return "", fmt.Errorf("opening source payload: %w", err)
	}
	defer src.Close()

	fi, err := src.Stat()
	if err != nil {
		return "", fmt.Errorf("reading payload stats: %w", err)
	}
	total := fi.Size()

	var startOffset int64
	var dst *os.File
	if dFi, dErr := os.Stat(destPath); dErr == nil && dFi.Size() < total {
		startOffset = dFi.Size()
		dst, err = os.OpenFile(destPath, os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return "", fmt.Errorf("reopening partial payload: %w", err)
		}
		if _, sErr := src.Seek(startOffset, io.SeekStart); sErr != nil {
			dst.Close()
			return "", fmt.Errorf("seeking source payload: %w", sErr)
		}
	} else {
		dst, err = os.Create(destPath)
		if err != nil {
			return "", fmt.Errorf("creating destination payload: %w", err)
		}
	}
	defer dst.Close()

	bufWriter := bufio.NewWriterSize(dst, 8*1024*1024)
	defer bufWriter.Flush()

	buf := make([]byte, 4*1024*1024)
	written := startOffset
	lastUpdate := time.Now()

	for {
		select {
		case <-ctx.Done():
			_ = bufWriter.Flush()
			_ = dst.Sync()
			return destPath, ctx.Err()
		default:
		}

		n, rErr := src.Read(buf)
		if n > 0 {
			wn, wErr := bufWriter.Write(buf[:n])
			if wErr != nil {
				return "", fmt.Errorf("writing to storage vault: %w", wErr)
			}
			written += int64(wn)
			if time.Since(lastUpdate) > 150*time.Millisecond || written == total {
				if progressFn != nil {
					progressFn(written, total)
				}
				lastUpdate = time.Now()
			}
		}
		if rErr != nil {
			if errors.Is(rErr, io.EOF) {
				break
			}
			return "", fmt.Errorf("reading source payload: %w", rErr)
		}
	}

	_ = bufWriter.Flush()
	_ = dst.Sync()
	return destPath, nil
}

func newOptimizedHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          128,
			MaxIdleConnsPerHost:   32,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			ReadBufferSize:        4 * 1024 * 1024,
			WriteBufferSize:       4 * 1024 * 1024,
		},
	}
}

// DownloadPayloadToUSB implements dual-engine acceleration:
// 1. Parallel 8-worker segmented downloads via HTTP Range headers.
// 2. High-throughput single-stream fallback with 8MB write buffering.
func DownloadPayloadToUSB(targetDev USBTargetDevice, imageURL string, progressFn func(written, total int64)) error {
	mountPoint := targetDev.MountPoint
	autoMounted := false

	if mountPoint == "" {
		tmpDir, err := os.MkdirTemp("/tmp", "orch_usb_dl_*")
		if err != nil {
			return fmt.Errorf("creating temporary mount dir: %w", err)
		}
		mountPoint = tmpDir

		mountCmd := exec.Command("mount", targetDev.DevPath, mountPoint)
		if out, err := mountCmd.CombinedOutput(); err != nil {
			_ = os.Remove(mountPoint)
			return fmt.Errorf("mounting usb partition %s: %w\n%s", targetDev.DevPath, err, string(out))
		}
		autoMounted = true
	}

	defer func() {
		_ = exec.Command("sync").Run()
		if autoMounted {
			_ = exec.Command("umount", "-l", mountPoint).Run()
			_ = os.Remove(mountPoint)
		}
	}()

	destPath := filepath.Join(mountPoint, "os_image.payload")
	client := newOptimizedHTTPClient()

	// Probe server capabilities via HEAD request
	headResp, err := client.Head(imageURL)
	var totalBytes int64 = -1
	supportsRanges := false

	if err == nil {
		defer headResp.Body.Close()
		totalBytes = headResp.ContentLength
		acceptRanges := headResp.Header.Get("Accept-Ranges")
		if strings.EqualFold(acceptRanges, "bytes") && totalBytes > 20*1024*1024 {
			supportsRanges = true
		}
	}

	// 1. Multi-Stream Parallel Segment Acceleration Engine
	if supportsRanges && totalBytes > 0 {
		out, cErr := os.OpenFile(destPath, os.O_CREATE|os.O_RDWR, 0644)
		if cErr == nil {
			if tErr := out.Truncate(totalBytes); tErr == nil {
				const numWorkers = 8
				chunkSize := totalBytes / numWorkers
				var totalWritten int64
				var wg sync.WaitGroup
				var workerErr error
				var errMu sync.Mutex

				lastUpdate := time.Now()
				var updateMu sync.Mutex

				for i := 0; i < numWorkers; i++ {
					wg.Add(1)
					startOffset := int64(i) * chunkSize
					endOffset := startOffset + chunkSize - 1
					if i == numWorkers-1 {
						endOffset = totalBytes - 1
					}

					go func(start, end int64) {
						defer wg.Done()
						req, rErr := http.NewRequest("GET", imageURL, nil)
						if rErr != nil {
							errMu.Lock()
							workerErr = rErr
							errMu.Unlock()
							return
						}
						req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))

						resp, doErr := client.Do(req)
						if doErr != nil {
							errMu.Lock()
							workerErr = doErr
							errMu.Unlock()
							return
						}
						defer resp.Body.Close()

						if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
							errMu.Lock()
							workerErr = fmt.Errorf("range request failed with HTTP %s", resp.Status)
							errMu.Unlock()
							return
						}

						buf := make([]byte, 512*1024)
						curOffset := start

						for {
							n, readErr := resp.Body.Read(buf)
							if n > 0 {
								_, writeErr := out.WriteAt(buf[:n], curOffset)
								if writeErr != nil {
									errMu.Lock()
									workerErr = writeErr
									errMu.Unlock()
									return
								}
								curOffset += int64(n)
								currentTotal := atomic.AddInt64(&totalWritten, int64(n))

								updateMu.Lock()
								if time.Since(lastUpdate) > 150*time.Millisecond || currentTotal >= totalBytes {
									if progressFn != nil {
										progressFn(currentTotal, totalBytes)
									}
									lastUpdate = time.Now()
								}
								updateMu.Unlock()
							}
							if readErr != nil {
								if readErr != io.EOF {
									errMu.Lock()
									workerErr = readErr
									errMu.Unlock()
								}
								break
							}
						}
					}(startOffset, endOffset)
				}

				wg.Wait()
				_ = out.Sync()
				out.Close()

				if workerErr == nil {
					return nil
				}
			} else {
				out.Close()
			}
		}
	}

	// 2. High-Throughput Buffered Single-Stream Fallback Engine
	resp, err := client.Get(imageURL)
	if err != nil {
		return fmt.Errorf("connecting to image stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad HTTP status from stream server: %s", resp.Status)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating destination payload on USB: %w", err)
	}
	defer out.Close()

	bufWriter := bufio.NewWriterSize(out, 8*1024*1024)
	defer bufWriter.Flush()

	total := resp.ContentLength
	buf := make([]byte, 4*1024*1024)
	var written int64
	lastUpdate := time.Now()

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			nWrite, writeErr := bufWriter.Write(buf[:n])
			if writeErr != nil {
				return fmt.Errorf("writing to USB storage: %w", writeErr)
			}
			written += int64(nWrite)

			if time.Since(lastUpdate) > 150*time.Millisecond || written == total {
				if progressFn != nil {
					progressFn(written, total)
				}
				lastUpdate = time.Now()
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return fmt.Errorf("streaming payload read error: %w", readErr)
		}
	}

	_ = bufWriter.Flush()
	_ = out.Sync()
	return nil
}

func ProbeUSBPayloads() ([]USBPayload, error) {
	var payloads []USBPayload
	visitedPaths := make(map[string]bool)

	addPayload := func(devName, filePath, mountPoint string, autoMounted bool) {
		if visitedPaths[filePath] {
			return
		}
		fi, err := os.Stat(filePath)
		if err != nil || fi.IsDir() || fi.Size() < 50*1024*1024 {
			return
		}

		lower := strings.ToLower(fi.Name())
		displayName := fi.Name()
		if lower == "os_image.payload" {
			displayName = fmt.Sprintf("Staged OS Payload [%.1f GB Ready]", float64(fi.Size())/(1024*1024*1024))
		}

		payloads = append(payloads, USBPayload{
			DeviceName:   devName,
			FilePath:     filePath,
			FileName:     displayName,
			SizeBytes:    fi.Size(),
			MountPoint:   mountPoint,
			WasAutoMount: autoMounted,
		})
		visitedPaths[filePath] = true
	}

	candidateVaults := []string{
		"/mnt/orch_usb_storage",
		"/media",
		"/run/media",
	}

	for _, vDir := range candidateVaults {
		if fi, err := os.Stat(vDir); err == nil && fi.IsDir() {
			_ = filepath.Walk(vDir, func(path string, info os.FileInfo, walkErr error) error {
				if walkErr == nil && !info.IsDir() {
					lower := strings.ToLower(info.Name())
					if isSupportedPayloadFormat(lower) {
						addPayload(filepath.Base(vDir), path, vDir, false)
					}
				}
				return nil
			})
		}
	}

	out, err := exec.Command("lsblk", "-J", "-o", "NAME,SIZE,TRAN,MOUNTPOINT,FSTYPE,TYPE,RM").Output()
	if err == nil {
		var data usbLsblkResponse
		if jsonErr := json.Unmarshal(out, &data); jsonErr == nil {
			for _, disk := range data.BlockDevices {
				// Same fail-closed TRAN/RM-only classification as
				// ProbeRemovableTargets above — no positional name guess.
				isUSBDisk := strings.ToLower(disk.Tran) == "usb" ||
					disk.Removable == "1" ||
					disk.Removable == "true"

				if isUSBDisk {
					for _, part := range disk.Children {
						devPath := "/dev/" + part.Name
						mountPath := part.Mountpoint
						autoMounted := false

						if mountPath == "" && part.FSType != "" && part.FSType != "swap" {
							targetDir := "/mnt/orch_usb_storage"
							if isMounted(targetDir) {
								targetDir, _ = os.MkdirTemp("/tmp", "orch_usb_scan_*")
							} else {
								_ = os.MkdirAll(targetDir, 0755)
							}

							_ = exec.Command("mount", devPath, targetDir).Run()

							if isMounted(targetDir) {
								mountPath = targetDir
								autoMounted = true
							}
						}

						if mountPath != "" {
							_ = filepath.Walk(mountPath, func(path string, info os.FileInfo, walkErr error) error {
								if walkErr == nil && !info.IsDir() {
									lower := strings.ToLower(info.Name())
									if isSupportedPayloadFormat(lower) {
										addPayload(part.Name, path, mountPath, autoMounted)
									}
								}
								return nil
							})
						}
					}
				}
			}
		}
	}

	return payloads, nil
}

func isMounted(mountPoint string) bool {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return false
	}
	return strings.Contains(string(data), mountPoint)
}

func isSupportedPayloadFormat(filename string) bool {
	suffixes := []string{
		".iso",
		".payload",
		".img",
		".raw",
		".bin",
		".zip",
		".7z",
		".tar.gz",
		".tgz",
		".tar.xz",
		".txz",
		".tar.zst",
	}
	for _, s := range suffixes {
		if strings.HasSuffix(filename, s) {
			return true
		}
	}
	return false
}

func CleanAutoMounts(payloads []USBPayload) {
	for _, p := range payloads {
		if p.MountPoint == "/mnt/orch_usb_storage" {
			continue
		}
		if p.WasAutoMount && p.MountPoint != "" {
			_ = exec.Command("umount", "-l", p.MountPoint).Run()
			_ = os.Remove(p.MountPoint)
		}
	}
}

func PurgeIntermediatePayloads(storageMountPath string) error {
	if storageMountPath == "" {
		return nil
	}

	entries, err := os.ReadDir(storageMountPath)
	if err != nil {
		return fmt.Errorf("reading storage vault: %w", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		lower := strings.ToLower(name)

		if strings.HasPrefix(lower, "host_os_backup") {
			continue
		}

		if strings.HasSuffix(lower, ".iso") ||
			strings.HasSuffix(lower, ".raw") ||
			strings.HasSuffix(lower, ".payload") ||
			strings.HasSuffix(lower, ".part") {
			_ = os.RemoveAll(filepath.Join(storageMountPath, name))
		}
	}

	_ = exec.Command("sync").Run()
	return nil
}

func FactoryResetUSB(diskPath, fsType string) error {
	if diskPath == "" {
		return fmt.Errorf("invalid disk path provided for factory reset")
	}

	_ = exec.Command("bash", "-c", fmt.Sprintf("umount -l %s* 2>/dev/null || true", diskPath)).Run()
	_ = exec.Command("wipefs", "-a", "-f", diskPath).Run()

	if out, err := exec.Command("parted", "-s", diskPath, "mklabel", "msdos").CombinedOutput(); err != nil {
		if outGpt, gptErr := exec.Command("parted", "-s", diskPath, "mklabel", "gpt").CombinedOutput(); gptErr != nil {
			return fmt.Errorf("relabeling drive failed: %w (%s | %s)", err, string(out), string(outGpt))
		}
	}

	partType := "ntfs"
	if strings.EqualFold(fsType, "fat32") {
		partType = "fat32"
	}

	if out, err := exec.Command("parted", "-s", diskPath, "mkpart", "primary", partType, "1MiB", "100%").CombinedOutput(); err != nil {
		return fmt.Errorf("creating primary partition: %w (%s)", err, string(out))
	}

	_ = exec.Command("partprobe", diskPath).Run()
	_ = exec.Command("udevadm", "settle", "--timeout=10").Run()
	time.Sleep(1 * time.Second)

	part1 := JoinDiskAndPartition(diskPath, "1")

	switch strings.ToLower(fsType) {
	case "ntfs":
		if out, err := exec.Command("mkfs.ntfs", "-f", "-L", "USB_DATA", part1).CombinedOutput(); err != nil {
			if outEx, errEx := exec.Command("mkfs.exfat", "-n", "USB_DATA", part1).CombinedOutput(); errEx != nil {
				return fmt.Errorf("formatting NTFS/exFAT failed: %w (%s | %s)", err, string(out), string(outEx))
			}
		}
	case "fat32":
		if out, err := exec.Command("mkfs.vfat", "-F32", "-n", "USB_DATA", part1).CombinedOutput(); err != nil {
			return fmt.Errorf("formatting FAT32 failed: %w (%s)", err, string(out))
		}
	default:
		if _, err := exec.Command("mkfs.exfat", "-n", "USB_DATA", part1).CombinedOutput(); err != nil {
			_ = exec.Command("mkfs.vfat", "-F32", "-n", "USB_DATA", part1).Run()
		}
	}

	_ = exec.Command("sync").Run()
	return nil
}
