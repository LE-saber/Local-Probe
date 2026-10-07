package rootfs

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"

	"github.com/LE-saber/Local-Probe/internal/readcore"
)

type fileIdentity struct {
	volume      uint64
	fileID      uint64
	links       uint64
	attributes  uint64
	hasIdentity bool
	hasLinks    bool
	reparse     bool
}

// nativeMetadataFn is replaceable only by package tests. Keeping the
// platform call behind this variable lets tests exercise the fail-closed
// error path without pretending that a platform lacks identity support.
// Tests changing it must not run in parallel.
var nativeMetadataFn = nativeMetadata

func metadataFor(file *os.File, _ string) (readcore.Metadata, error) {
	if file == nil {
		return readcore.Metadata{}, ErrUnavailable
	}
	info, err := file.Stat()
	if err != nil {
		return readcore.Metadata{}, mapStatError(err)
	}
	if !info.Mode().IsRegular() {
		return readcore.Metadata{}, unsupportedTypeError()
	}
	identity, identityErr := nativeMetadataFn(file, info)
	if identityErr != nil {
		// A real platform lookup failure is not the same as a port that
		// explicitly reports no identity. Never downgrade an operational
		// failure to m0, because that would bypass hardlink/reparse checks.
		return readcore.Metadata{}, ErrUnavailable
	}
	if identity.reparse {
		return readcore.Metadata{}, deniedError()
	}
	if identity.hasLinks && identity.links != 1 {
		return readcore.Metadata{}, unsupportedTypeError()
	}
	return readcore.Metadata{
		Size: info.Size(),
		Version: readcore.Version{
			Token:    metadataToken(info, identity),
			Strength: "metadata",
		},
	}, nil
}

func metadataToken(info os.FileInfo, identity fileIdentity) string {
	var encoded [64]byte
	binary.BigEndian.PutUint64(encoded[0:8], uint64(info.Size()))
	binary.BigEndian.PutUint64(encoded[8:16], uint64(info.ModTime().UnixNano()))
	binary.BigEndian.PutUint64(encoded[16:24], uint64(info.Mode()))
	binary.BigEndian.PutUint64(encoded[24:32], identity.volume)
	binary.BigEndian.PutUint64(encoded[32:40], identity.fileID)
	binary.BigEndian.PutUint64(encoded[40:48], identity.links)
	binary.BigEndian.PutUint64(encoded[48:56], identity.attributes)
	var flags uint64
	if identity.hasIdentity {
		flags |= 1
	}
	if identity.hasLinks {
		flags |= 2
	}
	binary.BigEndian.PutUint64(encoded[56:64], flags)
	digest := sha256.Sum256(encoded[:])
	prefix := "m0."
	if identity.hasIdentity {
		prefix = "m1."
	}
	return prefix + hex.EncodeToString(digest[:])
}
