package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLookupSupplementaryGroupsInFile(t *testing.T) {
	dir := t.TempDir()
	groupPath := filepath.Join(dir, "group")
	data := `root:x:0:
claude:x:1000:
docker:x:998:claude
video:x:44:other, claude
primary-duplicate:x:1000:claude
malformed
badgid:x:not-a-number:claude
`
	if err := os.WriteFile(groupPath, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := lookupSupplementaryGroupsInFile(groupPath, "claude", 1000)
	if err != nil {
		t.Fatal(err)
	}

	want := []int{998, 44}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
}

func TestLookupSupplementaryGroupsInFileNoMemberships(t *testing.T) {
	dir := t.TempDir()
	groupPath := filepath.Join(dir, "group")
	if err := os.WriteFile(groupPath, []byte("docker:x:998:someoneelse\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := lookupSupplementaryGroupsInFile(groupPath, "claude", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("groups = %v, want empty", got)
	}
}
