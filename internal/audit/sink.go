package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Sink struct {
	directory    string
	prefix       string
	activePath   string
	archiveRE    *regexp.Regexp
	maxFileBytes int64
	maxFiles     int
	clock        func() time.Time
	instanceID   string

	fileMu   sync.Mutex
	file     *os.File
	fileSize int64
	sequence uint64

	stateMu   sync.Mutex
	closed    bool
	accepted  uint64
	completed uint64
	progress  chan struct{}
	lastErr   error

	queue     chan Event
	workerWG  sync.WaitGroup
	closeOnce sync.Once
	closeDone chan struct{}
	closeErr  error

	eventSequence atomic.Uint64
	dropped       atomic.Uint64
	degraded      atomic.Bool

	beforeWrite func()
}

func New(cfg Config) (*Sink, error) {
	cfg = withDefaults(cfg)
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	instanceID := cfg.InstanceID
	if instanceID == "" {
		var err error
		instanceID, err = randomID("i")
		if err != nil {
			return nil, ErrStorage
		}
	}
	if err := os.MkdirAll(cfg.Directory, 0700); err != nil {
		return nil, ErrStorage
	}
	if err := os.Chmod(cfg.Directory, 0700); err != nil {
		return nil, ErrStorage
	}

	s := &Sink{
		directory: cfg.Directory, prefix: cfg.FilePrefix, maxFileBytes: cfg.MaxFileBytes,
		maxFiles: cfg.MaxFiles, clock: time.Now, instanceID: instanceID,
		activePath: filepath.Join(cfg.Directory, cfg.FilePrefix+".jsonl"),
		archiveRE:  regexp.MustCompile("^" + regexp.QuoteMeta(cfg.FilePrefix) + `-([0-9]{20})\.jsonl$`),
		queue:      make(chan Event, cfg.QueueSize), progress: make(chan struct{}), closeDone: make(chan struct{}),
	}

	s.fileMu.Lock()
	_, next, err := s.archivesLocked()
	if err == nil {
		s.sequence = next
		s.file, s.fileSize, err = s.openActiveLocked()
	}
	if err == nil && s.fileSize > s.maxFileBytes {
		err = s.rotateLocked()
	}
	if err == nil {
		err = s.pruneLocked()
	}
	s.fileMu.Unlock()
	if err != nil {
		if s.file != nil {
			_ = s.file.Close()
		}
		return nil, err
	}

	s.workerWG.Add(1)
	go s.run()
	return s, nil
}

func withDefaults(cfg Config) Config {
	if cfg.FilePrefix == "" {
		cfg.FilePrefix = defaultFilePrefix
	}
	if cfg.MaxFileBytes == 0 {
		cfg.MaxFileBytes = defaultMaxFileBytes
	}
	if cfg.MaxFiles == 0 {
		cfg.MaxFiles = defaultMaxFiles
	}
	if cfg.QueueSize == 0 {
		cfg.QueueSize = defaultQueueSize
	}
	return cfg
}

func validateConfig(cfg Config) error {
	if cfg.Directory == "" || len(cfg.Directory) > maxPathBytes || strings.ContainsRune(cfg.Directory, 0) || !validFilename(cfg.FilePrefix) {
		return ErrInvalidConfig
	}
	if cfg.MaxFileBytes < 1 || cfg.MaxFileBytes > MaxFileBytes || cfg.MaxFiles < 1 || cfg.MaxFiles > MaxFiles || cfg.QueueSize < 1 || cfg.QueueSize > MaxQueueSize {
		return ErrInvalidConfig
	}
	if cfg.InstanceID != "" && validateID(cfg.InstanceID, false) != nil {
		return ErrInvalidConfig
	}
	return nil
}

