package operations

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxRecord = 2 << 20
const maxJournal = 64 << 20

// Journal is owner-private and write-once. Partial or malformed records are
// retained and fail closed. Only Engine owns state transitions; the local CLI
// may append a separate decision. Do not put this directory inside a workspace.
type Journal struct {
	root *os.Root
	Path string
}

func OpenJournal(path string, create bool) (*Journal, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrStorage
	}
	if create {
		if err := os.Mkdir(path, 0700); err != nil {
			return nil, ErrStorage
		}
		if err := protectJournal(path); err != nil {
			return nil, ErrStorage
		}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrStorage
	}
	if err = checkPrivateJournal(path, info); err != nil {
		return nil, ErrStorage
	}
	r, err := os.OpenRoot(path)
	if err != nil {
		return nil, ErrStorage
	}
	return &Journal{r, path}, nil
}
func (j *Journal) Close() error { return j.root.Close() }
func (j *Journal) write(name string, v any) error {
	b, e := json.Marshal(v)
	if e != nil || len(b) > maxRecord {
		return ErrStorage
	}
	defer clear(b)
	b, e = sealJournal(b)
	if e != nil {
		return ErrStorage
	}
	entries, e := j.root.ReadFile("journal.version")
	if name != "journal.version" && (e != nil || string(entries) != Version) {
		return ErrStorage
	}
	if e = j.checkSize(len(b)); e != nil {
		return e
	}
	var random [16]byte
	if _, e = rand.Read(random[:]); e != nil {
		return ErrStorage
	}
	pending := "pending-" + hex.EncodeToString(random[:])
	f, e := j.root.OpenFile(pending, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return ErrStorage
	}
	_, we := f.Write(b)
	se := f.Sync()
	ce := f.Close()
	if we != nil || se != nil || ce != nil {
		return ErrStorage
	}
	// Link publishes a complete write-once record atomically and refuses to
	// replace an existing decision. Readers never observe partial approvals.
	if e = j.root.Link(pending, name); e != nil {
		return ErrStorage
	}
	if e = j.root.Remove(pending); e != nil {
		return ErrStorage
	}
	return nil
}

