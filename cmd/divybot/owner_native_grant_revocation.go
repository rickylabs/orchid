package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// The immutable tombstone names the exact installed record. It never removes
// grant history or the owner-intent fence, and cannot authorize another launch.
type ownerNativeRevocation struct {
	SchemaVersion  int    `json:"schema_version"`
	OperationID    string `json:"operation_id"`
	RecordChecksum string `json:"record_checksum"`
}

func (r ownerNativeRevocation) ack() ownerNativeAck {
	return ownerNativeAck{SchemaVersion: 1, OperationID: r.OperationID, State: "REVOKED", RecordChecksum: r.RecordChecksum}
}

func (s *ownerNativeGrantStore) revocationPath(operation string) string {
	return filepath.Join(s.options.StoreRoot, "revocations", ownerNativeOperationKey(operation)+".json")
}

// Missing is distinct from damaged: an unreadable revocation always fences
// admission, including after a publication with an ambiguous outcome.
func (s *ownerNativeGrantStore) revocationLocked(operation string) (*ownerNativeRevocation, error) {
	path := s.revocationPath(operation)
	if !ownerNativePrivateDir(filepath.Dir(path), os.Getuid()) {
		return nil, errMatrix
	}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, errMatrix
	}
	raw, err := ownerNativePrivateRead(path, os.Getuid())
	var revoked ownerNativeRevocation
	if err != nil || ownerNativeStrictJSON(raw, &revoked) != nil || revoked.SchemaVersion != 1 || revoked.OperationID != operation || !digestPattern.MatchString(revoked.RecordChecksum) {
		return nil, errMatrix
	}
	return &revoked, nil
}

// Reuse publication's file/directory sync and exact readback for retries and
// status, so a failed directory sync cannot become a premature success reply.
func (s *ownerNativeGrantStore) confirmRevocationLocked(revoked ownerNativeRevocation) error {
	raw, _ := json.Marshal(revoked)
	return s.publish(s.revocationPath(revoked.OperationID), raw)
}

func (s *ownerNativeGrantStore) revoke(operation, checksum string) ownerNativeAck {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !actionIDPattern.MatchString(operation) || !digestPattern.MatchString(checksum) {
		return s.refuse(0, "", "REFUSED", "override-invalid", "the revocation request is malformed")
	}
	if !s.healthy() {
		return s.refuse(0, operation, "UNKNOWN", "override-invalid", "the private grant store is unhealthy")
	}
	revoked, err := s.revocationLocked(operation)
	if err != nil {
		return s.refuse(0, operation, "UNKNOWN", "override-invalid", "the revocation could not be read")
	}
	if revoked != nil {
		if revoked.RecordChecksum != checksum {
			return s.refuse(0, operation, "REFUSED", "grant-conflict", "the revocation names a different record")
		}
	} else {
		_, raw, err := s.readBoundRecordLocked(operation)
		if err != nil {
			return s.refuse(0, operation, "UNKNOWN", "override-invalid", "the installed record and intent could not be verified")
		}
		if shaText(raw) != checksum {
			return s.refuse(0, operation, "REFUSED", "grant-conflict", "the revocation names a different record")
		}
		revoked = &ownerNativeRevocation{SchemaVersion: 1, OperationID: operation, RecordChecksum: checksum}
	}
	delete(s.active, operation)
	if s.confirmRevocationLocked(*revoked) != nil {
		return s.refuse(0, operation, "UNKNOWN", "override-invalid", "the revocation could not be saved")
	}
	return revoked.ack()
}
