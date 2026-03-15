package main

import "testing"

func TestUnescapeMountInfo(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"hello", "hello"},
		{`C:\134`, "C:\\"},
		{`path\040with\040spaces`, "path with spaces"},
		{`no\134escape`, "no\\escape"},
		{`\011tab`, "\ttab"},
		{"plain", "plain"},
	}
	for _, tt := range tests {
		if got := unescapeMountInfo(tt.in); got != tt.want {
			t.Errorf("unescapeMountInfo(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestResolveWSLBindMountFrom(t *testing.T) {
	// Real mountinfo excerpt from a WSL2 + Docker Desktop environment.
	// The workspace at C:\Source\mittens is bind-mounted by Docker Desktop.
	mountinfo := `28 1 8:64 / / rw,relatime - ext4 /dev/sde rw,discard,errors=remount-ro,data=ordered
4235 28 0:130 /Source/mittens /mnt/wsl/docker-desktop-bind-mounts/Ubuntu/809eb3721e19d6ef4aed5e01ed73226c5b7c454f38489234f6d232cbbbab84ac rw,noatime - 9p C:\134 rw,dirsync,aname=drvfs;path=C:\;uid=1000;gid=1000;symlinkroot=/mnt/
`

	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "non-bind-mount path unchanged",
			path: "/home/user/project",
			want: "/home/user/project",
		},
		{
			name: "drvfs bind-mount resolved to /mnt/<drive>",
			path: "/mnt/wsl/docker-desktop-bind-mounts/Ubuntu/809eb3721e19d6ef4aed5e01ed73226c5b7c454f38489234f6d232cbbbab84ac",
			want: "/mnt/c/Source/mittens",
		},
		{
			name: "drvfs bind-mount subpath resolved",
			path: "/mnt/wsl/docker-desktop-bind-mounts/Ubuntu/809eb3721e19d6ef4aed5e01ed73226c5b7c454f38489234f6d232cbbbab84ac/cmd/mittens",
			want: "/mnt/c/Source/mittens/cmd/mittens",
		},
		{
			name: "unknown bind-mount hash unchanged",
			path: "/mnt/wsl/docker-desktop-bind-mounts/Ubuntu/aaaaaaaaaaaaaaaa",
			want: "/mnt/wsl/docker-desktop-bind-mounts/Ubuntu/aaaaaaaaaaaaaaaa",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveWSLBindMountFrom(tt.path, mountinfo)
			if got != tt.want {
				t.Errorf("resolveWSLBindMountFrom(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestResolveWSLBindMountFrom_Ext4(t *testing.T) {
	// Simulated mountinfo where the bind-mount source is an ext4 filesystem
	// (native WSL filesystem, not Windows drvfs).
	mountinfo := `28 1 8:64 / / rw,relatime - ext4 /dev/sde rw,discard
500 28 8:64 /home/user/project /mnt/wsl/docker-desktop-bind-mounts/Ubuntu/abc123 rw,relatime - ext4 /dev/sde rw,discard
`

	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "ext4 bind-mount resolved to root path",
			path: "/mnt/wsl/docker-desktop-bind-mounts/Ubuntu/abc123",
			want: "/home/user/project",
		},
		{
			name: "ext4 bind-mount subpath",
			path: "/mnt/wsl/docker-desktop-bind-mounts/Ubuntu/abc123/src",
			want: "/home/user/project/src",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveWSLBindMountFrom(tt.path, mountinfo)
			if got != tt.want {
				t.Errorf("resolveWSLBindMountFrom(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestResolveWSLBindMountFrom_EmptyMountinfo(t *testing.T) {
	path := "/mnt/wsl/docker-desktop-bind-mounts/Ubuntu/abc123"
	got := resolveWSLBindMountFrom(path, "")
	if got != path {
		t.Errorf("expected unchanged path with empty mountinfo, got %q", got)
	}
}
