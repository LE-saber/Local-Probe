package rootfs

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/search"
)

func TestBoundSearchSourceUsesRootfsPolicyAndDirectoryGeneration(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootPath, "src", "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rootPath, "vendor"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"src/main.go":        "package main\nneedle\n",
		"src/nested/util.go": "package nested\nneedle in utf8\n",
		"vendor/third.go":    "needle ignored\n",
		"notes.txt":          "literal [a-z]+\n",
		"secret.txt":         "needle denied\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(rootPath, filepath.FromSlash(name)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := config.NewRootWithIgnore("project", rootPath, []string{"secret.txt"}, []string{"vendor/**"})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := config.NewProfileWithIgnore("read", []string{"project"}, []string{"list_directory", "find_files", "search_text", "tree_directory"}, nil, []string{"*.tmp"})
	if err != nil {
		t.Fatal(err)
	}
	credential := config.NewCredentialRef("credential", "local_token")
	connection := config.NewConnection("connection", "read", profile.ID(), credential.ID(), true)
	cfg, err := config.New(config.SchemaVersionV1, []config.Root{root}, []config.Profile{profile}, []config.Connection{connection}, []config.CredentialRef{credential})
	if err != nil {
		t.Fatal(err)
	}
	store, err := config.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := policy.NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := manager.BindAuthenticated(connection.ID())
	if err != nil {
		t.Fatal(err)
	}
	source, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	service, err := search.New(source, search.DefaultLimits(), []byte("rootfs-search-test-key-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	listing, err := service.ListDirectory(ctx, bound, search.ListDirectoryRequest{RootID: "project", PageSize: 16})
	if err != nil {
		t.Fatal(err)
	}
	listingPaths := make([]string, 0, len(listing.Entries))
	for _, entry := range listing.Entries {
		listingPaths = append(listingPaths, entry.Path)
	}
	sort.Strings(listingPaths)
	if strings.Join(listingPaths, ",") != "notes.txt,src" || listing.Coverage.DeniedEntries == 0 || listing.Coverage.IgnoredEntries == 0 {
		t.Fatalf("list_directory paths = %v coverage=%#v warnings=%v", listingPaths, listing.Coverage, listing.Warnings)
	}
	found, err := service.FindFiles(ctx, bound, search.FindFilesRequest{RootID: "project", Pattern: "**/*.go", PageSize: 16})
	if err != nil {
		t.Fatal(err)
	}
	foundPaths := make([]string, 0, len(found.Entries))
	for _, entry := range found.Entries {
		foundPaths = append(foundPaths, entry.Path)
	}
	sort.Strings(foundPaths)
	if strings.Join(foundPaths, ",") != "src/main.go,src/nested/util.go" {
		t.Fatalf("find_files paths = %v coverage=%#v warnings=%v", foundPaths, found.Coverage, found.Warnings)
	}
	text, err := service.SearchText(ctx, bound, search.SearchTextRequest{RootID: "project", Query: "needle", PageSize: 16})
	if err != nil {
		t.Fatal(err)
	}
	if len(text.Matches) != 2 || text.Coverage.DeniedEntries == 0 || text.Coverage.IgnoredEntries == 0 {
		t.Fatalf("search_text = %#v", text)
	}
	tree, err := service.TreeDirectory(ctx, bound, search.TreeDirectoryRequest{RootID: "project", MaxDepth: 2, PageSize: 16})
	if err != nil {
		t.Fatal(err)
	}
	treePaths := make([]string, 0, len(tree.Entries))
	for _, entry := range tree.Entries {
		treePaths = append(treePaths, entry.Path)
	}
	sortedTreePaths := append([]string(nil), treePaths...)
	sort.Strings(sortedTreePaths)
	if strings.Join(sortedTreePaths, ",") != ",notes.txt,src,src/main.go,src/nested" || tree.Coverage.DeniedEntries == 0 || tree.Coverage.IgnoredEntries == 0 || tree.Coverage.Complete {
		t.Fatalf("tree_directory paths = %v coverage=%#v warnings=%v", treePaths, tree.Coverage, tree.Warnings)
	}

	// The generation is tied to the opened directory's metadata. A changed
	// directory invalidates a continuation instead of silently skipping entries.
	page, err := service.FindFiles(ctx, bound, search.FindFilesRequest{RootID: "project", Pattern: "**/*.go", PageSize: 1})
	if err != nil || page.Continuation == "" {
		t.Fatalf("find first page = %#v, %v", page, err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "new.go"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.FindFiles(ctx, bound, search.FindFilesRequest{RootID: "project", Pattern: "**/*.go", PageSize: 1, Cursor: page.Continuation}); err == nil {
		t.Fatal("accepted continuation after directory generation changed")
	}
}