func (j *Journal) BeforeImage(id, expected string) ([]byte, error) {
	if !validID(id) || !validID(expected) {
		return nil, ErrInvalid
	}
	var v struct {
		ID, Digest string
		Data       []byte
	}
	if j.read(id+".before", &v) != nil || v.ID != id || v.Digest != expected {
		return nil, ErrDenied
	}
	return v.Data, nil
}
func (j *Journal) checkSize(extra int) error {
	d, e := j.root.Open(".")
	if e != nil {
		return ErrStorage
	}
	defer d.Close()
	entries, e := d.ReadDir(-1)
	if e != nil || len(entries) > 4096 {
		return ErrCapacity
	}
	total := int64(extra)
	for _, entry := range entries {
		info, e := entry.Info()
		if e != nil {
			if errors.Is(e, os.ErrNotExist) && (strings.HasPrefix(entry.Name(), "pending-") || strings.HasPrefix(entry.Name(), "run-")) {
				continue
			}
			return ErrStorage
		}
		if info.IsDir() && strings.HasPrefix(entry.Name(), "run-") {
			total += MaxContent
			continue
		}
		if !info.Mode().IsRegular() {
			return ErrStorage
		}
		total += info.Size()
	}
	if total > maxJournal {
		return ErrCapacity
	}
	return nil
}
func (j *Journal) read(name string, v any) error {
	i, e := j.root.Lstat(name)
	if e != nil || !i.Mode().IsRegular() || i.Size() > 2*maxRecord {
		return ErrStorage
	}
	f, e := j.root.Open(name)
	if e != nil {
		return ErrStorage
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 2*maxRecord+1))
	if e != nil || len(b) > 2*maxRecord {
		return ErrStorage
	}
	b, e = unsealJournal(b)
	if e != nil {
		return ErrStorage
	}
	defer clear(b)
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return ErrStorage
	}
	if d.Decode(new(any)) != io.EOF {
		return ErrStorage
	}
	return nil
}
func (j *Journal) Initialize() error {
	f, e := j.root.OpenFile("journal.version", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return ErrStorage
	}
	_, we := f.WriteString(Version)
	se := f.Sync()
	ce := f.Close()
	if we != nil || se != nil || ce != nil {
		return ErrStorage
	}
	return nil
}
func (j *Journal) Records() (map[string]Record, error) {
	b, e := j.root.ReadFile("journal.version")
	if e != nil || string(b) != Version {
		return nil, ErrStorage
	}
	d, e := j.root.Open(".")
	if e != nil {
		return nil, ErrStorage
	}
	defer d.Close()
	names, e := d.Readdirnames(-1)
	if e != nil || len(names) > 4096 {
		return nil, ErrStorage
	}
	sort.Strings(names)
	result := map[string]Record{}
	for _, name := range names {
		if !strings.HasSuffix(name, ".record") {
			continue
		}
		var r Record
		if j.read(name, &r) != nil {
			return nil, ErrStorage
		}
		if !validID(r.Summary.ID) || r.Version != Version || r.Summary.Digest != digest(r.Plan) || name != recordName(r) {
			return nil, ErrStorage
		}
		old, ok := result[r.Summary.ID]
		if (!ok && r.Sequence != 1) || (ok && (r.Sequence != old.Sequence+1 || old.Summary.Digest != r.Summary.Digest || terminal(old.Summary.State))) {
			return nil, ErrStorage
		}
		result[r.Summary.ID] = r
	}
	return result, nil
}
func recordName(r Record) string       { return fmt.Sprintf("%s.%06d.record", r.Summary.ID, r.Sequence) }
func (j *Journal) save(r Record) error { return j.write(recordName(r), r) }
func (j *Journal) decision(id string) (Decision, bool, error) {
	var d Decision
	if !validID(id) {
		return d, false, ErrInvalid
	}
	_, e := j.root.Lstat(id + ".decision")
	if errors.Is(e, os.ErrNotExist) {
		return d, false, nil
	}
	if e != nil {
		return d, false, ErrStorage
	}
	if e = j.read(id+".decision", &d); e != nil {
		return d, false, e
	}
	return d, true, nil
}

// Decide is a LOCAL control API. It is deliberately absent from MCP tools.
func (j *Journal) Decide(id, expected string, approve, ack bool) error {
	if !validID(id) || !validID(expected) || approve && !ack {
		return ErrInvalid
	}
	records, e := j.Records()
	if e != nil {
		return e
	}
	r, ok := records[id]
	if !ok || r.Summary.Digest != expected || r.Summary.State != "awaiting_approval" {
		return ErrConflict
	}
	d := Decision{Version: Version, ID: id, Digest: expected, Approved: approve, TrustedHostAcknowledged: ack, Expires: r.Summary.Expires}
	return j.write(id+".decision", d)
}

func (j *Journal) CancelLocal(id, expected string) error {
	if !validID(id) || !validID(expected) {
		return ErrInvalid
	}
	records, err := j.Records()
	if err != nil {
		return err
	}
	r, ok := records[id]
	if !ok || r.Summary.Digest != expected || terminal(r.Summary.State) {
		return ErrConflict
	}
	return j.write(id+".cancel", struct{ Version, ID, Digest string }{Version, id, expected})
}
func (j *Journal) localCancel(r Record) (bool, error) {
	name := r.Summary.ID + ".cancel"
	_, err := j.root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, ErrStorage
	}
	var v struct{ Version, ID, Digest string }
	if j.read(name, &v) != nil || v.Version != Version || v.ID != r.Summary.ID || v.Digest != r.Summary.Digest {
		return false, ErrStorage
	}
	return true, nil
}
