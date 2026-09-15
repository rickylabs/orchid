package main

import "os"

// IDs are private operator configuration. Nil means retain the creator's owner.
// chown is an injected syscall for failure controls, never configuration input.
type receiptOwner struct {
	uid, gid int
	chown    func(string, int, int) error
	sync     func(string) error
}

func configuredReceiptOwner(cfg MatrixConfig) (*receiptOwner, error) {
	if cfg.ReceiptOwnerUID == nil && cfg.ReceiptOwnerGID == nil {
		return nil, nil
	}
	if cfg.ReceiptOwnerUID == nil || cfg.ReceiptOwnerGID == nil {
		return nil, errMatrix
	}
	uid, gid := *cfg.ReceiptOwnerUID, *cfg.ReceiptOwnerGID
	if uid < 0 || gid < 0 || uint64(uid) >= 4294967295 || uint64(gid) >= 4294967295 {
		return nil, errMatrix
	}
	return &receiptOwner{uid: uid, gid: gid}, nil
}
func transferReceiptOwner(owner *receiptOwner, paths ...string) error {
	if owner == nil {
		return nil
	}
	chown := owner.chown
	if chown == nil {
		chown = os.Chown
	}
	sync := owner.sync
	if sync == nil {
		sync = syncDirectory
	}
	for _, path := range paths {
		if err := chown(path, owner.uid, owner.gid); err != nil {
			return err
		}
		if err := sync(path); err != nil {
			return err
		}
	}
	return nil
}
