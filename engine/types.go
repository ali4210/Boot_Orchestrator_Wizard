package engine

// Unified Partition representation across Linux, Windows, and macOS
type UnifiedPartition struct {
	DiskID       string `json:"disk_id"`       // e.g. "/dev/sda" or "Disk 0"
	PartitionID  string `json:"partition_id"`  // e.g. "/dev/sda1" or "C:"
	Label        string `json:"label"`
	FileSystem   string `json:"filesystem"`    // "ext4", "ntfs", "vfat", "xfs", "btrfs"
	SizeBytes    uint64 `json:"size_bytes"`
	FreeBytes    uint64 `json:"free_bytes"`
	MountPoint   string `json:"mount_point"`
	IsSystemRoot bool   `json:"is_system_root"`
	IsRemovable  bool   `json:"is_removable"`
}

// StorageEngine defines the common operations needed for cross-platform provisioning
type StorageEngine interface {
	EnumerateStorage() ([]UnifiedPartition, error)
	DetectActiveRoot() (UnifiedPartition, error)
	ShrinkVolume(partitionID string, shrinkSizeBytes uint64, logFn func(string)) error
	CreatePartition(diskID string, sizeBytes uint64, fsType string, label string, logFn func(string)) (string, error)
	MountVolume(devPath string, logFn func(string)) (mountPoint string, cleanup func(), err error)
	RegisterBootEntry(title string, efiRelativePath string, diskID string, partNum uint, logFn func(string)) error
}

// RealRevertParams encapsulates parameters to roll back an allocated dual-boot partition
type RealRevertParams struct {
	DiskDevice        string `json:"disk_device"`
	PartitionNum      string `json:"partition_num"`
	HostPartNum       string `json:"host_part_num"`
	EFIDirNameToPurge string `json:"efi_dir_to_purge"`
	EFIBootEntryNum   string `json:"efi_boot_entry_num"`
}
