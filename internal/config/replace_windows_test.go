//go:build windows

package config

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWindowsRepeatedConfigReplacement(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "config.json")
	if err := os.WriteFile(destination, []byte("initial"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := range 300 {
		temp := filepath.Join(dir, "next.tmp")
		data := []byte(strconv.Itoa(i))
		if err := os.WriteFile(temp, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := replaceFile(temp, destination); err != nil {
			t.Fatalf("Windows replacement error: %T %v", err, err)
		}
		actual, err := os.ReadFile(destination)
		if err != nil || string(actual) != string(data) {
			t.Fatal("replacement bytes lost")
		}
	}
}

func TestWindowsReplacementRetryIsBoundedAndCategorySpecific(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		failure                 error
		recoverAfter, wantCalls int
	}{
		{"transient-denial", windows.ERROR_ACCESS_DENIED, 3, 4},
		{"permanent-denial", windows.ERROR_ACCESS_DENIED, 100, 16},
		{"sharing", windows.ERROR_SHARING_VIOLATION, 2, 3},
		{"unknown", windows.ERROR_INVALID_HANDLE, 100, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			err := retryWindowsReplacement(func() error {
				calls++
				if calls <= tc.recoverAfter {
					return tc.failure
				}
				return nil
			}, func(time.Duration) {})
			if calls != tc.wantCalls {
				t.Fatalf("calls=%d want=%d", calls, tc.wantCalls)
			}
			if tc.recoverAfter < 16 {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, tc.failure) {
				t.Fatal("failure hidden")
			}
		})
	}
}

func TestFileStoreNumericErrorDoesNotExposePath(t *testing.T) {
	err := fileStoreIO("replace configuration", &os.PathError{Op: "rename", Path: "private-sentinel-path", Err: windows.ERROR_ACCESS_DENIED})
	if !errors.Is(err, ErrFileStoreIO) || !strings.Contains(err.Error(), "os code 5") || strings.Contains(err.Error(), "private-sentinel") {
		t.Fatal("unsafe storage diagnostic")
	}
}