func validFilename(value string) bool {
	if value == "" || len(value) > 64 || value == "." || value == ".." || strings.HasPrefix(value, ".") {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

func randomID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(b[:]), nil
}

func (s *Sink) Emit(event Event) error {
	if s == nil {
		return ErrClosed
	}
	if event.Class == ClassSecurity {
		return ErrSecurityDelivery
	}
	normalized, err := s.normalize(event, ClassNormal)
	if err != nil {
		return err
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.closed {
		return ErrClosed
	}
	select {
	case s.queue <- normalized:
		s.accepted++
		return nil
	default:
		s.dropped.Add(1)
		s.degraded.Store(true)
		return ErrQueueFull
	}
}

func (s *Sink) EmitSecurity(event Event) error {
	if s == nil {
		return ErrClosed
	}
	normalized, err := s.normalize(event, ClassSecurity)
	if err != nil {
		return err
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if err := s.writeNormalized(normalized, true); err != nil {
		s.markErrorLocked(err)
		return err
	}
	return nil
}

func (s *Sink) normalize(event Event, class EventClass) (Event, error) {
	event.Class = class
	if event.EventID == "" {
		event.EventID = fmt.Sprintf("%s-e-%d", s.instanceID, s.eventSequence.Add(1))
	}
	if event.CorrelationID == "" {
		event.CorrelationID = event.EventID
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = s.clock()
	}
	event.Timestamp = event.Timestamp.UTC()
	if err := validateEvent(event); err != nil {
		return Event{}, err
	}
	return event, nil
}

func (s *Sink) run() {
	defer s.workerWG.Done()
	for event := range s.queue {
		if err := s.writeNormalized(event, false); err != nil {
			s.markError(err)
		}
		s.stateMu.Lock()
		s.completed++
		close(s.progress)
		s.progress = make(chan struct{})
		s.stateMu.Unlock()
	}
}

func (s *Sink) Flush(ctx context.Context) error {
	if s == nil {
		return ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.stateMu.Lock()
	target, progress, closed, closeDone := s.accepted, s.progress, s.closed, s.closeDone
	s.stateMu.Unlock()
	if closed {
		select {
		case <-closeDone:
			s.stateMu.Lock()
			err := s.closeErr
			s.stateMu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for {
		s.stateMu.Lock()
		if s.completed >= target {
			s.stateMu.Unlock()
			break
		}
		progress = s.progress
		s.stateMu.Unlock()
		select {
		case <-progress:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.fileMu.Lock()
	var syncErr error
	if s.file != nil {
		if err := s.file.Sync(); err != nil {
			syncErr = ErrStorage
		}
	}
	s.fileMu.Unlock()
	if syncErr != nil {
		s.markError(syncErr)
		return syncErr
	}
	s.stateMu.Lock()
	lastErr := s.lastErr
	s.stateMu.Unlock()
	return lastErr
}

func (s *Sink) Close() error {
	if s == nil {
		return ErrClosed
	}
	s.closeOnce.Do(func() {
		s.stateMu.Lock()
		s.closed = true
		close(s.queue)
		s.stateMu.Unlock()

		s.workerWG.Wait()
		s.fileMu.Lock()
		var closeErr error
		if s.file != nil {
			if err := s.file.Sync(); err != nil {
				closeErr = ErrStorage
			}
			if err := s.file.Close(); err != nil && closeErr == nil {
				closeErr = ErrStorage
			}
			s.file = nil
		}
		s.fileMu.Unlock()

		s.stateMu.Lock()
		if s.lastErr != nil {
			s.closeErr = s.lastErr
		} else {
			s.closeErr = closeErr
		}
		if closeErr != nil {
			s.degraded.Store(true)
		}
		s.stateMu.Unlock()
		close(s.closeDone)
	})
	<-s.closeDone
	s.stateMu.Lock()
	err := s.closeErr
	s.stateMu.Unlock()
	return err
}

func (s *Sink) Stats() Stats {
	if s == nil {
		return Stats{Degraded: true}
	}
	s.stateMu.Lock()
	stats := Stats{Accepted: s.accepted, Completed: s.completed, QueueDepth: len(s.queue)}
	s.stateMu.Unlock()
	stats.Dropped, stats.Degraded = s.dropped.Load(), s.degraded.Load()
	return stats
}

func (s *Sink) markError(err error) {
	if err == nil {
		return
	}
	s.stateMu.Lock()
	s.markErrorLocked(err)
	s.stateMu.Unlock()
}

func (s *Sink) markErrorLocked(err error) {
	if s.lastErr == nil {
		s.lastErr = err
	}
	s.degraded.Store(true)
}

type archive struct {
	name string
	seq  uint64
}

func (s *Sink) archivesLocked() ([]archive, uint64, error) {
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return nil, 0, ErrStorage
	}
	archives, next := make([]archive, 0), uint64(1)
	for _, entry := range entries {
		match := s.archiveRE.FindStringSubmatch(entry.Name())
		if len(match) != 2 {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, 0, ErrInvalidConfig
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil, 0, ErrInvalidConfig
		}
		seq, err := strconv.ParseUint(match[1], 10, 64)
		if err != nil || seq == 0 || seq == ^uint64(0) {
			return nil, 0, ErrInvalidConfig
		}
		archives = append(archives, archive{name: entry.Name(), seq: seq})
		if seq >= next {
			next = seq + 1
		}
	}
	sort.Slice(archives, func(i, j int) bool { return archives[i].seq < archives[j].seq })
	return archives, next, nil
}

func (s *Sink) openActiveLocked() (*os.File, int64, error) {
	if info, err := os.Lstat(s.activePath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, 0, ErrInvalidConfig
		}
	} else if !os.IsNotExist(err) {
		return nil, 0, ErrStorage
	}
	file, err := os.OpenFile(s.activePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, 0, ErrStorage
	}
	if err := os.Chmod(s.activePath, 0600); err != nil {
		_ = file.Close()
		return nil, 0, ErrStorage
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, 0, ErrStorage
	}
	return file, info.Size(), nil
}

func (s *Sink) ensureOpenLocked() error {
	if s.file != nil {
		return nil
	}
	file, size, err := s.openActiveLocked()
	if err != nil {
		return err
	}
	s.file, s.fileSize = file, size
	return nil
}

func (s *Sink) rotateLocked() error {
	if err := s.ensureOpenLocked(); err != nil {
		return err
	}
	if err := s.file.Sync(); err != nil {
		_ = s.file.Close()
		s.file = nil
		return ErrRotation
	}
	if err := s.file.Close(); err != nil {
		s.file = nil
		return ErrRotation
	}
	s.file = nil
	for {
		if s.sequence == ^uint64(0) {
			return ErrRotation
		}
		name := fmt.Sprintf("%s-%020d.jsonl", s.prefix, s.sequence)
		s.sequence++
		path := filepath.Join(s.directory, name)
		if info, err := os.Lstat(path); err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return ErrRotation
			}
			continue
		} else if !os.IsNotExist(err) {
			return ErrRotation
		}
		if err := os.Rename(s.activePath, path); err != nil {
			return ErrRotation
		}
		break
	}
	if err := s.ensureOpenLocked(); err != nil {
		return ErrRotation
	}
	if err := s.pruneLocked(); err != nil {
		return err
	}
	return nil
}

func (s *Sink) pruneLocked() error {
	archives, _, err := s.archivesLocked()
	if err != nil {
		return ErrRetention
	}
	keep := s.maxFiles - 1
	if len(archives) <= keep {
		return nil
	}
	for _, old := range archives[:len(archives)-keep] {
		if err := os.Remove(filepath.Join(s.directory, old.name)); err != nil && !os.IsNotExist(err) {
			return ErrRetention
		}
	}
	return nil
}

func (s *Sink) writeNormalized(event Event, durable bool) error {
	line, err := json.Marshal(toWireEvent(event, s.instanceID))
	if err != nil {
		return ErrInvalidEvent
	}
	line = append(line, '\n')
	if int64(len(line)) > s.maxFileBytes {
		return ErrEventTooLarge
	}

	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	if s.beforeWrite != nil {
		s.beforeWrite()
	}
	if err := s.ensureOpenLocked(); err != nil {
		return err
	}
	if s.fileSize+int64(len(line)) > s.maxFileBytes {
		if err := s.rotateLocked(); err != nil {
			return err
		}
	}
	n, err := s.file.Write(line)
	if err != nil || n != len(line) {
		return ErrStorage
	}
	s.fileSize += int64(n)
	if durable {
		if err := s.file.Sync(); err != nil {
			return ErrStorage
		}
	}
	return nil
}
